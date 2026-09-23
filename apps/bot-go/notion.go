package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// notionVersion — usamos a API com data sources (database multi-source).
const notionVersion = "2025-09-03"

// brLocation — fuso fixo do Brasil (UTC-3, sem horário de verão). FixedZone evita
// depender de tzdata, ausente na imagem distroless.
var brLocation = time.FixedZone("BRT", -3*60*60)

// NotionClient lê e grava no data source "Agenda de Aulas" do Notion
// via API REST, com um token de integração escopado. O LLM NUNCA acessa o Notion —
// só este código.
type NotionClient struct {
	token string
	dsID  string // data source id (não o database id)
	http  *http.Client
	log   *slog.Logger

	// Janela de leitura: quanto do futuro interessa. Agenda de daqui a seis
	// meses não ajuda a propor horário e só infla o prompt.
	janela time.Duration

	// cache da leitura da agenda (TTL curto) para não consultar a cada mensagem.
	mu      sync.RWMutex
	cache   []ScheduleEntry
	cacheAt time.Time
	cacheOK bool // a última leitura deu certo?
	ttl     time.Duration
}

// EstadoAgenda — o que se sabe sobre a agenda neste instante.
//
// Existe porque "lista vazia" era ambíguo: agenda realmente livre e Notion fora
// do ar produziam exatamente o mesmo resultado, e o prompt não tinha como
// distinguir. Com o bot agendando sozinho, essa ambiguidade vira aula marcada
// em cima de outra.
type EstadoAgenda int

// A ordem importa: o VALOR ZERO é AgendaIndisponivel, não AgendaOK.
//
// Quem constrói um TenantConfig sem preencher este campo (conversa de admin,
// fixture de teste, código futuro) deve herdar "não sei nada sobre a agenda",
// nunca "pode confiar". Confiança precisa ser afirmada de propósito.
const (
	AgendaIndisponivel EstadoAgenda = iota // não há nada confiável para mostrar
	AgendaAntiga                           // leitura falhou, mas há cache anterior
	AgendaOK                               // leitura fresca, pode confiar
)

// NewNotionClient cria o cliente. Se token/dsID estiverem vazios, Enabled()=false
// e as operações degradam (sem erro fatal).
func NewNotionClient(token, dsID string, log *slog.Logger) *NotionClient {
	if log == nil {
		log = slog.Default()
	}
	return &NotionClient{
		token:  strings.TrimSpace(token),
		dsID:   strings.TrimSpace(dsID),
		http:   &http.Client{Timeout: 15 * time.Second},
		log:    log,
		janela: 21 * 24 * time.Hour,
		ttl:    2 * time.Minute,
	}
}

// Enabled indica se há credenciais configuradas.
func (c *NotionClient) Enabled() bool {
	return c != nil && c.token != "" && c.dsID != ""
}

// Schedule retorna as aulas agendadas e o QUANTO SE PODE CONFIAR nelas.
//
// O segundo retorno não é decoração: sem ele, "nenhuma aula marcada" e "não
// consegui falar com o Notion" produzem a mesma lista vazia, e quem lê decide
// errado. Com o bot propondo horário sozinho, essa ambiguidade vira aula
// marcada em cima de outra.
func (c *NotionClient) Schedule(ctx context.Context) ([]ScheduleEntry, EstadoAgenda) {
	if !c.Enabled() {
		return nil, AgendaIndisponivel
	}

	c.mu.RLock()
	fresco := c.cacheOK && time.Since(c.cacheAt) < c.ttl
	cache, temCache := c.cache, c.cacheOK
	c.mu.RUnlock()
	if fresco {
		return cache, AgendaOK
	}

	// A chamada HTTP fica FORA do lock de propósito: são até 15s, e segurar o
	// mutex aqui enfileiraria todas as conversas simultâneas atrás de uma só.
	entries, err := c.fetchSchedule(ctx)
	if err != nil {
		c.log.Error("notion: falha ao ler a agenda", "err", err, "tem_cache", temCache)
		if temCache {
			return cache, AgendaAntiga
		}
		return nil, AgendaIndisponivel
	}

	c.mu.Lock()
	c.cache, c.cacheAt, c.cacheOK = entries, time.Now(), true
	c.mu.Unlock()
	return entries, AgendaOK
}

