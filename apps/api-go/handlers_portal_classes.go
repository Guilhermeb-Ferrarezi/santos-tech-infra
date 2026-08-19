package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (s *Server) handlePortalListClasses(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalListClasses(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalCreateClass(w http.ResponseWriter, r *http.Request) {
	var in portalClassInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	class, err := s.portalCreateClass(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_create", "class", class.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"class": class})
}

func (s *Server) handlePortalGetClass(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	class, err := s.portalGetClass(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Turma"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	students, err := s.portalListClassStudents(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"class": class, "students": students})
}

func (s *Server) handlePortalUpdateClass(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	var in portalClassInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	class, err := s.portalUpdateClass(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_update", "class", class.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"class": class})
}

func (s *Server) handlePortalDeleteClass(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteClass(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_delete", "class", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePortalListClassStudents(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	students, err := s.portalListClassStudents(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"students": students})
}

func (s *Server) handlePortalAddClassStudents(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	var in portalAddStudentsInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if len(in.StudentIDs) == 0 {
		writeErr(w, validationErr("studentIds obrigatório"))
		return
	}
	added, err := s.portalAddClassStudents(r.Context(), id, in.StudentIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_students_add", "class", fmt.Sprint(id), map[string]any{"requested": len(in.StudentIDs), "added": added, "studentIds": in.StudentIDs})
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

func (s *Server) handlePortalRemoveClassStudent(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	studentID, err := portalPathID(r, "studentId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalRemoveClassStudent(r.Context(), classID, studentID); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_student_remove", "class", fmt.Sprint(classID), map[string]any{"studentId": fmt.Sprint(studentID)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePortalClassCronograma(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	class, cronograma, err := s.portalClassCronograma(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Turma"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"class": class, "cronograma": cronograma})
}

func (s *Server) handlePortalIniciarFases(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), id); err != nil {
		writeErr(w, err)
		return
	}
	var in portalAddStudentsInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if len(in.StudentIDs) == 0 {
		writeErr(w, validationErr("studentIds obrigatório"))
		return
	}
	phase, count, err := s.portalIniciarFases(r.Context(), id, in.StudentIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "class_iniciar_fases", "class", fmt.Sprint(id), map[string]any{"phaseId": phase.ID, "students": count})
	writeJSON(w, http.StatusOK, map[string]any{"phase": phase, "students": count})
}
