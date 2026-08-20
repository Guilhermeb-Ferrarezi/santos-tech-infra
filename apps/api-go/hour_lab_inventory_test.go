package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// O padrão vai para um ILIKE '%...%': curinga aceito aqui casaria com qualquer
// programa e marcaria o item como instalado em TODOS os PCs — falso "está tudo
// certo" é pior que falso "está faltando".
func TestExpectedProgramInputRejeitaCuringaDoLike(t *testing.T) {
	for _, pat := range []string{"%", "uni%", "_nity", `uni\ty`} {
		// json.Marshal e não string crua: a barra invertida precisa chegar
		// escapada no JSON, senão "\t" vira TAB antes da validação ver o padrão.
		body, err := json.Marshal(map[string]string{"label": "Unity", "matchPattern": pat})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/hour-lab-expected-programs", strings.NewReader(string(body)))
		if _, _, err := expectedProgramInput(r); err == nil {
			t.Errorf("padrão %q deveria ser recusado", pat)
		}
	}
}

// Sem matchPattern o rótulo vira o trecho procurado — é o caso comum (o admin
// digita "Blender" e espera que case com "Blender 4.2").
func TestExpectedProgramInputUsaLabelComoPadrao(t *testing.T) {
	r := httptest.NewRequest("POST", "/hour-lab-expected-programs",
		strings.NewReader(`{"label":"  Blender  "}`))
	label, pattern, err := expectedProgramInput(r)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if label != "Blender" || pattern != "Blender" {
		t.Fatalf("esperava label/pattern = Blender, veio %q/%q", label, pattern)
	}
}

func TestExpectedProgramInputExigeLabel(t *testing.T) {
	r := httptest.NewRequest("POST", "/hour-lab-expected-programs", strings.NewReader(`{"label":"   "}`))
	if _, _, err := expectedProgramInput(r); err == nil {
		t.Error("label vazio deveria ser recusado")
	}
}

// Inventário grande demais é barrado ANTES de tocar o banco — a rota é pública
// (só o segredo do dispositivo autentica) e um payload inflado não pode virar
// milhares de INSERTs.
func TestInventoryRecusaListaGrandeDemais(t *testing.T) {
	progs := make([]LabProgram, maxInventoryPrograms+1)
	for i := range progs {
		progs[i] = LabProgram{Name: "prog"}
	}
	body, _ := json.Marshal(map[string]any{
		"deviceId": "3b35ede6-c1a5-4625-812a-f33c7c73adc4",
		"programs": progs,
	})
	w := httptest.NewRecorder()
	s := &Server{}
	s.handleLabDeviceInventory(w, httptest.NewRequest("POST", "/public/lab-devices/inventory",
		strings.NewReader(string(body))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVENTORY_TOO_LARGE") {
		t.Errorf("esperava código INVENTORY_TOO_LARGE, veio %s", w.Body.String())
	}
}

func TestInventoryRecusaDeviceIDInvalido(t *testing.T) {
	w := httptest.NewRecorder()
	s := &Server{}
	s.handleLabDeviceInventory(w, httptest.NewRequest("POST", "/public/lab-devices/inventory",
		strings.NewReader(`{"deviceId":"nao-e-uuid","programs":[]}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", w.Code)
	}
}

// Segredo maior que o tamanho emitido é descartado sem comparar string
// arbitrária no banco — mesma proteção do heartbeat.
func TestInventoryRecusaSegredoForaDoTamanho(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"deviceId":     "3b35ede6-c1a5-4625-812a-f33c7c73adc4",
		"deviceSecret": strings.Repeat("a", 2*labDeviceSecretBytes+1),
	})
	w := httptest.NewRecorder()
	s := &Server{}
	s.handleLabDeviceInventory(w, httptest.NewRequest("POST", "/public/lab-devices/inventory",
		strings.NewReader(string(body))))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("esperava 401, veio %d", w.Code)
	}
}

// Regressão de produção: o registro do Windows devolve nomes com NUL embutido
// ("Roblox Player for guibf\x00"), o Postgres recusa 0x00 em coluna text
// (SQLSTATE 22021) e o inventário INTEIRO da máquina falhava com 500.
func TestSanitizeInventoryFieldTiraNulEControles(t *testing.T) {
	if got := sanitizeInventoryField("Roblox Player for guibf\x00"); got != "Roblox Player for guibf" {
		t.Fatalf("NUL deveria sumir, veio %q", got)
	}
	if got := sanitizeInventoryField("  Unity\t6000\n "); got != "Unity6000" {
		t.Fatalf("controles deveriam sumir e as pontas serem aparadas, veio %q", got)
	}
	if got := sanitizeInventoryField("Blender"); got != "Blender" {
		t.Fatalf("nome normal não deveria mudar, veio %q", got)
	}
}

// O ícone vai direto num src="data:image/png;base64,..." — o que não for
// base64 puro precisa virar vazio, não conteúdo solto dentro do atributo.
func TestSanitizeIconSoAceitaBase64Puro(t *testing.T) {
	valid := "iVBORw0KGgo="
	if got := sanitizeIcon(valid); got != valid {
		t.Fatalf("base64 válido deveria passar, veio %q", got)
	}
	for _, bad := range []string{
		"data:image/png;base64,iVBORw0KGgo=", // prefixo colado
		`" onerror="alert(1)`,                // tentativa de sair do atributo
		"não é base64!",
	} {
		if got := sanitizeIcon(bad); got != "" {
			t.Errorf("%q deveria ser descartado, veio %q", bad, got)
		}
	}
	if got := sanitizeIcon(strings.Repeat("A", maxInventoryIconBytes+1)); got != "" {
		t.Error("ícone acima do teto deveria ser descartado")
	}
}

func TestTruncRunesNaoQuebraAcento(t *testing.T) {
	if got := truncRunes("ãããã", 2); got != "ãã" {
		t.Fatalf("esperava 2 runas, veio %q", got)
	}
	if got := truncRunes("abc", 10); got != "abc" {
		t.Fatalf("string curta deveria passar inteira, veio %q", got)
	}
}

func TestSortExpectedByLabelIgnoraCaixa(t *testing.T) {
	items := []ExpectedProgramStatus{{Label: "unity"}, {Label: "Blender"}, {Label: "cura"}}
	sortExpectedByLabel(items)
	got := []string{items[0].Label, items[1].Label, items[2].Label}
	want := []string{"Blender", "cura", "unity"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordem errada: %v", got)
		}
	}
}
