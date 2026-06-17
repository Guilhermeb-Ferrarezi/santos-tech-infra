package main

import (
	"encoding/json"

	"github.com/hibiken/asynq"
)

// Nomes dos tasks asynq.
const (
	TaskIncidentCreate = "incident:create"
	TaskFixRun         = "fix:run"
	TaskNotifyFinal    = "notify:final"
	TaskDeployTimeout  = "deploy:timeout"
)

// IncidentPayload abre um incidente a partir de uma falha de deploy.
type IncidentPayload struct {
	App            string `json:"app"`
	AppUUID        string `json:"app_uuid"`
	DeploymentUUID string `json:"deployment_uuid"`
	Repo           string `json:"repo"`
	Branch         string `json:"branch"`
	Commit         string `json:"commit"`
	CommitMsg      string `json:"commit_msg"`
}

// IDPayload referencia um incidente existente (fix:run, deploy:timeout).
type IDPayload struct {
	IncidentID string `json:"incident_id"`
}

// NotifyPayload fecha um incidente com o desfecho. Outcome ∈ "success"|"failed".
type NotifyPayload struct {
	IncidentID string `json:"incident_id"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
}

func newTask(name string, v any) *asynq.Task {
	b, _ := json.Marshal(v)
	return asynq.NewTask(name, b)
}