// fetchSchedule lê a agenda da janela útil, paginando até o fim.
//
// Antes, o corpo da consulta era literalmente `{}`: sem filtro, sem ordenação e
// sem paginação. O Notion devolve no máximo 100 linhas por página, e aula
// passada nunca sai da base — então bastava a agenda acumular 100 registros
// antigos para as 100 linhas retornadas serem todas passado, o filtro
// client-side descartar tudo, e o bot passar a enxergar a agenda VAZIA. Sem
// erro, sem log, sem sintoma: ele simplesmente começaria a marcar em cima de
// aula existente.
//
// Agora o Notion faz o recorte: só o intervalo que interessa, em ordem, e o
// código segue o next_cursor até acabar.
func (c *NotionClient) fetchSchedule(ctx context.Context) ([]ScheduleEntry, error) {
	// SEM filtro de data: a base não tem campo de data preenchido. É uma grade
	// semanal — cada linha é um espaço tomado num dia da semana. Filtrar por
	// "Data" aqui devolvia zero linhas e o bot enxergava a agenda vazia.
	filtro := map[string]any{"page_size": 100}

	url := fmt.Sprintf("https://api.notion.com/v1/data_sources/%s/query", c.dsID)
	var entries []ScheduleEntry
	cursor := ""

	for pagina := 0; pagina < 20; pagina++ {
		if cursor != "" {
			filtro["start_cursor"] = cursor
		}
		corpo, err := json.Marshal(filtro)
		if err != nil {
			return nil, fmt.Errorf("notion: marshal query: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(corpo))
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("notion: query status %d: %s", resp.StatusCode, string(raw))
		}

		var out struct {
			Results []struct {
				ID         string                     `json:"id"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"results"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("notion: query unmarshal: %w", err)
		}

		for _, r := range out.Results {
			e := ScheduleEntry{
				PageID:    r.ID,
				Titulo:    notionTitle(r.Properties["Aula"]),
				Dia:       notionSelect(r.Properties["Dia"]),
				Horario:   notionRichText(r.Properties["Horário"]),
				Professor: notionSelect(r.Properties["Professor"]),
				Conteudo:  notionSelect(r.Properties["Conteúdo"]),
			}
			// Linha sem dia nem horário não é aula — é cabeçalho ou rascunho.
			if e.Dia == "" && e.Horario == "" {
				continue
			}
			e.Intervalo, e.Ok = ParseIntervalo(e.Dia, e.Horario)
			if !e.Ok && e.Horario != "" {
				// Aparece no prompt, mas não bloqueia horário: melhor o modelo
				// ver a linha e ficar em dúvida do que o código fingir que
				// entendeu um texto que não entendeu.
				c.log.Warn("notion: horário não interpretado", "aula", e.Titulo, "dia", e.Dia, "horario", e.Horario)
			}
			entries = append(entries, e)
		}

		if !out.HasMore || out.NextCursor == "" {
			return entries, nil
		}
		cursor = out.NextCursor
	}
	c.log.Warn("notion: agenda passou do teto de páginas", "lidas", len(entries))
	return entries, nil
}

// notionSelect lê uma propriedade do tipo select.
func notionSelect(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v struct {
		Select *struct {
			Name string `json:"name"`
		} `json:"select"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.Select == nil {
		return ""
	}
	return v.Select.Name
}

// notionRichText junta o texto de uma propriedade rich_text.
func notionRichText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v struct {
		RichText []struct {
			PlainText string `json:"plain_text"`
		} `json:"rich_text"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, t := range v.RichText {
		sb.WriteString(t.PlainText)
	}
	return strings.TrimSpace(sb.String())
}

// CreateBooking grava uma aula na grade e devolve o ID da página criada.
//
// Escreve no formato da escola: título com o marcador do bot e a data, dia da
// semana e horário no mesmo padrão das outras linhas. Uma linha que destoa faz
// a grade parecer remendada.
//
// O campo Professor fica VAZIO de propósito: quem dá a aula é decisão da
// escola, não do bot. Deixar em branco é um convite visível a preencher;
// chutar um nome seria comprometer a agenda de alguém.
func (c *NotionClient) CreateBooking(ctx context.Context, b Booking) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("notion: não configurado")
	}
	inicio, ok := parseNotionTime(b.DataHora)
	if !ok {
		return "", fmt.Errorf("notion: data/hora ilegível: %q", b.DataHora)
	}
	dur := time.Duration(b.DuracaoMin) * time.Minute
	if dur <= 0 {
		dur = time.Hour
	}

	props := map[string]any{
		"Aula": map[string]any{
			"title": []any{map[string]any{"text": map[string]any{"content": TituloAulaBot(b.Aluno, inicio)}}},
		},
		"Dia": map[string]any{"select": map[string]any{"name": DiaDaSemanaPT(inicio)}},
		"Horário": map[string]any{
			"rich_text": []any{map[string]any{"text": map[string]any{"content": FormataIntervalo(inicio, dur)}}},
		},
	}
	// Período é select com valores fixos; só preenche quando o horário cai
	// claramente num deles.
	if p := periodoDoDia(inicio); p != "" {
		props["Período"] = map[string]any{"select": map[string]any{"name": p}}
	}

	body := map[string]any{
		"parent":     map[string]any{"type": "data_source_id", "data_source_id": c.dsID},
		"properties": props,
	}
	if filhos := blocosDoAtendimento(b); len(filhos) > 0 {
		body["children"] = filhos
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("notion: marshal booking: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.notion.com/v1/pages", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("notion: create page status %d: %s", resp.StatusCode, string(raw))
	}
	c.invalidaCache()

	var criada struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &criada); err != nil {
		c.log.Warn("notion: página criada mas id ilegível", "err", err)
		return "", nil
	}
	return criada.ID, nil
}

// periodoDoDia traduz a hora para o select que a escola usa.
func periodoDoDia(t time.Time) string {
	switch h := t.Hour(); {
	case h < 12:
		return "Manhã"
	case h < 18:
		return "Tarde"
	default:
		return "Noite"
	}
}

// blocosDoAtendimento monta o CONTEÚDO da página do agendamento: a ficha do
// aluno e o resumo do que foi conversado.
//
// Vai como conteúdo, e não como propriedade, por dois motivos: a base "Agenda —
// Aulas Experimentais" não tem campo para texto livre (são seis propriedades, e
// nenhuma serve), e um resumo de atendimento numa coluna de tabela é ilegível.
// Quem abre o agendamento antes da aula quer o contexto na página.
func blocosDoAtendimento(b Booking) []any {
	var ficha []string
	if b.Tipo != "" {
		ficha = append(ficha, "Tipo: "+b.Tipo)
	}
	if b.Curso != "" {
		ficha = append(ficha, "Curso de interesse: "+b.Curso)
	}
	if b.Idade > 0 {
		ficha = append(ficha, fmt.Sprintf("Idade do aluno: %d", b.Idade))
	}
	if b.WhatsApp != "" {
		ficha = append(ficha, "WhatsApp: "+b.WhatsApp)
	}

	resumo := strings.TrimSpace(b.Resumo)
	if len(ficha) == 0 && resumo == "" {
		return nil
	}

	blocos := []any{titulo("Atendimento pelo bot")}
	if len(ficha) > 0 {
		blocos = append(blocos, paragrafo(strings.Join(ficha, " · ")))
	}
	if resumo != "" {
		blocos = append(blocos, titulo("Resumo da conversa"))
		// O Notion recusa rich_text acima de 2000 caracteres por bloco. Quebrar
		// é melhor que truncar: o resumo é justamente o que dá contexto.
		for _, pedaco := range fatia(resumo, 1900) {
			blocos = append(blocos, paragrafo(pedaco))
		}
	}
	return blocos
}

func titulo(txt string) map[string]any {
	return map[string]any{
		"object": "block", "type": "heading_3",
		"heading_3": map[string]any{
			"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": txt}}},
		},
	}
}

func paragrafo(txt string) map[string]any {
	return map[string]any{
		"object": "block", "type": "paragraph",
		"paragraph": map[string]any{
			"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": txt}}},
		},
	}
}

// fatia quebra em pedaços de no máximo n RUNES, preferindo cortar no espaço
// mais próximo para não partir palavra no meio.
func fatia(s string, n int) []string {
	r := []rune(s)
	if len(r) <= n {
		return []string{s}
	}
	var out []string
	for len(r) > 0 {
		if len(r) <= n {
			out = append(out, string(r))
			break
		}
		corte := n
		for i := n; i > n/2; i-- {
			if r[i] == ' ' || r[i] == '\n' {
				corte = i
				break
			}
		}
		out = append(out, strings.TrimSpace(string(r[:corte])))
		r = r[corte:]
	}
	return out
}

// UpdateBookingDateTime atualiza a propriedade "Data e hora" de uma página de aula
// já existente (remarcação). iso é RFC3339 com offset (ex.: vindo de ResolveBookingDateTime).
func (c *NotionClient) UpdateBookingDateTime(ctx context.Context, pageID, iso string) error {
	if !c.Enabled() {
		return fmt.Errorf("notion: não configurado")
	}
	if pageID == "" {
		return fmt.Errorf("notion: pageID vazio")
	}

	body := map[string]any{
		"properties": map[string]any{
			"Data e hora": map[string]any{"date": map[string]any{"start": iso}},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notion: marshal update: %w", err)
	}

	url := "https://api.notion.com/v1/pages/" + pageID
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notion: update page status %d: %s", resp.StatusCode, string(raw))
	}
	// Invalida o cache pra o próximo Schedule() refletir o novo horário.
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
	return nil
}

func (c *NotionClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Content-Type", "application/json")
}

// ── resolução de data/hora ─────────────────────────────────────────────────────

var weekdaysPT = map[string]time.Weekday{
	"domingo": time.Sunday, "dom": time.Sunday,
	"segunda": time.Monday, "segunda-feira": time.Monday, "seg": time.Monday,
	"terca": time.Tuesday, "terça": time.Tuesday, "terca-feira": time.Tuesday, "terça-feira": time.Tuesday, "ter": time.Tuesday,
	"quarta": time.Wednesday, "quarta-feira": time.Wednesday, "qua": time.Wednesday,
	"quinta": time.Thursday, "quinta-feira": time.Thursday, "qui": time.Thursday,
	"sexta": time.Friday, "sexta-feira": time.Friday, "sex": time.Friday,
	"sabado": time.Saturday, "sábado": time.Saturday, "sab": time.Saturday, "sáb": time.Saturday,
}

// ResolveBookingDateTime monta um datetime ISO 8601 (com offset -03:00) a partir do
// dia ("terça", "17/06", "2026-06-17", "hoje", "amanhã") e da hora ("19h30", "19:30")
// coletados na conversa. Retorna ok=false se não der pra resolver com confiança.
func ResolveBookingDateTime(day, tm string, now time.Time) (string, bool) {
	hh, mm, okT := parseClock(tm)
	if !okT {
		return "", false
	}
	now = now.In(brLocation)
	y, mo, d, okD := resolveDay(day, now)
	if !okD {
		return "", false
	}
	res := time.Date(y, mo, d, hh, mm, 0, 0, brLocation)
	return res.Format(time.RFC3339), true
}

// parseClock extrai hora e minuto de "19h30", "19:30", "19h", "19", "9h".
func parseClock(s string) (int, int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "hs", "h")
	s = strings.ReplaceAll(s, "horas", "h")
	s = strings.ReplaceAll(s, "hora", "h")
	sep := strings.IndexAny(s, "h:")
	hStr, mStr := s, ""
	if sep >= 0 {
		hStr = s[:sep]
		mStr = strings.Trim(s[sep+1:], " h")
	}
	hStr = strings.TrimSpace(hStr)
	h, err := strconv.Atoi(onlyDigits(hStr))
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m := 0
	if d := onlyDigits(mStr); d != "" {
		m, _ = strconv.Atoi(d)
	}
	if m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// resolveDay devolve ano/mês/dia a partir de um dia em linguagem natural ou data.
func resolveDay(day string, now time.Time) (int, time.Month, int, bool) {
	s := strings.ToLower(strings.TrimSpace(day))
	switch s {
	case "hoje":
		return now.Year(), now.Month(), now.Day(), true
	case "amanha", "amanhã":
		t := now.AddDate(0, 0, 1)
		return t.Year(), t.Month(), t.Day(), true
	}
	// Data ISO YYYY-MM-DD
	if t, err := time.ParseInLocation("2006-01-02", s, brLocation); err == nil {
		return t.Year(), t.Month(), t.Day(), true
	}
	// Data DD/MM ou DD/MM/YYYY
	if y, mo, d, ok := parseSlashDate(s, now); ok {
		return y, mo, d, true
	}
	// Dia da semana → próxima ocorrência. Se cair em HOJE (ex.: "sábado" dito num
	// sábado), pula pra semana que vem: agendar é sempre pra frente, e "hoje" tem
	// palavra própria. Evita gravar a aula no dia errado.
	key := strings.Fields(s)
	if len(key) > 0 {
		if wd, ok := weekdaysPT[key[0]]; ok {
			ahead := (int(wd) - int(now.Weekday()) + 7) % 7
			if ahead == 0 {
				ahead = 7
			}
			t := now.AddDate(0, 0, ahead)
			return t.Year(), t.Month(), t.Day(), true
		}
	}
	return 0, 0, 0, false
}

// parseSlashDate trata "17/06" (ano corrente) e "17/06/2026".
func parseSlashDate(s string, now time.Time) (int, time.Month, int, bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return 0, 0, 0, false
	}
	d, err1 := strconv.Atoi(onlyDigits(parts[0]))
	mo, err2 := strconv.Atoi(onlyDigits(parts[1]))
	if err1 != nil || err2 != nil || d < 1 || d > 31 || mo < 1 || mo > 12 {
		return 0, 0, 0, false
	}
	y := now.Year()
	if len(parts) >= 3 {
		if yy, err := strconv.Atoi(onlyDigits(parts[2])); err == nil && yy > 0 {
			if yy < 100 {
				yy += 2000
			}
			y = yy
		}
	} else if time.Month(mo) < now.Month() {
		y++ // mês já passou neste ano → assume o ano que vem
	}
	return y, time.Month(mo), d, true
}

// formatBRDateTime formata um ISO em "ter 17/06 às 19:30". Vazio se não parsear.
func formatBRDateTime(iso string) string {
	t, ok := parseNotionTime(iso)
	if !ok {
		return iso
	}
	t = t.In(brLocation)
	wd := []string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}[t.Weekday()]
	return fmt.Sprintf("%s %02d/%02d às %02d:%02d", wd, t.Day(), int(t.Month()), t.Hour(), t.Minute())
}

// parseNotionTime aceita datetime com hora (RFC3339) ou data pura (YYYY-MM-DD).
func parseNotionTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, brLocation); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ── parsers de propriedade do Notion ──────────────────────────────────────────

func notionStatus(raw json.RawMessage) string {
	var p struct {
		Status *struct {
			Name string `json:"name"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Status != nil {
		return p.Status.Name
	}
	return ""
}

func notionDateStart(raw json.RawMessage) string {
	var p struct {
		Date *struct {
			Start string `json:"start"`
		} `json:"date"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Date != nil {
		return p.Date.Start
	}
	return ""
}

func notionPhone(raw json.RawMessage) string {
	var p struct {
		PhoneNumber string `json:"phone_number"`
	}
	if json.Unmarshal(raw, &p) == nil {
		return p.PhoneNumber
	}
	return ""
}

func notionPeopleNames(raw json.RawMessage) string {
	var p struct {
		People []struct {
			Name string `json:"name"`
		} `json:"people"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	names := make([]string, 0, len(p.People))
	for _, m := range p.People {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return strings.Join(names, ", ")
}

func notionTitle(raw json.RawMessage) string {
	var p struct {
		Title []struct {
			PlainText string `json:"plain_text"`
		} `json:"title"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	var sb strings.Builder
	for _, t := range p.Title {
		sb.WriteString(t.PlainText)
	}
	return sb.String()
}

// ── operações que o bot faz sozinho ──────────────────────────────────────────

// SlotOcupado checa, SEM CACHE, se já existe aula no intervalo pedido.
//
// O cache de dois minutos serve para montar prompt; não serve para decidir
// gravar. Duas conversas simultâneas leem a mesma agenda cacheada, as duas
// acham o horário livre, e as duas marcam. Esta consulta é a última palavra,
// feita imediatamente antes do INSERT.
//
// Ainda é TOCTOU — entre a checagem e a gravação cabe uma corrida. Mas reduz a
// janela de dois minutos para alguns milissegundos, e o Notion não oferece
// transação para fechar isso de vez.
// ignorarPageID sai da grade ANTES da checagem — é como o cliente consegue
// remarcar sem esbarrar na própria aula.
func (c *NotionClient) SlotOcupadoExceto(ctx context.Context, inicio time.Time, dur time.Duration, ignorarPageID string) (ScheduleEntry, bool, error) {
	if !c.Enabled() {
		return ScheduleEntry{}, false, fmt.Errorf("notion: não configurado")
	}
	// Relê a grade inteira, SEM cache. O cache de dois minutos serve para montar
	// prompt; não serve para decidir gravar. Duas conversas simultâneas leem a
	// mesma agenda cacheada, as duas acham o horário livre, e as duas marcam.
	//
	// A grade tem algumas dezenas de linhas — reler é barato, e é a última
	// palavra antes de escrever.
	entries, err := c.fetchSchedule(ctx)
	if err != nil {
		return ScheduleEntry{}, false, err
	}
	// A exclusão entra ANTES do Conflito, nunca depois: Conflito devolve só o
	// PRIMEIRO choque, e perdoar o resultado deixaria passar um segundo
	// compromisso no mesmo horário — uma aula de aluno real escondida atrás da
	// aula do próprio cliente.
	e, bateu := Conflito(inicio, dur, semAPagina(entries, ignorarPageID))
	return e, bateu, nil
}

// ArquivarBooking tira o agendamento da agenda (archive, não delete — o Notion
// mantém na lixeira e dá para recuperar).
//
// NUNCA arquiva o que não for do bot. Antes de mexer, lê o título da página e
// exige o marcador. Henrique e Rodrigo lançam aulas na mão na mesma base: um
// erro aqui apaga compromisso de gente de verdade, e "o bot sumiu com a minha
// aula" não tem desfazer bom.
func (c *NotionClient) ArquivarBooking(ctx context.Context, pageID string) error {
	if !c.Enabled() {
		return fmt.Errorf("notion: não configurado")
	}
	titulo, err := c.tituloDaPagina(ctx, pageID)
	if err != nil {
		return fmt.Errorf("notion: não deu para conferir o título antes de arquivar: %w", err)
	}
	if !EhDoBot(titulo) {
		return fmt.Errorf("notion: recusado — %q não foi criado pelo bot", titulo)
	}

	corpo, err := json.Marshal(map[string]any{"archived": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"https://api.notion.com/v1/pages/"+pageID, bytes.NewReader(corpo))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Página já arquivada não é falha: o fim desejado — aquele horário fora
		// da grade — já está valendo. Tratar como erro travava a remarcação
		// inteira, e o cliente ficava sem a aula nova por causa de uma aula
		// velha que nem existe mais. Aconteceu em produção depois de uma
		// limpeza manual na base.
		if strings.Contains(string(raw), "is archived") ||
			strings.Contains(string(raw), "archived block") {
			c.log.Info("notion: agendamento já estava arquivado", "page", pageID, "titulo", titulo)
			c.invalidaCache()
			return nil
		}
		return fmt.Errorf("notion: archive status %d: %s", resp.StatusCode, string(raw))
	}
	c.invalidaCache()
	c.log.Info("notion: agendamento arquivado", "page", pageID, "titulo", titulo)
	return nil
}

// tituloDaPagina lê só o título — é a verificação de dono antes de qualquer
// escrita destrutiva.
func (c *NotionClient) tituloDaPagina(ctx context.Context, pageID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.notion.com/v1/pages/"+pageID, nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("notion: get page status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return notionTitle(out.Properties["Aula"]), nil
}

// ExperimentaisPassadas lista as aulas do bot que já aconteceram, para a
// faxina. Só devolve as que têm o marcador — as lançadas à mão ficam de fora
// da lista, então nem chegam perto do arquivamento.
// ExperimentaisPassadas lista as aulas que O BOT criou e que já aconteceram.
//
// A base não tem campo de data, então a data vem do título que o próprio bot
// escreveu ("🤖 23/09 Aula experimental — Caio"). Só títulos dele entram: ler
// data de texto escrito à mão seria adivinhação, e adivinhar aqui significa
// arquivar a aula de alguém.
func (c *NotionClient) ExperimentaisPassadas(ctx context.Context, antesDe time.Time) ([]ScheduleEntry, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("notion: não configurado")
	}
	entries, err := c.fetchSchedule(ctx)
	if err != nil {
		return nil, err
	}
	var passadas []ScheduleEntry
	for _, e := range entries {
		if !EhDoBot(e.Titulo) {
			continue
		}
		data, ok := DataNoTitulo(e.Titulo, antesDe)
		if !ok || !data.Before(antesDe) {
			continue
		}
		passadas = append(passadas, e)
	}
	return passadas, nil
}

func (c *NotionClient) invalidaCache() {
	c.mu.Lock()
	c.cacheOK = false
	c.mu.Unlock()
}
