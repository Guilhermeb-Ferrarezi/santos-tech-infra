package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santos-tech/auth/db"
)

// portalLegadoMinimo cria só o pedaço do schema legado do Portal que as turmas
// ao vivo tocam — o schema do Portal não é versionado neste repo (ver
// portal_migrate.go), então o teste traz o mínimo pra migração do Portal rodar
// por cima, como roda em produção.
const portalLegadoMinimo = `
CREATE TABLE IF NOT EXISTS "user" (id SERIAL PRIMARY KEY, email TEXT, name TEXT, role SMALLINT NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS course (id SERIAL PRIMARY KEY, name TEXT);
CREATE TABLE IF NOT EXISTS module (id SERIAL PRIMARY KEY, course_id INTEGER, index_order INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS class (
	id SERIAL PRIMARY KEY, name TEXT, course_id INTEGER NOT NULL, current_module_id INTEGER NOT NULL,
	start_date TIMESTAMP(3) NOT NULL, end_date TIMESTAMP(3) NOT NULL,
	individual_class BOOLEAN NOT NULL DEFAULT false,
	created_at TIMESTAMP(3) NOT NULL DEFAULT now(), updated_at TIMESTAMP(3) NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS enrollment (id SERIAL PRIMARY KEY, user_id INTEGER NOT NULL, class_id INTEGER NOT NULL, contract_date TIMESTAMP(3));
`

