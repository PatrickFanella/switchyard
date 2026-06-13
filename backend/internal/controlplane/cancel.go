package controlplane

import (
	"bytes"
	"context"
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
	changemakerBaseURL string
	changemakerToken   string
	ocqBaseURL         string
	ocqGatewayKey      string
	llamaLineBaseURL   string
	llamaLineAdminKey  string
	openPilotCommand   string
	client             *http.Client
}

func NewEnvCancelDispatcher() *EnvCancelDispatcher {
	return &EnvCancelDispatcher{
		changemakerBaseURL: strings.TrimRight(os.Getenv("CHANGEMAKER_BASE_URL"), "/"),
		changemakerToken:   os.Getenv("CHANGEMAKER_ADMIN_TOKEN"),
		ocqBaseURL:         strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:      os.Getenv("OCQ_GATEWAY_KEY"),
		llamaLineBaseURL:   strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAdminKey:  os.Getenv("LLAMA_LINE_ADMIN_KEY"),
		openPilotCommand:   strings.TrimSpace(os.Getenv("OPEN_PILOT_COMMAND")),
		client:             &http.Client{Timeout: 5 * time.Second},
	}
}

func (d *EnvCancelDispatcher) Cancel(ctx context.Context, run Run) CancelResult {
	return d.CancelRunDetail(ctx, RunDetail{Run: run})
}

func (d *EnvCancelDispatcher) CancelRunDetail(ctx context.Context, detail RunDetail) CancelResult {
	run := detail.Run
	result := CancelResult{}
	switch run.WorkflowID {
	case string(WorkflowChangemaker):
		result.Events = append(result.Events, d.cancelChangemaker(ctx)...)
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
	if len(result.Events) == 0 {
		result.Events = append(result.Events, WorkflowExecutionEvent{Type: "cancel.noop", Message: "No upstream cancellation adapter matched this run"})
	}
	return result
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

func (d *EnvCancelDispatcher) cancelChangemaker(ctx context.Context) []WorkflowExecutionEvent {
	if d.changemakerBaseURL == "" {
		return []WorkflowExecutionEvent{{Type: "changemaker.cancel.skipped", Message: "CHANGEMAKER_BASE_URL is not configured"}}
	}
	endpoint, _ := url.JoinPath(d.changemakerBaseURL, "/api/runs/cancel")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(nil))
	if err != nil {
		return []WorkflowExecutionEvent{{Type: "changemaker.cancel.failed", Message: err.Error()}}
	}
	if d.changemakerToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.changemakerToken)
	}
	return d.doCancelRequest("changemaker.cancel", req)
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
