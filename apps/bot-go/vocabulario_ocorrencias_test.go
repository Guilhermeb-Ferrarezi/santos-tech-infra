package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// "Onde este termo aparece" — é o que responde o "como eu ia saber?" do
// Henrique (25/09/2026): o termo "adulto" estava na Base, no código e em áudio
// gravado, e nenhuma tela mostrava isso.

func TestOcorrenciasDoTermoIgnoraAcentoECaixaMasNaoPedacoDePalavra(t *testing.T) {
	casos := []struct {
		texto, termo string
		n            int
	}{
		{"Temos CURSO DE ADULTO e curso de adulto.", "curso de adulto", 2},
		{"aulas para adultos às terças", "para adultos", 1},
		{"Programação é legal", "programacao", 1},
		{"Aula de adultos", "aula de adulto", 0}, // "adultos" não é "adulto"
		{"professor ou professora", "professor ou professora", 1},
		{"nada aqui", "curso de adulto", 0},
		{"", "x", 0},
		{"texto qualquer", "   ", 0},
	}
	for _, c := range casos {
		n, trecho := ocorrenciasDoTermo(c.texto, c.termo)
		if n != c.n {
			t.Errorf("%q em %q: esperado %d, veio %d", c.termo, c.texto, c.n, n)
		}
		if n > 0 && trecho == "" {
			t.Errorf("%q em %q: achou mas não devolveu trecho", c.termo, c.texto)
		}
	}
}

func TestOcorrenciasDoTermoTrechoMostraOTextoOriginal(t *testing.T) {
	longo := strings.Repeat("palavra ", 30) + "Curso de Adulto no fim " + strings.Repeat("fim ", 30)
	_, trecho := ocorrenciasDoTermo(longo, "curso de adulto")
	if !strings.Contains(trecho, "Curso de Adulto") {
		t.Errorf("o trecho deveria trazer o texto como está escrito, veio %q", trecho)
	}
	if len([]rune(trecho)) > 160 {
		t.Errorf("trecho longo demais (%d)", len([]rune(trecho)))
	}
}

func TestOcorrenciasNoBancoAchaEmCadaFonte(t *testing.T) {
	pool, tenant := tenantRegrasDeTeste(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	kb, _ := json.Marshal([]KBEntry{
		{ID: "a", Title: "Cursos para ADULTOS", Content: "O curso de adulto é individual."},
		{ID: "b", Title: "Preços", Content: "Sem o termo."},
	})
	_, err := pool.Exec(ctx, `INSERT INTO tenant_config (tenant_id, kb_content, system_prompt) VALUES ($1, $2::jsonb, 'Você fala de curso de adulto.')`, tenant, string(kb))
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO audio_clips (tenant_id, voice, intent_key, file_path, transcript) VALUES ($1, 'henrique', 'conv_adulto', 'x.ogg', 'Nosso curso de adulto é ótimo')`, tenant)
	must(err)
	var contact, ident, conv string
	must(pool.QueryRow(ctx, `INSERT INTO contact (tenant_id, display_name) VALUES ($1, 'Cliente') RETURNING id`, tenant).Scan(&contact))
	must(pool.QueryRow(ctx, `INSERT INTO channel_identity (tenant_id, contact_id, channel, external_id) VALUES ($1, $2, 'whatsapp', '5516999990000') RETURNING id`, tenant, contact).Scan(&ident))
	must(pool.QueryRow(ctx, `INSERT INTO conversation (tenant_id, channel_identity_id, channel) VALUES ($1, $2, 'whatsapp') RETURNING id`, tenant, ident).Scan(&conv))
	_, err = pool.Exec(ctx, `INSERT INTO outbound_message (tenant_id, conversation_id, idempotency_key, status, intent_category, content)
		VALUES ($1, $2, 'k1', 'sent', 'FREE_FORM', '{"text":"Temos o curso de adulto sim!"}'),
		       ($1, $2, 'k2', 'failed', 'FREE_FORM', '{"text":"curso de adulto que falhou"}')`, tenant, conv)
	must(err)

	res, err := buscaOcorrencias(ctx, pool, tenant, RegrasVenda{})
	must(err)
	var achado *OcorrenciasTermo
	for i := range res.Termos {
		if res.Termos[i].Evitar == "curso de adulto" {
			achado = &res.Termos[i]
		}
	}
	if achado == nil {
		t.Fatalf("o termo padrão 'curso de adulto' não foi analisado: %+v", res)
	}
	por := map[string]int{}
	for _, f := range achado.Fontes {
		por[f.Fonte] = f.Total
		if f.Total > 0 && len(f.Exemplos) == 0 {
			t.Errorf("fonte %s sem exemplo", f.Fonte)
		}
	}
	for fonte, esperado := range map[string]int{
		"base": 1, "prompt_sistema": 1, "audios": 1, "respostas": 1, "prompt_admin": 0, "regras": 0,
	} {
		if por[fonte] != esperado {
			t.Errorf("fonte %s: esperado %d, veio %d (todas: %v)", fonte, esperado, por[fonte], por)
		}
	}
	if achado.Total != 4 {
		t.Errorf("total esperado 4, veio %d", achado.Total)
	}
	// "curso para adultos" aparece no título da Base.
	for _, tm := range res.Termos {
		if tm.Evitar == "curso para adultos" && tm.Total != 0 {
			t.Errorf("'curso para adultos' não está em lugar nenhum (o título diz 'Cursos para ADULTOS'), veio %d", tm.Total)
		}
	}
}
