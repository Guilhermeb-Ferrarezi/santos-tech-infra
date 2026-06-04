package main

import (
	"fmt"
	"html"
)

// Layout padrão dos emails transacionais da Santos Tech. Email de cliente é
// renderizado por engines antigas: tabela + CSS inline, nada de classes/flex.
// Paleta da marca: navy #0E2937 (header), azul #187ABF (ações), cinza #F5F8FA (fundo).

// emailLayout embrulha o corpo (já em HTML) no card padrão com header e rodapé.
func emailLayout(body string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-BR">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:0;background-color:#F5F8FA">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#F5F8FA;padding:32px 16px">
<tr><td align="center">
<table role="presentation" width="480" cellpadding="0" cellspacing="0" style="max-width:480px;width:100%%;background-color:#FFFFFF;border-radius:12px;overflow:hidden;border:1px solid #E2E8F0">
<tr><td style="background-color:#0E2937;padding:20px 32px">
<span style="font-family:Arial,Helvetica,sans-serif;font-size:18px;font-weight:bold;color:#FFFFFF">Santos&nbsp;Tech</span>
</td></tr>
<tr><td style="padding:32px;font-family:Arial,Helvetica,sans-serif;font-size:15px;line-height:1.6;color:#212121">
%s
</td></tr>
<tr><td style="padding:20px 32px;border-top:1px solid #E2E8F0">
<p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:12px;line-height:1.5;color:#496B84">
Email automático — não responda.<br>© Santos Tech · santos-tech.com
</p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`, body)
}

// emailButton gera o CTA primário (azul da marca, link em aba nova).
func emailButton(url, label string) string {
	return fmt.Sprintf(`<table role="presentation" cellpadding="0" cellspacing="0" style="margin:24px 0">
<tr><td style="border-radius:8px;background-color:#187ABF">
<a href="%s" target="_blank" style="display:inline-block;padding:12px 28px;font-family:Arial,Helvetica,sans-serif;font-size:15px;font-weight:bold;color:#FFFFFF;text-decoration:none">%s</a>
</td></tr>
</table>`, url, html.EscapeString(label))
}

// emailLinkFallback mostra a URL crua para quem não consegue clicar no botão.
func emailLinkFallback(url string) string {
	return fmt.Sprintf(`<p style="margin:16px 0 0;font-size:12px;color:#496B84">Se o botão não funcionar, copie e cole este link no navegador:<br><a href="%[1]s" target="_blank" style="color:#187ABF;word-break:break-all">%[1]s</a></p>`, url)
}

// emailCodeBox destaca um código de verificação.
func emailCodeBox(code string) string {
	return fmt.Sprintf(`<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="margin:24px 0">
<tr><td align="center" style="background-color:#F5F8FA;border:1px solid #E2E8F0;border-radius:8px;padding:16px">
<span style="font-family:'Courier New',monospace;font-size:28px;font-weight:bold;letter-spacing:6px;color:#0E2937">%s</span>
</td></tr>
</table>`, html.EscapeString(code))
}

// emailGreeting monta a saudação escapando o nome (vem de input do usuário).
func emailGreeting(name string) string {
	if name == "" {
		return "<p style=\"margin:0 0 16px\">Olá!</p>"
	}
	return fmt.Sprintf("<p style=\"margin:0 0 16px\">Olá, <strong>%s</strong>!</p>", html.EscapeString(name))
}

// emailCodeHTML é o corpo padrão dos emails de código (MFA, verificação).
func emailCodeHTML(code, purpose string) string {
	return emailLayout(fmt.Sprintf(`<p style="margin:0 0 8px">%s</p>
%s
<p style="margin:0;color:#496B84;font-size:13px">O código expira em <strong>10 minutos</strong> e vale uma única vez. Se não foi você, ignore este email.</p>`,
		purpose, emailCodeBox(code)))
}
