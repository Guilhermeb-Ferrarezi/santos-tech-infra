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
	var in portalCourseInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	course, err := s.portalCreateCourse(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"course": course})
}

func (s *Server) handlePortalUpdateCourse(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalCourseInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	course, err := s.portalUpdateCourse(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"course": course})
}

func (s *Server) handlePortalDeleteCourse(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteByID(r.Context(), "course", id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalModuleInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	module, err := s.portalCreateModule(r.Context(), courseID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"module": module})
}

func (s *Server) handlePortalUpdateModule(w http.ResponseWriter, r *http.Request) {
	moduleID, err := portalPathID(r, "moduleId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalModuleInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	module, err := s.portalUpdateModule(r.Context(), moduleID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"module": module})
}

func (s *Server) handlePortalDeleteModule(w http.ResponseWriter, r *http.Request) {
	moduleID, err := portalPathID(r, "moduleId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteByID(r.Context(), "module", moduleID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	moduleID, err := portalPathID(r, "moduleId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalPhaseInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	phase, err := s.portalCreatePhase(r.Context(), moduleID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"phase": phase})
}

func (s *Server) handlePortalUpdatePhase(w http.ResponseWriter, r *http.Request) {
	phaseID, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalPhaseInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	phase, err := s.portalUpdatePhase(r.Context(), phaseID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"phase": phase})
}

func (s *Server) handlePortalDeletePhase(w http.ResponseWriter, r *http.Request) {
	phaseID, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteByID(r.Context(), "phase", phaseID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePortalReorderModule(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "moduleId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalReorderInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalReorder(r.Context(), "module", "course_id", "Módulo", id, in.Direction); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePortalReorderPhase(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalReorderInput
	if err := decodePortalJSON(r.Body, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalReorder(r.Context(), "phase", "module_id", "Fase", id, in.Direction); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
