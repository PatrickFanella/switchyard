package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type AgentAction struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Status    string         `json:"status,omitempty"`
	Time      any            `json:"time,omitempty"`
	Details   map[string]any `json:"details"`
}

type AgentActivity struct {
	Actions   []AgentAction `json:"actions"`
	Warnings  []string      `json:"warnings"`
	Truncated bool          `json:"truncated"`
}

var activityQuotedSecret = regexp.MustCompile(`(?i)((?:api[_-]?key|password|secret|token|authorization)["']?\s*[:=]\s*["']?)[^"'\s,;}]+`)

// Round-trip typed store records before applying the recursive public projection.
func redactedActivityJSON(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var projected any
	if json.Unmarshal(encoded, &projected) != nil {
		return nil
	}
	return activitySafeWithLimit(projected, false)
}

func activitySafe(value any) any {
	return activitySafeWithLimit(value, true)
}

func activitySafeWithLimit(value any, bounded bool) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			switch strings.ToLower(key) {
			case "reasoning", "providerstate", "providerresultstate", "providercontext":
				continue
			}
			if sensitiveJSONKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = activitySafeWithLimit(child, bounded)
			}
		}
		return out
	case []any:
		out := []any{}
		for _, child := range v {
			if m, ok := child.(map[string]any); ok && (m["type"] == "reasoning" || m["type"] == "image") {
				continue
			}
			out = append(out, activitySafeWithLimit(child, bounded))
		}
		return out
	case string:
		// Redact before bounding so a cutoff cannot split a credential.
		v = activityQuotedSecret.ReplaceAllString(RedactForLog(v), `${1}[REDACTED]`)
		if bounded && len(v) > 65536 {
			v = v[:65536] + "\n[truncated after 64 KiB]"
		}
		return v
	default:
		return v
	}
}

func projectAgentMessages(session string, messages []map[string]any) []AgentAction {
	actions := []AgentAction{}
	for _, message := range messages {
		id, _ := message["id"].(string)
		kind, _ := message["type"].(string)
		if kind == "assistant" {
			content, _ := message["content"].([]any)
			for index, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				partKind, _ := part["type"].(string)
				if partKind != "tool" && partKind != "text" {
					continue
				}
				partID := fmt.Sprintf("%s:%d", id, index)
				title := "Agent response"
				status := ""
				stamp := message["time"]
				if partKind == "tool" {
					if toolID, ok := part["id"].(string); ok {
						partID = toolID
					}
					title, _ = part["name"].(string)
					if state, ok := part["state"].(map[string]any); ok {
						status, _ = state["status"].(string)
					}
					if part["time"] != nil {
						stamp = part["time"]
					}
				}
				details := activitySafe(part).(map[string]any)
				for _, key := range []string{"model", "agent", "cost", "error", "finish", "retry"} {
					if value, ok := message[key]; ok {
						details[key] = activitySafe(value)
					}
				}
				if usage, ok := message["tokens"]; ok {
					details["usage"] = activitySafe(usage)
				}
				actions = append(actions, AgentAction{ID: partID, SessionID: session, Kind: partKind, Title: title, Status: status, Time: stamp, Details: details})
			}
		} else {
			switch kind {
			case "user", "shell", "idle", "system", "synthetic", "skill", "model-switched", "agent-switched", "location-switched", "compaction":
			default:
				continue
			}
			// Compaction content can contain internal reasoning summaries.
			if kind == "compaction" {
				message = map[string]any{"id": id, "type": kind, "status": message["status"], "time": message["time"]}
			}
			actions = append(actions, AgentAction{ID: id, SessionID: session, Kind: kind, Title: kind, Time: message["time"], Details: activitySafe(message).(map[string]any)})
		}
	}
	return actions
}

