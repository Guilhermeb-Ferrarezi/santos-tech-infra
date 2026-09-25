package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/santos-tech/bot/db"
)

// Turmas ao vivo — o bot fala de turma com dado real da plataforma
// (spec 2026-09-25-bot-turmas-ao-vivo, no repo dashboard).
//
// Duas consultas na API central: GET /portal/turmas-abertas (turmas de grupo
// que um humano liberou, com vaga e andamento) e GET /agenda/horarios-livres
// (onde caberia uma turma nova). Cada consulta boa grava um retrato no banco
// do bot (0046); se a consulta falhar, o bot responde pelo retrato.
//
// O que pode ser afirmado é decidido AQUI, em Go (decideTurmas), não pedido
// ao modelo: "lembre de pôr ressalva se o dado for velho" é o que ele esquece.

// retratoConfiavel — até aqui o retrato vale como consulta ao vivo. Depois
// disso, turma/horário/início/fim continuam afirmáveis (não envelhecem), mas
// a vaga vai com ressalva. Constante de código (decisão técnica da spec): a
// consulta falhar por dias seguidos já é incidente e aparece no log.
const retratoConfiavel = 72 * time.Hour

// fusoEscola — Ribeirão Preto. Fixo em −3: o Brasil não tem horário de verão
// desde 2019, e não depender do tzdata da imagem evita um "hoje" em UTC às 22h.
var fusoEscola = time.FixedZone("BRT", -3*60*60)

type HorarioTurma struct {
	DiaSemana  int    `json:"diaSemana"`
	HoraInicio string `json:"horaInicio"`
	HoraFim    string `json:"horaFim"`
}

// TurmaAoVivo — uma turma como GET /portal/turmas-abertas devolve.
type TurmaAoVivo struct {
	ID    int64  `json:"id"`
	Nome  string `json:"nome"`
	Curso struct {
		ID   int64  `json:"id"`
		Nome string `json:"nome"`
	} `json:"curso"`
	Horarios    []HorarioTurma `json:"horarios"`
	Inicio      string         `json:"inicio"`
	FimPrevisto string         `json:"fimPrevisto"`
	Alunos      int            `json:"alunos"`
	Capacidade  int            `json:"capacidade"`
	Vagas       int            `json:"vagas"`
}

// JanelaTurma — um trecho livre da semana onde caberia uma turma nova.
type JanelaTurma struct {
	DiaSemana  int    `json:"diaSemana"`
	HoraInicio string `json:"horaInicio"`
	HoraFim    string `json:"horaFim"`
}

// TurmaRetrato — o que o banco do bot lembra de uma turma.
type TurmaRetrato struct {
	TurmaID     int64
	Nome        string
	Curso       string
	Horarios    []HorarioTurma
	Inicio      string
	FimPrevisto string
	Vagas       int
	CapturadoEm time.Time
	SumiuEm     *time.Time
}

type OrigemTurmas int

const (
	// TurmasSemDados — o valor zero de propósito: quem não preencher o estado
	// cai no comportamento de hoje (não afirma nada sobre turma).
	TurmasSemDados OrigemTurmas = iota
	TurmasAoVivo
	TurmasDoRetrato
)

// TurmaAfirmavel — uma turma que o bot PODE citar, com o quanto pode confiar
// na vaga.
type TurmaAfirmavel struct {
	Nome        string
	Curso       string
	Horarios    []HorarioTurma
	Inicio      string
	FimPrevisto string
	Vagas       int
	// VagasConfirmadas false = retrato velho: "tinha vaga em DD/MM, a equipe
	// confirma".
	VagasConfirmadas bool
	CapturadoEm      time.Time
}

// EstadoTurmas — o que o prompt recebe.
type EstadoTurmas struct {
	Origem OrigemTurmas
	Turmas []TurmaAfirmavel
	// Janelas nil = consulta dos horários livres falhou ou não foi feita; a
	// regra da turma nova fica como antes (sem dado).
	Janelas []JanelaTurma
}

