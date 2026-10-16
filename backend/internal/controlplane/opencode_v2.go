package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// A /api endpoint explicitly selects the released OpenCode 2 protocol. Legacy
// endpoints keep their existing wire contracts during a staged cutover.
func openCodeV2(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/api")
}
func openCodeUsername(baseURL, configured string) string {
	if openCodeV2(baseURL) {
		return "opencode"
	}
	return firstNonEmpty(configured, "switchyard")
}
func openCodeInterruptPath(baseURL string) string {
	if openCodeV2(baseURL) {
		return "/interrupt"
	}
	return "/abort"
}

func (e *EnvOpenCodeExecutor) v2JSON(ctx context.Context, method, path string, body any, target any, preAttempt ...PreAttemptCallback) error {
	endpoint, err := url.JoinPath(e.baseURL, path)
	if err != nil {
		return err
	}
	var encoded []byte
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	e.applyOpenCodeAuth(req)
	if len(preAttempt) > 0 && preAttempt[0] != nil {
		if err := preAttempt[0](resolveOpenCodeExecutorBackend()); err != nil {
			return err
		}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &staleOpenCodeSessionError{operation: "v2 session", status: resp.Status}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("OpenCode v2 request failed: HTTP %d", resp.StatusCode)
	}
	if target == nil {
		return nil
	}
	raw, err := readModelResponseBody(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
func (e *EnvOpenCodeExecutor) createOpenCodeV2Session(ctx context.Context, directory string, preAttempt ...PreAttemptCallback) (string, error) {
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := e.v2JSON(ctx, http.MethodPost, "/session", map[string]any{"location": map[string]string{"directory": directory}}, &response, preAttempt...)
	if err != nil {
		return "", err
	}
	if response.Data.ID == "" {
		return "", fmt.Errorf("OpenCode v2 session create response missing id")
	}
	return response.Data.ID, nil
}
func (e *EnvOpenCodeExecutor) postOpenCodeV2Prompt(ctx context.Context, sessionID string, request OpenCodeRequest) (string, error) {
	if request.Extraction {
		if err := e.openCodeExtractionReady(ctx); err != nil {
			return "", err
		}
	}
	if err := e.openCodeV2ManagedReady(ctx); err != nil {
		return "", err
	}
	path := "/session/" + url.PathEscape(sessionID)
	if request.Extraction {
		// A default title triggers a separate small-model generation in OpenCode.
		// Name the session before admitting source text, and fail closed if it fails.
		if err := e.v2JSON(ctx, http.MethodPatch, path, map[string]string{"title": "Switchyard news extraction"}, nil, request.PreAttempt); err != nil {
			return "", err
		}
		request.PreAttempt = nil
	}
	// Selection is session state in v2, rather than an ignored prompt field.
	if request.Agent != "" {
		if err := e.v2JSON(ctx, http.MethodPost, path+"/agent", map[string]string{"agent": request.Agent}, nil, request.PreAttempt); err != nil {
			return "", err
		}
		request.PreAttempt = nil
	}
	if request.Model != "" {
		model := openCodeModelPayload(request.Model)
		if err := e.v2JSON(ctx, http.MethodPost, path+"/model", map[string]any{"model": extractionModelSelection(model, request.Extraction)}, nil, request.PreAttempt); err != nil {
			return "", err
		}
		request.PreAttempt = nil
	}
	prompt := request.Prompt
	if request.OutputSchema != nil {
		// V2 has no v1 StructuredOutput endpoint. Keep this request tool-free and
		// enforce the response schema in Switchyard's existing adapter validator.
		schema, err := json.Marshal(request.OutputSchema)
		if err != nil {
			return "", err
		}
		prompt += "\nReturn only one JSON object conforming to this schema, without Markdown fences. Do not use tools.\n" + string(schema)
	}
	body := map[string]any{"text": prompt, "metadata": map[string]string{"switchyard_request_id": request.RequestID}}
	if err := e.v2JSON(ctx, http.MethodPost, path+"/prompt", body, nil, request.PreAttempt); err != nil {
		return "", err
	}
	return "", nil
}

type openCodeV2Message struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Finish  string `json:"finish"`
	Outcome string `json:"outcome"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	} `json:"error"`
	Time struct {
		Created   float64 `json:"created"`
		Completed float64 `json:"completed"`
	} `json:"time"`
}

func (e *EnvOpenCodeExecutor) openCodeV2LatestMessage(ctx context.Context, sessionID string) (openCodeSessionMessage, error) {
	// Include the terminal idle record: provider finish=stop alone can precede
	// tool settlement. Descending order also avoids reading the oldest message.
	var response struct {
		Data []openCodeV2Message `json:"data"`
	}
	endpoint, err := url.JoinPath(e.baseURL, "/session/", sessionID, "/message")
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?limit=2&order=desc", nil)
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	e.applyOpenCodeAuth(req)
	resp, err := e.client.Do(req)
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return openCodeSessionMessage{}, &staleOpenCodeSessionError{operation: "v2 session messages", status: resp.Status}
	}
	if resp.StatusCode != 200 {
		return openCodeSessionMessage{}, fmt.Errorf("OpenCode v2 messages failed: HTTP %d", resp.StatusCode)
	}
	raw, err := readModelResponseBody(resp.Body)
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return openCodeSessionMessage{}, err
	}
	if len(response.Data) == 0 {
		return openCodeSessionMessage{}, nil
	}
	latest := response.Data[0]
	if latest.Type == "idle" {
		if latest.Outcome == "interrupted" {
			return openCodeSessionMessage{}, context.Canceled
		}
		if len(response.Data) < 2 || response.Data[1].Type != "assistant" {
			return openCodeSessionMessage{}, fmt.Errorf("OpenCode v2 terminal session has no assistant response")
		}
		message := response.Data[1]
		if message.Error != nil {
			return openCodeSessionMessage{}, openCodeProviderError{status: message.Error.Status, retryable: modelBrokerRetryableStatus(message.Error.Status)}
		}
		if latest.Outcome != "succeeded" {
			return openCodeSessionMessage{}, fmt.Errorf("OpenCode v2 session failed")
		}
		result := openCodeSessionMessage{ID: latest.ID, Role: "assistant", Finish: "stop", ProviderFinish: message.Finish}
		for _, part := range message.Content {
			result.Parts = append(result.Parts, struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: part.Type, Text: part.Text})
		}
		return result, nil
	}
	if latest.Error != nil {
		return openCodeSessionMessage{}, openCodeProviderError{status: latest.Error.Status, retryable: modelBrokerRetryableStatus(latest.Error.Status)}
	}
	return openCodeSessionMessage{ID: latest.ID, Role: latest.Type}, nil
}
func (p *EnvOpenCodeExecutorStatusProvider) nativeV2Status(ctx context.Context, status OpenCodeExecutorStatus) OpenCodeExecutorStatus {
	e := &EnvOpenCodeExecutor{baseURL: p.openCodeURL, username: p.username, password: p.password, client: p.client}
	var response struct {
		Data []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Time  struct {
				Created int64 `json:"created"`
				Updated int64 `json:"updated"`
			} `json:"time"`
		} `json:"data"`
	}
	if err := e.v2JSON(ctx, http.MethodGet, "/session", nil, &response); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	var active struct {
		Data map[string]any `json:"data"`
	}
	if err := e.v2JSON(ctx, http.MethodGet, "/session/active", nil, &active); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	if err := e.openCodeV2ManagedReady(ctx); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	status.Status = "healthy"
	status.Healthy = true
	for _, s := range response.Data {
		state := "idle"
		if _, ok := active.Data[s.ID]; ok {
			state = "running"
			status.ActiveSessionCount++
		}
		created, updated := time.UnixMilli(s.Time.Created).UTC(), time.UnixMilli(s.Time.Updated).UTC()
		status.Sessions = append(status.Sessions, OpenCodeExecutorStatusSession{ID: s.ID, Title: firstNonEmpty(s.Title, s.ID), Status: state, CreatedAt: &created, UpdatedAt: &updated})
	}
	status.SessionCount = len(status.Sessions)
	return status
}
func decodeOpenCodeV2SessionProbe(body io.Reader) ExecutorSessionProbe {
	var response struct {
		Data []struct {
			Type    string `json:"type"`
			Outcome string `json:"outcome"`
			Time    struct {
				Created   float64 `json:"created"`
				Completed float64 `json:"completed"`
			} `json:"time"`
		} `json:"data"`
	}
	probe := ExecutorSessionProbe{Presence: ExecutorSessionPresent, Reason: "executor holds this session", Activity: "idle"}
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		probe.Reason = "executor holds this session; message metadata was unreadable"
		return probe
	}
	if len(response.Data) > 0 {
		last := response.Data[len(response.Data)-1]
		if last.Type != "idle" {
			probe.Activity = "active"
		} else if last.Outcome == "failed" {
			probe.Activity = "failed"
		}
	}
	return probe
}
func openCodeV2ModelCatalog(r *http.Request, catalog providerModelCatalog, baseURL, password string) providerModelCatalog {
	response, err := fetchJSONMap(r, baseURL, "/model", http.MethodGet, nil, "opencode", password)
	if err != nil {
		catalog.Status = "failed"
		catalog.Error = err.Error()
		return catalog
	}
	catalog.Status = "healthy"
	catalog.Models = extractOpenCodeModels(map[string]any{"models": response["data"]})
	return catalog
}

// Snapshot endpoints may return before asynchronous plugin settlement. Never
// admit strict-mode prompts until the managed hooks and deny rules are visible.
func (e *EnvOpenCodeExecutor) openCodeV2ManagedReady(ctx context.Context) error {
	if os.Getenv("OPENCODE_SECURITY_MODE") == "development" {
		return nil
	}
	var plugins struct {
		Data []struct {
			ID    string `json:"id"`
			State struct {
				Status string `json:"status"`
			} `json:"state"`
		} `json:"data"`
	}
	if err := e.v2JSON(ctx, http.MethodGet, "/plugin", nil, &plugins); err != nil {
		return err
	}
	for _, id := range []string{"switchyard.rtk", "switchyard.managed"} {
		ready := false
		for _, plugin := range plugins.Data {
			if plugin.ID == id && plugin.State.Status == "active" {
				ready = true
			}
		}
		if !ready {
			return fmt.Errorf("OpenCode v2 managed plugins are not ready")
		}
	}
	var agents struct {
		Data []struct {
			ID          string `json:"id"`
			Permissions []struct {
				Action   string `json:"action"`
				Resource string `json:"resource"`
				Effect   string `json:"effect"`
			} `json:"permissions"`
		} `json:"data"`
	}
	if err := e.v2JSON(ctx, http.MethodGet, "/agent", nil, &agents); err != nil {
		return err
	}
	for _, agent := range agents.Data {
		if agent.ID != "build" {
			continue
		}
		for _, action := range []string{"shell", "subagent", "external_directory", "question"} {
			effect := ""
			for _, rule := range agent.Permissions {
				if (rule.Action == action || rule.Action == "*") && rule.Resource == "*" {
					effect = rule.Effect
				}
			}
			if effect != "deny" {
				return fmt.Errorf("OpenCode v2 managed permissions are not ready")
			}
		}
		return nil
	}
	return fmt.Errorf("OpenCode v2 managed agent is not ready")
}

func extractionModelSelection(model map[string]string, extraction bool) map[string]string {
	selected := map[string]string{"providerID": model["providerID"], "id": model["modelID"]}
	if extraction {
		selected["variant"] = "low"
	}
	return selected
}

func (e *EnvOpenCodeExecutor) openCodeExtractionReady(ctx context.Context) error {
	var plugins struct {
		Data []struct {
			ID    string `json:"id"`
			State struct {
				Status string `json:"status"`
			} `json:"state"`
		} `json:"data"`
	}
	if err := e.v2JSON(ctx, http.MethodGet, "/plugin", nil, &plugins); err != nil {
		return err
	}
	for _, p := range plugins.Data {
		if p.ID == "switchyard.extraction" && p.State.Status == "active" {
			return nil
		}
	}
	return fmt.Errorf("OpenCode bounded extraction plugin is not ready")
}
