package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Handlers do Material vivo (ver posaula_material.go). Staff: lê, edita,
// pede a semente e mexe no histórico do material de um curso (guard
// portal_cursos — professor lê, admin/cargo escreve). Aluno: lê o material
// do curso em que está matriculado (só authGuard; a matrícula é a
// autorização, como nos arquivos do diário).

// materialBodyBytesMax: o PUT aceita mais que os 64 KiB do portalBodyJSON —
// 60 mil caracteres acentuados em UTF-8, escapados em JSON, passam disso.
const materialBodyBytesMax = 512 << 10

// materialPutInput é o corpo do PUT: o markdown inteiro (não é patch).
// Version: a versão que a tela leu antes de editar — concorrência otimista
// (ver courseDocSalvarManual). 0 = sem checagem (compat com quem não manda).
type materialPutInput struct {
	BodyMd  string `json:"bodyMd"`
	Version int    `json:"version"`
}

func (in *materialPutInput) validate() error {
	in.BodyMd = strings.TrimSpace(strings.ReplaceAll(in.BodyMd, "\r\n", "\n"))
	if in.BodyMd == "" {
		return validationErr("bodyMd não pode ser vazio")
	}
	if !utf8.ValidString(in.BodyMd) {
		return validationErr("bodyMd com caracteres inválidos")
	}
	// Fecha uma cerca de código deixada aberta (edição manual incompleta) —
	// senão as seções seguintes seriam lidas como código no próximo
	// materialDividir (ver materialFecharCercaAberta).
	in.BodyMd = materialFecharCercaAberta(in.BodyMd)
	if in.Version < 0 {
		return validationErr("version inválida")
	}
	if n := utf8.RuneCountInString(in.BodyMd); n > materialBodyMax {
		return validationErr(fmt.Sprintf("bodyMd deve ter no máximo %d caracteres (recebidos %d)", materialBodyMax, n))
	}
	in.BodyMd += "\n"
	return nil
}

// materialCourseExists separa "curso não existe" (404) de "curso sem
// material" (200 com doc null).
func (s *Server) materialCourseExists(ctx context.Context, courseID int64) error {
	_, err := s.portalGetCourse(ctx, courseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFoundErr("Curso")
	}
	return err
}

// handlePortalGetCourseDoc (GET /portal/courses/{courseId}/doc) → {doc}.
// doc é null (com 200) enquanto ninguém pediu a semente; depois do seed a
// linha existe com version 0 e bodyMd vazio até a primeira versão sair —
// é assim que a tela mostra "gerando…"/"falhou" antes de existir material.
func (s *Server) handlePortalGetCourseDoc(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.materialCourseExists(r.Context(), courseID); err != nil {
		writeErr(w, err)
		return
	}
	doc, err := s.courseDocGet(r.Context(), courseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc": doc})
}

// handlePortalPutCourseDoc (PUT /portal/courses/{courseId}/doc {bodyMd}) →
// 200 {doc}. Versão nova com source 'manual' e o e-mail de quem salvou.
func (s *Server) handlePortalPutCourseDoc(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in materialPutInput
	r.Body = http.MaxBytesReader(w, r.Body, materialBodyBytesMax)
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.materialCourseExists(r.Context(), courseID); err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	version, err := s.courseDocSalvarManual(r.Context(), courseID, in.BodyMd, u.Email, in.Version)
	if errors.Is(err, errMaterialVersaoMudou) {
		writeErr(w, conflictErr("o material mudou enquanto você editava — recarregue e tente de novo"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "material_editado", "course", fmt.Sprint(courseID), map[string]any{
		"version": version, "chars": utf8.RuneCountInString(in.BodyMd),
	})
	doc, err := s.courseDocGet(r.Context(), courseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc": doc})
}

// handlePortalSeedCourseDoc (POST /portal/courses/{courseId}/doc/seed) →
// 202 {queued:true}. Escreve (ou reescreve — "Regerar") o material do zero
// com o Claude; as versões anteriores ficam no histórico. A versão da chave
// de idempotência é o instante do pedido, então pedir de novo gera de novo.
func (s *Server) handlePortalSeedCourseDoc(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.materialCourseExists(r.Context(), courseID); err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	if err := s.enqueueMaterial(r.Context(), materialPayload{CourseID: courseID, Versao: time.Now(), AuthorEmail: u.Email}); err != nil {
		slog.Warn("material: não consegui enfileirar a semente", "course", courseID, "err", err)
		writeErr(w, appErr(http.StatusServiceUnavailable, "QUEUE_UNAVAILABLE", "Não consegui enfileirar a geração agora — tente de novo em instantes"))
		return
	}
	s.portalLogActivity(r, "material_semear", "course", fmt.Sprint(courseID), nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// handlePortalCourseDocRevisions (GET /portal/courses/{courseId}/doc/revisions)
// → {revisions}, mais recente primeiro, sem o corpo.
func (s *Server) handlePortalCourseDocRevisions(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.materialCourseExists(r.Context(), courseID); err != nil {
		writeErr(w, err)
		return
	}
	revisions, err := s.courseDocRevisions(r.Context(), courseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revisions})
}

// handlePortalCourseDocRevision (GET /portal/courses/{courseId}/doc/revisions/{version})
// → {revision} com bodyMd. 404 se a versão não existe.
func (s *Server) handlePortalCourseDocRevision(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	version, err := portalPathID(r, "version")
	if err != nil {
		writeErr(w, err)
		return
	}
	rev, err := s.courseDocRevision(r.Context(), courseID, version)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev})
}

// handlePortalRestoreCourseDoc (POST /portal/courses/{courseId}/doc/revisions/{version}/restore)
// → 200 {doc}. Versão NOVA com o corpo da antiga (source 'restore') — o
// histórico nunca anda pra trás.
func (s *Server) handlePortalRestoreCourseDoc(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	version, err := portalPathID(r, "version")
	if err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	nova, err := s.courseDocRestaurar(r.Context(), courseID, version, u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "material_restaurado", "course", fmt.Sprint(courseID), map[string]any{
		"fromVersion": version, "version": nova,
	})
	doc, err := s.courseDocGet(r.Context(), courseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc": doc})
}

// handlePortalMyCourseDoc (GET /portal/me/courses/{courseId}/doc) → {doc}.
// O aluno lendo o material do curso da matrícula dele. Só authGuard: a
// matrícula (casada por e-mail) é a autorização; sem matrícula em turma
// desse curso, ou curso ainda sem material, é 404.
func (s *Server) handlePortalMyCourseDoc(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	u := s.portalUserFromSession(w, r)
	if u == nil {
		return
	}
	doc, err := s.courseDocDoAluno(r.Context(), u.Email, courseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc": doc})
}
