package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// ── Exercícios ───────────────────────────────────────────────────────────────

func (s *Server) handlePortalListExercises(w http.ResponseWriter, r *http.Request) {
	phaseID, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	var dailyTask *bool
	if v := r.URL.Query().Get("dailyTask"); v != "" {
		b := v == "true" || v == "1"
		dailyTask = &b
	}
	items, total, err := s.portalListExercises(r.Context(), phaseID, p, dailyTask)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalGetExercise(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	ex, err := s.portalGetExercise(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Exercício"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exercise": ex})
}

func (s *Server) handlePortalCreateExercise(w http.ResponseWriter, r *http.Request) {
	phaseID, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalExerciseInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	ex, err := s.portalCreateExercise(r.Context(), phaseID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "exercise_create", "exercise", ex.ID, map[string]any{"phaseId": fmt.Sprint(phaseID)})
	writeJSON(w, http.StatusCreated, map[string]any{"exercise": ex})
}

func (s *Server) handlePortalUpdateExercise(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalExerciseInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateUpdate(); err != nil {
		writeErr(w, err)
		return
	}
	ex, err := s.portalUpdateExercise(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "exercise_update", "exercise", ex.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"exercise": ex})
}

func (s *Server) handlePortalDeleteExercise(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteExercise(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "exercise_delete", "exercise", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePortalReorderExercise(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalReorderInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalReorder(r.Context(), "exercise", "phase_id", "Exercício", id, in.Direction); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "exercise_reorder", "exercise", fmt.Sprint(id), map[string]any{"direction": in.Direction})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Containers ───────────────────────────────────────────────────────────────

func (s *Server) handlePortalListContainers(w http.ResponseWriter, r *http.Request) {
	phaseID, err := portalPathID(r, "phaseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := s.portalListContainers(r.Context(), phaseID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePortalExerciseContainer(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "exerciseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	ref, err := s.portalExerciseContainer(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"container": ref})
}

func (s *Server) handlePortalCreateContainer(w http.ResponseWriter, r *http.Request) {
	var in portalContainerInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	count, err := s.portalCreateContainer(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "container_create", "container", "", map[string]any{"name": in.Name, "phaseId": fmt.Sprint(in.PhaseID), "created": count})
	writeJSON(w, http.StatusCreated, map[string]any{"created": count})
}

func (s *Server) handlePortalAddContainerExercises(w http.ResponseWriter, r *http.Request) {
	var in portalAddContainerInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	count, err := s.portalAddContainerExercises(r.Context(), in.ref(), in.ExerciseIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "container_exercises_add", "container", "", map[string]any{"name": in.Name, "phaseId": fmt.Sprint(in.PhaseID), "requested": len(in.ExerciseIDs), "added": count})
	writeJSON(w, http.StatusOK, map[string]any{"added": count})
}

func (s *Server) handlePortalDeleteContainerGroup(w http.ResponseWriter, r *http.Request) {
	var in portalContainerGroupRef
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validate(); err != nil {
		writeErr(w, err)
		return
	}
	deleted, err := s.portalDeleteContainerGroup(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "container_group_delete", "container", "", map[string]any{"name": in.Name, "phaseId": fmt.Sprint(in.PhaseID), "deleted": deleted})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (s *Server) handlePortalDeleteContainerTask(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "containerTaskId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteContainerTask(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "container_task_delete", "container", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Materiais ────────────────────────────────────────────────────────────────

func (s *Server) handlePortalListMaterials(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	p := portalPaginationFrom(r)
	items, total, err := s.portalListMaterials(r.Context(), courseID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalGetMaterial(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "materialId")
	if err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.portalGetMaterial(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Material"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"material": m})
}

func (s *Server) handlePortalCreateMaterial(w http.ResponseWriter, r *http.Request) {
	courseID, err := portalPathID(r, "courseId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalMaterialInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	m, err := s.portalCreateMaterial(r.Context(), courseID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "material_create", "material", m.ID, map[string]any{"courseId": fmt.Sprint(courseID)})
	writeJSON(w, http.StatusCreated, map[string]any{"material": m})
}

func (s *Server) handlePortalUpdateMaterial(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "materialId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalMaterialInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	m, err := s.portalUpdateMaterial(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "material_update", "material", m.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"material": m})
}

func (s *Server) handlePortalDeleteMaterial(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "materialId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteMaterial(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "material_delete", "material", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Vídeos ───────────────────────────────────────────────────────────────────

func (s *Server) handlePortalListVideos(w http.ResponseWriter, r *http.Request) {
	p := portalPaginationFrom(r)
	items, total, err := s.portalListVideos(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
}

func (s *Server) handlePortalGetVideo(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "videoId")
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.portalGetVideo(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, notFoundErr("Vídeo"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"video": v})
}

func (s *Server) handlePortalCreateVideo(w http.ResponseWriter, r *http.Request) {
	var in portalVideoInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.portalCreateVideo(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "video_create", "video", v.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"video": v})
}

func (s *Server) handlePortalUpdateVideo(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "videoId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in portalVideoInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	v, err := s.portalUpdateVideo(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "video_update", "video", v.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"video": v})
}

func (s *Server) handlePortalDeleteVideo(w http.ResponseWriter, r *http.Request) {
	id, err := portalPathID(r, "videoId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteVideo(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.portalLogActivity(r, "video_delete", "video", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
}
