package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fase 2 do retorno ao cliente (spec dashboard/docs/superpowers/specs/
// 2026-09-25-bot-follow-up-reativacao-design.md): a escola escolhe na tela o
// que acontece no dia do retorno.
//
//	avisar             — só avisa um humano (a fase 1)
//	reativar           — o bot manda a mensagem ao cliente sozinho
//	reativar_e_avisar  — manda e avisa
//
// Mensagem automática errada queima o lead. Por isso toda dúvida cai para
// "avisar": o humano decide, e o aviso diz por que o bot não mandou.

const (
	ModoAvisar          = "avisar"
	ModoReativar        = "reativar"
	ModoReativarEAvisar = "reativar_e_avisar"
)

// Os dois tipos de retorno que passam por este caminho.
const (
	KindRetorno         = "reactivation"     // o cliente pediu ("me chama dia 10")
	KindPosExperimental = "pos_experimental" // N dias depois da aula experimental
)

// Limites da configuração — os mesmos CHECKs da migration 0042.
const (
	diasPosExperimentalMax = 30
	// margemVoltouAFalar — mensagem logo depois de pedir o retorno ("tá bom,
	// obrigada!") ainda é a mesma conversa, não "voltou a falar".
	margemVoltouAFalar = 6 * time.Hour
	// tamanhoMaxReativacao — mensagem do modelo acima disto desandou. Não manda.
	tamanhoMaxReativacao = 600
)

func modoFollowupValido(m string) bool {
	return m == ModoAvisar || m == ModoReativar || m == ModoReativarEAvisar
}

// validaFollowupPatch confere os campos de follow-up de um PATCH /api/config.
// nil = não veio (preserva). Responsável 0 = volta ao padrão do ambiente.
func validaFollowupPatch(modo *string, dias *int, responsavel *int) error {
	if modo != nil && !modoFollowupValido(*modo) {
		return fmt.Errorf("modo de follow-up inválido %q (use avisar, reativar ou reativar_e_avisar)", *modo)
	}
	if dias != nil && (*dias < 0 || *dias > diasPosExperimentalMax) {
		return fmt.Errorf("dias depois da experimental precisa estar entre 0 e %d", diasPosExperimentalMax)
	}
	if responsavel != nil && *responsavel < 0 {
		return errors.New("responsável inválido")
	}
	return nil
}

// responsavelEfetivo — a conta escolhida na tela; sem escolha (0), a do ambiente.
func responsavelEfetivo(daTela, doAmbiente int) int {
	if daTela > 0 {
		return daTela
	}
	return doAmbiente
}

// condicoesRetorno — o estado da conversa no momento do disparo.
type condicoesRetorno struct {
	BotLigado           bool // o bot responde nesta conversa
	EmHandoff           bool // conversa passada para um humano
	CanalOficial        bool // API oficial da Meta (tem janela de 24h)
	DentroDaJanela      bool // o cliente falou nas últimas 24h
	ClienteVoltouAFalar bool // falou depois de pedir o retorno
}

// condicoesDoRetorno lê as condições a partir do retorno pendente.
// evolutionBotLigado é o toggle global do número não-oficial: lá, é ele que
// decide se o bot responde, não a flag da conversa sozinha.
func condicoesDoRetorno(p retornoPendente, agora time.Time, evolutionBotLigado bool) condicoesRetorno {
	c := condicoesRetorno{
		CanalOficial: p.Canal != "evolution",
		EmHandoff:    p.Estado == string(StateHandoff),
	}
	c.BotLigado = p.BotLigado && (c.CanalOficial || evolutionBotLigado)
	if u := p.UltimaDoCliente; u != nil {
		c.DentroDaJanela = agora.Sub(*u) < 24*time.Hour
		c.ClienteVoltouAFalar = p.Kind == KindRetorno && !p.CriadoEm.IsZero() &&
			u.After(p.CriadoEm.Add(margemVoltouAFalar))
	}
	return c
}

// acaoRetorno — o que fazer com um retorno. Motivo explica por que o modo
// automático caiu para "só avisar"; vai no aviso.
type acaoRetorno struct {
	Avisar   bool
	Reativar bool
	Motivo   string
}

// decideAcao aplica o modo e as travas. Modo vazio ou desconhecido = avisar.
func decideAcao(modo string, p retornoPendente, c condicoesRetorno) acaoRetorno {
	if modo != ModoReativar && modo != ModoReativarEAvisar {
		return acaoRetorno{Avisar: true}
	}
	if motivo := travaDaReativacao(p, c); motivo != "" {
		return acaoRetorno{Avisar: true, Motivo: motivo}
	}
	return acaoRetorno{Reativar: true, Avisar: modo == ModoReativarEAvisar}
}

func travaDaReativacao(p retornoPendente, c condicoesRetorno) string {
	switch {
	case !c.BotLigado:
		return "o bot está desligado nesta conversa"
	case c.EmHandoff:
		return "a conversa está com um humano"
	case c.ClienteVoltouAFalar:
		return "a pessoa voltou a falar depois de pedir o retorno"
	case c.CanalOficial && !c.DentroDaJanela:
		return "no número oficial, fora da janela de 24h o WhatsApp só aceita modelo aprovado"
	case p.Confianca > 0 && p.Confianca < confiancaDataFirme:
		return "a data é aproximada (a pessoa não cravou o dia)"
	}
	return ""
}

// avisoComAcao — o aviso ao humano, dizendo o que o bot fez (ou por que não fez).
func avisoComAcao(p retornoPendente, painel string, a acaoRetorno, enviado string) string {
	base := avisoDeRetorno(p, painel)
	switch {
	case a.Reativar && enviado != "":
		return fmt.Sprintf("🤖 *Reativei* %s. Mandei: _\"%s\"_\nSe a pessoa responder, a conversa segue com o bot.\n\n%s", p.quem(), enviado, base)
	case a.Motivo != "":
		return fmt.Sprintf("%s\n\n⚠️ Não reativei sozinho: %s. Chame você.", base, a.Motivo)
	}
	return base
}

// promptDeReativacao — o pedido ao modelo para a mensagem de retorno.
func promptDeReativacao(p retornoPendente, cfg TenantConfig) string {
	var b strings.Builder
	nome := cfg.BotName
	if nome == "" {
		nome = "o atendente"
	}
	fmt.Fprintf(&b, "Você é %s, atendente de uma escola de tecnologia no WhatsApp. ", nome)
	b.WriteString("Escreva UMA mensagem curta (no máximo duas frases) para retomar a conversa")
	if p.Nome != "" {
		fmt.Fprintf(&b, " com %s", p.Nome)
	}
	b.WriteString(".\n\nPor que você está chamando agora: ")
	switch p.Kind {
	case KindPosExperimental:
		b.WriteString("a pessoa fez uma aula experimental")
		if p.AulaEm != nil {
			fmt.Fprintf(&b, " em %s", p.AulaEm.In(brLocation).Format("02/01"))
		}
		b.WriteString(" e ainda não decidiu se vai se matricular. Pergunte como foi a experiência.")
	default:
		b.WriteString("a pessoa pediu para ser chamada nesta data")
		if p.Frase != "" {
			fmt.Fprintf(&b, " e disse: \"%s\"", p.Frase)
		}
		b.WriteString(".")
	}
	if p.Resumo != "" {
		fmt.Fprintf(&b, "\n\nResumo da conversa até aqui: %s", p.Resumo)
	}
	b.WriteString("\n\nRegras: português do Brasil, tom natural e gentil, sem pressão; " +
		"não invente preços, datas, promoções ou qualquer informação que não está acima; " +
		"termine com uma pergunta simples. Responda só com o texto da mensagem, sem aspas.")
	return b.String()
}

// limpaMensagemDoModelo tira aspas e espaços; vazia ou longa demais vira "",
// e aí o retorno cai para "avisar".
func limpaMensagemDoModelo(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"“”")
	s = strings.TrimSpace(s)
	if len([]rune(s)) > tamanhoMaxReativacao {
		return ""
	}
	return s
}

// responsavelDoRetorno — quem recebe o retorno: o responsável do lead no CRM,
// senão o padrão da tela, senão o do ambiente. 0 = ninguém.
func responsavelDoRetorno(doLead, daTela, doAmbiente int) int {
	if doLead > 0 {
		return doLead
	}
	return responsavelEfetivo(daTela, doAmbiente)
}

// avisoTituloMax — o mesmo limite do POST /avisos do api-go.
const avisoTituloMax = 120

// textoParaAvisoDaConta — título e corpo para o sino e o e-mail. Sem a
// marcação do WhatsApp (*negrito*, _itálico_), que lá vira sujeira.
func textoParaAvisoDaConta(p retornoPendente, aviso string) (titulo, corpo string) {
	titulo = "Hoje é dia de retomar: " + p.quem()
	if r := []rune(titulo); len(r) > avisoTituloMax {
		titulo = string(r[:avisoTituloMax-1]) + "…"
	}
	corpo = strings.NewReplacer("*", "", "_", "").Replace(aviso)
	return titulo, strings.TrimSpace(corpo)
}
