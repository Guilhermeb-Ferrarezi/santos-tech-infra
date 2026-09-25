package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// As duas consultas que o bot de vendas faz (spec 2026-09-25-bot-turmas-ao-vivo):
// "que turma tem vaga" (Portal + Agenda) e "que horário está livre pra abrir
// turma nova" (só Agenda). As regras ficam em funções puras, testadas sem
// banco; os handlers só carregam os dados e validam parâmetros.

// ── Turmas abertas ─────────────────────────────────────────────────────────

type HorarioTurma struct {
	DiaSemana  int    `json:"diaSemana"`
	HoraInicio string `json:"horaInicio"`
	HoraFim    string `json:"horaFim"`
}

type CursoResumo struct {
	ID   int64  `json:"id"`
	Nome string `json:"nome"`
}

type TurmaAberta struct {
	ID               int64          `json:"id"`
	Nome             string         `json:"nome"`
	Curso            CursoResumo    `json:"curso"`
	Horarios         []HorarioTurma `json:"horarios"`
	Inicio           string         `json:"inicio"`
	FimPrevisto      string         `json:"fimPrevisto"`
	MesesDeAndamento int            `json:"mesesDeAndamento"`
	Alunos           int            `json:"alunos"`
	Capacidade       int            `json:"capacidade"`
	Vagas            int            `json:"vagas"`
	// Divergente: o horário da Agenda (que vale pro bot) não bate com a grade
	// do Portal (class_schedule, usada pela chamada). A tela da turma avisa.
	Divergente bool `json:"divergente"`
}

// turmaPortalLinha: o que o Portal diz de uma turma de grupo liberada e em vigor.
type turmaPortalLinha struct {
	ID         int64
	Nome       string
	CursoID    int64
	CursoNome  string
	Inicio     time.Time
	Fim        time.Time
	Capacidade int
	Alunos     int
}

// mesesDeAndamento: meses completos entre o início e hoje (0 se ainda não começou).
func mesesDeAndamento(inicio, hoje time.Time) int {
	if hoje.Before(inicio) {
		return 0
	}
	m := (hoje.Year()-inicio.Year())*12 + int(hoje.Month()) - int(inicio.Month())
	if hoje.Day() < inicio.Day() {
		m--
	}
	if m < 0 {
		return 0
	}
	return m
}

func horarioDoEvento(e AgendaEvento) HorarioTurma {
	h := HorarioTurma{HoraInicio: hhmm(e.HoraInicio), HoraFim: hhmm(e.HoraFim)}
	if e.DiaSemana != nil {
		h.DiaSemana = *e.DiaSemana
	}
	return h
}

func hhmm(s string) string {
	if len(s) >= 5 {
		return s[:5]
	}
	return s
}

func ordenaHorarios(h []HorarioTurma) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].DiaSemana != h[j].DiaSemana {
			return h[i].DiaSemana < h[j].DiaSemana
		}
		return h[i].HoraInicio < h[j].HoraInicio
	})
}

