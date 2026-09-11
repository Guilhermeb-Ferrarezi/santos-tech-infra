package main

import (
	"fmt"
	"log/slog"
	"net/http"
)

// GET /portal/students-overview — visão consolidada de alunos pro dashboard
// administrativo (ver dashboard/web Home.tsx): nome, curso, progresso em
// fases ("aulas") e professor (quando class_teacher já tem o vínculo). Rota
// transversal (não é uma "área" específica do portal), mesmo padrão de
// GET /portal/overview e GET /portal/users — ver portal_routes.go.
func (s *Server) handlePortalStudentsOverview(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalStudentsOverview(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

// GET /portal/me/overview — o próprio progresso de quem está logado (Home,
// quando a pessoa não é staff — ver dashboard/web Home.tsx). Rota aberta a
// qualquer sessão autenticada (não passa por portalAnyRead/portalRead): o
// escopo já é a própria pessoa, não precisa de permissão de portal nenhuma.
// Sem matrícula no Portal (a maioria dos usuários do auth central hoje):
// devolve lista vazia, não erro.
func (s *Server) handlePortalMyOverview(w http.ResponseWriter, r *http.Request) {
	u, err := s.cachedUserByID(r.Context(), userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if u == nil {
		writeErr(w, appErr(http.StatusUnauthorized, "UNAUTHORIZED", "Token inválido ou expirado"))
		return
	}
	items, err := s.portalMyOverview(r.Context(), u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// DELETE /portal/students/{studentId} — exclui o aluno do Portal E o login dele
// no auth central. É a opção "excluir do portal também" do diálogo de remover
// aluno da turma (ver TurmaDetalhe.tsx no dashboard); a outra opção de lá é
// DELETE /portal/classes/{classId}/students/{studentId}, que só desmatricula.
//
// Os dois bancos são separados e casam por EMAIL (ver portalSyncUserFromAuth).
// A ordem importa: primeiro o Portal, depois o auth. Se o auth falhasse
// primeiro, o aluno ficaria sem login mas continuaria aparecendo nas turmas —
// o pior dos dois estados. O auth é best-effort justamente por isso: um erro
// ali não desfaz a exclusão no Portal, só entra no log.
func (s *Server) handlePortalDeleteStudent(w http.ResponseWriter, r *http.Request) {
	studentID, err := portalPathID(r, "studentId")
	if err != nil {
		writeErr(w, err)
		return
	}
	email, err := s.portalDeleteStudent(r.Context(), studentID)
	if err != nil {
		writeErr(w, err)
		return
	}

	loginRemovido := false
	if email != "" {
		if u, err := s.userByEmail(r.Context(), email); err == nil && u != nil && u.Role == 1 {
			if err := s.deleteUser(r.Context(), u.ID); err != nil {
				slog.WarnContext(r.Context(), "aluno excluído do portal, login do auth permaneceu",
					"email", email, "err", err)
			} else {
				loginRemovido = true
			}
		}
	}

	s.portalLogActivity(r, "student_delete", "student", fmt.Sprint(studentID), map[string]any{
		"email": email, "loginRemovido": loginRemovido,
	})
	writeJSON(w, http.StatusOK, map[string]any{"email": email, "loginRemovido": loginRemovido})
}
