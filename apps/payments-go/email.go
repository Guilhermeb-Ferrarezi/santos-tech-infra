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
// manualmente no dashboard. Layout branded com um único CTA para a tela de pagamento
// (payURL = {PUBLIC_PAY_URL}/pay/{token}), onde estão o QR Code e o copia-e-cola.
// O comprovante de confirmação é outro e-mail (paymentReceiptEmailHTML), no pagamento.
func chargeEmailHTML(payerName string, amountCents int64, dueDate, description, payURL string) string {
	reais := float64(amountCents) / 100
	name := "cliente"
	if strings.TrimSpace(payerName) != "" {
		name = html.EscapeString(payerName)
	}
	venc := dueDate
	if d, err := time.Parse("2006-01-02", dueDate); err == nil {
		venc = d.Format("02/01/2006")
	}
	descPart := ""
	if strings.TrimSpace(description) != "" {
		descPart = " — " + html.EscapeString(description)
	}
	return fmt.Sprintf(`<div style="background:#F5F8FA;padding:24px 0;font-family:Arial,Helvetica,sans-serif">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0">
   <tr><td align="center">
    <table role="presentation" width="480" cellpadding="0" cellspacing="0" style="max-width:480px">
     <tr><td style="background:#0E2937;border-radius:12px 12px 0 0;padding:20px 28px">
       <span style="color:#ffffff;font-size:18px;font-weight:bold;letter-spacing:.3px">Santos Tech</span>
     </td></tr>
     <tr><td style="background:#ffffff;padding:28px;border:1px solid #e3eaef;border-top:none;border-radius:0 0 12px 12px">
       <p style="margin:0 0 16px;color:#212121;font-size:15px">Olá, <b>%s</b>!</p>
       <p style="margin:0 0 2px;color:#496B84;font-size:13px">Cobrança PIX%s</p>
       <p style="margin:0 0 2px;color:#0E2937;font-size:34px;font-weight:bold;line-height:1.1">R$ %.2f</p>
       <p style="margin:0 0 24px;color:#496B84;font-size:13px">Vencimento: <b>%s</b></p>
       <table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 20px">
        <tr><td style="border-radius:8px;background:#0DB88F">
          <a href="%s" style="display:inline-block;padding:14px 36px;color:#ffffff;font-size:15px;font-weight:bold;text-decoration:none">Pagar com PIX</a>
        </td></tr>
       </table>
       <p style="margin:0;color:#496B84;font-size:12px;line-height:1.5">No botão acima você vê o <b>QR Code</b> e o <b>copia e cola</b> para pagar pelo app do seu banco. Assim que o pagamento for confirmado, você recebe o comprovante por e-mail.</p>
     </td></tr>
     <tr><td style="padding:16px 28px;text-align:center">
       <p style="margin:0;color:#9bb0bd;font-size:11px">Equipe Santos Tech · <a href="https://santos-tech.com/privacidade" style="color:#9bb0bd">Privacidade</a> · <a href="https://santos-tech.com/termos" style="color:#9bb0bd">Termos</a></p>
     </td></tr>
    </table>
   </td></tr>
  </table>
</div>`, name, descPart, reais, venc, html.EscapeString(payURL))
}

// periodicityLabelPT — rótulo curto em português da periodicidade da assinatura.
func periodicityLabelPT(p string) string {
	switch p {
	case "SEMANAL":
		return "por semana"
	case "MENSAL":
		return "por mês"
	case "TRIMESTRAL":
		return "por trimestre"
	case "SEMESTRAL":
		return "por semestre"
	case "ANUAL":
		return "por ano"
	default:
		return ""
	}
}

// subscriptionEmailHTML — e-mail de AUTORIZAÇÃO de assinatura (PIX Automático) enviado quando
// o admin gera a assinatura no dashboard. O CTA leva à tela onde o cliente autoriza o débito
// recorrente no app do banco — NÃO é um pagamento. authURL = {PUBLIC_PAY_URL}/subscribe/{token}.
func subscriptionEmailHTML(payerName string, amountCents int64, periodicity string, dueDay int, authURL string) string {
	reais := float64(amountCents) / 100
	name := "cliente"
	if strings.TrimSpace(payerName) != "" {
		name = html.EscapeString(payerName)
	}
	per := periodicityLabelPT(periodicity)
	return fmt.Sprintf(`<div style="background:#F5F8FA;padding:24px 0;font-family:Arial,Helvetica,sans-serif">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0">
   <tr><td align="center">
    <table role="presentation" width="480" cellpadding="0" cellspacing="0" style="max-width:480px">
     <tr><td style="background:#0E2937;border-radius:12px 12px 0 0;padding:20px 28px">
       <span style="color:#ffffff;font-size:18px;font-weight:bold;letter-spacing:.3px">Santos Tech</span>
     </td></tr>
     <tr><td style="background:#ffffff;padding:28px;border:1px solid #e3eaef;border-top:none;border-radius:0 0 12px 12px">
       <p style="margin:0 0 16px;color:#212121;font-size:15px">Olá, <b>%s</b>!</p>
       <p style="margin:0 0 2px;color:#496B84;font-size:13px">Assinatura via PIX Automático</p>
       <p style="margin:0 0 2px;color:#0E2937;font-size:34px;font-weight:bold;line-height:1.1">R$ %.2f <span style="font-size:15px;font-weight:normal;color:#496B84">%s</span></p>
       <p style="margin:0 0 24px;color:#496B84;font-size:13px">Débito automático todo dia <b>%d</b></p>
       <table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 20px">
        <tr><td style="border-radius:8px;background:#0DB88F">
          <a href="%s" style="display:inline-block;padding:14px 36px;color:#ffffff;font-size:15px;font-weight:bold;text-decoration:none">Autorizar assinatura</a>
        </td></tr>
       </table>
       <p style="margin:0;color:#496B84;font-size:12px;line-height:1.5">No botão acima você <b>autoriza</b> o débito recorrente no app do seu banco (não é uma cobrança agora). Após autorizar, os pagamentos acontecem automaticamente a cada ciclo e você recebe o comprovante de cada um por e-mail.</p>
     </td></tr>
     <tr><td style="padding:16px 28px;text-align:center">
       <p style="margin:0;color:#9bb0bd;font-size:11px">Equipe Santos Tech · <a href="https://santos-tech.com/privacidade" style="color:#9bb0bd">Privacidade</a> · <a href="https://santos-tech.com/termos" style="color:#9bb0bd">Termos</a></p>
     </td></tr>
    </table>
   </td></tr>
  </table>
</div>`, name, reais, per, dueDay, html.EscapeString(authURL))
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
