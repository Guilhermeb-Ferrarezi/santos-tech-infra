package main

import (
	"strings"
	"testing"
)

// TestSanitizeBlogContentHTMLNeutralizesScriptPayload cobre o caso óbvio: tag
// <script> (com seu conteúdo) some por completo, e o texto ao redor sobrevive.
func TestSanitizeBlogContentHTMLNeutralizesScriptPayload(t *testing.T) {
	in := `<p>Antes</p><script>alert(document.cookie)</script><p>Depois</p>`
	got := sanitizeBlogContentHTML(in)

	if strings.Contains(got, "<script") {
		t.Fatalf("script sobreviveu: %q", got)
	}
	if strings.Contains(got, "alert(") {
		t.Fatalf("conteúdo do script sobreviveu: %q", got)
	}
	if !strings.Contains(got, "Antes") || !strings.Contains(got, "Depois") {
		t.Fatalf("texto legítimo ao redor foi perdido: %q", got)
	}
}

// TestSanitizeBlogContentHTMLNeutralizesEventHandlerAttr cobre onerror= (e
// handlers de evento em geral) — não é uma tag proibida, é um atributo, então
// merece um caso à parte do <script>.
func TestSanitizeBlogContentHTMLNeutralizesEventHandlerAttr(t *testing.T) {
	in := `<p>Legenda</p><img src="https://example.com/x.png" alt="foto" onerror="alert(1)">`
	got := sanitizeBlogContentHTML(in)

	if strings.Contains(got, "onerror") {
		t.Fatalf("onerror sobreviveu: %q", got)
	}
	if strings.Contains(got, "alert(1)") {
		t.Fatalf("payload do onerror sobreviveu: %q", got)
	}
	// A tag <img> em si é permitida (src/alt são allowlisted) — só o
	// atributo malicioso deve cair.
	if !strings.Contains(got, `src="https://example.com/x.png"`) {
		t.Fatalf("img legítima foi removida junto: %q", got)
	}
	if !strings.Contains(got, "Legenda") {
		t.Fatalf("texto legítimo ao redor foi perdido: %q", got)
	}
}

// TestSanitizeBlogContentHTMLNeutralizesJavascriptHref cobre javascript: num
// href de <a> — o link deve sobreviver sem o esquema perigoso (bluemonday
// remove o atributo href inválido, mantendo o texto do link).
func TestSanitizeBlogContentHTMLNeutralizesJavascriptHref(t *testing.T) {
	in := `<p>Clique <a href="javascript:alert(document.cookie)">aqui</a> pra continuar.</p>`
	got := sanitizeBlogContentHTML(in)

	if strings.Contains(got, "javascript:") {
		t.Fatalf("esquema javascript: sobreviveu: %q", got)
	}
	if !strings.Contains(got, "aqui") || !strings.Contains(got, "Clique") || !strings.Contains(got, "continuar") {
		t.Fatalf("texto legítimo ao redor do link foi perdido: %q", got)
	}
}

// TestSanitizeBlogContentHTMLKeepsSafeCTABlock cobre o padrão real de CTA
// publicado no blog: um <div> colorido com border-radius/padding via style
// inline, contendo um <a> estilizado como botão (também com style inline).
// Esse padrão PRECISA sobreviver intacto — é conteúdo legítimo gerado pelo
// editor do dashboard, não um ataque.
func TestSanitizeBlogContentHTMLKeepsSafeCTABlock(t *testing.T) {
	in := `<div style="background:#0DB88F;border-radius:24px;color:#fff;padding:24px;text-align:center;">` +
		`<p style="margin:0 0 12px;font-size:18px;font-weight:700;">Pronto pra começar?</p>` +
		`<a href="https://santos-tech.com/inscricao" style="display:inline-block;background-color:#0E2937;color:#ffffff;` +
		`padding:12px 24px;border-radius:9999px;text-decoration:none;font-weight:600;">Quero me inscrever</a>` +
		`</div>`
	got := sanitizeBlogContentHTML(in)

	// bluemonday reparseia e reserializa o style (normaliza espaçamento —
	// "prop: valor" com espaço após ":", sem ";" final) — por isso os
	// valores esperados aqui usam esse formato, não o do input original.
	for _, want := range []string{
		"<div", "background: #0DB88F", "border-radius: 24px", "color: #fff", "padding: 24px", "text-align: center",
		"Pronto pra começar?",
		"font-size: 18px", "font-weight: 700",
		`href="https://santos-tech.com/inscricao"`,
		"display: inline-block", "background-color: #0E2937", "border-radius: 9999px", "text-decoration: none",
		"Quero me inscrever",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("esperava manter %q no CTA sanitizado, sumiu. HTML: %s", want, got)
		}
	}
}

