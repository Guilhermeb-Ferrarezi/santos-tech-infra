package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Fase 2 do retorno ao cliente: a escola escolhe na tela o que o bot faz no dia
// do retorno (só avisar, reativar sozinho, ou os dois). Mensagem automática
// errada queima o lead — por isso as travas abaixo sempre caem para "avisar".

func ptrS(s string) *string { return &s }
func ptrI(i int) *int       { return &i }

func TestValidaFollowupPatch(t *testing.T) {
	casos := []struct {
		nome        string
		modo        *string
		dias        *int
		responsavel *int
		ok          bool
	}{
		{"nada enviado", nil, nil, nil, true},
		{"avisar", ptrS("avisar"), nil, nil, true},
		{"reativar", ptrS("reativar"), nil, nil, true},
		{"reativar_e_avisar", ptrS("reativar_e_avisar"), nil, nil, true},
		{"modo vazio", ptrS(""), nil, nil, false},
		{"modo desconhecido", ptrS("mandar_tudo"), nil, nil, false},
		{"dias 0 desliga", nil, ptrI(0), nil, true},
		{"dias 30", nil, ptrI(30), nil, true},
		{"dias 31", nil, ptrI(31), nil, false},
		{"dias negativo", nil, ptrI(-1), nil, false},
		{"responsável 0 volta ao padrão", nil, nil, ptrI(0), true},
		{"responsável 30", nil, nil, ptrI(30), true},
		{"responsável negativo", nil, nil, ptrI(-5), false},
	}
	for _, c := range casos {
		err := validaFollowupPatch(c.modo, c.dias, c.responsavel)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, queria ok=%v", c.nome, err, c.ok)
		}
	}
}

func TestResponsavelEfetivo(t *testing.T) {
	if got := responsavelEfetivo(0, 30); got != 30 {
		t.Errorf("sem escolha na tela, vale o do ambiente: %d", got)
	}
	if got := responsavelEfetivo(12, 30); got != 12 {
		t.Errorf("a escolha da tela vence o ambiente: %d", got)
	}
}

func TestDecideAcao(t *testing.T) {
	livre := condicoesRetorno{BotLigado: true, CanalOficial: false}
	casos := []struct {
		nome      string
		modo      string
		confianca float64
		cond      condicoesRetorno
		avisar    bool
		reativar  bool
		comMotivo bool
	}{
		{"padrão vazio = avisar", "", 0.9, livre, true, false, false},
		{"avisar", ModoAvisar, 0.9, livre, true, false, false},
		{"modo desconhecido = avisar", "xpto", 0.9, livre, true, false, false},
		{"reativar livre", ModoReativar, 0.9, livre, false, true, false},
		{"reativar e avisar livre", ModoReativarEAvisar, 0.9, livre, true, true, false},
		{"confiança 0 (não informada) não trava", ModoReativar, 0, livre, false, true, false},
		{"data vaga trava", ModoReativar, 0.5, livre, true, false, true},
		{"data vaga trava também no reativar_e_avisar", ModoReativarEAvisar, 0.5, livre, true, false, true},
		{"oficial fora da janela trava", ModoReativar, 0.9,
			condicoesRetorno{BotLigado: true, CanalOficial: true, DentroDaJanela: false}, true, false, true},
		{"oficial dentro da janela passa", ModoReativar, 0.9,
			condicoesRetorno{BotLigado: true, CanalOficial: true, DentroDaJanela: true}, false, true, false},
		{"bot desligado trava", ModoReativar, 0.9,
			condicoesRetorno{BotLigado: false}, true, false, true},
		{"handoff trava", ModoReativar, 0.9,
			condicoesRetorno{BotLigado: true, EmHandoff: true}, true, false, true},
		{"cliente voltou a falar trava", ModoReativarEAvisar, 0.9,
			condicoesRetorno{BotLigado: true, ClienteVoltouAFalar: true}, true, false, true},
	}
	for _, c := range casos {
		a := decideAcao(c.modo, retornoPendente{Confianca: c.confianca}, c.cond)
		if a.Avisar != c.avisar || a.Reativar != c.reativar || (a.Motivo != "") != c.comMotivo {
			t.Errorf("%s: %+v (queria avisar=%v reativar=%v motivo=%v)", c.nome, a, c.avisar, c.reativar, c.comMotivo)
		}
	}
}

// Quem pediu o retorno e depois voltou a falar já está em outra conversa: o
// "me chama dia 10" pode ter ficado velho. Mandar sozinho seria no escuro.
func TestCondicoesDoRetorno(t *testing.T) {
	agora := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	pedido := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	falouOntem := agora.Add(-20 * time.Hour)
	falouHaUmMes := agora.AddDate(0, -1, 0)
	falouLogoDepois := pedido.Add(10 * time.Minute)

	c := condicoesDoRetorno(retornoPendente{Kind: KindRetorno, CriadoEm: pedido, Canal: "whatsapp",
		BotLigado: true, UltimaDoCliente: &falouOntem}, agora, true)
	if !c.CanalOficial || !c.DentroDaJanela || !c.ClienteVoltouAFalar || !c.BotLigado {
		t.Errorf("oficial, falou ontem depois do pedido: %+v", c)
	}

	c = condicoesDoRetorno(retornoPendente{Kind: KindRetorno, CriadoEm: pedido, Canal: "evolution",
		BotLigado: true, UltimaDoCliente: &falouHaUmMes}, agora, true)
	if c.CanalOficial || c.ClienteVoltouAFalar {
		t.Errorf("evolution, última mensagem antes do pedido: %+v", c)
	}

	// "Obrigada!" logo depois de pedir o retorno não é voltar a falar.
	c = condicoesDoRetorno(retornoPendente{Kind: KindRetorno, CriadoEm: pedido, Canal: "evolution",
		BotLigado: true, UltimaDoCliente: &falouLogoDepois}, agora, true)
	if c.ClienteVoltouAFalar {
		t.Error("mensagem logo depois do pedido contou como voltar a falar")
	}

	// No número não-oficial, o bot só responde com o toggle do Evolution ligado.
	c = condicoesDoRetorno(retornoPendente{Kind: KindRetorno, Canal: "evolution", BotLigado: true}, agora, false)
	if c.BotLigado {
		t.Error("com o bot do Evolution desligado, a conversa não tem bot")
	}

	c = condicoesDoRetorno(retornoPendente{Kind: KindRetorno, Canal: "whatsapp", BotLigado: true, Estado: string(StateHandoff)}, agora, true)
	if !c.EmHandoff {
		t.Error("estado HANDOFF não virou EmHandoff")
	}
}

