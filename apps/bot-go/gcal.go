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

// driveScope — escrever os dossiês dos clientes no Drive.
//
// drive.file, não drive: dá acesso SÓ aos arquivos que o próprio bot criar.
// Ele não enxerga nem toca em mais nada do Drive de quem autorizou — nem
// planilha da escola, nem foto de família. Menor privilégio que resolve.
//
// Por isso o bot CRIA a própria pasta em vez de escrever numa existente: com
// este escopo ele só pode mexer no que é dele.
const driveScope = "https://www.googleapis.com/auth/drive.file"

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
func (g *GCalClient) URLDeAutorizacao(state, uso string) string {
	q := url.Values{}
	q.Set("client_id", g.clientID)
	q.Set("redirect_uri", g.redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", escopoDoUso(uso)+" https://www.googleapis.com/auth/userinfo.email")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	// include_granted_scopes fica DE FORA de propósito.
	//
	// Ele faz o Google somar ao token tudo que aquela conta já tinha concedido
	// a este mesmo app, em qualquer momento do passado. Numa conta que já tinha
	// autorizado o Drive INTEIRO para outra ferramenta, pedir drive.file devolvia
	// um token com drive completo — acesso a todos os arquivos, exatamente o que
	// este desenho existe para evitar. Aconteceu com a conta da diretoria.
	//
	// Sem ele, cada autorização vale pelo que foi pedido, e só.
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
	// ConvidarEmail — Gmail do cliente, quando ele informa.
	//
	// Convidado no evento, ele recebe os MESMOS lembretes na agenda dele, sem a
	// escola precisar manter um segundo sistema. Só funciona com Gmail/Google
	// Workspace; e-mail de outro provedor recebe o convite mas não ganha os
	// lembretes, que é a razão de pedir Gmail especificamente.
	ConvidarEmail string
}

// lembretesDaAula — quando avisar, em minutos antes.
//
// 1 dia: dá tempo de reorganizar a agenda se algo mudar.
// 4 horas: é quando se prepara a sala e se remaneja PC.
// 1 hora: o empurrão final, para ninguém esquecer no meio do dia.
//
// Explícitos em vez de herdar o padrão de cada conta — senão o aviso chega em
// hora diferente para cada pessoa.
var lembretesDaAula = []int{24 * 60, 4 * 60, 60}

// CriarEvento põe a aula na agenda da conta e devolve o id do evento.
func (g *GCalClient) CriarEvento(ctx context.Context, refreshToken, calendarID string, ev EventoAula) (string, error) {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	if calendarID == "" {
		calendarID = "primary"
	}

	overrides := make([]any, 0, len(lembretesDaAula))
	for _, m := range lembretesDaAula {
		overrides = append(overrides, map[string]any{"method": "popup", "minutes": m})
	}

	evento := map[string]any{
		"summary":     ev.Titulo,
		"description": ev.Descricao,
		"start":       map[string]any{"dateTime": ev.Inicio.Format(time.RFC3339), "timeZone": "America/Sao_Paulo"},
		"end":         map[string]any{"dateTime": ev.Fim.Format(time.RFC3339), "timeZone": "America/Sao_Paulo"},
		"reminders": map[string]any{
			"useDefault": false,
			"overrides":  overrides,
		},
	}
	if ev.ConvidarEmail != "" {
		evento["attendees"] = []any{map[string]any{"email": ev.ConvidarEmail}}
	}

	corpo, err := json.Marshal(evento)
	if err != nil {
		return "", err
	}

	// sendUpdates=all faz o Google mandar o convite por e-mail ao cliente. Sem
	// isso o evento entra na agenda dele calado, e um convite que ninguém viu
	// não lembra ninguém de nada.
	endpoint := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events?sendUpdates=all",
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

// GmailValido devolve o e-mail normalizado quando dá para convidar na agenda,
// ou "" quando não dá.
//
// Exige Gmail/Googlemail de propósito. Qualquer endereço recebe o convite por
// e-mail, mas só uma conta Google ganha o evento NA AGENDA com os lembretes —
// e é o lembrete que interessa. Aceitar hotmail daria a impressão de que o
// cliente vai ser lembrado quando não vai.
func GmailValido(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return ""
	}
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return ""
	}
	usuario, dominio := e[:at], e[at+1:]
	if dominio != "gmail.com" && dominio != "googlemail.com" {
		return ""
	}
	// Endereço malformado não pode chegar ao Google: ele responde 400 e derruba
	// a criação do evento INTEIRO — a escola ficaria sem a aula na agenda por
	// causa de um e-mail que o cliente digitou errado. Melhor não convidar.
	if strings.Count(e, "@") != 1 {
		return ""
	}
	if usuario == "" || strings.ContainsAny(usuario, " ,;:/\\\"'<>()[]") {
		return ""
	}
	// Gmail não aceita ponto no começo/fim nem dois pontos seguidos.
	if strings.HasPrefix(usuario, ".") || strings.HasSuffix(usuario, ".") || strings.Contains(usuario, "..") {
		return ""
	}
	for _, r := range usuario {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == '+'
		if !ok {
			return ""
		}
	}
	return e
}

