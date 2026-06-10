package main

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (s *Server) handlePortalOverview(w http.ResponseWriter, r *http.Request) {
	out, err := s.portalOverview(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePortalListCourses(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalListCourses(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalGetCourse(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	course, err := s.portalGetCourse(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Curso"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"course": course})
}

func (s *Server) handlePortalCreateCourse(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalUpdateCourse(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalDeleteCourse(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalListModules(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalListModules(r.Context(), courseID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}
func (s *Server) handlePortalCreateModule(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalUpdateModule(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalDeleteModule(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalListPhases(w http.ResponseWriter, r *http.Request) {
	moduleID, err := portalPathID(r, "moduleId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalListPhases(r.Context(), moduleID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}
func (s *Server) handlePortalCreatePhase(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalUpdatePhase(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
func (s *Server) handlePortalDeletePhase(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}
