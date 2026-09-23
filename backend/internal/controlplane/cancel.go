package controlplane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type CancelResult struct {
	Events []WorkflowExecutionEvent
}

type CancelDispatcher interface {
	Cancel(ctx context.Context, run Run) CancelResult
}

type RunDetailCancelDispatcher interface {
	CancelRunDetail(ctx context.Context, detail RunDetail) CancelResult
}

type EnvCancelDispatcher struct {
	ocqBaseURL        string
	ocqGatewayKey     string
	openCodeBaseURL   string
	openCodeUsername  string
	openCodePassword  string
	llamaLineBaseURL  string
	llamaLineAdminKey string
	openPilotCommand  string
	client            *http.Client
	openPilotGate     *OpenPilotAdmissionGate
	openPilotRoutes   map[string]openPilotCancelRoute
	openPilotRouteErr error
}

type openPilotCancelRoute struct {
	baseURL  string
	username string
	password string
}

func NewEnvCancelDispatcher() *EnvCancelDispatcher {
	return NewEnvCancelDispatcherWithStore(nil)
}

func NewEnvCancelDispatcherWithStore(store Store) *EnvCancelDispatcher {
	dispatcher := &EnvCancelDispatcher{
		ocqBaseURL:        strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:     os.Getenv("OCQ_GATEWAY_KEY"),
		openCodeBaseURL:   strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"), "/"),
		openCodeUsername:  os.Getenv("OPENCODE_SERVER_USERNAME"),
		openCodePassword:  os.Getenv("OPENCODE_SERVER_PASSWORD"),
		llamaLineBaseURL:  strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAdminKey: os.Getenv("LLAMA_LINE_ADMIN_KEY"),
		openPilotCommand:  strings.TrimSpace(os.Getenv("OPEN_PILOT_COMMAND")),
		client:            &http.Client{Timeout: 5 * time.Second},
	}
	dispatcher.openPilotGate, dispatcher.openPilotRouteErr = NewEnvOpenPilotAdmissionGate()
	if dispatcher.openPilotGate != nil {
		dispatcher.openPilotGate.rollout = store
	}
	if dispatcher.openPilotRouteErr == nil {
		var bundles map[string]openPilotExecutionBundle
		bundles, dispatcher.openPilotRouteErr = newOpenPilotExecutionBundles(dispatcher.openPilotGate)
		dispatcher.openPilotRoutes = make(map[string]openPilotCancelRoute, len(bundles))
		for key, bundle := range bundles {
			executor, ok := bundle.openCodeExecutor.(*EnvOpenCodeExecutor)
			if !ok {
				dispatcher.openPilotRouteErr = errors.New("Open Pilot cancellation route has an invalid executor")
				break
			}
			dispatcher.openPilotRoutes[key] = openPilotCancelRoute{baseURL: executor.baseURL, username: executor.username, password: executor.password}
		}
	}
	return dispatcher
}

func (d *EnvCancelDispatcher) Cancel(ctx context.Context, run Run) CancelResult {
	return d.CancelRunDetail(ctx, RunDetail{Run: run})
}

func (d *EnvCancelDispatcher) CancelRunDetail(ctx context.Context, detail RunDetail) CancelResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	run := detail.Run
	result := CancelResult{}
	switch run.WorkflowID {
	case string(WorkflowOpenPilot):
		result.Events = append(result.Events, d.cancelOpenPilot(ctx, run)...)
	}
	correlation := cancelCorrelationIDs(detail)
	if requestID := correlation["ocq_request_id"]; requestID != "" {
		result.Events = append(result.Events, d.abortOCQRequest(ctx, requestID, correlation["ocq_session_id"])...)
	}
	if requestID := correlation["llama_line_request_id"]; requestID != "" {
		result.Events = append(result.Events, d.dropLlamaLineRequest(ctx, requestID)...)
	}
	if sessionID := correlation["opencode_session_id"]; sessionID != "" {
		route, routeErr := d.openCodeRouteForRun(run)
		if routeErr != nil {
			result.Events = append(result.Events, WorkflowExecutionEvent{Type: "opencode.abort.failed", Message: routeErr.Error()})
		} else {
			result.Events = append(result.Events, d.abortOpenCodeSession(ctx, sessionID, route)...)
		}
	}
	if len(result.Events) == 0 {
		result.Events = append(result.Events, WorkflowExecutionEvent{Type: "cancel.noop", Message: "No upstream cancellation adapter matched this run"})
	}
	return result
}

func (d *EnvCancelDispatcher) openCodeRouteForRun(run Run) (openPilotCancelRoute, error) {
	fallback := openPilotCancelRoute{baseURL: d.openCodeBaseURL, username: d.openCodeUsername, password: d.openCodePassword}
	if run.WorkflowID != string(WorkflowOpenPilot) || len(d.openPilotRoutes) == 0 {
		return fallback, nil
	}
	if d.openPilotRouteErr != nil || d.openPilotGate == nil {
		return openPilotCancelRoute{}, errors.New("Open Pilot repository cancellation routing is unavailable")
	}
	target, err := d.openPilotGate.Resolve(run.Inputs["repo"], run.Inputs["issue"])
	if err != nil || run.Inputs[openPilotRepositoryIDInputKey] != target.RepositoryID || run.Inputs[openPilotExecutorGenerationInputKey] != target.ExecutorGeneration {
		return openPilotCancelRoute{}, errors.New("Open Pilot cancellation affinity is invalid")
	}
	route, ok := d.openPilotRoutes[openPilotExecutionBundleKey(target.RepositoryID, target.ExecutorGeneration)]
	if !ok {
		return openPilotCancelRoute{}, errors.New("Open Pilot cancellation route is missing")
	}
	return route, nil
}

