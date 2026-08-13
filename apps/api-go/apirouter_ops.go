package main

// Operações normalizadas do roteador de APIs (/providers/{id}/op/{op}):
// transcrever áudio, gerar voz, gerar imagem e prever via modelos — cada uma
// adaptada ao formato nativo do provider (assemblyai/deepgram/elevenlabs/
// stability/replicate). Operações assíncronas (AssemblyAI e Replicate) são
// disparadas no endpoint de criação e o resultado é buscado por polling interno
// usando a MESMA chave que criou o job.
//
// Formato de entrada normalizado (apiRouterOpInput) e de saída por op:
//   - transcribe → { text, confidence?, language?, audioDuration?, keyUsed }
//   - tts        → { audioB64, contentType, keyUsed }
//   - image      → { imageB64, finishReason?, seed?, keyUsed }
//   - predict    → { output, status, keyUsed }
//   - voices     → { voices: [{id,name}...], keyUsed }

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	opAdapterAssemblyAI = "assemblyai"
	opAdapterDeepgram   = "deepgram"
	opAdapterElevenLabs = "elevenlabs"
	opAdapterStability  = "stability"
	opAdapterReplicate  = "replicate"
)

const (
	opTranscribe = "transcribe"
	opTTS        = "tts"
	opImage      = "image"
	opPredict    = "predict"
	opVoices     = "voices"
)

// validOpAdapters é a allowlist aceita em provider.opAdapter (create/update).
var validOpAdapters = map[string]bool{
	opAdapterAssemblyAI: true,
	opAdapterDeepgram:   true,
	opAdapterElevenLabs: true,
	opAdapterStability:  true,
	opAdapterReplicate:  true,
}

// apiRouterOpCapabilities mapeia cada adapter de operação às operações que
// suporta. Vira "capabilities" no JSON do provider e alimenta a UI.
var apiRouterOpCapabilities = map[string][]string{
	opAdapterAssemblyAI: {opTranscribe},
	opAdapterDeepgram:   {opTranscribe},
	opAdapterElevenLabs: {opTTS, opTranscribe, opVoices},
	opAdapterStability:  {opImage},
	opAdapterReplicate:  {opPredict},
}

// apiRouterOpInput é o corpo normalizado aceito por qualquer /op/{op}.
type apiRouterOpInput struct {
	URL            string          `json:"url"`            // áudio remoto p/ transcrever
	AudioB64       string          `json:"audioB64"`       // áudio em base64 p/ transcrever
	AudioMime      string          `json:"audioMime"`      // mime do áudio (ex: audio/mpeg)
	Text           string          `json:"text"`           // texto p/ TTS
	Voice          string          `json:"voice"`          // voice_id p/ TTS
	Model          string          `json:"model"`          // modelo específico (opcional)
	Prompt         string          `json:"prompt"`         // image/predict
	NegativePrompt string          `json:"negativePrompt"` // image
	AspectRatio    string          `json:"aspectRatio"`    // image
	OutputFormat   string          `json:"outputFormat"`   // image (ex: png/jpeg)
	Seed           json.RawMessage `json:"seed"`           // image (número)
	Version        string          `json:"version"`        // Replicate: version do modelo
	Input          json.RawMessage `json:"input"`          // Replicate: input bruto
}

// apiRouterOpRequest é uma requisição nativa pronta para ser executada pelo
// roteador (já sem a auth — o roteador injeta a chave).
type apiRouterOpRequest struct {
	Method      string
	Path        string
	Body        []byte
	ContentType string
	Headers     map[string]string
}

// ── buildOpSubmit: monta a requisição que DISPARA a operação ────────────────

func buildOpSubmit(adapter, op string, in apiRouterOpInput) (apiRouterOpRequest, error) {
	switch adapter {
	case opAdapterDeepgram:
		return buildDeepgramSubmit(op, in)
	case opAdapterAssemblyAI:
		return buildAssemblyAISubmit(in)
	case opAdapterElevenLabs:
		return buildElevenLabsSubmit(op, in)
	case opAdapterStability:
		return buildStabilitySubmit(in)
	case opAdapterReplicate:
		return buildReplicateSubmit(in)
	default:
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: adapter de operação desconhecido %q", adapter)
	}
}