// ConvidarNoEvento acrescenta o cliente a um evento que já existe.
//
// Serve para o caso normal do fluxo: a aula é marcada primeiro, o Gmail chega
// na mensagem seguinte. Sem isto, quem desse o e-mail depois — que é todo mundo,
// porque o bot só pergunta depois de confirmar — nunca entraria na agenda.
//
// PATCH de attendees SUBSTITUI a lista, então mandar o mesmo endereço de novo
// é inofensivo: repetir não duplica convidado.
// ConvidarNoEvento é implementada em termos de ConvidarNoEventoComTexto, para
// que o convite NUNCA saia sem trocar a descrição interna por uma limpa.
func (g *GCalClient) ConvidarNoEvento(ctx context.Context, refreshToken, calendarID, eventID, email, descricaoParaOCliente string) error {
	if eventID == "" || email == "" {
		return nil
	}
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return err
	}
	if calendarID == "" {
		calendarID = "primary"
	}
	endpoint := fmt.Sprintf("https://www.googleapis.com/calendar/v3/calendars/%s/events/%s",
		url.PathEscape(calendarID), url.PathEscape(eventID))

	// Lê os convidados que já estão no evento antes de mexer.
	//
	// PATCH de attendees SUBSTITUI a lista inteira. Mandar só o cliente
	// DESCONVIDA quem um humano tivesse adicionado à mão — o professor que vai
	// dar a aula, por exemplo, sumiria do próprio compromisso sem ninguém pedir.
	var atual struct {
		Attendees []struct {
			Email string `json:"email"`
		} `json:"attendees"`
	}
	if err := g.doJSON(ctx, http.MethodGet, endpoint, access, nil, &atual); err != nil {
		return err
	}
	convidados := make([]any, 0, len(atual.Attendees)+1)
	jaEsta := false
	for _, a := range atual.Attendees {
		if strings.EqualFold(strings.TrimSpace(a.Email), email) {
			jaEsta = true
		}
		convidados = append(convidados, map[string]any{"email": a.Email})
	}
	if jaEsta {
		// Repetir o convite dispararia outro e-mail para quem já foi convidado.
		return nil
	}
	convidados = append(convidados, map[string]any{"email": email})

	patch := map[string]any{"attendees": convidados}
	// A descrição interna (o resumo escrito para a equipe) não pode ficar num
	// evento que o cliente passa a enxergar.
	if descricaoParaOCliente != "" {
		patch["description"] = descricaoParaOCliente
	}
	corpo, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return g.doJSON(ctx, http.MethodPatch, endpoint+"?sendUpdates=all", access, corpo, &struct{}{})
}

// ── propósito da conta ───────────────────────────────────────────────────────

// Os dois usos que o bot faz de uma conta Google. São separados porque as
// contas da escola são separadas: as agendas pessoais do Henrique e do Rodrigo
// de um lado, e a conta da diretoria — onde ficam os arquivos sensíveis da
// empresa — do outro.
//
// Pedir Drive na agenda pessoal seria pedir acesso que ninguém precisa dar.
const (
	UsoAgenda = "agenda"
	UsoDrive  = "drive"
)

// escopoDoUso devolve só o que aquele propósito precisa.
//
// O padrão é a agenda: se o link vier sem parâmetro, o pedido é o menor dos
// dois. Errar para o lado de pedir de menos é recuperável — pedir de mais, em
// conta com arquivo sensível, não.
func escopoDoUso(uso string) string {
	if uso == UsoDrive {
		return driveScope
	}
	return gcalScope
}

// UsoValido protege contra um link com parâmetro inventado virar autorização
// larga por acidente.
func UsoValido(uso string) bool {
	return uso == UsoAgenda || uso == UsoDrive
}

// ClienteDoDrive devolve o cliente que fala com o Drive.
//
// Quando existem credenciais próprias, o Drive usa um app DIFERENTE do que
// cuida da agenda. O motivo é concreto: o app antigo carrega, na conta da
// diretoria, uma lista arquivo-a-arquivo de PDFs que ele alcançou quando teve o
// Drive inteiro — e essa lista sobreviveu à revogação e à nova autorização.
// Reduzir o escopo não a apaga; só um app novo começa sem ela.
//
// Separado, em vez de trocar o único app, porque trocar obrigaria Henrique e
// Rodrigo a reautorizar a agenda deles — que está certa, mínima e funcionando.
// Uma pessoa autoriza uma conta, e não três pessoas três contas.
func NovoClienteDoDrive(cfg Config, log *slog.Logger) *GCalClient {
	id, secret := cfg.GoogleDriveClientID, cfg.GoogleDriveClientSecret
	if id == "" || secret == "" {
		id, secret = cfg.GoogleClientID, cfg.GoogleClientSecret
	}
	return NewGCalClient(id, secret, cfg.GoogleRedirectURL, log)
}