// montaTurmasAbertas cruza as turmas do Portal com os horários ligados na
// Agenda. Turma sem nenhum horário aula_turma ligado fica de fora: sem
// horário o bot não tem o que afirmar (fail-closed).
func montaTurmasAbertas(linhas []turmaPortalLinha, eventos []AgendaEvento, grade map[int64][]HorarioTurma, hoje time.Time) []TurmaAberta {
	out := []TurmaAberta{}
	for _, l := range linhas {
		evs := horariosDaTurma(eventos, l.ID)
		if len(evs) == 0 {
			continue
		}
		horarios := make([]HorarioTurma, len(evs))
		for i, e := range evs {
			horarios[i] = horarioDoEvento(e)
		}
		ordenaHorarios(horarios)
		vagas := l.Capacidade - l.Alunos
		if vagas < 0 {
			vagas = 0
		}
		out = append(out, TurmaAberta{
			ID: l.ID, Nome: l.Nome, Curso: CursoResumo{ID: l.CursoID, Nome: l.CursoNome},
			Horarios: horarios, Inicio: l.Inicio.Format("2006-01-02"), FimPrevisto: l.Fim.Format("2006-01-02"),
			MesesDeAndamento: mesesDeAndamento(l.Inicio, hoje),
			Alunos:           l.Alunos, Capacidade: l.Capacidade, Vagas: vagas,
			Divergente: gradeDiverge(horarios, grade[l.ID]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// gradeDiverge: só compara quando o Portal tem grade — sem class_schedule não
// há o que contrariar.
func gradeDiverge(agenda, portal []HorarioTurma) bool {
	if len(portal) == 0 {
		return false
	}
	p := append([]HorarioTurma(nil), portal...)
	ordenaHorarios(p)
	if len(p) != len(agenda) {
		return true
	}
	for i := range p {
		if p[i] != agenda[i] {
			return true
		}
	}
	return false
}

type turmasAbertasFiltros struct {
	curso     *int64
	diaSemana *int
}

func parseTurmasAbertasFiltros(r *http.Request) (turmasAbertasFiltros, error) {
	var f turmasAbertasFiltros
	q := r.URL.Query()
	if v := q.Get("curso"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return f, validationErr("curso inválido")
		}
		f.curso = &id
	}
	if v := q.Get("diaSemana"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 0 || d > 6 {
			return f, validationErr("diaSemana deve ser de 0 (domingo) a 6 (sábado)")
		}
		f.diaSemana = &d
	}
	return f, nil
}

func (f turmasAbertasFiltros) aplica(ts []TurmaAberta) []TurmaAberta {
	out := []TurmaAberta{}
	for _, t := range ts {
		if f.curso != nil && t.Curso.ID != *f.curso {
			continue
		}
		if f.diaSemana != nil {
			tem := false
			for _, h := range t.Horarios {
				if h.DiaSemana == *f.diaSemana {
					tem = true
				}
			}
			if !tem {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// portalTurmasLiberadas: turmas de grupo liberadas pro bot e com fim >= hoje,
// com o total de matrículas. SQL no store do Portal, como o resto do domínio
// (o schema legado do Portal não é gerido pelo sqlc deste serviço).
func (s *Server) portalTurmasLiberadas(ctx context.Context, hoje time.Time) ([]turmaPortalLinha, map[int64][]HorarioTurma, error) {
	rows, err := s.portalDB.Query(ctx, `
		SELECT c.id, COALESCE(c.name, ''), c.course_id, COALESCE(co.name, ''), c.start_date, c.end_date, c.capacity,
		       (SELECT COUNT(*) FROM enrollment e WHERE e.class_id = c.id)::int
		FROM class c LEFT JOIN course co ON co.id = c.course_id
		WHERE NOT c.individual_class AND c.bot_validada_em IS NOT NULL AND c.end_date::date >= $1::date
		ORDER BY c.id`, hoje)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var linhas []turmaPortalLinha
	var ids []int64
	for rows.Next() {
		var l turmaPortalLinha
		if err := rows.Scan(&l.ID, &l.Nome, &l.CursoID, &l.CursoNome, &l.Inicio, &l.Fim, &l.Capacidade, &l.Alunos); err != nil {
			return nil, nil, err
		}
		if l.Nome == "" {
			l.Nome = fmt.Sprintf("Turma %d", l.ID)
		}
		linhas = append(linhas, l)
		ids = append(ids, l.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	grade := map[int64][]HorarioTurma{}
	if len(ids) == 0 {
		return linhas, grade, nil
	}
	gr, err := s.portalDB.Query(ctx, `
		SELECT class_id, day_of_week, to_char(start_time, 'HH24:MI'), to_char(end_time, 'HH24:MI')
		FROM class_schedule WHERE class_id = ANY($1)`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer gr.Close()
	for gr.Next() {
		var id int64
		var h HorarioTurma
		if err := gr.Scan(&id, &h.DiaSemana, &h.HoraInicio, &h.HoraFim); err != nil {
			return nil, nil, err
		}
		grade[id] = append(grade[id], h)
	}
	return linhas, grade, gr.Err()
}

// GET /portal/turmas-abertas[?curso=ID&diaSemana=0-6]
func (s *Server) handleTurmasAbertas(w http.ResponseWriter, r *http.Request) {
	f, err := parseTurmasAbertasFiltros(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	agora := time.Now()
	hoje := hojeNaEscola(agora)
	linhas, grade, err := s.portalTurmasLiberadas(r.Context(), hoje)
	if err != nil {
		writeErr(w, err)
		return
	}
	eventos, err := s.listAgendaEventos(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"geradoEm": agora.In(posaulaLocation()).Format(time.RFC3339),
		"turmas":   f.aplica(montaTurmasAbertas(linhas, eventos, grade, hoje)),
	})
}

// ── Horários livres ────────────────────────────────────────────────────────

type JanelaLivre struct {
	DiaSemana  int    `json:"diaSemana"`
	HoraInicio string `json:"horaInicio"`
	HoraFim    string `json:"horaFim"`
}

// eventoOcupaSemana: evento semanal ainda em vigor. Só o semanal conta —
// avulso (Arena, experimental) cai numa semana só e é conflito de política,
// que a escola resolve com confirmação. Qualquer evento semanal que encosta no
// horário tira o laboratório da turma nova, que precisa dos 10 PCs e do
// professor: com PCs informados, sobra menos que 10; com PCs não informados
// (nil), o desconhecido conta como cheio (fail-closed).
func eventoOcupaSemana(e AgendaEvento, hoje time.Time) bool {
	if e.Recorrencia != "semanal" || e.DiaSemana == nil {
		return false
	}
	if e.DataFimRecorrencia != nil {
		if fim, err := parseData(*e.DataFimRecorrencia); err == nil && fim.Before(hoje) {
			return false
		}
	}
	return true
}

// janelasLivres: por dia pedido, os trechos de [abre, fecha] sem evento
// semanal que caibam pelo menos `duracao` minutos.
func janelasLivres(eventos []AgendaEvento, dias []int, abre, fecha, duracao int, hoje time.Time) []JanelaLivre {
	out := []JanelaLivre{}
	for _, dia := range dias {
		type iv struct{ ini, fim int }
		var ocup []iv
		for _, e := range eventos {
			if !eventoOcupaSemana(e, hoje) || *e.DiaSemana != dia {
				continue
			}
			ini, err1 := parseHoraMinutos(e.HoraInicio)
			fim, err2 := parseHoraMinutos(e.HoraFim)
			if err1 != nil || err2 != nil {
				continue
			}
			ocup = append(ocup, iv{ini, fim})
		}
		sort.Slice(ocup, func(i, j int) bool { return ocup[i].ini < ocup[j].ini })
		cursor := abre
		emite := func(ini, fim int) {
			if fim-ini >= duracao {
				out = append(out, JanelaLivre{DiaSemana: dia, HoraInicio: minParaHora(ini), HoraFim: minParaHora(fim)})
			}
		}
		for _, o := range ocup {
			if o.fim <= cursor {
				continue
			}
			if o.ini >= fecha {
				break
			}
			if o.ini > cursor {
				emite(cursor, o.ini)
			}
			cursor = o.fim
		}
		if cursor < fecha {
			emite(cursor, fecha)
		}
	}
	return out
}

func minParaHora(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

type horariosLivresParams struct {
	abre, fecha, duracao int
	dias                 []int
}

// parseHoraEstrita: exatamente HH:MM (parseHoraMinutos aceita sufixo, que em
// parâmetro de URL é lixo).
func parseHoraEstrita(s string) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("hora inválida")
	}
	return parseHoraMinutos(s)
}

func parseHorariosLivresParams(r *http.Request) (horariosLivresParams, error) {
	var p horariosLivresParams
	q := r.URL.Query()
	var err1, err2 error
	p.abre, err1 = parseHoraEstrita(q.Get("abre"))
	p.fecha, err2 = parseHoraEstrita(q.Get("fecha"))
	if err1 != nil || err2 != nil || p.fecha <= p.abre {
		return p, validationErr("abre e fecha são obrigatórios (HH:MM), com fecha depois de abre")
	}
	d, err := strconv.Atoi(q.Get("duracao"))
	if err != nil || d < 15 || d > 480 {
		return p, validationErr("duracao deve ficar entre 15 e 480 minutos")
	}
	p.duracao = d
	partes := strings.Split(q.Get("dias"), ",")
	vistos := map[int]bool{}
	for _, s := range partes {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || v < 0 || v > 6 || vistos[v] {
			return p, validationErr("dias deve ser uma lista de 0 (domingo) a 6 (sábado), sem repetir")
		}
		vistos[v] = true
		p.dias = append(p.dias, v)
	}
	sort.Ints(p.dias)
	return p, nil
}

// GET /agenda/horarios-livres?abre=HH:MM&fecha=HH:MM&duracao=MIN&dias=0,..,6
func (s *Server) handleHorariosLivres(w http.ResponseWriter, r *http.Request) {
	p, err := parseHorariosLivresParams(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	eventos, err := s.listAgendaEventos(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	agora := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"geradoEm": agora.In(posaulaLocation()).Format(time.RFC3339),
		"janelas":  janelasLivres(eventos, p.dias, p.abre, p.fecha, p.duracao, hojeNaEscola(agora)),
	})
}