func buildDeepgramSubmit(op string, in apiRouterOpInput) (apiRouterOpRequest, error) {
	if op != opTranscribe {
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: deepgram não suporta op %q", op)
	}
	const q = "/v1/listen?model=nova-3&smart_format=true"
	if in.URL != "" {
		body, err := json.Marshal(map[string]string{"url": in.URL})
		if err != nil {
			return apiRouterOpRequest{}, err
		}
		return apiRouterOpRequest{Method: http.MethodPost, Path: q, Body: body, ContentType: "application/json"}, nil
	}
	if in.AudioB64 == "" {
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: deepgram precisa de url ou audioB64")
	}
	audio, err := decodeAudioInput(in)
	if err != nil {
		return apiRouterOpRequest{}, err
	}
	mime := in.AudioMime
	if mime == "" {
		mime = "audio/wav"
	}
	return apiRouterOpRequest{Method: http.MethodPost, Path: q, Body: audio, ContentType: mime}, nil
}

func buildAssemblyAISubmit(in apiRouterOpInput) (apiRouterOpRequest, error) {
	if in.URL == "" {
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: assemblyai precisa de url (o upload de áudio usa /v2/upload antes)")
	}
	body, err := json.Marshal(map[string]string{"audio_url": in.URL})
	if err != nil {
		return apiRouterOpRequest{}, err
	}
	return apiRouterOpRequest{Method: http.MethodPost, Path: "/v2/transcript", Body: body, ContentType: "application/json"}, nil
}

func buildElevenLabsSubmit(op string, in apiRouterOpInput) (apiRouterOpRequest, error) {
	switch op {
	case opTTS:
		voice := in.Voice
		if voice == "" {
			voice = "21m00Tcm4TlvDq8ikWAM" // Rachel, voz padrão da ElevenLabs
		}
		model := in.Model
		if model == "" {
			model = "eleven_multilingual_v2"
		}
		body, err := json.Marshal(map[string]any{"text": in.Text, "model_id": model})
		if err != nil {
			return apiRouterOpRequest{}, err
		}
		return apiRouterOpRequest{
			Method:      http.MethodPost,
			Path:        fmt.Sprintf("/v1/text-to-speech/%s?output_format=mp3_44100_128", voice),
			Body:        body,
			ContentType: "application/json",
		}, nil

	case opTranscribe:
		model := in.Model
		if model == "" {
			model = "scribe_v2"
		}
		var req apiRouterOpRequest
		if in.URL != "" {
			body, ct, err := buildMultipart(map[string]string{"model_id": model, "source_url": in.URL}, "", nil, "", "")
			if err != nil {
				return apiRouterOpRequest{}, err
			}
			req = apiRouterOpRequest{Method: http.MethodPost, Path: "/v1/speech-to-text", Body: body, ContentType: ct}
		} else {
			audio, err := decodeAudioInput(in)
			if err != nil {
				return apiRouterOpRequest{}, err
			}
			body, ct, err := buildMultipart(map[string]string{"model_id": model}, "file", audio, "audio."+mimeExt(in.AudioMime), mimeOr(in.AudioMime, "application/octet-stream"))
			if err != nil {
				return apiRouterOpRequest{}, err
			}
			req = apiRouterOpRequest{Method: http.MethodPost, Path: "/v1/speech-to-text", Body: body, ContentType: ct}
		}
		return req, nil

	case opVoices:
		return apiRouterOpRequest{Method: http.MethodGet, Path: "/v2/voices"}, nil

	default:
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: elevenlabs não suporta op %q", op)
	}
}

func buildStabilitySubmit(in apiRouterOpInput) (apiRouterOpRequest, error) {
	if in.Prompt == "" {
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: stability precisa de prompt")
	}
	fields := map[string]string{"prompt": in.Prompt}
	if in.NegativePrompt != "" {
		fields["negative_prompt"] = in.NegativePrompt
	}
	if in.AspectRatio != "" {
		fields["aspect_ratio"] = in.AspectRatio
	}
	if in.OutputFormat != "" {
		fields["output_format"] = in.OutputFormat
	}
	if len(in.Seed) > 0 {
		fields["seed"] = string(in.Seed)
	}
	body, ct, err := buildMultipart(fields, "", nil, "", "")
	if err != nil {
		return apiRouterOpRequest{}, err
	}
	return apiRouterOpRequest{
		Method:      http.MethodPost,
		Path:        "/v2beta/stable-image/generate/core",
		Body:        body,
		ContentType: ct,
		Headers:     map[string]string{"Accept": "application/json"},
	}, nil
}

