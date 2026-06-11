package controlplane

import "time"

type WorkflowKind string

const (
	WorkflowOpenPilot   WorkflowKind = "open-pilot"
	WorkflowChangemaker WorkflowKind = "changemaker"
	WorkflowDevlog      WorkflowKind = "devlog"
)

type Workflow struct {
	ID          string       `json:"id"`
	Kind        WorkflowKind `json:"kind"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Owns        []string     `json:"owns"`
	Uses        []string     `json:"uses"`
	Status      string       `json:"status"`
}

type TriggerType string

const (
	TriggerManual  TriggerType = "manual"
	TriggerCron    TriggerType = "cron"
	TriggerPoll    TriggerType = "gitea-poll"
	TriggerWebhook TriggerType = "webhook"
)

type Trigger struct {
	ID          string            `json:"id"`
	WorkflowID  string            `json:"workflow_id"`
	Type        TriggerType       `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	Schedule    string            `json:"schedule,omitempty"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	NextRunAt   *time.Time        `json:"next_run_at,omitempty"`
}

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

type Run struct {
	ID         string            `json:"id"`
	WorkflowID string            `json:"workflow_id"`
	TriggerID  string            `json:"trigger_id,omitempty"`
	Status     RunStatus         `json:"status"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Summary    string            `json:"summary,omitempty"`
}

type RunEvent struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type Artifact struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	URI       string    `json:"uri"`
	CreatedAt time.Time `json:"created_at"`
}

type RunDetail struct {
	Run       Run        `json:"run"`
	Events    []RunEvent `json:"events"`
	Artifacts []Artifact `json:"artifacts"`
}

type CreateRunInput struct {
	WorkflowID string            `json:"workflow_id"`
	TriggerID  string            `json:"trigger_id,omitempty"`
	Inputs     map[string]string `json:"inputs,omitempty"`
}