func hojeNaEscolaBot(agora time.Time) string {
	return agora.In(fusoEscola).Format("2006-01-02")
}

// decideTurmas é a tabela de decisão da spec, linha a linha.
func decideTurmas(aoVivo []TurmaAoVivo, errAoVivo error, retrato []TurmaRetrato, agora time.Time) EstadoTurmas {
	hoje := hojeNaEscolaBot(agora)
	var e EstadoTurmas
	if errAoVivo == nil {
		for _, t := range aoVivo {
			if t.FimPrevisto < hoje {
				continue // defesa: a API já filtra, mas vencida nunca é afirmada
			}
			e.Turmas = append(e.Turmas, TurmaAfirmavel{
				Nome: t.Nome, Curso: t.Curso.Nome, Horarios: t.Horarios, Inicio: t.Inicio,
				FimPrevisto: t.FimPrevisto, Vagas: t.Vagas, VagasConfirmadas: true, CapturadoEm: agora,
			})
		}
		// Lista vazia = nenhuma turma LIBERADA pela equipe, não "a escola não
		// tem turma". Sem turma afirmável, fica sem dados (comportamento de hoje).
		if len(e.Turmas) > 0 {
			e.Origem = TurmasAoVivo
		}
		return e
	}
	for _, r := range retrato {
		if r.SumiuEm != nil || r.FimPrevisto < hoje {
			continue
		}
		e.Turmas = append(e.Turmas, TurmaAfirmavel{
			Nome: r.Nome, Curso: r.Curso, Horarios: r.Horarios, Inicio: r.Inicio, FimPrevisto: r.FimPrevisto,
			Vagas: r.Vagas, VagasConfirmadas: agora.Sub(r.CapturadoEm) <= retratoConfiavel, CapturadoEm: r.CapturadoEm,
		})
	}
	if len(e.Turmas) > 0 {
		e.Origem = TurmasDoRetrato
	}
	return e
}

// mesesDeAulaBot: meses completos entre o início e hoje (0 se não começou).
func mesesDeAulaBot(inicio, hoje string) int {
	i, err1 := time.Parse("2006-01-02", inicio)
	h, err2 := time.Parse("2006-01-02", hoje)
	if err1 != nil || err2 != nil || h.Before(i) {
		return 0
	}
	m := (h.Year()-i.Year())*12 + int(h.Month()) - int(i.Month())
	if h.Day() < i.Day() {
		m--
	}
	if m < 0 {
		return 0
	}
	return m
}

func dataBR(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("02/01/2006")
}

func mesAnoBR(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("01/2006")
}

func diaPT(d int) string {
	if d < 0 || d > 6 {
		return "?"
	}
	return diasPT[d]
}

func horariosLegiveis(hs []HorarioTurma) string {
	partes := make([]string, len(hs))
	for i, h := range hs {
		partes[i] = fmt.Sprintf("%s %s–%s", diaPT(h.DiaSemana), h.HoraInicio, h.HoraFim)
	}
	return strings.Join(partes, ", ")
}

