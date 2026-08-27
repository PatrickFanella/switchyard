package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const operatorSummarySchemaVersion = "1.0"

type OperatorSummary struct {
	SchemaVersion string                      `json:"schema_version"`
	Service       OperatorSummaryService      `json:"service"`
	Deployment    OperatorSummaryDeployment   `json:"deployment"`
	Status        OperatorSummaryStatus       `json:"status"`
	GeneratedAt   time.Time                   `json:"generated_at"`
	ObservedAt    time.Time                   `json:"observed_at"`
	Partial       bool                        `json:"partial"`
	Alerts        []OperatorSummaryAlert      `json:"alerts"`
	Capabilities  OperatorSummaryCapabilities `json:"capabilities"`
	Data          OperatorSummaryData         `json:"data"`
}

type OperatorSummaryDeployment struct {
	SourceRevision string `json:"source_revision"`
	DeploymentID   string `json:"deployment_id"`
}

type OperatorSummaryService struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OperatorSummaryStatus struct {
	Level   string `json:"level"`
	Summary string `json:"summary"`
}

type OperatorSummaryAlert struct {
	ID         string    `json:"id"`
	Severity   string    `json:"severity"`
	Title      string    `json:"title"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
}

type OperatorSummaryCapabilities struct {
	Details bool     `json:"details"`
	Actions []string `json:"actions"`
}

type OperatorSummaryData struct {
	Scheduler  OperatorSummaryScheduler   `json:"scheduler"`
	Workers    OperatorSummaryWorkers     `json:"workers"`
	Runs       OperatorSummaryRuns        `json:"runs"`
	Components []OperatorSummaryComponent `json:"components"`
}

type OperatorSummaryScheduler struct {
	State      string     `json:"state"`
	Enabled    bool       `json:"enabled"`
	LastTickAt *time.Time `json:"last_tick_at"`
	NextTickAt *time.Time `json:"next_tick_at"`
}

type OperatorSummaryWorkers struct {
	Configured int `json:"configured"`
	Active     int `json:"active"`
	Busy       int `json:"busy"`
	Draining   int `json:"draining"`
}

type OperatorSummaryRuns struct {
	Queued       int                        `json:"queued"`
	Running      int                        `json:"running"`
	Failed24H    int                        `json:"failed_24h"`
	Succeeded24H int                        `json:"succeeded_24h"`
	Recent       []OperatorSummaryRecentRun `json:"recent"`
}

type OperatorSummaryRecentRun struct {
	ID           string     `json:"id"`
	WorkflowID   string     `json:"workflow_id"`
	WorkflowName string     `json:"workflow_name"`
	State        RunStatus  `json:"state"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	Summary      string     `json:"summary,omitempty"`
}

type OperatorSummaryComponent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

func (a *API) operatorSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.buildOperatorSummary(ctx, time.Now().UTC()))
}