func (d *EnvCancelDispatcher) abortOpenCodeSession(ctx context.Context, sessionID string, route openPilotCancelRoute) []WorkflowExecutionEvent {
	if route.baseURL == "" {
		return []WorkflowExecutionEvent{{Type: "opencode.abort.skipped", Message: "OPENCODE_SERVER_URL is not configured"}}
	}
	endpoint, err := url.JoinPath(route.baseURL, "/session/", sessionID, "/abort")
	if err != nil {
		return []WorkflowExecutionEvent{{Type: "opencode.abort.failed", Message: err.Error()}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return []WorkflowExecutionEvent{{Type: "opencode.abort.failed", Message: err.Error()}}
	}
	req.Header.Set("Content-Type", "application/json")
	if route.username != "" || route.password != "" {
		req.SetBasicAuth(firstNonEmpty(route.username, "switchyard"), route.password)
	}
	if d.ocqGatewayKey != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+d.ocqGatewayKey)
	}
	return d.doCancelRequest("opencode.abort", req)
}

func cancelRunDetail(ctx context.Context, dispatcher CancelDispatcher, detail RunDetail) CancelResult {
	if detailed, ok := dispatcher.(RunDetailCancelDispatcher); ok {
		return detailed.CancelRunDetail(ctx, detail)
	}
	return dispatcher.Cancel(ctx, detail.Run)
}

func cancelCorrelationIDs(detail RunDetail) map[string]string {
	out := cloneMap(detail.Run.Inputs)
	if out == nil {
		out = map[string]string{}
	}
	for _, step := range detail.Steps {
		if step.Status != StepRunning && step.Status != StepQueued {
			continue
		}
		for key, value := range step.CorrelationIDs {
			if value != "" {
				out[key] = value
			}
		}
	}
	return out
}

func (d *EnvCancelDispatcher) abortOCQRequest(ctx context.Context, requestID string, sessionID string) []WorkflowExecutionEvent {
	if d.ocqBaseURL == "" {
		return []WorkflowExecutionEvent{{Type: "ocq.abort.skipped", Message: "OCQ_BASE_URL is not configured"}}
	}
	path := "/ocq/requests/" + requestID + "/abort"
	if sessionID != "" {
		path = "/ocq/sessions/" + sessionID + "/messages/" + requestID + "/abort"
	}
	endpoint, _ := url.JoinPath(d.ocqBaseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(nil))
	if err != nil {
		return []WorkflowExecutionEvent{{Type: "ocq.abort.failed", Message: err.Error()}}
	}
	if d.ocqGatewayKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.ocqGatewayKey)
	}
	return d.doCancelRequest("ocq.abort", req)
}

func (d *EnvCancelDispatcher) dropLlamaLineRequest(ctx context.Context, requestID string) []WorkflowExecutionEvent {
	if d.llamaLineBaseURL == "" {
		return []WorkflowExecutionEvent{{Type: "llama-line.drop.skipped", Message: "LLAMA_LINE_BASE_URL is not configured"}}
	}
	endpoint, _ := url.JoinPath(d.llamaLineBaseURL, "/admin/queue/"+requestID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return []WorkflowExecutionEvent{{Type: "llama-line.drop.failed", Message: err.Error()}}
	}
	if d.llamaLineAdminKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.llamaLineAdminKey)
	}
	return d.doCancelRequest("llama-line.drop", req)
}

func (d *EnvCancelDispatcher) cancelOpenPilot(ctx context.Context, run Run) []WorkflowExecutionEvent {
	if d.openPilotCommand == "" {
		return []WorkflowExecutionEvent{{Type: "open-pilot.cancel.skipped", Message: "OPEN_PILOT_COMMAND is not configured"}}
	}
	parts := strings.Fields(d.openPilotCommand)
	if len(parts) == 0 {
		return []WorkflowExecutionEvent{{Type: "open-pilot.cancel.failed", Message: "OPEN_PILOT_COMMAND is empty"}}
	}
	args := append(parts[1:], "cancel", "--run", run.ID, "--json")
	if run.Inputs["repo"] != "" {
		args = append(args, "--repo", run.Inputs["repo"])
	}
	if run.Inputs["issue"] != "" {
		args = append(args, "--issue", run.Inputs["issue"])
	}
	cmd := exec.CommandContext(ctx, parts[0], args...)
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return []WorkflowExecutionEvent{{Type: "open-pilot.cancel.failed", Message: message}}
	}
	if message == "" {
		message = "Open Pilot cancel accepted"
	}
	return []WorkflowExecutionEvent{{Type: "open-pilot.cancel.accepted", Message: message}}
}

func (d *EnvCancelDispatcher) doCancelRequest(eventPrefix string, req *http.Request) []WorkflowExecutionEvent {
	resp, err := d.client.Do(req)
	if err != nil {
		return []WorkflowExecutionEvent{{Type: eventPrefix + ".failed", Message: err.Error()}}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return []WorkflowExecutionEvent{{Type: eventPrefix + ".failed", Message: fmt.Sprintf("%s %s", req.URL.String(), resp.Status)}}
	}
	return []WorkflowExecutionEvent{{Type: eventPrefix + ".accepted", Message: fmt.Sprintf("%s %s", req.Method, req.URL.String())}}
}