// servidorTurmasAoVivo sobe um Server contra o Postgres de teste, com o
// schema do auth (db/schema.sql já aplicado + migrate) e o Portal mínimo. Roda
// só com API_TEST_DATABASE_URL — banco descartável, as tabelas tocadas aqui
// são esvaziadas a cada teste.
func servidorTurmasAoVivo(t *testing.T) (*Server, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("API_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("API_TEST_DATABASE_URL vazio")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, portalLegadoMinimo); err != nil {
		t.Fatalf("portal legado: %v", err)
	}
	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migratePortal(ctx, pool); err != nil {
		t.Fatalf("migratePortal: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE agenda_eventos, class, enrollment, course, module, class_schedule, "user" RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	return &Server{db: pool, q: db.New(pool), portalDB: pool}, pool
}

// chamaHandler roda um handler com o usuário 1 no contexto e devolve status +
// corpo JSON decodificado. pattern registra o path param ({id}, {classId}).
func chamaHandler(t *testing.T, pattern string, h http.HandlerFunc, met, path, body string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	req := reqAs(httptest.NewRequest(met, path, strings.NewReader(body)), 1)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out map[string]any
	raw, _ := io.ReadAll(w.Body)
	_ = json.Unmarshal(raw, &out)
	return w.Code, out
}

// criaTurmaPortal insere uma turma no Portal e devolve o id.
func criaTurmaPortal(t *testing.T, pool *pgxpool.Pool, nome string, individual bool, inicio, fim string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO class (name, course_id, current_module_id, start_date, end_date, individual_class)
		 VALUES ($1, 1, 1, $2::date, $3::date, $4) RETURNING id`, nome, inicio, fim, individual).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// criaEventoAgenda insere um evento semanal direto no banco e devolve o id.
func criaEventoAgenda(t *testing.T, pool *pgxpool.Pool, tipo, titulo string, dia int, ini, fim string, pcs *int) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO agenda_eventos (tipo, titulo, computadores_usados, data_inicio, hora_inicio, hora_fim, recorrencia, dia_semana, status_preparo)
		 VALUES ($1, $2, $3, '2026-09-01', $4::time, $5::time, 'semanal', $6, 'nao_aplica') RETURNING id::text`,
		tipo, titulo, pcs, ini, fim, dia).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLigaEventoATurmaIntegracao(t *testing.T) {
	s, pool := servidorTurmasAoVivo(t)
	const rota = "PUT /agenda/eventos/{id}/turma"
	h := s.handleSetAgendaEventoTurma

	grupo := criaTurmaPortal(t, pool, "Turma Programação", false, "2026-08-01", "2027-08-01")
	particular := criaTurmaPortal(t, pool, "Walisson", true, "2026-09-08", "2027-03-08")
	evTurma := criaEventoAgenda(t, pool, "aula_turma", "Turma Programação", 6, "13:00", "15:00", nil)
	evPart := criaEventoAgenda(t, pool, "aula_particular", "Walisson", 3, "18:00", "19:00", nil)

	// Caminho feliz: aula_turma ↔ turma de grupo, e o GET devolve a ligação.
	code, out := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evTurma+"/turma", `{"portalClassId":`+itoa(grupo)+`}`)
	if code != 200 {
		t.Fatalf("ligar turma: %d %v", code, out)
	}
	ev, err := s.getAgendaEvento(context.Background(), evTurma)
	if err != nil || ev == nil || ev.PortalClassID == nil || *ev.PortalClassID != grupo {
		t.Fatalf("evento deveria estar ligado à turma %d: %+v %v", grupo, ev, err)
	}

	// aula_particular ↔ turma particular também liga.
	if code, out := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evPart+"/turma", `{"portalClassId":`+itoa(particular)+`}`); code != 200 {
		t.Fatalf("ligar particular: %d %v", code, out)
	}

	// Tipo não bate: 400, e nada muda.
	if code, _ := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evTurma+"/turma", `{"portalClassId":`+itoa(particular)+`}`); code != 400 {
		t.Fatalf("aula_turma em turma particular deveria ser 400, got %d", code)
	}
	if code, _ := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evPart+"/turma", `{"portalClassId":`+itoa(grupo)+`}`); code != 400 {
		t.Fatalf("aula_particular em turma de grupo deveria ser 400, got %d", code)
	}
	ev, _ = s.getAgendaEvento(context.Background(), evTurma)
	if ev.PortalClassID == nil || *ev.PortalClassID != grupo {
		t.Fatal("recusa não pode mexer na ligação existente")
	}

	// Turma inexistente: 404. Evento inexistente: 404. Corpo ruim: 400.
	if code, _ := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evTurma+"/turma", `{"portalClassId":999999}`); code != 404 {
		t.Fatalf("turma inexistente deveria ser 404, got %d", code)
	}
	if code, _ := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/00000000-0000-0000-0000-000000000000/turma", `{"portalClassId":`+itoa(grupo)+`}`); code != 404 {
		t.Fatalf("evento inexistente deveria ser 404, got %d", code)
	}
	if code, _ := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evTurma+"/turma", `xxx`); code != 400 {
		t.Fatalf("corpo inválido deveria ser 400, got %d", code)
	}

	// PUT normal do evento (sem o campo) NÃO apaga a ligação.
	pcs := 3
	in := AgendaEventoInput{Tipo: "aula_turma", Titulo: "Turma Programação (renomeada)", DataInicio: "2026-09-01",
		HoraInicio: "13:00", HoraFim: "15:00", ComputadoresUsados: &pcs, DiaSemana: intPtr(6)}
	if err := validateAgendaEventoInput(&in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.updateAgendaEvento(context.Background(), evTurma, in); err != nil {
		t.Fatal(err)
	}
	ev, _ = s.getAgendaEvento(context.Background(), evTurma)
	if ev.PortalClassID == nil || *ev.PortalClassID != grupo {
		t.Fatal("PUT do evento sem portalClassId apagou a ligação")
	}

	// null desliga.
	if code, out := chamaHandler(t, rota, h, "PUT", "/agenda/eventos/"+evTurma+"/turma", `{"portalClassId":null}`); code != 200 {
		t.Fatalf("desligar: %d %v", code, out)
	}
	ev, _ = s.getAgendaEvento(context.Background(), evTurma)
	if ev.PortalClassID != nil {
		t.Fatal("null deveria desligar")
	}
}

func intPtr(v int) *int { return &v }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