func TestAvisoPosExperimental(t *testing.T) {
	aula := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC) // 15h em Brasília
	p := retornoPendente{Kind: KindPosExperimental, ConvID: "c9", Nome: "Vivian", AulaEm: &aula}
	txt := avisoDeRetorno(p, "https://p")
	for _, esperado := range []string{"Vivian", "aula experimental em 23/09", "https://p/admin/whats/conversas?c=c9"} {
		if !strings.Contains(txt, esperado) {
			t.Errorf("aviso pós-experimental sem %q:\n%s", esperado, txt)
		}
	}
}

func TestAvisoComAcao(t *testing.T) {
	p := retornoPendente{Kind: KindRetorno, ConvID: "c1", Nome: "Ana", Frase: "me chama dia 10"}

	txt := avisoComAcao(p, "https://p", acaoRetorno{Avisar: true, Motivo: "a data é aproximada"}, "")
	if !strings.Contains(txt, "Não reativei sozinho") || !strings.Contains(txt, "a data é aproximada") {
		t.Errorf("quem recebe o aviso precisa saber por que o bot não mandou:\n%s", txt)
	}

	txt = avisoComAcao(p, "https://p", acaoRetorno{Avisar: true, Reativar: true}, "Oi Ana! Tudo bem?")
	if !strings.Contains(txt, "Reativei") || !strings.Contains(txt, "Oi Ana! Tudo bem?") {
		t.Errorf("no reativar_e_avisar o aviso mostra o que foi mandado:\n%s", txt)
	}

	txt = avisoComAcao(p, "https://p", acaoRetorno{Avisar: true}, "")
	if strings.Contains(txt, "Reativei") || strings.Contains(txt, "Não reativei") {
		t.Errorf("no modo avisar o aviso é o da fase 1:\n%s", txt)
	}
}

func TestPromptDeReativacao(t *testing.T) {
	cfg := TenantConfig{BotName: "Marcos"}
	p := retornoPendente{Kind: KindRetorno, Nome: "Vivian", Frase: "em dezembro consigo mais", Resumo: "Gostou da experimental."}
	pr := promptDeReativacao(p, cfg)
	for _, esperado := range []string{"Marcos", "Vivian", "em dezembro consigo mais", "Gostou da experimental.", "não invente"} {
		if !strings.Contains(pr, esperado) {
			t.Errorf("prompt sem %q:\n%s", esperado, pr)
		}
	}

	aula := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC)
	pr = promptDeReativacao(retornoPendente{Kind: KindPosExperimental, Nome: "Rafa", AulaEm: &aula}, cfg)
	if !strings.Contains(pr, "aula experimental") || !strings.Contains(pr, "23/09") {
		t.Errorf("prompt pós-experimental sem a aula:\n%s", pr)
	}
}

func TestLimpaMensagemDoModelo(t *testing.T) {
	casos := map[string]string{
		`  "Oi Ana! Tudo bem?"  `: "Oi Ana! Tudo bem?",
		"Oi Ana!":                 "Oi Ana!",
		"   ":                     "",
		strings.Repeat("a", 1000): "", // modelo desandou: não manda
	}
	for in, want := range casos {
		if got := limpaMensagemDoModelo(in); got != want {
			t.Errorf("limpaMensagemDoModelo(%q) = %q, queria %q", in, got, want)
		}
	}
}

// O painel antigo não conhece os campos novos: PATCH sem eles não pode mexer
// na configuração de follow-up.
func TestPatchDeConfigSemFollowupPreserva(t *testing.T) {
	var body dashConfigPatch
	if err := json.Unmarshal([]byte(`{"botName":"Marcos"}`), &body); err != nil {
		t.Fatal(err)
	}
	if body.FollowupModo != nil || body.FollowupDiasPosExperimental != nil || body.FollowupResponsavelID != nil {
		t.Error("campo de follow-up não enviado chegou preenchido — seria sobrescrito")
	}
	if err := json.Unmarshal([]byte(`{"followupModo":"reativar","followupDiasPosExperimental":0,"followupResponsavelId":0}`), &body); err != nil {
		t.Fatal(err)
	}
	if body.FollowupModo == nil || *body.FollowupModo != "reativar" ||
		body.FollowupDiasPosExperimental == nil || *body.FollowupDiasPosExperimental != 0 ||
		body.FollowupResponsavelID == nil || *body.FollowupResponsavelID != 0 {
		t.Errorf("zero enviado de propósito precisa chegar como zero: %+v", body)
	}
}
