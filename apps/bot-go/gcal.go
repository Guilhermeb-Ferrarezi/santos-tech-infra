package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Google Agenda — a aula marcada no Notion vira evento na agenda de quem opera
// a escola, com lembrete.
//
// POR QUE ALÉM DO NOTION. O Notion é o controle: é lá que Henrique, Rodrigo e
// os professores olham. Mas o Notion não avisa ninguém. O Google Agenda avisa —
// no celular, no relógio, sem ninguém precisar abrir nada. Um é o registro, o
// outro é o alarme; os dois precisam existir.
//
// O escopo pedido é calendar.events, não calendar: dá para criar e mexer em
// eventos, não para mexer na agenda em si. Menor privilégio que resolve.

const gcalScope = "https://www.googleapis.com/auth/calendar.events"

// GCalClient fala com a API do Google Agenda em nome de cada conta autorizada.
type GCalClient struct {
	clientID     string
	clientSecret string
	redirectURL  string
	http         *http.Client
	log          *slog.Logger

	// Access tokens duram uma hora. Guardar em memória evita uma ida ao Google
	// por evento criado; a chave é o refresh token de cada conta.
	mu     sync.Mutex
	tokens map[string]tokenEmCache
}

type tokenEmCache struct {
	access string
	expira time.Time
}

func NewGCalClient(clientID, clientSecret, redirectURL string, log *slog.Logger) *GCalClient {
	if log == nil {
		log = slog.Default()
	}
	return &GCalClient{
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		redirectURL:  strings.TrimSpace(redirectURL),
		http:         &http.Client{Timeout: 20 * time.Second},
		log:          log,
		tokens:       map[string]tokenEmCache{},
	}
}

func (g *GCalClient) Enabled() bool {
	return g != nil && g.clientID != "" && g.clientSecret != "" && g.redirectURL != ""
}

// ── autorização ──────────────────────────────────────────────────────────────

// URLDeAutorizacao monta o link que a pessoa abre para autorizar.
//
// access_type=offline e prompt=consent juntos são o que garante que o Google
// devolva um REFRESH token. Sem os dois, a segunda autorização da mesma conta
// volta só com access token de uma hora — e aí a integração morre sozinha uma
// hora depois, sem erro visível.
func (g *GCalClient) URLDeAutorizacao(state string) string {
	q := url.Values{}
	q.Set("client_id", g.clientID)
	q.Set("redirect_uri", g.redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", gcalScope+" https://www.googleapis.com/auth/userinfo.email")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	q.Set("state", state)
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
}

// TrocaCodigo converte o código da tela de consentimento em refresh token, e
// descobre de qual conta ele é.
func (g *GCalClient) TrocaCodigo(ctx context.Context, code string) (refreshToken, email string, err error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", g.clientID)
	form.Set("client_secret", g.clientSecret)
	form.Set("redirect_uri", g.redirectURL)
	form.Set("grant_type", "authorization_code")

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := g.postForm(ctx, "https://oauth2.googleapis.com/token", form, &tok); err != nil {
		return "", "", err
	}
	if tok.RefreshToken == "" {
		// Acontece quando a conta já tinha autorizado antes e o prompt=consent
		// não foi respeitado. Falhar alto é melhor que gravar uma conta que
		// para de funcionar em uma hora.
		return "", "", fmt.Errorf("gcal: Google não devolveu refresh token; revogue o acesso em myaccount.google.com/permissions e autorize de novo")
	}

	email, err = g.emailDoToken(ctx, tok.AccessToken)
	if err != nil {
		return "", "", err
	}
	return tok.RefreshToken, email, nil
}

func (g *GCalClient) emailDoToken(ctx context.Context, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcal: userinfo status %d: %s", resp.StatusCode, string(raw))
	}
	var u struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return "", err
	}
	return u.Email, nil
}