func (a *API) buildOperatorSummary(ctx context.Context, now time.Time) OperatorSummary {
	now = now.UTC()
	scheduler := SchedulerStatus{Enabled: false, Running: false}
	if a.scheduler != nil {
		scheduler = a.scheduler.Status()
	}
	workers := WorkerPoolStatus{Enabled: false, Running: false}
	if a.workerPool != nil {
		workers = a.workerPool.Status()
	}
	broker := a.modelBroker.Status(ctx)
	executor := a.openCode.Status(ctx)
	runSnapshot, snapshotErr := operatorRunSnapshot(ctx, a.store, now, 10)
	if snapshotErr != nil {
		runSnapshot = OperatorRunSnapshot{Recent: []Run{}}
	}
	workflows := a.store.ListWorkflows()

	workflowNames := make(map[string]string, len(workflows))
	for _, workflow := range workflows {
		workflowNames[workflow.ID] = workflow.Name
	}

	summary := OperatorSummary{
		SchemaVersion: operatorSummarySchemaVersion,
		Service:       OperatorSummaryService{ID: "switchyard", Name: "Switchyard"},
		Deployment:    operatorSummaryDeployment(),
		Status:        OperatorSummaryStatus{Level: "healthy", Summary: "Runtime operating normally"},
		GeneratedAt:   now,
		ObservedAt:    latestSummaryObservation(now, broker.CheckedAt, executor.CheckedAt),
		Alerts:        []OperatorSummaryAlert{},
		Capabilities:  OperatorSummaryCapabilities{Details: true, Actions: []string{}},
		Data: OperatorSummaryData{
			Scheduler: operatorSummaryScheduler(scheduler),
			Workers: OperatorSummaryWorkers{
				Configured: workers.Workers,
				Active:     workers.Active,
				Busy:       workers.Active,
				Draining:   0,
			},
			Runs: OperatorSummaryRuns{Recent: []OperatorSummaryRecentRun{}},
			Components: []OperatorSummaryComponent{
				operatorSummaryBrokerComponent(broker),
				operatorSummaryExecutorComponent(executor),
			},
		},
	}
	summary.Partial = snapshotErr != nil

	summary.Data.Runs.Queued, summary.Data.Runs.Running = runSnapshot.Queued, runSnapshot.Running
	summary.Data.Runs.Failed24H, summary.Data.Runs.Succeeded24H = runSnapshot.Failed24H, runSnapshot.Succeeded24H
	for _, run := range runSnapshot.Recent {
		reference := run.CreatedAt
		if run.FinishedAt != nil {
			reference = *run.FinishedAt
		}
		if len(summary.Data.Runs.Recent) < 5 {
			summary.Data.Runs.Recent = append(summary.Data.Runs.Recent, OperatorSummaryRecentRun{
				ID:           run.ID,
				WorkflowID:   run.WorkflowID,
				WorkflowName: firstNonEmpty(workflowNames[run.WorkflowID], run.WorkflowID),
				State:        run.Status,
				StartedAt:    run.StartedAt,
				FinishedAt:   run.FinishedAt,
				Summary:      boundedOperatorText(run.Summary, 240),
			})
		}
		if (run.Status == RunFailed || run.Status == RunTimedOut) && len(summary.Alerts) < 5 {
			summary.Alerts = append(summary.Alerts, OperatorSummaryAlert{
				ID:         "run-" + run.ID,
				Severity:   "warning",
				Title:      fmt.Sprintf("%s run %s", firstNonEmpty(workflowNames[run.WorkflowID], run.WorkflowID), run.Status),
				State:      "active",
				StartedAt:  reference.UTC(),
				TargetType: "run",
				TargetID:   run.ID,
			})
		}
	}

	if scheduler.Enabled && !scheduler.Running {
		summary.Alerts = appendSummaryAlert(summary.Alerts, OperatorSummaryAlert{ID: "scheduler-not-running", Severity: "critical", Title: "Scheduler is not running", State: "active", StartedAt: now, TargetType: "component", TargetID: "scheduler"})
	}
	if scheduler.LastError != "" {
		summary.Alerts = appendSummaryAlert(summary.Alerts, OperatorSummaryAlert{ID: "scheduler-error", Severity: "warning", Title: "Scheduler reported an error", State: "active", StartedAt: firstNonZeroTime(scheduler.LastTick, now), TargetType: "component", TargetID: "scheduler"})
	}
	if workers.Enabled && !workers.Running {
		summary.Alerts = appendSummaryAlert(summary.Alerts, OperatorSummaryAlert{ID: "workers-not-running", Severity: "critical", Title: "Worker pool is not running", State: "active", StartedAt: now, TargetType: "component", TargetID: "workers"})
	}
	if workers.LastError != "" {
		summary.Alerts = appendSummaryAlert(summary.Alerts, OperatorSummaryAlert{ID: "worker-error", Severity: "warning", Title: "Worker pool reported an error", State: "active", StartedAt: firstNonZeroTime(workers.LastRecoverAt, now), TargetType: "component", TargetID: "workers"})
	}

	for _, component := range summary.Data.Components {
		if component.Status != "healthy" {
			summary.Partial = true
			summary.Alerts = appendSummaryAlert(summary.Alerts, OperatorSummaryAlert{
				ID: "component-" + component.ID, Severity: "warning", Title: component.Name + " is " + component.Status,
				State: "active", StartedAt: now, TargetType: "component", TargetID: component.ID,
			})
		}
	}

	summary.Status = deriveOperatorSummaryStatus(summary)
	return summary
}

