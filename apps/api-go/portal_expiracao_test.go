package main

// Testes das regras PURAS dos avisos de expiração do pacote
// (portal_expiracao_worker.go) e da validação de contractDate no PATCH da
// matrícula. Nada aqui precisa de banco.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func dia(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestAvisosDevidos(t *testing.T) {
	venc := dia(2026, 11, 11)
	cases := []struct {
		name       string
		hoje       time.Time
		a60, a30   bool
		want       string
		diasQuerer int
	}{
		{"muito antes", dia(2026, 9, 1), false, false, "", 71},
		{"exatamente 60 dias antes", dia(2026, 9, 12), false, false, "60", 60},
		{"59 dias antes, 60 já avisado", dia(2026, 9, 13), true, false, "", 59},
		{"30 dias antes", dia(2026, 10, 12), true, false, "30", 30},
		{"contrato lançado atrasado: os dois de uma vez", dia(2026, 10, 22), false, false, "60 30", 20},
		{"vence hoje, nada avisado", dia(2026, 11, 11), false, false, "60 30", 0},
		{"já venceu: marca os dois (o chamador não avisa)", dia(2027, 1, 5), false, false, "60 30", -55},
		{"tudo avisado", dia(2026, 11, 1), true, true, "", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := avisosDevidos(tc.hoje, venc, tc.a60, tc.a30)
			var partes []string
			for _, m := range got {
				partes = append(partes, strconv.Itoa(m))
			}
			if strings.Join(partes, " ") != tc.want {
				t.Errorf("devidos=%v want %q", got, tc.want)
			}
			if d := diasAte(tc.hoje, venc); d != tc.diasQuerer {
				t.Errorf("diasAte=%d want %d", d, tc.diasQuerer)
			}
		})
	}
}

func TestPacoteVencimento(t *testing.T) {
	contrato, inicio := dia(2026, 2, 10), dia(2025, 12, 1)
	if got := pacoteVencimento(&contrato, &inicio); got == nil || !got.Equal(dia(2027, 2, 10)) {
		t.Errorf("com data do contrato: %v", got)
	}
	if got := pacoteVencimento(nil, &inicio); got == nil || !got.Equal(dia(2026, 12, 1)) {
		t.Errorf("sem contrato cai no início da turma: %v", got)
	}
	if got := pacoteVencimento(nil, nil); got != nil {
		t.Errorf("sem nada: %v", got)
	}
	// Hora no meio do dia (timestamp de start_date) não muda o dia.
	meio := time.Date(2026, 3, 5, 15, 30, 0, 0, time.UTC)
	if got := pacoteVencimento(nil, &meio); !got.Equal(dia(2027, 3, 5)) {
		t.Errorf("hora deveria ser zerada: %v", got)
	}

	if got := pacoteVenceEm(true, &contrato, &inicio); got == nil || *got != "2027-02-10" {
		t.Errorf("packageExpiresAt particular: %v", got)
	}
	if got := pacoteVenceEm(false, &contrato, &inicio); got != nil {
		t.Errorf("turma de grupo não tem vencimento: %v", *got)
	}
	if got := dataISO(&contrato); got == nil || *got != "2026-02-10" {
		t.Errorf("dataISO: %v", got)
	}
	if dataISO(nil) != nil {
		t.Error("dataISO(nil) deveria ser nil")
	}
}

// "Hoje" é o dia do calendário em Ribeirão Preto: 23:30 de sábado lá já é
// domingo 02:30Z, mas o dia da escola ainda é sábado.
func TestHojeNaEscola(t *testing.T) {
	loc := posaulaLocation()
	if got := hojeNaEscola(time.Date(2026, 9, 12, 23, 30, 0, 0, loc)); !got.Equal(dia(2026, 9, 12)) {
		t.Errorf("23:30 local: %v", got)
	}
	if got := hojeNaEscola(time.Date(2026, 9, 13, 2, 30, 0, 0, time.UTC)); !got.Equal(dia(2026, 9, 12)) {
		t.Errorf("02:30Z (= 23:30 de sábado local): %v", got)
	}
	if got := hojeNaEscola(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)); !got.Equal(dia(2026, 9, 13)) {
		t.Errorf("meio-dia Z: %v", got)
	}
}

func TestExpiracaoTextos(t *testing.T) {
	for dias, want := range map[int]string{0: "hoje", -3: "hoje", 1: "amanhã", 2: "em 2 dias", 30: "em 30 dias", 60: "em 60 dias"} {
		if got := expiracaoQuando(dias); got != want {
			t.Errorf("expiracaoQuando(%d)=%q want %q", dias, got, want)
		}
	}
	if got := dataPorExtenso(dia(2026, 11, 11)); got != "11 de novembro de 2026" {
		t.Errorf("dataPorExtenso: %q", got)
	}
	if got := dataPorExtenso(dia(2027, 3, 1)); got != "1 de março de 2027" {
		t.Errorf("dataPorExtenso: %q", got)
	}
}

func TestPortalSetStudentIndividualContractDateValidationBeforeDB(t *testing.T) {
	s := testServer(Config{})
	for name, body := range map[string]string{
		"data inválida":      `{"individual":true,"contractDate":"2026-13-01"}`,
		"formato brasileiro": `{"individual":true,"contractDate":"11/09/2026"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("PATCH", "/portal/classes/1/students/1", strings.NewReader(body))
			r.SetPathValue("classId", "1")
			r.SetPathValue("studentId", "1")
			w := httptest.NewRecorder()
			s.handlePortalSetStudentIndividual(w, reqAs(r, 1))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want %d body=%s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
	// validate normaliza: aparas; vazio continua vazio (limpa a data).
	com := " 2026-09-11 "
	in := portalStudentIndividualInput{ContractDate: &com}
	if err := in.validate(); err != nil || *in.ContractDate != "2026-09-11" {
		t.Errorf("contractDate aparada: %q err=%v", *in.ContractDate, err)
	}
	vazio := "  "
	limpa := portalStudentIndividualInput{ContractDate: &vazio}
	if err := limpa.validate(); err != nil || *limpa.ContractDate != "" {
		t.Errorf("contractDate vazia limpa: %q err=%v", *limpa.ContractDate, err)
	}
}
