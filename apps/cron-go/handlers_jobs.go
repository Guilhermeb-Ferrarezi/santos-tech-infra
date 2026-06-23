package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/santos-tech/cron-go/db"
)

type jobInput struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	ScheduleCron string          `json:"scheduleCron"`
	Timezone     string          `json:"timezone"`
	ActionKind   string          `json:"actionKind"`
	ActionRef    string          `json:"actionRef"`
	HTTPMethod   string          `json:"httpMethod"`
	HTTPURL      string          `json:"httpUrl"`
	HTTPHeaders  json.RawMessage `json:"httpHeaders"`
	HTTPBody     string          `json:"httpBody"`
	Params       json.RawMessage `json:"params"`
	TimeoutSecs  int32           `json:"timeoutSecs"`
	MaxRetries   int32           `json:"maxRetries"`
}

func (in *jobInput) defaults() {
	if in.Timezone == "" {
		in.Timezone = "America/Sao_Paulo"
	}
	if in.TimeoutSecs <= 0 {
		in.TimeoutSecs = 30
	}
	if in.MaxRetries <= 0 {
		in.MaxRetries = 3
	}
	if len(in.HTTPHeaders) == 0 {
		in.HTTPHeaders = []byte("{}")
	}
	if len(in.Params) == 0 {
		in.Params = []byte("{}")
	}
}

func (in jobInput) validate(allowRaw bool) (string, bool) {
	if in.Name == "" {
		return "name obrigatório", false
	}
	if in.ActionKind != "catalog" && in.ActionKind != "http" {
		return "actionKind deve ser catalog ou http", false
	}
	if in.ActionKind == "catalog" {
		if _, ok := lookupCatalog(in.ActionRef); !ok {
			return "ação de catálogo desconhecida", false
		}
	}
	if in.ActionKind == "http" && !allowRaw {
		return "HTTP cru desabilitado", false
	}
	if err := validateCron(in.ScheduleCron, in.Timezone); err != nil {
		return err.Error(), false
	}
	return "", true
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var in jobInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "JSON inválido")
		return
	}
	in.defaults()
	if msg, ok := in.validate(s.cfg.AllowRawHTTP); !ok {
		writeError(w, http.StatusBadRequest, "validation", msg)
		return
	}
	next, _ := nextRun(in.ScheduleCron, in.Timezone, time.Now().UTC())
	job, err := s.q.CreateJob(r.Context(), db.CreateJobParams{
		Name: in.Name, Description: in.Description, ScheduleCron: in.ScheduleCron,
		Timezone: in.Timezone, Enabled: true, ActionKind: in.ActionKind, ActionRef: in.ActionRef,
		HttpMethod: in.HTTPMethod, HttpUrl: in.HTTPURL, HttpHeaders: in.HTTPHeaders,
		HttpBody: in.HTTPBody, Params: in.Params, TimeoutSecs: in.TimeoutSecs,
		MaxRetries: in.MaxRetries, NextRunAt: pgTimestamp(next), CreatedBy: "",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao criar job")
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.q.ListJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao listar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	job, err := s.q.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "job não encontrado")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	var in jobInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "JSON inválido")
		return
	}
	in.defaults()
	if msg, ok := in.validate(s.cfg.AllowRawHTTP); !ok {
		writeError(w, http.StatusBadRequest, "validation", msg)
		return
	}
	next, _ := nextRun(in.ScheduleCron, in.Timezone, time.Now().UTC())
	job, err := s.q.UpdateJob(r.Context(), db.UpdateJobParams{
		ID: id, Name: in.Name, Description: in.Description, ScheduleCron: in.ScheduleCron,
		Timezone: in.Timezone, ActionKind: in.ActionKind, ActionRef: in.ActionRef,
		HttpMethod: in.HTTPMethod, HttpUrl: in.HTTPURL, HttpHeaders: in.HTTPHeaders,
		HttpBody: in.HTTPBody, Params: in.Params, TimeoutSecs: in.TimeoutSecs,
		MaxRetries: in.MaxRetries, NextRunAt: pgTimestamp(next),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao atualizar job")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	if s.q.DeleteJob(r.Context(), id) != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "falha ao remover")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
