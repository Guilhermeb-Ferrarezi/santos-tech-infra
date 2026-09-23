package main

import (
	"os"
	"strings"
	"testing"
)

// Os dois motores (WhatsApp Cloud e Evolution) precisam ter a MESMA capacidade.
//
// Já divergiram: o do Evolution ficou sem Google Agenda, sem lembretes, sem voz
// e com o agendamento automático desligado. Nada disso dava erro — cada um
// desses caminhos só faz `return` quando a dependência é nil —, então o cliente
// que entrasse por aquele canal era atendido por um bot mais burro, em silêncio.
//
// A proteção é estrutural: existe UMA composição de EngineDeps, e cada motor
// copia e troca só o remetente. Este teste falha se alguém escrever a segunda
// lista à mão, que é exatamente como a divergência nasceu.
func TestUmaSoComposicaoDeEngineDeps(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("não consegui ler main.go: %v", err)
	}
	if n := strings.Count(string(src), "EngineDeps{"); n != 1 {
		t.Errorf("main.go tem %d composições de EngineDeps; deve ter 1.\n"+
			"Monte a base uma vez e faça `deps := depsBase; deps.Sender = ...` para cada canal —\n"+
			"duas listas escritas à mão divergem, e a diferença não aparece em teste nem em erro.", n)
	}
}

// As capacidades que o bot usa para marcar aula ficam TODAS na base
// compartilhada. Se alguém mover uma delas para um motor só, o outro canal
// perde a função sem avisar.
func TestCapacidadesDeAgendamentoEstaoNaBaseCompartilhada(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("não consegui ler main.go: %v", err)
	}
	s := string(src)
	ini := strings.Index(s, "depsBase := EngineDeps{")
	if ini < 0 {
		t.Fatal("não achei a base compartilhada depsBase em main.go")
	}
	fim := strings.Index(s[ini:], "\n\t}")
	if fim < 0 {
		t.Fatal("não achei o fim da composição de depsBase")
	}
	base := s[ini : ini+fim]

	for _, campo := range []string{
		"GCal:", "GCalRepo:", "Lembretes:", // agenda e avisos
		"AgendaAutoConfirm:", "EscolaAbre:", "EscolaFecha:", "AulaDuracaoMin:", // travas
		"Notion:", "Bookings:", // onde a aula é gravada
		"Voice:", "AudioClips:", // a voz do Henrique
		"Qualificacoes:", // a memória sobre cada pessoa
	} {
		if !strings.Contains(base, campo) {
			t.Errorf("%s saiu da base compartilhada — um dos canais vai ficar sem essa capacidade", campo)
		}
	}
}
