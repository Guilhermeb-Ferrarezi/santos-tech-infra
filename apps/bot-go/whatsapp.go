package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// WhatsAppSender envia mensagens via Meta Cloud API.
type WhatsAppSender struct {
	accessToken   string
	phoneNumberID string
	http          *http.Client
}

// NewWhatsAppSender cria um sender com timeout padrão de 30s.
func NewWhatsAppSender(accessToken, phoneNumberID string) *WhatsAppSender {
	return &WhatsAppSender{
		accessToken:   accessToken,
		phoneNumberID: phoneNumberID,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

// SendMessage envia uma OutboundMessage para o destinatário via WhatsApp.
func (s *WhatsAppSender) SendMessage(ctx context.Context, msg OutboundMessage) (string, error) {
	var body map[string]any
	if msg.Intent == IntentStructuredReengagement {
		body = templateToBody(msg)
	} else {
		body = contentToBody(msg.Content)
	}

	body["messaging_product"] = "whatsapp"
	body["recipient_type"] = "individual"
	body["to"] = msg.To

	return s.post(ctx, body)
}

// SendText envia uma mensagem de texto avulso (notificações admin).
func (s *WhatsAppSender) SendText(ctx context.Context, to, text string) error {
	body := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                to,
		"type":              "text",
		"text": map[string]any{
			"body":        text,
			"preview_url": false,
		},
	}

	_, err := s.post(ctx, body)
	return err
}

// SendTypingIndicator marca a mensagem como lida e exibe o indicador "digitando…"
// no WhatsApp do destinatário por até 25s (ou até o bot enviar a resposta).
// Best-effort: usado enquanto o LLM processa. Resposta da API é {success:true}.
func (s *WhatsAppSender) SendTypingIndicator(ctx context.Context, messageID string) error {
	if messageID == "" {
		return nil
	}
	body := map[string]any{
		"messaging_product": "whatsapp",
		"status":            "read",
		"message_id":        messageID,
		"typing_indicator":  map[string]any{"type": "text"},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("whatsapp: marshal typing: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/messages", s.phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("whatsapp: new typing request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: typing http do: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("whatsapp: typing status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

func (s *WhatsAppSender) post(ctx context.Context, body map[string]any) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("whatsapp: marshal body: %w", err)
	}

	url := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/messages", s.phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("whatsapp: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("whatsapp: http do: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whatsapp: status %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("whatsapp: unmarshal response: %w", err)
	}
	if len(result.Messages) == 0 {
		return "", fmt.Errorf("whatsapp: empty messages in response")
	}
	return result.Messages[0].ID, nil
}

// DownloadMedia baixa o conteúdo de uma mídia do WhatsApp pelo media id (lookup→bytes).
func (s *WhatsAppSender) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	return s.downloadMediaFrom(ctx, fmt.Sprintf("https://graph.facebook.com/v21.0/%s", mediaID))
}

func (s *WhatsAppSender) downloadMediaFrom(ctx context.Context, lookupURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, "", fmt.Errorf("whatsapp: media lookup status %d: %s", resp.StatusCode, string(raw))
	}
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, "", fmt.Errorf("whatsapp: media lookup decode: %w", err)
	}
	if meta.URL == "" {
		return nil, "", fmt.Errorf("whatsapp: media sem url")
	}
	dreq, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, "", err
	}
	dreq.Header.Set("Authorization", "Bearer "+s.accessToken)
	dresp, err := s.http.Do(dreq)
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media download: %w", err)
	}
	defer dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(dresp.Body, 4<<10))
		return nil, "", fmt.Errorf("whatsapp: media download status %d: %s", dresp.StatusCode, string(raw))
	}
	data, err := io.ReadAll(io.LimitReader(dresp.Body, 16<<20))
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media read: %w", err)
	}
	return data, meta.MimeType, nil
}