func buildReplicateSubmit(in apiRouterOpInput) (apiRouterOpRequest, error) {
	var input any
	if len(in.Input) > 0 {
		input = json.RawMessage(in.Input)
	} else if in.Prompt != "" {
		input = map[string]string{"prompt": in.Prompt}
	}
	if input == nil {
		return apiRouterOpRequest{}, fmt.Errorf("apirouter: replicate precisa de input (bruto ou prompt)")
	}
	payload := map[string]any{"input": input}
	if in.Version != "" {
		payload["version"] = in.Version
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return apiRouterOpRequest{}, err
	}
	return apiRouterOpRequest{Method: http.MethodPost, Path: "/v1/predictions", Body: body, ContentType: "application/json"}, nil
}

// ── assíncrono (submit → poll) ───────────────────────────────────────────────

// opAsync informa se a operação precisa de polling (cria um job e o resultado
// só vem numa chamada posterior).
func opAsync(adapter, op string) bool {
	switch adapter {
	case opAdapterAssemblyAI:
		return op == opTranscribe
	case opAdapterReplicate:
		return op == opPredict
	}
	return false
}

// opJobID extrai o id do job criado pela resposta do submit.
func opJobID(adapter, op string, raw []byte) (string, error) {
	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("apirouter: parse de job (%s/%s): %w", adapter, op, err)
	}
	if r.ID == "" {
		return "", fmt.Errorf("apirouter: resposta de submit sem id (job): %s", truncateBody(raw))
	}
	return r.ID, nil
}

// buildOpPoll monta a requisição de polling de um job.
func buildOpPoll(adapter, op, jobID string) apiRouterOpRequest {
	switch adapter {
	case opAdapterAssemblyAI:
		return apiRouterOpRequest{Method: http.MethodGet, Path: "/v2/transcript/" + jobID}
	case opAdapterReplicate:
		return apiRouterOpRequest{Method: http.MethodGet, Path: "/v1/predictions/" + jobID}
	default:
		return apiRouterOpRequest{}
	}
}

// opPollStatus interpreta a resposta de polling. done=true quando terminou;
// nesse caso resultErr traz a mensagem de erro se o job falhou.
func opPollStatus(adapter, op string, raw []byte) (done bool, resultErr string) {
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return true, ""
	}
	switch adapter {
	case opAdapterAssemblyAI:
		switch r.Status {
		case "completed":
			return true, ""
		case "error":
			return true, r.Error
		default:
			return false, ""
		}
	case opAdapterReplicate:
		switch r.Status {
		case "succeeded":
			return true, ""
		case "failed", "canceled":
			return true, r.Error
		default:
			return false, ""
		}
	}
	return true, ""
}

// ── parseOpResult: extrai o resultado normalizado ────────────────────────────