func operatorSummaryDeployment() OperatorSummaryDeployment {
	revision := strings.TrimSpace(os.Getenv("SWITCHYARD_SOURCE_REVISION"))
	if !validSourceRevision(revision) {
		revision = ""
	}
	deploymentID := strings.TrimSpace(os.Getenv("SWITCHYARD_DEPLOYMENT_ID"))
	if !validCompatibilityIdentity(deploymentID) {
		deploymentID = ""
	}
	return OperatorSummaryDeployment{SourceRevision: revision, DeploymentID: deploymentID}
}

func operatorSummaryScheduler(status SchedulerStatus) OperatorSummaryScheduler {
	state := "disabled"
	if status.Enabled && status.Running {
		state = "running"
	} else if status.Enabled {
		state = "stopped"
	}
	var lastTick *time.Time
	var nextTick *time.Time
	if !status.LastTick.IsZero() {
		value := status.LastTick.UTC()
		lastTick = &value
		if interval, err := time.ParseDuration(status.Interval); err == nil {
			next := value.Add(interval)
			nextTick = &next
		}
	}
	return OperatorSummaryScheduler{State: state, Enabled: status.Enabled, LastTickAt: lastTick, NextTickAt: nextTick}
}

func operatorSummaryBrokerComponent(status ModelBrokerStatus) OperatorSummaryComponent {
	level := normalizeOperatorComponentStatus(status.Status, status.Configured)
	detail := "Accepting requests"
	if level != "healthy" {
		detail = firstNonEmpty(boundedOperatorText(status.Error, 160), "Model broker "+strings.ReplaceAll(level, "_", " "))
	}
	return OperatorSummaryComponent{ID: "model-broker", Name: "Model broker", Status: level, Summary: detail}
}

func operatorSummaryExecutorComponent(status OpenCodeExecutorStatus) OperatorSummaryComponent {
	level := normalizeOperatorComponentStatus(status.Status, status.Configured)
	detail := "Executor available"
	if level != "healthy" {
		detail = firstNonEmpty(boundedOperatorText(status.Error, 160), "OpenCode executor "+strings.ReplaceAll(level, "_", " "))
	}
	return OperatorSummaryComponent{ID: "opencode-executor", Name: "OpenCode executor", Status: level, Summary: detail}
}

func normalizeOperatorComponentStatus(status string, configured bool) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "healthy", "ready", "running", "ok":
		return "healthy"
	case "failed", "unreachable", "down", "critical":
		return "critical"
	case "degraded", "starting":
		return "degraded"
	case "not_configured", "unknown", "":
		if !configured {
			return "unknown"
		}
		return "degraded"
	default:
		return "degraded"
	}
}

func deriveOperatorSummaryStatus(summary OperatorSummary) OperatorSummaryStatus {
	critical := false
	degraded := summary.Partial || summary.Data.Runs.Failed24H > 0
	for _, alert := range summary.Alerts {
		if alert.Severity == "critical" {
			critical = true
		} else {
			degraded = true
		}
	}
	if critical {
		return OperatorSummaryStatus{Level: "critical", Summary: "Runtime requires operator attention"}
	}
	if degraded {
		return OperatorSummaryStatus{Level: "degraded", Summary: "Runtime operating with warnings"}
	}
	return OperatorSummaryStatus{Level: "healthy", Summary: "Runtime operating normally"}
}

func appendSummaryAlert(alerts []OperatorSummaryAlert, alert OperatorSummaryAlert) []OperatorSummaryAlert {
	if len(alerts) >= 8 {
		return alerts
	}
	return append(alerts, alert)
}

func latestSummaryObservation(fallback time.Time, values ...time.Time) time.Time {
	valid := make([]time.Time, 0, len(values))
	for _, value := range values {
		if !value.IsZero() && !value.After(fallback.Add(time.Minute)) {
			valid = append(valid, value.UTC())
		}
	}
	if len(valid) == 0 {
		return fallback
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].After(valid[j]) })
	return valid[0]
}

func firstNonZeroTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
}

func boundedOperatorText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "…"
}