// accessToken devolve um token válido para a conta, renovando quando precisa.
func (g *GCalClient) accessToken(ctx context.Context, refreshToken string) (string, error) {
	g.mu.Lock()
	t, ok := g.tokens[refreshToken]
	g.mu.Unlock()
	// Margem de um minuto: um token que vence no meio da requisição é erro
	// difícil de diagnosticar.
	if ok && time.Until(t.expira) > time.Minute {
		return t.access, nil
	}

	form := url.Values{}
	form.Set("client_id", g.clientID)
	form.Set("client_secret", g.clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := g.postForm(ctx, "https://oauth2.googleapis.com/token", form, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("gcal: refresh não devolveu access token")
	}

	g.mu.Lock()
	g.tokens[refreshToken] = tokenEmCache{
		access: tok.AccessToken,
		expira: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
	}
	g.mu.Unlock()
	return tok.AccessToken, nil
}

func (g *GCalClient) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gcal: token status %d: %s", resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}

// ── eventos ──────────────────────────────────────────────────────────────────

// EventoAula — o que vai para a agenda.
type EventoAula struct {
	Titulo    string
	Descricao string
	Inicio    time.Time
	Fim       time.Time
}

// CriarEvento põe a aula na agenda da conta e devolve o id do evento.
//
// Os lembretes são explícitos (24h e 4h antes) em vez de herdar o padrão de
// cada um: 4h antes é o tempo de preparar a sala e remanejar PC; 1 dia antes é
// o tempo de reorganizar a agenda se algo mudar. Depender do padrão de cada
// conta faria o aviso chegar em hora diferente para cada pessoa.
func (g *GCalClient) CriarEvento(ctx context.Context, refreshToken, calendarID string, ev EventoAula) (string, error) {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	if calendarID == "" {
		calendarID = "primary"
	}

	corpo, err := json.Marshal(map[string]any{
		"summary":     ev.Titulo,
		"description": ev.Descricao,
		"start":       map[string]any{"dateTime": ev.Inicio.Format(time.RFC3339), "timeZone": "America/Sao_Paulo"},
		"end":         map[string]any{"dateTime": ev.Fim.Format(time.RFC3339), "timeZone": "America/Sao_Paulo"},
		"reminders": map[string]any{
			"useDefault": false,
			"overrides": []any{
				map[string]any{"method": "popup", "minutes": 24 * 60},
				map[string]any{"method": "popup", "minutes": 4 * 60},
			},
		},
	})
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events",
		url.PathEscape(calendarID))
	var out struct {
		ID string `json:"id"`
	}
	if err := g.doJSON(ctx, http.MethodPost, endpoint, access, corpo, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// MoverEvento muda o horário de um evento já criado (remarcação).
func (g *GCalClient) MoverEvento(ctx context.Context, refreshToken, calendarID, eventID string, inicio, fim time.Time) error {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return err
	}
	if calendarID == "" {
		calendarID = "primary"
	}
	corpo, err := json.Marshal(map[string]any{
		"start": map[string]any{"dateTime": inicio.Format(time.RFC3339), "timeZone": "America/Sao_Paulo"},
		"end":   map[string]any{"dateTime": fim.Format(time.RFC3339), "timeZone": "America/Sao_Paulo"},
	})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events/%s",
		url.PathEscape(calendarID), url.PathEscape(eventID))
	return g.doJSON(ctx, http.MethodPatch, endpoint, access, corpo, nil)
}

// ApagarEvento tira a aula da agenda (cancelamento).
//
// 410 (já apagado) é tratado como sucesso: o objetivo é "não está mais lá", e
// alguém ter apagado na mão antes não é erro.
func (g *GCalClient) ApagarEvento(ctx context.Context, refreshToken, calendarID, eventID string) error {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return err
	}
	if calendarID == "" {
		calendarID = "primary"
	}
	endpoint := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events/%s",
		url.PathEscape(calendarID), url.PathEscape(eventID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusGone, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("gcal: delete status %d: %s", resp.StatusCode, string(raw))
	}
}

func (g *GCalClient) doJSON(ctx context.Context, metodo, endpoint, access string, corpo []byte, out any) error {
	var body io.Reader
	if corpo != nil {
		body = bytes.NewReader(corpo)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("gcal: %s status %d: %s", metodo, resp.StatusCode, string(raw))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
