package main

import (
	"os"
	"strings"
	"testing"
)

// O incidente de 25/09: um cliente escreveu e nunca foi respondido.
//
// A marca de deduplicação era gravada DENTRO da transação, que confirma ANTES
// de o modelo ser chamado. O serviço de IA devolveu 502, a mensagem já constava
// como vista, e o retry — ao reler essa marca — concluiu "duplicada", desistiu
// e logou SUCESSO. Falha, silêncio, e um lead a menos.
//
// Estes testes travam as duas pontas da correção: a pergunta que o dedup faz, e
// o momento em que a mensagem é dada por encerrada.

// O dedup precisa perguntar "já RESPONDI?", não "já vi?".
func TestDedupPerguntaSeRespondeuNaoSeViu(t *testing.T) {
	src, err := os.ReadFile("repos.go")
	if err != nil {
		t.Fatalf("não consegui ler repos.go: %v", err)
	}
	s := string(src)

	i := strings.Index(s, "func (r *MessageRepo) RecordInbound")
	if i < 0 {
		t.Fatal("RecordInbound sumiu")
	}
	fim := strings.Index(s[i:], "\n}")
	corpo := s[i : i+fim]

	if !strings.Contains(corpo, "respondida_em IS NULL") {
		t.Error("RecordInbound voltou a tratar QUALQUER reentrega como duplicada — " +
			"mensagem que falhou não vai poder ser reprocessada")
	}
	if strings.Contains(corpo, "DO NOTHING") {
		t.Error("o ON CONFLICT DO NOTHING voltou: ele é o que apaga a chance do retry")
	}
}

// A mensagem só está encerrada depois que a resposta saiu.
func TestMarcaRespondidaSoDepoisDoSucesso(t *testing.T) {
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("não consegui ler engine.go: %v", err)
	}
	s := string(src)

	chamada := strings.Index(s, "MarcaRespondida(")
	if chamada < 0 {
		t.Fatal("ninguém marca a mensagem como respondida — o retry nunca vai parar")
	}
	llm := strings.Index(s, "Responder.Respond(")
	if llm < 0 {
		t.Fatal("não achei a chamada do modelo")
	}
	if chamada < llm {
		t.Error("a mensagem está sendo dada por respondida ANTES de o modelo responder — " +
			"é exatamente o bug de 25/09")
	}

	// E a marca tem que depender do sucesso, não acontecer de qualquer jeito.
	trecho := s[max0(chamada-260) : chamada+40]
	if !strings.Contains(trecho, "err == nil") {
		t.Error("a marca não está condicionada a err == nil: uma falha encerraria " +
			"a mensagem do mesmo jeito")
	}
}

// Falhar em silêncio é o que transforma um erro técnico em lead perdido.
func TestClienteSemRespostaViraAvisoParaOsAdmins(t *testing.T) {
	src, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("não consegui ler worker.go: %v", err)
	}
	s := string(src)

	if !strings.Contains(s, "avisaMensagemSemResposta") {
		t.Fatal("não existe aviso quando o bot desiste de responder alguém")
	}
	if !strings.Contains(s, "NÃO CONSEGUI RESPONDER") {
		t.Error("o aviso não diz, em português claro, o que aconteceu")
	}
	// O aviso precisa levar o telefone e o que a pessoa escreveu — sem isso
	// ninguém consegue responder à mão.
	i := strings.Index(s, "func (w *Worker) avisaMensagemSemResposta")
	fim := i + 2000
	if fim > len(s) {
		fim = len(s) // a função é a última do arquivo
	}
	corpo := s[i:fim]
	if !strings.Contains(corpo, "inbound.ExternalID") {
		t.Error("o aviso não diz QUEM ficou sem resposta")
	}
	if !strings.Contains(corpo, "Ele escreveu") {
		t.Error("o aviso não diz O QUE a pessoa escreveu")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