// UploadAudio envia bytes OGG/Opus ao endpoint de mídia do Meta e devolve o media id.
func (s *WhatsAppSender) UploadAudio(ctx context.Context, ogg []byte) (string, error) {
	return s.uploadAudioTo(ctx, fmt.Sprintf("https://graph.facebook.com/v21.0/%s/media", s.phoneNumberID), ogg)
}

func (s *WhatsAppSender) uploadAudioTo(ctx context.Context, url string, ogg []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("messaging_product", "whatsapp")
	_ = mw.WriteField("type", "audio/ogg")
	fw, err := mw.CreateFormFile("file", "voice.ogg")
	if err != nil {
		return "", fmt.Errorf("whatsapp: upload form: %w", err)
	}
	if _, err := fw.Write(ogg); err != nil {
		return "", fmt.Errorf("whatsapp: upload write: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("whatsapp: upload close: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("whatsapp: upload do: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whatsapp: upload status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("whatsapp: upload sem id: %s", string(raw))
	}
	return out.ID, nil
}

// contentToBody converte MessageContent no formato de body da Meta API.
func contentToBody(content MessageContent) map[string]any {
	switch content.Type {
	case "text":
		return map[string]any{
			"type": "text",
			"text": map[string]any{
				"body":        content.Text,
				"preview_url": false,
			},
		}
	case "image":
		img := map[string]any{}
		if content.MediaURL != nil {
			img["link"] = *content.MediaURL
		}
		if content.Caption != nil {
			img["caption"] = *content.Caption
		}
		return map[string]any{"type": "image", "image": img}
	case "audio":
		audio := map[string]any{}
		if content.MediaURL != nil {
			if id, ok := strings.CutPrefix(*content.MediaURL, "wa_media_id:"); ok {
				audio["id"] = id
			} else {
				audio["link"] = *content.MediaURL
			}
		}
		return map[string]any{"type": "audio", "audio": audio}
	case "video":
		video := map[string]any{}
		if content.MediaURL != nil {
			video["link"] = *content.MediaURL
		}
		if content.Caption != nil {
			video["caption"] = *content.Caption
		}
		return map[string]any{"type": "video", "video": video}
	case "document":
		doc := map[string]any{}
		if content.MediaURL != nil {
			doc["link"] = *content.MediaURL
		}
		return map[string]any{"type": "document", "document": doc}
	case "sticker":
		sticker := map[string]any{}
		if content.MediaURL != nil {
			sticker["link"] = *content.MediaURL
		}
		return map[string]any{"type": "sticker", "sticker": sticker}
	case "location":
		// MediaURL codifica "lat,lon" para location
		loc := map[string]any{}
		if content.MediaURL != nil {
			parts := strings.SplitN(*content.MediaURL, ",", 2)
			if len(parts) == 2 {
				loc["latitude"] = parts[0]
				loc["longitude"] = parts[1]
			}
		}
		return map[string]any{"type": "location", "location": loc}
	default:
		return map[string]any{
			"type": "text",
			"text": map[string]any{
				"body":        content.Text,
				"preview_url": false,
			},
		}
	}
}

// templateToBody monta o body para mensagens de template (reengajamento).
func templateToBody(msg OutboundMessage) map[string]any {
	tp := msg.TemplatePayload
	if tp == nil {
		tp = &TemplatePayload{Name: "reengagement", Language: "pt_BR"}
	}

	lang := tp.Language
	if lang == "" {
		lang = "pt_BR"
	}

	var parameters []map[string]any
	for _, v := range tp.Variables {
		parameters = append(parameters, map[string]any{
			"type": "text",
			"text": v,
		})
	}

	components := []map[string]any{
		{
			"type":       "body",
			"parameters": parameters,
		},
	}

	return map[string]any{
		"type": "template",
		"template": map[string]any{
			"name": tp.Name,
			"language": map[string]any{
				"code": lang,
			},
			"components": components,
		},
	}
}

// metaWebhookMessage representa uma mensagem dentro do payload da Meta.
type metaWebhookMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      *struct {
		Body string `json:"body"`
	} `json:"text"`
	Image *struct {
		ID      string `json:"id"`
		Caption string `json:"caption"`
	} `json:"image"`
	Audio *struct {
		ID string `json:"id"`
	} `json:"audio"`
	Video *struct {
		ID      string `json:"id"`
		Caption string `json:"caption"`
	} `json:"video"`
	Document *struct {
		ID string `json:"id"`
	} `json:"document"`
	Sticker *struct {
		ID string `json:"id"`
	} `json:"sticker"`
}

type metaWebhookContact struct {
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
	WaID string `json:"wa_id"`
}

type metaWebhookValue struct {
	Metadata struct {
		PhoneNumberID string `json:"phone_number_id"`
	} `json:"metadata"`
	Messages []metaWebhookMessage `json:"messages"`
	Contacts []metaWebhookContact `json:"contacts"`
}

type metaWebhookChange struct {
	Value metaWebhookValue `json:"value"`
}

type metaWebhookEntry struct {
	Changes []metaWebhookChange `json:"changes"`
}

type metaWebhookPayload struct {
	Object string             `json:"object"`
	Entry  []metaWebhookEntry `json:"entry"`
}

// ParseMetaWebhook parseia o payload da Meta Cloud API e retorna InboundMessages.
// Filtra apenas entradas cujo phone_number_id corresponde ao configurado.
func ParseMetaWebhook(body []byte, phoneNumberID string) ([]InboundMessage, error) {
	var payload metaWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("whatsapp: parse webhook: %w", err)
	}

	var msgs []InboundMessage

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			val := change.Value
			if val.Metadata.PhoneNumberID != phoneNumberID {
				continue
			}

			// Indexa contatos por wa_id para lookup de nome
			contactName := map[string]string{}
			for _, c := range val.Contacts {
				contactName[c.WaID] = c.Profile.Name
			}

			for _, m := range val.Messages {
				var ts int64
				fmt.Sscanf(m.Timestamp, "%d", &ts)
				receivedAt := time.Unix(ts, 0).UTC()

				content := MessageContent{Type: m.Type}

				switch m.Type {
				case "text":
					if m.Text != nil {
						content.Text = m.Text.Body
					}
				case "image":
					if m.Image != nil {
						mediaID := "wa_media_id:" + m.Image.ID
						content.MediaURL = &mediaID
						if m.Image.Caption != "" {
							cap := m.Image.Caption
							content.Caption = &cap
						}
					}
				case "audio":
					if m.Audio != nil {
						mediaID := "wa_media_id:" + m.Audio.ID
						content.MediaURL = &mediaID
					}
				case "video":
					if m.Video != nil {
						mediaID := "wa_media_id:" + m.Video.ID
						content.MediaURL = &mediaID
						if m.Video.Caption != "" {
							cap := m.Video.Caption
							content.Caption = &cap
						}
					}
				case "document":
					if m.Document != nil {
						mediaID := "wa_media_id:" + m.Document.ID
						content.MediaURL = &mediaID
					}
				case "sticker":
					if m.Sticker != nil {
						mediaID := "wa_media_id:" + m.Sticker.ID
						content.MediaURL = &mediaID
					}
				}

				msgs = append(msgs, InboundMessage{
					TenantID:          "",
					Channel:           "whatsapp",
					ExternalID:        m.From,
					DisplayHandle:     contactName[m.From],
					ProviderMessageID: m.ID,
					Content:           content,
					ReceivedAt:        receivedAt,
				})
			}
		}
	}

	return msgs, nil
}

// ValidateMetaHMAC verifica a assinatura HMAC-SHA256 do webhook da Meta.
// signature deve vir do header X-Hub-Signature-256 no formato "sha256=<hex>".
// Se appSecret estiver vazio (modo dev/sem credenciais), a validação é pulada.
func ValidateMetaHMAC(body []byte, signature, appSecret string) bool {
	if appSecret == "" {
		return true
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	sigHex := strings.TrimPrefix(signature, prefix)
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(expected, sigBytes)
}