func (p *envExecutorSessionProber) readActivity(ctx context.Context, key, session string) ([]AgentAction, bool, error) {
	route, ok := p.routes[key]
	if !ok || !route.native || !openCodeV2(route.baseURL) {
		return nil, false, fmt.Errorf("native v2 activity is unavailable for this executor generation")
	}
	endpoint, err := url.JoinPath(route.baseURL, "session", session, "message")
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?limit=101&order=desc", nil)
	if err != nil {
		return nil, false, err
	}
	if route.username != "" || route.password != "" {
		req.SetBasicAuth(openCodeUsername(route.baseURL, route.username), route.password)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("executor activity request did not complete")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("executor activity returned HTTP %d; retained run evidence remains available", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, false, fmt.Errorf("executor activity exceeded the 2 MiB response limit")
	}
	var body struct {
		Data   []map[string]any `json:"data"`
		Cursor struct {
			Next string `json:"next"`
		} `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, fmt.Errorf("executor activity response was unreadable")
	}
	// V2 emits pagination cursors even at the end of history. Read one extra
	// message so the omitted-history flag is based on evidence, not cursor presence.
	truncated := len(body.Data) > 100
	if truncated {
		body.Data = body.Data[:100]
	}
	return projectAgentMessages(session, body.Data), truncated, nil
}

func (a *API) runActivity(w http.ResponseWriter, r *http.Request) {
	detail, err := a.store.RunDetailContext(r.Context(), r.PathValue("id"))
	if err != nil {
		writeControlError(w, err)
		return
	}
	result := AgentActivity{Actions: []AgentAction{}, Warnings: []string{}}
	key := ExecutorSessionDefaultKey
	if repository := detail.Run.Inputs[openPilotRepositoryIDInputKey]; repository != "" {
		key = executorSessionRepositoryKey(repository, detail.Run.Inputs[openPilotExecutorGenerationInputKey])
	}
	sessions := []string{}
	add := func(id string) {
		if !executorSessionIDPattern.MatchString(id) {
			return
		}
		for _, known := range sessions {
			if known == id {
				return
			}
		}
		sessions = append(sessions, id)
	}
	// Newest step first; the session is persisted at prompt submission, before completion.
	for i := len(detail.Steps) - 1; i >= 0; i-- {
		add(detail.Steps[i].CorrelationIDs["opencode_session_id"])
	}
	add(detail.Run.Inputs["opencode_session_id"])
	inputKeys := make([]string, 0, len(detail.Run.Inputs))
	for input := range detail.Run.Inputs {
		inputKeys = append(inputKeys, input)
	}
	sort.Strings(inputKeys)
	for _, input := range inputKeys {
		if strings.HasSuffix(input, ".opencode_session_id") {
			add(detail.Run.Inputs[input])
		}
	}
	if len(sessions) > 4 {
		sessions = sessions[:4]
		result.Truncated = true
		result.Warnings = append(result.Warnings, "Only the four newest correlated sessions are displayed.")
	}
	p, supported := a.executorSessionProber.(*envExecutorSessionProber)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, session := range sessions {
		if !supported {
			result.Warnings = append(result.Warnings, "Agent activity reader is unavailable.")
			break
		}
		actions, truncated, err := p.readActivity(ctx, key, session)
		if err != nil {
			result.Warnings = append(result.Warnings, session+": "+err.Error())
			continue
		}
		result.Actions = append(result.Actions, actions...)
		result.Truncated = result.Truncated || truncated
	}
	if result.Truncated {
		result.Warnings = append(result.Warnings, "This view contains the latest 100 messages per session; earlier messages are omitted.")
	}
	w.Header().Set("Cache-Control", "no-store")
	// Apply the same redaction to full run and step inputs/outputs shown beside actions.
	if len(detail.Events) > 500 {
		detail.Events = detail.Events[len(detail.Events)-500:]
		result.Warnings = append(result.Warnings, "Run context contains the latest 500 runtime events.")
	}
	if len(detail.Steps) > 200 {
		detail.Steps = detail.Steps[len(detail.Steps)-200:]
		result.Warnings = append(result.Warnings, "Run context contains the latest 200 steps.")
	}
	if len(detail.Artifacts) > 200 {
		detail.Artifacts = detail.Artifacts[len(detail.Artifacts)-200:]
		result.Warnings = append(result.Warnings, "Run context contains the latest 200 artifacts.")
	}
	encoded, _ := json.Marshal(detail)
	var contextDetail map[string]any
	_ = json.Unmarshal(encoded, &contextDetail)
	writeJSON(w, http.StatusOK, map[string]any{"run_id": detail.Run.ID, "generated_at": a.now(), "activity": result, "detail": activitySafe(contextDetail)})
}