func parseOpResult(adapter, op string, raw []byte) (map[string]any, error) {
	switch adapter {
	case opAdapterAssemblyAI:
		var r struct {
			Text          string  `json:"text"`
			Error         string  `json:"error"`
			Language      string  `json:"language_code"`
			Confidence    float64 `json:"confidence"`
			AudioDuration float64 `json:"audio_duration"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("apirouter: parse assemblyai: %w", err)
		}
		if r.Error != "" {
			return nil, fmt.Errorf("assemblyai: %s", r.Error)
		}
		out := map[string]any{"text": r.Text}
		if r.Language != "" {
			out["language"] = r.Language
		}
		if r.Confidence != 0 {
			out["confidence"] = r.Confidence
		}
		if r.AudioDuration != 0 {
			out["audioDuration"] = r.AudioDuration
		}
		return out, nil

	case opAdapterDeepgram:
		var r struct {
			Err *struct {
				Message string `json:"message"`
			} `json:"err"`
			Results *struct {
				Channels []struct {
					Alternatives []struct {
						Transcript string  `json:"transcript"`
						Confidence float64 `json:"confidence"`
					} `json:"alternatives"`
				} `json:"channels"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("apirouter: parse deepgram: %w", err)
		}
		if r.Err != nil {
			return nil, fmt.Errorf("deepgram: %s", r.Err.Message)
		}
		if r.Results == nil || len(r.Results.Channels) == 0 || len(r.Results.Channels[0].Alternatives) == 0 {
			return nil, fmt.Errorf("apirouter: resposta deepgram sem transcrição")
		}
		alt := r.Results.Channels[0].Alternatives[0]
		out := map[string]any{"text": alt.Transcript}
		if alt.Confidence != 0 {
			out["confidence"] = alt.Confidence
		}
		return out, nil

	case opAdapterElevenLabs:
		switch op {
		case opTTS:
			// Resposta é o áudio MP3 em binário — normalizamos em base64.
			return map[string]any{"audioB64": base64.StdEncoding.EncodeToString(raw), "contentType": "audio/mpeg"}, nil
		case opTranscribe:
			var r struct {
				Text  string `json:"text"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, fmt.Errorf("apirouter: parse elevenlabs: %w", err)
			}
			if r.Error != "" {
				return nil, fmt.Errorf("elevenlabs: %s", r.Error)
			}
			return map[string]any{"text": r.Text}, nil
		case opVoices:
			var r struct {
				Voices []struct {
					ID   string `json:"voice_id"`
					Name string `json:"name"`
				} `json:"voices"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, fmt.Errorf("apirouter: parse elevenlabs voices: %w", err)
			}
			voices := make([]map[string]any, 0, len(r.Voices))
			for _, v := range r.Voices {
				voices = append(voices, map[string]any{"id": v.ID, "name": v.Name})
			}
			return map[string]any{"voices": voices}, nil
		}

	case opAdapterStability:
		var r struct {
			Image        string `json:"image"`
			FinishReason string `json:"finish_reason"`
			Seed         int64  `json:"seed"`
		}
		if json.Unmarshal(raw, &r) == nil && r.Image != "" {
			out := map[string]any{"imageB64": r.Image}
			if r.FinishReason != "" {
				out["finishReason"] = r.FinishReason
			}
			if r.Seed != 0 {
				out["seed"] = r.Seed
			}
			return out, nil
		}
		// Fallback: resposta binária (image/*) quando Accept não aplica.
		return map[string]any{"imageB64": base64.StdEncoding.EncodeToString(raw), "contentType": "image/png"}, nil

	case opAdapterReplicate:
		var r struct {
			Status string `json:"status"`
			Output any    `json:"output"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("apirouter: parse replicate: %w", err)
		}
		if r.Error != "" {
			return nil, fmt.Errorf("replicate: %s", r.Error)
		}
		return map[string]any{"output": r.Output, "status": r.Status}, nil
	}
	return nil, fmt.Errorf("apirouter: adapter de operação desconhecido %q", adapter)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// decodeAudioInput decodifica audioB64 do input normalizado.
func decodeAudioInput(in apiRouterOpInput) ([]byte, error) {
	audio, err := base64.StdEncoding.DecodeString(in.AudioB64)
	if err != nil {
		return nil, fmt.Errorf("apirouter: audioB64 inválido: %w", err)
	}
	return audio, nil
}

func mimeOr(mime, fallback string) string {
	if mime == "" {
		return fallback
	}
	return mime
}

func mimeExt(mime string) string {
	switch {
	case strings.Contains(mime, "mpeg"):
		return "mp3"
	case strings.Contains(mime, "wav"):
		return "wav"
	case strings.Contains(mime, "ogg"):
		return "ogg"
	case strings.Contains(mime, "webm"):
		return "webm"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"):
		return "m4a"
	default:
		return "bin"
	}
}

// buildMultipart monta um corpo multipart/form-data. fileField vazio = só
// campos de texto. Devolve o corpo e o Content-Type com boundary.
func buildMultipart(fields map[string]string, fileField string, fileBytes []byte, fileName, fileMime string) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	if fileField != "" && fileBytes != nil {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fileField, fileName))
		h.Set("Content-Type", fileMime)
		pw, err := mw.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := pw.Write(fileBytes); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

func truncateBody(raw []byte) string {
	t := strings.TrimSpace(string(raw))
	if len(t) > 300 {
		t = t[:300]
	}
	return t
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// apiRouterOpTimeout é o teto do polling de jobs assíncronos.
const apiRouterOpTimeout = 90 * time.Second

// apiRouterOpPollInterval é o intervalo entre polls.
const apiRouterOpPollInterval = 2 * time.Second
