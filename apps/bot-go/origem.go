package main

import (
	"fmt"
	"strings"
)

// De onde o lead veio.
//
// POR QUE ISTO EXISTE. A escola paga anúncio, mantém blog, aparece no Google e
// tem um perfil no Instagram, e hoje não sabe qual desses traz matrícula. Sem
// origem, "investir mais em anúncio" e "escrever mais no blog" são a mesma
// aposta no escuro.
//
// TRÊS CAMINHOS, NESTA ORDEM DE CONFIANÇA:
//
//  1. O anúncio conta sozinho. Quem clica num Click-to-WhatsApp chega com o
//     bloco `referral` da Meta — id do anúncio, título, rede. É fato, não
//     resposta, e é o único que não depende de ninguém colaborar. Só vem na
//     PRIMEIRA mensagem: se não for lido ali, some para sempre.
//  2. O link conta pela mensagem que ele escreve. Um `wa.me` pode vir com texto
//     pronto; se cada lugar onde a escola publica o link usar um texto
//     diferente, a primeira mensagem identifica a origem sem ninguém perguntar.
//     Os marcadores ficam na config do tenant, não aqui — a escola muda o texto
//     de um link sem esperar deploy.
//  3. O bot pergunta. Último recurso, uma vez só, e no meio da conversa —
//     nunca como primeira pergunta, que é quando soa a formulário.
//
// O vocabulário é fechado de propósito. Origem em texto livre vira dez grafias
// da mesma coisa e não soma em relatório nenhum.

var origensValidas = map[string]string{
	"anuncio_instagram": "anúncio no Instagram",
	"anuncio_facebook":  "anúncio no Facebook",
	"anuncio":           "anúncio (rede não identificada)",
	"instagram":         "perfil no Instagram",
	"google":            "busca no Google",
	"site":              "site da escola",
	"blog":              "blog",
	"indicacao":         "indicação de alguém",
	"passou_na_frente":  "passou em frente à escola",
	"ja_conhecia":       "já conhecia ou já estudou aqui",
	"outro":             "outro",
}

// OrigemLegivel devolve a origem em português para o painel e para o dossiê.
// Origem desconhecida volta vazia em vez de virar "outro": não saber e ter
// respondido "outro" são coisas diferentes, e misturar as duas estraga a conta.
func OrigemLegivel(origem string) string {
	return origensValidas[strings.TrimSpace(strings.ToLower(origem))]
}

// OrigemValida — o modelo só pode gravar origem que exista no vocabulário.
func OrigemValida(origem string) bool {
	_, ok := origensValidas[strings.TrimSpace(strings.ToLower(origem))]
	return ok
}

// origemDoAnuncio traduz a rede a partir da URL do anúncio. A Meta manda
// `source_url` apontando para o post ou para a página da campanha; o domínio é
// o que diz se foi Instagram ou Facebook. Quando não dá para saber, fica
// "anuncio" — que ainda separa tráfego pago de orgânico, que é a divisão que
// mais importa.
func origemDoAnuncio(sourceURL string) string {
	u := strings.ToLower(sourceURL)
	switch {
	case strings.Contains(u, "instagram."):
		return "anuncio_instagram"
	case strings.Contains(u, "facebook.") || strings.Contains(u, "fb."):
		return "anuncio_facebook"
	default:
		return "anuncio"
	}
}

// detalheDoAnuncio monta a linha que a coordenação lê no painel: qual anúncio
// trouxe esta pessoa. O id sozinho não diz nada para quem não abre o Gerenciador
// de Anúncios, então o título vem junto quando a Meta manda.
func detalheDoAnuncio(sourceType, sourceID, headline, sourceURL string) string {
	tipo := "anúncio"
	if strings.EqualFold(sourceType, "post") {
		tipo = "publicação"
	}
	partes := []string{tipo}
	if h := strings.TrimSpace(headline); h != "" {
		partes = append(partes, fmt.Sprintf("%q", h))
	}
	if id := strings.TrimSpace(sourceID); id != "" {
		partes = append(partes, "id "+id)
	}
	if len(partes) == 1 && strings.TrimSpace(sourceURL) != "" {
		partes = append(partes, sourceURL)
	}
	return strings.Join(partes, " ")
}

// origemDaChegada aplica ao dossiê o que o CANAL contou sobre a procedência —
// antes de o modelo entrar na conversa.
//
// Tem que rodar aqui, e não depois da resposta: o bloco `referral` da Meta vem
// só na primeira mensagem, e um erro qualquer no meio do turno faria a origem se
// perder sem segunda chance. Gravar cedo custa nada e é irrecuperável se tarde.
func origemDaChegada(q Qualificacao, inbound InboundMessage, cfg TenantConfig, primeiraMensagem bool) Qualificacao {
	// 1. O anúncio conta sozinho — é a fonte mais forte que existe.
	if inbound.Origem != "" {
		return q.comOrigem(inbound.Origem, inbound.OrigemDetalhe, "anuncio")
	}
	// 2. O texto pronto do link. Só na primeira mensagem: "vim pelo site" dito
	//    no meio de uma conversa é assunto, não procedência.
	if primeiraMensagem && q.Origem == "" && len(cfg.OrigemMarcadores) > 0 {
		texto := inbound.Content.Text
		if texto == "" && inbound.Content.Transcript != nil {
			texto = *inbound.Content.Transcript
		}
		if origem, detalhe := origemPorMarcador(texto, cfg.OrigemMarcadores); origem != "" {
			return q.comOrigem(origem, detalhe, "marcador")
		}
	}
	// 3. Sobrou perguntar. Quem faz isso é o prompt, uma vez só e de leve.
	return q
}

// MarcadorOrigem — um texto pronto de link `wa.me` e a origem que ele identifica.
type MarcadorOrigem struct {
	Marcador string `json:"marcador"` // trecho que a primeira mensagem contém
	Origem   string `json:"origem"`   // chave de origensValidas
}

// origemPorMarcador procura, no texto da PRIMEIRA mensagem, um dos marcadores
// configurados pela escola.
//
// Só na primeira mensagem de propósito: "vim pelo site" dito no meio de uma
// conversa é assunto, não procedência. E a comparação é por conteúdo
// normalizado — o cliente pode editar o texto pronto antes de enviar, e
// costuma editar.
func origemPorMarcador(texto string, marcadores []MarcadorOrigem) (origem, detalhe string) {
	alvo := normalizaMarcador(texto)
	if alvo == "" {
		return "", ""
	}
	for _, m := range marcadores {
		mm := normalizaMarcador(m.Marcador)
		if mm == "" || !OrigemValida(m.Origem) {
			continue
		}
		if strings.Contains(alvo, mm) {
			return strings.ToLower(strings.TrimSpace(m.Origem)), "link com texto pronto"
		}
	}
	return "", ""
}

// normalizaMarcador tira acento, caixa e pontuação, e colapsa espaço. Reaproveita
// a mesma tabela de acentos do casamento de áudio: são as mesmas cinco vogais.
func normalizaMarcador(s string) string {
	s = semAcento.Replace(strings.ToLower(s))
	var b strings.Builder
	espaco := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if espaco && b.Len() > 0 {
				b.WriteByte(' ')
			}
			espaco = false
			b.WriteRune(r)
		default:
			espaco = true
		}
	}
	return b.String()
}
