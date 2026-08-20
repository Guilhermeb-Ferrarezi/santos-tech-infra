package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
)

// ── Vínculo professor ↔ turma ────────────────────────────────────────────────
//
// Até aqui a autorização do portal era PLANA: permGuard decidia só por papel
// (admin/professor) ou permissão global do cargo, e nenhum store recebia o ID
// do ator. Na prática um professor conseguia ler `GET /portal/classes/57/...`
// de uma turma que não é dele. A tabela `class` não tem dono no schema — não
// existia sequer a capacidade de escopar.
//
// Este arquivo introduz essa capacidade em três partes:
//  1. `class_teacher (class_id, user_id)` — criada de forma idempotente na
//     migração do portal (portal_migrate.go).
//  2. Os helpers `portalCanAccess*` abaixo, que respondem "esse ator alcança
//     essa turma/fase/aluno/resposta?" — admin sempre alcança tudo.
//  3. A env `PORTAL_CLASS_SCOPE`, DESLIGADA por padrão. Com a flag off os
//     helpers retornam nil imediatamente e o comportamento é idêntico ao de
//     hoje — ligar sem popular `class_teacher` trancaria todo professor fora
//     das próprias turmas.
//
// Para ligar: popular `class_teacher` com o par (turma, professor) de TODAS as
// turmas ativas e só então setar PORTAL_CLASS_SCOPE=1 (ver .env.example).

// portalClassScopeEnv é o nome da env que liga o escopo por turma.
const portalClassScopeEnv = "PORTAL_CLASS_SCOPE"

var (
	portalClassScopeOnce sync.Once
	portalClassScopeOn   bool
)

// portalClassScopeEnabled lê PORTAL_CLASS_SCOPE uma única vez (a env não muda
// em runtime). Aceita "1"/"true" para ligar; qualquer outro valor mantém o
// escopo desligado — o default é fail-open de propósito, porque a tabela de
// vínculo pode ainda estar vazia em produção.
func portalClassScopeEnabled() bool {
	portalClassScopeOnce.Do(func() {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(portalClassScopeEnv)))
		portalClassScopeOn = v == "1" || v == "true"
	})
	return portalClassScopeOn
}

// portalScopeForbidden é o erro devolvido quando o ator não alcança o recurso.
// Mensagem propositalmente genérica (não revela se a turma existe).
func portalScopeForbidden() error {
	return appErr(http.StatusForbidden, "FORBIDDEN", "Acesso restrito a turmas vinculadas ao seu usuário")
}

// portalActorIsAdmin resolve o papel do ator. Erro de leitura do usuário é
// tratado como "não admin" (fail-closed nesse ponto específico: quem não
// conseguimos identificar não recebe passe livre).
func (s *Server) portalActorIsAdmin(ctx context.Context, actorID int64) bool {
	u, err := s.cachedUserByID(ctx, actorID)
	if err != nil || u == nil {
		return false
	}
	return u.Role == RoleAdmin
}

// portalCanAccessClass: o ator alcança a turma? Admin sempre; os demais só se
// houver linha em class_teacher. No-op com PORTAL_CLASS_SCOPE desligada.
func (s *Server) portalCanAccessClass(ctx context.Context, actorID, classID int64) error {
	if !portalClassScopeEnabled() {
		return nil
	}
	if actorID <= 0 {
		return portalScopeForbidden()
	}
	if s.portalActorIsAdmin(ctx, actorID) {
		return nil
	}
	var ok bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM class_teacher WHERE class_id = $1 AND user_id = $2)`, classID, actorID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return portalScopeForbidden()
	}
	return nil
}

// portalCanAccessPhase: a fase pertence ao curso de alguma turma do ator.
// O vínculo é no nível do curso (e não do módulo atual) porque uma turma
// percorre todos os módulos do curso ao longo do semestre.
func (s *Server) portalCanAccessPhase(ctx context.Context, actorID, phaseID int64) error {
	if !portalClassScopeEnabled() {
		return nil
	}
	if actorID <= 0 {
		return portalScopeForbidden()
	}
	if s.portalActorIsAdmin(ctx, actorID) {
		return nil
	}
	var ok bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM phase p
		JOIN module m ON m.id = p.module_id
		JOIN class c ON c.course_id = m.course_id
		JOIN class_teacher ct ON ct.class_id = c.id
		WHERE p.id = $1 AND ct.user_id = $2)`, phaseID, actorID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return portalScopeForbidden()
	}
	return nil
}

// portalCanAccessStudent: o aluno está matriculado em alguma turma do ator.
func (s *Server) portalCanAccessStudent(ctx context.Context, actorID, studentID int64) error {
	if !portalClassScopeEnabled() {
		return nil
	}
	if actorID <= 0 {
		return portalScopeForbidden()
	}
	if s.portalActorIsAdmin(ctx, actorID) {
		return nil
	}
	var ok bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM enrollment e
		JOIN class_teacher ct ON ct.class_id = e.class_id
		WHERE e.user_id = $1 AND ct.user_id = $2)`, studentID, actorID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return portalScopeForbidden()
	}
	return nil
}

// portalCanAccessAnswers: TODAS as respostas do lote pertencem a alunos de
// turmas do ator. Uma única resposta fora do escopo derruba a requisição
// inteira — corrigir "quase tudo" silenciosamente seria pior.
func (s *Server) portalCanAccessAnswers(ctx context.Context, actorID int64, answerIDs []int64) error {
	if !portalClassScopeEnabled() || len(answerIDs) == 0 {
		return nil
	}
	if actorID <= 0 {
		return portalScopeForbidden()
	}
	if s.portalActorIsAdmin(ctx, actorID) {
		return nil
	}
	var outside int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM answer a
		WHERE a.id = ANY($1) AND NOT EXISTS (
			SELECT 1 FROM enrollment e
			JOIN class_teacher ct ON ct.class_id = e.class_id
			WHERE e.user_id = a.user_id AND ct.user_id = $2)`, answerIDs, actorID).Scan(&outside); err != nil {
		return err
	}
	if outside > 0 {
		return portalScopeForbidden()
	}
	return nil
}

// portalCanAccessRoom: a sala pertence a uma turma do ator (a rota de sala
// recebe só o roomId, então a turma vem por JOIN).
func (s *Server) portalCanAccessRoom(ctx context.Context, actorID, roomID int64) error {
	if !portalClassScopeEnabled() {
		return nil
	}
	if actorID <= 0 {
		return portalScopeForbidden()
	}
	if s.portalActorIsAdmin(ctx, actorID) {
		return nil
	}
	var ok bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM class_rooms cr
		JOIN class_teacher ct ON ct.class_id = cr.class_id
		WHERE cr.id = $1 AND ct.user_id = $2)`, roomID, actorID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return portalScopeForbidden()
	}
	return nil
}