// BlocoDasTurmas escreve "# Turmas abertas agora" — só para quem tem idade de
// turma (idade conhecida e abaixo da faixa do particular). Para 17+ a turma não
// é opção; sem idade, a regra 1 já manda descobrir a idade antes de indicar.
// Sem nada afirmável e sem janelas: "" (comportamento de hoje).
func (q Qualificacao) BlocoDasTurmas(regras RegrasVenda, e EstadoTurmas, agora time.Time) string {
	faixa := regras.Resolvida().Faixas.faixaDe(q.AlunoIdade)
	if faixa != ParteFaixaTurma && faixa != ParteFaixaFimTurma {
		return ""
	}
	if len(e.Turmas) == 0 && e.Janelas == nil {
		return ""
	}
	hoje := hojeNaEscolaBot(agora)
	var b strings.Builder
	b.WriteString("# Turmas abertas agora\n")
	if len(e.Turmas) > 0 {
		if e.Origem == TurmasAoVivo {
			fmt.Fprintf(&b, "Consulta ao vivo na plataforma da escola (%s). São as turmas que a equipe confirmou:\n", agora.In(fusoEscola).Format("02/01 15:04"))
		} else {
			b.WriteString("A consulta à plataforma falhou agora; estes são os dados da última consulta boa, confirmados pela equipe:\n")
		}
		for _, t := range e.Turmas {
			fmt.Fprintf(&b, "- %s", t.Nome)
			if t.Curso != "" {
				fmt.Fprintf(&b, " (%s)", t.Curso)
			}
			fmt.Fprintf(&b, ": %s.", horariosLegiveis(t.Horarios))
			if t.Inicio > hoje {
				fmt.Fprintf(&b, " Começa em %s.", dataBR(t.Inicio))
			} else {
				m := mesesDeAulaBot(t.Inicio, hoje)
				plural := "meses"
				if m == 1 {
					plural = "mês"
				}
				fmt.Fprintf(&b, " Começou em %s (%d %s de aula).", dataBR(t.Inicio), m, plural)
			}
			fmt.Fprintf(&b, " Vai até %s.", mesAnoBR(t.FimPrevisto))
			switch {
			case t.Vagas <= 0:
				b.WriteString(" Situação: turma cheia — NÃO ofereça esta turma.\n")
			case t.VagasConfirmadas:
				b.WriteString(" Situação: tem vaga.\n")
			default:
				fmt.Fprintf(&b, " Situação: tinha vaga em %s — diga que a equipe confirma a vaga antes de fechar.\n", t.CapturadoEm.In(fusoEscola).Format("02/01"))
			}
		}
		b.WriteString("Como usar:\n")
		b.WriteString("- Pode citar o dia, o horário, quando a turma começou e quantos meses de aula ela já teve. Turma em andamento: siga a regra da reposição de aulas.\n")
		b.WriteString("- Diga se tem vaga, mas NÃO diga quantas vagas nem quantos alunos a turma tem.\n")
		b.WriteString("- Se o horário da pessoa não bate com nenhuma turma acima, diga que não há turma confirmada nesse horário e siga a indicação (particular ou experimental). NUNCA diga que a escola \"não tem turma\" de forma absoluta.\n")
	}
	if e.Janelas != nil {
		b.WriteString("Horários livres na escola onde caberia uma turma NOVA (uso interno — é a agenda da escola, não a do cliente):\n")
		if len(e.Janelas) == 0 {
			b.WriteString("- nenhum horário livre para turma nova nesta semana.\n")
		}
		js := append([]JanelaTurma(nil), e.Janelas...)
		sort.SliceStable(js, func(i, j int) bool { return ordemSemana(js[i].DiaSemana) < ordemSemana(js[j].DiaSemana) })
		for _, j := range js {
			fmt.Fprintf(&b, "- %s %s–%s\n", diaPT(j.DiaSemana), j.HoraInicio, j.HoraFim)
		}
		b.WriteString("- Só registre \"Oportunidade de turma nova\" se o horário que a pessoa quer cair dentro de um desses horários livres. Continua valendo: NÃO prometa abrir turma.\n")
	}
	b.WriteString("\n")
	return b.String()
}

// ordemSemana: segunda primeiro, domingo por último (como a escola lê a grade).
func ordemSemana(d int) int {
	if d == 0 {
		return 7
	}
	return d
}

// ── Fonte: consulta + cache + retrato ───────────────────────────────────────

type turmasAPI interface {
	TurmasAbertas(ctx context.Context) ([]TurmaAoVivo, error)
	HorariosLivres(ctx context.Context, abre, fecha string, duracao int, dias []int) ([]JanelaTurma, error)
}

type turmasRetratoStore interface {
	Grava(ctx context.Context, tenant TenantID, turmas []TurmaAoVivo, agora time.Time) error
	Lista(ctx context.Context, tenant TenantID) ([]TurmaRetrato, error)
}

