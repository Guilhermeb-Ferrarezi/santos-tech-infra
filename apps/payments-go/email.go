package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
)

// emailClient envia pela NOSSA API de emails (email-sender), não pelo Resend. Espelha o api-go.
type emailClient struct {
	url    string
	key    string
	client *http.Client
}

func newEmailClient(cfg Config) *emailClient {
	return &emailClient{url: cfg.EmailAPIURL, key: cfg.EmailAPIKey, client: &http.Client{Timeout: 20 * time.Second}}
}

func (e *emailClient) send(ctx context.Context, to, subject, html string) error {
	if e.url == "" || e.key == "" || to == "" {
		return nil // email desabilitado ou sem destinatário: não bloqueia a cobrança
	}
	body, _ := json.Marshal(map[string]string{"to": to, "subject": subject, "html": html})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", e.key)
	res, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("email api status %d: %q", res.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}

// chargeEmailHTML — e-mail de cobrança enviado ao cliente quando o admin gera o PIX
// manualmente no dashboard. Contém valor, vencimento e o copia-e-cola (PIX pra PAGAR).
// O comprovante de confirmação é outro e-mail (paymentReceiptEmailHTML), disparado no pagamento.
func chargeEmailHTML(payerName string, amountCents int64, dueDate, description, brCode string) string {
	reais := float64(amountCents) / 100
	greet := "Olá!"
	if payerName != "" {
		greet = fmt.Sprintf("Olá, %s!", html.EscapeString(payerName))
	}
	venc := dueDate
	if d, err := time.Parse("2006-01-02", dueDate); err == nil {
		venc = d.Format("02/01/2006")
	}
	desc := ""
	if strings.TrimSpace(description) != "" {
		desc = fmt.Sprintf("<p>Referente a: <b>%s</b></p>", html.EscapeString(description))
	}
	return fmt.Sprintf(`<p>%s</p>
<p>Você tem uma cobrança PIX de <b>R$ %.2f</b>.</p>
%s<p><b>Vencimento:</b> %s</p>
<p>Para pagar, copie o código PIX abaixo (copia e cola) e cole na opção "Pix Copia e Cola" do app do seu banco:</p>
<p style="word-break:break-all;background:#F5F8FA;border:1px solid #cdd9e1;border-radius:8px;padding:12px;font-family:monospace;font-size:13px">%s</p>
<p style="color:#496B84;font-size:13px">Assim que o pagamento for confirmado, você recebe o comprovante por e-mail.</p>
<p style="color:#496B84;font-size:12px">Equipe Santos Tech</p>
<p style="color:#496B84;font-size:12px"><a href="https://santos-tech.com/privacidade" style="color:#496B84">Política de Privacidade</a> · <a href="https://santos-tech.com/termos" style="color:#496B84">Termos de Uso</a></p>`,
		greet, reais, desc, venc, html.EscapeString(brCode))
}

// paymentReceiptEmailHTML — recibo enviado a quem pagou, na confirmação.
func paymentReceiptEmailHTML(payerName string, amountCents int64) string {
	reais := float64(amountCents) / 100
	greet := "Olá!"
	if payerName != "" {
		greet = fmt.Sprintf("Olá, %s!", payerName)
	}
	return fmt.Sprintf(`<p>%s</p>
<p>✅ Recebemos o seu pagamento de <b>R$ %.2f</b>. Obrigado!</p>
<p>Este é o seu comprovante de confirmação. Qualquer dúvida, é só responder este email.</p>
<p style="color:#496B84;font-size:12px">Equipe Santos Tech</p>
<p style="color:#496B84;font-size:12px"><a href="https://santos-tech.com/privacidade" style="color:#496B84">Política de Privacidade</a> · <a href="https://santos-tech.com/termos" style="color:#496B84">Termos de Uso</a></p>`, greet, reais)
}

// paymentPaidEmailHTML — aviso interno de pagamento confirmado (para o admin).
func paymentPaidEmailHTML(c *Charge) string {
	reais := float64(c.AmountCents) / 100
	payer := c.PayerName
	if payer == "" {
		payer = "—"
	}
	when := time.Now()
	if c.PaidAt != nil {
		when = *c.PaidAt
	}
	return fmt.Sprintf(`<p>✅ <b>Pagamento confirmado</b></p>
<table style="border-collapse:collapse;font-size:14px">
<tr><td style="padding:4px 12px 4px 0;color:#496B84">Valor</td><td><b>R$ %.2f</b></td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#496B84">Pagador</td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#496B84">Tipo</td><td>%s</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#496B84">Cobrança</td><td>#%d</td></tr>
<tr><td style="padding:4px 12px 4px 0;color:#496B84">Pago em</td><td>%s</td></tr>
</table>
<p style="color:#496B84;font-size:12px">Santos Tech · Pagamentos</p>
<p style="color:#496B84;font-size:12px"><a href="https://santos-tech.com/privacidade" style="color:#496B84">Política de Privacidade</a> · <a href="https://santos-tech.com/termos" style="color:#496B84">Termos de Uso</a></p>`,
		reais, payer, c.Kind, c.ID, when.Format("02/01/2006 15:04"))
}
