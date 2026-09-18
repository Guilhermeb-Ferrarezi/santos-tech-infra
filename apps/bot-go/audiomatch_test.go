package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

func clipe(intent, transcript string, ms int, cat string) AudioClip {
	return AudioClip{IntentKey: intent, Variant: 1, FilePath: intent + ".ogg",
		Transcript: transcript, DurationMs: ms, Category: cat}
}

// acervoReal carrega o manifesto que vai na imagem. Sem ele o teste é pulado —
// o pacote precisa compilar e testar em máquina que não tem os áudios.
func acervoReal(t *testing.T) []AudioClip {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("audio", "manifest.json"))
	if err != nil {
		t.Skip("sem audio/manifest.json")
	}
	var man struct {
		Clips []struct {
			Voice      string `json:"voice"`
			IntentKey  string `json:"intentKey"`
			Variant    int    `json:"variant"`
			FilePath   string `json:"filePath"`
			Transcript string `json:"transcript"`
			DurationMs int    `json:"durationMs"`
			Category   string `json:"category"`
		} `json:"clips"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifesto ilegível: %v", err)
	}
	out := make([]AudioClip, 0, len(man.Clips))
	for _, c := range man.Clips {
		out = append(out, AudioClip{IntentKey: c.IntentKey, Variant: c.Variant,
			FilePath: c.FilePath, Transcript: c.Transcript,
			DurationMs: c.DurationMs, Category: c.Category})
	}
	return out
}

// ── unidades ─────────────────────────────────────────────────────────────────

func TestTokenizaNormaliza(t *testing.T) {
	got := tokeniza("Olá, BOM dia! Tudo bem com você?")
	quer := map[string]bool{"ola": true, "bom": true, "dia": true, "tudo": true}
	for _, tok := range got {
		if !quer[tok] {
			t.Errorf("token inesperado: %q (stopword deveria ter saído)", tok)
		}
	}
	for k := range quer {
		if !contem(got, k) {
			t.Errorf("faltou o token %q em %v", k, got)
		}
	}
	if len(tokeniza("")) != 0 || len(tokeniza("!!! ... ???")) != 0 {
		t.Error("texto vazio ou só pontuação deveria dar zero tokens")
	}
}

func contem(xs []string, alvo string) bool {
	for _, x := range xs {
		if x == alvo {
			return true
		}
	}
	return false
}

func TestEhFato(t *testing.T) {
	sim := []string{"1970", "14h", "terca", "noite", "reais", "nao", "vinte", "excel", "r$1.970"}
	nao := []string{"perfeito", "combinado", "legal", "instante", "conferir"}
	for _, s := range sim {
		if !ehFato(s) {
			t.Errorf("%q deveria ser fato", s)
		}
	}
	for _, s := range nao {
		if ehFato(s) {
			t.Errorf("%q NÃO deveria ser fato", s)
		}
	}
}

// O caso que motiva o gate inteiro: as duas frases são quase idênticas em
// palavras e afirmam períodos do dia diferentes.
func TestGateDeFatosSeparaBoaTardeDeBoaNoite(t *testing.T) {
	ix := NovoAudioIndex([]AudioClip{
		clipe("saud_boanoite", "Olá, boa noite! Tudo bem com você?", 2000, "saudacao"),
	})
	clip, info := ix.Casa("Olá, boa tarde! Tudo bem com você?", MatchOpts{})
	if clip != nil {
		t.Fatalf("mandou %q de noite para uma resposta de tarde", clip.IntentKey)
	}
	if info.Motivo != "sem_candidato" {
		t.Errorf("motivo=%q, queria sem_candidato", info.Motivo)
	}
}

func TestRecusaRespostaComDadoDinamico(t *testing.T) {
	ix := NovoAudioIndex(acervoReal(t))
	perigosas := []string{
		"O curso sai por R$ 1.970 no plano Essencial, com 24 aulas.",
		"Consigo te encaixar na terça às 14h, pode ser?",
		"A matrícula é R$ 199,90 e o material R$ 389,90.",
		"Ficamos na Rua São Sebastião, 1992, no centro.",
		"Temos vaga na quinta de manhã ou na sexta à tarde.",
		"Nesse horário não temos vaga, infelizmente.",
		"O valor da mensalidade é 539,90 por mês.",
		"A turma fechou ontem, mas abre outra em duas semanas.",
	}
	for _, r := range perigosas {
		if clip, info := ix.Casa(r, MatchOpts{}); clip != nil {
			t.Errorf("casou áudio %q (score %.2f) para resposta com dado dinâmico:\n  %s\n  áudio diz: %s",
				clip.IntentKey, info.Score, r, clip.Transcript)
		}
	}
}

func TestCoberturaBidirecionalRecusaClipeQueDizMais(t *testing.T) {
	ix := NovoAudioIndex([]AudioClip{
		clipe("longo", "Perfeito! Vou conferir aqui a agenda, falar com o professor responsável, "+
			"organizar o material necessário e te retorno ainda hoje com tudo certinho e confirmado.",
			9000, "auxiliar"),
	})
	// A resposta diz só a primeira parte. O clipe afirma um monte de coisa a mais.
	if clip, info := ix.Casa("Perfeito! Vou conferir aqui.", MatchOpts{}); clip != nil {
		t.Errorf("aceitou clipe que afirma muito além da resposta (score %.2f)", info.Score)
	}
}

func TestTetoDeDuracao(t *testing.T) {
	texto := "Deixa eu conferir isso para você rapidinho e já te retorno com a resposta certa"
	curto := clipe("curto", texto, 6000, "espera")
	longo := clipe("longo", texto, 40000, "explicacao")

	if clip, _ := NovoAudioIndex([]AudioClip{curto}).Casa(texto, MatchOpts{}); clip == nil {
		t.Error("clipe curto idêntico deveria casar")
	}
	if clip, _ := NovoAudioIndex([]AudioClip{longo}).Casa(texto, MatchOpts{}); clip != nil {
		t.Error("clipe de 40s não deveria virar resposta")
	}
}

func TestCategoriaProativaNaoViraResposta(t *testing.T) {
	texto := "Oi! Passando só pra lembrar que amanhã tem aula."
	ix := NovoAudioIndex([]AudioClip{clipe("lembr_vespera", texto, 2700, "lembrete_aula")})
	if clip, _ := ix.Casa(texto, MatchOpts{}); clip != nil {
		t.Error("fala proativa (lembrete) não pode ser usada como resposta")
	}
}

func TestRespostaCurtaExigeCasamentoQuaseExato(t *testing.T) {
	ix := NovoAudioIndex([]AudioClip{
		clipe("aux_perfeito", "Perfeito!", 1000, "auxiliar"),
		clipe("aux_perfeito_longo", "Perfeito, combinado então!", 1600, "auxiliar"),
	})
	clip, _ := ix.Casa("Perfeito!", MatchOpts{})
	if clip == nil {
		t.Fatal("'Perfeito!' deveria casar com a gravação idêntica")
	}
	if clip.IntentKey != "aux_perfeito" {
		t.Errorf("escolheu %q; para resposta de uma palavra só o casamento exato serve", clip.IntentKey)
	}
}

func TestSorteiaEntreVariantes(t *testing.T) {
	ix := NovoAudioIndex([]AudioClip{
		clipe("a", "Combinado, fico no aguardo!", 1500, "fechamento"),
		clipe("b", "Combinado, fico no aguardo!", 1500, "fechamento"),
		clipe("c", "Combinado, fico no aguardo!", 1500, "fechamento"),
	})
	vistos := map[string]bool{}
	for i := 0; i < 200; i++ {
		if clip, _ := ix.Casa("Combinado, fico no aguardo!", MatchOpts{}); clip != nil {
			vistos[clip.IntentKey] = true
		}
	}
	if len(vistos) < 2 {
		t.Errorf("sorteio não variou: %v", vistos)
	}
}

func TestConversaNovaBarraSaudacaoDeRetorno(t *testing.T) {
	texto := "Oi! Que bom falar com você de novo por aqui."
	ix := NovoAudioIndex([]AudioClip{clipe("saud_retorno", texto, 2500, "saudacao")})

	if clip, _ := ix.Casa(texto, MatchOpts{ConversaNova: true}); clip != nil {
		t.Error("não pode dizer 'de novo' para quem nunca falou com a escola")
	}
	if clip, _ := ix.Casa(texto, MatchOpts{ConversaNova: false}); clip == nil {
		t.Error("em conversa já existente, a saudação de retorno deveria valer")
	}
}

// ── acervo real ──────────────────────────────────────────────────────────────

// As falas que DEVEM virar áudio: protocolares, sem dado dinâmico, e que o
// acervo sabe dizer. Se isto regride, o recurso virou inútil em silêncio.
func TestAcervoRealCasaFalasProtocolares(t *testing.T) {
	clips := acervoReal(t)
	ix := NovoAudioIndex(clips)

	// Cada caso é o transcript de uma gravação real, escrito como o modelo
	// escreveria. Casar consigo mesmo é o piso do que o matcher tem que fazer.
	var casos []AudioClip
	for _, c := range clips {
		if categoriasProativas[c.Category] || c.DurationMs > 12000 {
			continue
		}
		casos = append(casos, c)
		if len(casos) >= 60 {
			break
		}
	}
	if len(casos) == 0 {
		t.Fatal("nenhum clipe elegível no acervo — algo está errado nos filtros")
	}

	recusados := 0
	for _, c := range casos {
		clip, info := ix.Casa(c.Transcript, MatchOpts{})
		if clip == nil {
			recusados++
			t.Logf("recusou a própria fala %q (melhor %.2f em %q): %s",
				c.IntentKey, info.Score, info.NearMiss, c.Transcript)
		}
	}
	if recusados > 0 {
		t.Errorf("%d de %d gravações não casaram com o próprio texto", recusados, len(casos))
	}
}

// Guarda de regressão: quantos pares de INTENÇÕES DIFERENTES o funil deixa
// passar. São equivalências legítimas ("Perfeito!" ↔ "Perfeito, combinado!").
// Se alguém dobrar o acervo e este número explodir, é sinal de que o gate
// afrouxou — e o CI avisa antes de um cliente ouvir a fala errada.
func TestAcervoRealPoucosCruzamentosEntreIntencoes(t *testing.T) {
	clips := acervoReal(t)
	ix := NovoAudioIndex(clips)

	cruzados, total := 0, 0
	for _, c := range clips {
		if categoriasProativas[c.Category] || c.DurationMs > 12000 {
			continue
		}
		total++
		clip, _ := ix.Casa(c.Transcript, MatchOpts{})
		if clip != nil && clip.IntentKey != c.IntentKey {
			cruzados++
		}
	}
	t.Logf("elegíveis=%d  casaram com intenção diferente=%d (%.1f%%)",
		total, cruzados, 100*float64(cruzados)/float64(total))

	if frac := float64(cruzados) / float64(total); frac > 0.35 {
		t.Errorf("cruzamento entre intenções em %.1f%% — acima do teto de 35%%", 100*frac)
	}
}

// A persona do bot pode não se chamar como a pessoa que gravou o acervo — a
// escola usa um nome distinto de propósito, para reconhecer o atendimento do
// bot quando o cliente chega. A gravação não pode desmentir a persona.
func TestNomeProprioNaoPodeDivergir(t *testing.T) {
	ix := NovoAudioIndex(acervoReal(t))

	if clip, info := ix.Casa("Aqui é o Marcos, da Escola Santos Tech.", MatchOpts{}); clip != nil {
		t.Errorf("persona Marcos casou com gravação %q (score %.2f): %s",
			clip.IntentKey, info.Score, clip.Transcript)
	}
	// Uma saudação sem nome também não pode puxar uma apresentação com nome.
	if clip, _ := ix.Casa("Oi, boa tarde! Como posso te ajudar?", MatchOpts{}); clip != nil {
		if strings.Contains(strings.ToLower(clip.Transcript), "henrique") {
			t.Errorf("saudação sem nome trouxe apresentação com nome: %s", clip.Transcript)
		}
	}
	// E o papel também é afirmação: o bot não é o coordenador pedagógico.
	if clip, info := ix.Casa("Eu faço as aulas experimentais aqui.", MatchOpts{}); clip != nil {
		if strings.Contains(strings.ToLower(clip.Transcript), "coordenador") {
			t.Errorf("bot assumiu papel de coordenador (score %.2f): %s", info.Score, clip.Transcript)
		}
	}
}