// duracaoTurmaNova — janela mínima pra valer como horário de turma nova.
const duracaoTurmaNova = 60

// ttlFalhaTurmas — depois de uma falha, espera isso antes de tentar de novo:
// cada tentativa custa até o timeout, e isso sairia do tempo de resposta de
// toda mensagem enquanto a API estiver fora.
const ttlFalhaTurmas = time.Minute

// TurmasFonte — de onde o prompt tira o estado das turmas. NUNCA falha: no
// pior caso devolve EstadoTurmas{} (sem dados = comportamento de hoje).
type TurmasFonte struct {
	api    turmasAPI
	store  turmasRetratoStore
	ttl    time.Duration
	logger *slog.Logger
	agora  func() time.Time

	mu    sync.Mutex
	cache map[TenantID]turmasEmCache
}

type turmasEmCache struct {
	estado  EstadoTurmas
	valeAte time.Time
}

func NewTurmasFonte(api turmasAPI, store turmasRetratoStore, ttl time.Duration, logger *slog.Logger) *TurmasFonte {
	if logger == nil {
		logger = slog.Default()
	}
	return &TurmasFonte{api: api, store: store, ttl: ttl, logger: logger, agora: time.Now, cache: map[TenantID]turmasEmCache{}}
}

// Estado devolve o que o bot pode afirmar sobre as turmas agora.
func (f *TurmasFonte) Estado(ctx context.Context, tenant TenantID, abre, fecha string) EstadoTurmas {
	if f == nil {
		return EstadoTurmas{}
	}
	agora := f.agora()
	f.mu.Lock()
	if c, ok := f.cache[tenant]; ok && agora.Before(c.valeAte) {
		f.mu.Unlock()
		return c.estado
	}
	f.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	turmas, err := f.api.TurmasAbertas(ctx)
	var retrato []TurmaRetrato
	var janelas []JanelaTurma
	ttl := f.ttl
	if err == nil {
		if errG := f.store.Grava(ctx, tenant, turmas, agora); errG != nil {
			f.logger.Warn("turmas: retrato não gravado", "err", errG)
		}
		j, errJ := f.api.HorariosLivres(ctx, abre, fecha, duracaoTurmaNova, []int{0, 1, 2, 3, 4, 5, 6})
		if errJ != nil {
			f.logger.Warn("turmas: horários livres indisponíveis", "err", errJ)
		} else {
			janelas = j
			if janelas == nil {
				janelas = []JanelaTurma{}
			}
		}
	} else {
		f.logger.Warn("turmas: consulta ao vivo falhou, usando o retrato", "err", err)
		ttl = ttlFalhaTurmas
		r, errR := f.store.Lista(ctx, tenant)
		if errR != nil {
			f.logger.Warn("turmas: retrato indisponível", "err", errR)
		}
		retrato = r
	}
	e := decideTurmas(turmas, err, retrato, agora)
	e.Janelas = janelas

	f.mu.Lock()
	f.cache[tenant] = turmasEmCache{estado: e, valeAte: agora.Add(ttl)}
	f.mu.Unlock()
	return e
}

// ── Cliente HTTP da API central ─────────────────────────────────────────────

// TurmasAPIHTTP chama a API central (mesmo host das Tarefas, AGENT_GO_URL)
// com o PLATFORM_API_TOKEN.
type TurmasAPIHTTP struct {
	base  string
	token string
	http  *http.Client
}

func NewTurmasAPIHTTP(base, token string) *TurmasAPIHTTP {
	return &TurmasAPIHTTP{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 5 * time.Second}}
}

func (c *TurmasAPIHTTP) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *TurmasAPIHTTP) TurmasAbertas(ctx context.Context) ([]TurmaAoVivo, error) {
	var out struct {
		Turmas *[]TurmaAoVivo `json:"turmas"`
	}
	if err := c.get(ctx, "/portal/turmas-abertas", &out); err != nil {
		return nil, err
	}
	// Resposta sem o campo não é "zero turmas": é contrato quebrado.
	if out.Turmas == nil {
		return nil, errors.New("turmas-abertas: resposta sem o campo turmas")
	}
	return *out.Turmas, nil
}