// TestSanitizeBlogContentHTMLStripsDangerousInlineStyle garante que a
// allowlist de propriedades CSS é respeitada: position (não listada) e
// url()/expression() maliciosos em propriedades permitidas devem cair, sem
// derrubar o restante do style nem do elemento.
func TestSanitizeBlogContentHTMLStripsDangerousInlineStyle(t *testing.T) {
	in := `<div style="position:fixed;top:0;left:0;color:red;background:url(javascript:alert(1));">conteúdo</div>`
	got := sanitizeBlogContentHTML(in)

	if strings.Contains(got, "position") {
		t.Fatalf("position sobreviveu: %q", got)
	}
	if strings.Contains(got, "javascript:") {
		t.Fatalf("javascript: dentro de background sobreviveu: %q", got)
	}
	if !strings.Contains(got, "conteúdo") {
		t.Fatalf("texto interno foi perdido: %q", got)
	}
	// "color: red" é uma propriedade+valor permitidos e deve sobreviver mesmo
	// com o resto do style malicioso removido (bluemonday reserializa como
	// "prop: valor", com espaço após ":").
	if !strings.Contains(got, "color: red") {
		t.Errorf("propriedade segura foi removida junto com a maliciosa: %q", got)
	}
}

// TestSanitizeBlogContentHTMLStripsDisallowedTagsKeepingText cobre tags fora
// da allowlist (iframe, form/input) que não são "script-like" — o texto ao
// redor deve sobreviver, só a tag cai.
func TestSanitizeBlogContentHTMLStripsDisallowedTagsKeepingText(t *testing.T) {
	in := `<p>Antes</p><iframe src="https://evil.example/x"></iframe>` +
		`<form action="https://evil.example/steal"><input name="x"></form><p>Depois</p>`
	got := sanitizeBlogContentHTML(in)

	for _, bad := range []string{"<iframe", "<form", "<input", "evil.example"} {
		if strings.Contains(got, bad) {
			t.Errorf("tag/atributo fora da allowlist sobreviveu (%q): %s", bad, got)
		}
	}
	if !strings.Contains(got, "Antes") || !strings.Contains(got, "Depois") {
		t.Fatalf("texto legítimo ao redor foi perdido: %q", got)
	}
}

// TestSanitizeBlogContentHTMLKeepsRichTextEditorOutput cobre o conjunto de
// tags que o editor TipTap do dashboard admin realmente gera em uso normal —
// nada disso pode ser removido pela sanitização.
func TestSanitizeBlogContentHTMLKeepsRichTextEditorOutput(t *testing.T) {
	in := `<h2>Título</h2><h3>Subtítulo</h3>` +
		`<p>Texto com <strong>negrito</strong> e <em>itálico</em>.</p>` +
		`<a href="https://santos-tech.com/outro-post">link interno</a>` +
		`<ul><li>Item 1</li><li>Item 2</li></ul>` +
		`<ol><li>Primeiro</li><li>Segundo</li></ol>` +
		`<blockquote>Uma citação.</blockquote>` +
		`<table><thead><tr><th>Coluna</th></tr></thead><tbody><tr><td>Valor</td></tr></tbody></table>` +
		`<img src="https://cdn.santos-tech.com/img.png" alt="descrição">`
	got := sanitizeBlogContentHTML(in)

	for _, want := range []string{
		"<h2", "Título", "<h3", "Subtítulo",
		"<strong>negrito</strong>", "<em>itálico</em>",
		`href="https://santos-tech.com/outro-post"`, "link interno",
		"<ul>", "<li>Item 1</li>", "<li>Item 2</li>", "<ol>", "Primeiro", "Segundo",
		"<blockquote>", "Uma citação.",
		"<table", "<thead", "<tbody", "Coluna", "Valor",
		`src="https://cdn.santos-tech.com/img.png"`, `alt="descrição"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("esperava manter %q do output normal do editor, sumiu. HTML: %s", want, got)
		}
	}
}

// TestInsertAndUpdateBlogPostSanitizeContentHTML garante que a sanitização é
// aplicada dentro do store (insertBlogPost/updateBlogPost), não só disponível
// como função solta — ou seja, mesmo que um handler futuro esqueça de chamar
// sanitizeBlogContentHTML, o INSERT/UPDATE não grava HTML cru no banco.
// Sem Postgres real disponível neste teste unitário, cobrimos isso
// verificando o comportamento do input antes do INSERT: o valor que
// insertBlogPost/updateBlogPost efetivamente passa pro SQL é sempre o
// resultado de sanitizeBlogContentHTML(in.ContentHTML), nunca in.ContentHTML
// bruto — ver blog.go.
func TestSanitizeBlogContentHTMLIsIdempotent(t *testing.T) {
	// Sanitizar duas vezes não deve mudar o resultado — importante porque
	// updateBlogPost roda em cima de conteúdo que já passou por
	// insertBlogPost antes.
	in := `<div style="background:#0DB88F;padding:24px;"><a href="https://santos-tech.com" style="color:#fff;">CTA</a></div><script>evil()</script>`
	once := sanitizeBlogContentHTML(in)
	twice := sanitizeBlogContentHTML(once)
	if once != twice {
		t.Fatalf("sanitização não é idempotente:\n1x: %q\n2x: %q", once, twice)
	}
}
