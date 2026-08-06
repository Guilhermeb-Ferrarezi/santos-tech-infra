package main

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// blogContentPolicy é a allowlist de HTML aceita em content_html de posts do
// blog. Parte de bluemonday.UGCPolicy(), que já cobre com segurança tudo que
// o editor rich-text (TipTap) do dashboard admin gera: h1–h6, p, br, hr,
// strong/em e demais tags de ênfase, blockquote[cite], listas (ul/ol/li),
// tabelas completas (table/thead/tbody/tr/th/td), a[href] (com validação de
// esquema — só http/https/mailto, então "javascript:" nunca passa) e
// img[src][alt][width][height].
//
// Estendemos só o necessário pro conteúdo real já publicado (conferido contra
// os posts em produção antes de fechar esta policy):
//
//  1. CTA colorido (ex.: um <div style="background:#0DB88F;border-radius:24px;...">
//     com um <a> estilizado como botão dentro): permitimos `style` em
//     div/a/span/p, restrito às propriedades abaixo. Cada valor ainda passa
//     pelos handlers padrão do bluemonday (ColorHandler, PaddingHandler,
//     DisplayHandler, ...), que recusam `url()`, `expression()` e qualquer
//     coisa fora do formato esperado — então mesmo essas propriedades não
//     abrem brecha pra CSS/JS malicioso. Propriedades perigosas (position,
//     url() sem restrição de esquema, etc.) não entram na lista e por isso já
//     saem removidas.
//  2. target="_blank" + rel="noopener noreferrer" em <a> — padrão usado em
//     todo link externo dos posts reais (fontes, CTA de contato). Sem isso o
//     UGCPolicy padrão derruba os dois atributos (e ainda troca por
//     rel="nofollow"), o que muda comportamento visível: o link deixa de
//     abrir em nova aba. target só aceita _blank/_self; rel só aceita tokens
//     de uma lista segura — nem um nem outro carregam URL/script.
//  3. alt de <img> com pontuação comum (ex.: dois-pontos) — o Paragraph
//     regex padrão do bluemonday (usado por AllowImages()) rejeita ":" e
//     descarta o atributo INTEIRO quando não casa 100%, o que já aconteceu
//     com alt text real em produção. alt é sempre texto simples (a
//     serialização de atributo do html/x/net escapa tudo), então não é vetor
//     de XSS — só restringimos pra não permitir quebrar pra fora do atributo.
var blogContentPolicy = newBlogContentPolicy()

// altText é mais permissivo que o Paragraph padrão do bluemonday (que
// rejeita ":" e derruba o alt inteiro) — aceita qualquer texto sem os
// caracteres que poderiam confundir o parser de atributos.
var altText = regexp.MustCompile(`^[^<>]*$`)

// linkTarget e linkRel restringem target/rel de <a> ao padrão seguro
// target="_blank" rel="noopener noreferrer" (mais nofollow/ugc, que o
// próprio bluemonday às vezes adiciona).
var linkTarget = regexp.MustCompile(`^(_blank|_self)$`)
var linkRel = regexp.MustCompile(`^(noopener|noreferrer|nofollow|ugc)(\s+(noopener|noreferrer|nofollow|ugc))*$`)

func newBlogContentPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()

	// UGCPolicy() liga nofollow em todo link por padrão — faz sentido pra
	// comentário de usuário anônimo, mas não é esse o caso aqui:
	// content_html só é gravado por staff com blog_posts:write (sem caminho
	// anônimo, ver blog.go/handlers_blog.go), então não é conteúdo
	// não-confiável no sentido que a proteção assume. Manter nofollow
	// penalizaria de graça tanto os links internos (blog ↔ site
	// institucional) quanto as citações externas legítimas dos posts (SBP,
	// legislação) — o oposto do sinal de autoridade/confiança que essas
	// citações deveriam passar pros buscadores.
	p.RequireNoFollowOnLinks(false)

	safeStyleProps := []string{
		"background", "background-color", "color", "border-radius",
		"padding", "margin", "text-align", "display", "font-weight",
		"font-size", "text-decoration",
	}
	p.AllowStyles(safeStyleProps...).OnElements("div", "a", "span", "p")

	p.AllowAttrs("target").Matching(linkTarget).OnElements("a")
	p.AllowAttrs("rel").Matching(linkRel).OnElements("a")
	p.AllowAttrs("alt").Matching(altText).OnElements("img")

	return p
}

// sanitizeBlogContentHTML aplica a allowlist acima e devolve HTML seguro pra
// gravar no banco. Chamada sempre em insertBlogPost/updateBlogPost — nunca
// confiamos no que o handler já validou (validateBlogPostInput não toca
// content_html), então isso é a última linha de defesa contra HTML
// malicioso vindo do editor, de uma chamada direta à API ou de qualquer
// outro client futuro.
func sanitizeBlogContentHTML(html string) string {
	return blogContentPolicy.Sanitize(html)
}