func (c *TurmasAPIHTTP) HorariosLivres(ctx context.Context, abre, fecha string, duracao int, dias []int) ([]JanelaTurma, error) {
	ds := make([]string, len(dias))
	for i, d := range dias {
		ds[i] = strconv.Itoa(d)
	}
	q := url.Values{}
	q.Set("abre", abre)
	q.Set("fecha", fecha)
	q.Set("duracao", strconv.Itoa(duracao))
	q.Set("dias", strings.Join(ds, ","))
	var out struct {
		Janelas *[]JanelaTurma `json:"janelas"`
	}
	if err := c.get(ctx, "/agenda/horarios-livres?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	if out.Janelas == nil {
		return nil, errors.New("horarios-livres: resposta sem o campo janelas")
	}
	return *out.Janelas, nil
}

// ── Retrato no banco (0046) ─────────────────────────────────────────────────

// TurmaRetratoRepo usa as queries do sqlc (db/query/turmas.sql) sobre o pool
// pgx, via o adaptador database/sql do próprio pgx.
type TurmaRetratoRepo struct {
	sqlDB *sql.DB
}

func NewTurmaRetratoRepo(pool *pgxpool.Pool) *TurmaRetratoRepo {
	return &TurmaRetratoRepo{sqlDB: stdlib.OpenDBFromPool(pool)}
}

// Grava faz upsert de cada turma e marca como sumidas as que não vieram — numa
// transação só, para o retrato nunca ficar pela metade.
func (r *TurmaRetratoRepo) Grava(ctx context.Context, tenant TenantID, turmas []TurmaAoVivo, agora time.Time) error {
	tx, err := r.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	presentes := make([]string, 0, len(turmas))
	for _, t := range turmas {
		inicio, err1 := time.Parse("2006-01-02", t.Inicio)
		fim, err2 := time.Parse("2006-01-02", t.FimPrevisto)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("turma %d: data inválida (%q, %q)", t.ID, t.Inicio, t.FimPrevisto)
		}
		horarios, err := json.Marshal(t.Horarios)
		if err != nil {
			return err
		}
		if err := q.UpsertTurmaRetrato(ctx, db.UpsertTurmaRetratoParams{
			TenantID: tenant, TurmaID: t.ID, Nome: t.Nome, Curso: t.Curso.Nome, Horarios: horarios,
			Inicio: inicio, FimPrevisto: fim, Alunos: t.Alunos, Capacidade: t.Capacidade, Vagas: t.Vagas, CapturadoEm: agora,
		}); err != nil {
			return err
		}
		presentes = append(presentes, strconv.FormatInt(t.ID, 10))
	}
	if err := q.MarcaTurmasSumidas(ctx, db.MarcaTurmasSumidasParams{Agora: agora, TenantID: tenant, Presentes: strings.Join(presentes, ",")}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *TurmaRetratoRepo) Lista(ctx context.Context, tenant TenantID) ([]TurmaRetrato, error) {
	rows, err := db.New(r.sqlDB).ListaTurmasRetrato(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]TurmaRetrato, 0, len(rows))
	for _, row := range rows {
		t := TurmaRetrato{TurmaID: row.TurmaID, Nome: row.Nome, Curso: row.Curso, Inicio: row.Inicio,
			FimPrevisto: row.FimPrevisto, Vagas: row.Vagas, CapturadoEm: row.CapturadoEm}
		if err := json.Unmarshal(row.Horarios, &t.Horarios); err != nil {
			return nil, fmt.Errorf("retrato da turma %d: %w", row.TurmaID, err)
		}
		if row.SumiuEm.Valid {
			s := row.SumiuEm.Time
			t.SumiuEm = &s
		}
		out = append(out, t)
	}
	return out, nil
}
