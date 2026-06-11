package persistence

import (
	"encoding/json"
	"time"
)

// AppLogEvent is a persisted structured app log record.
type AppLogEvent struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Msg     string         `json:"msg"`
	Fields  map[string]any `json:"fields,omitempty"`
	RawJSON string         `json:"raw_json"`
}

// RequestEvent is a persisted request/audit record.
type RequestEvent struct {
	RequestID           string          `json:"request_id"`
	Time                time.Time       `json:"ts"`
	Client              string          `json:"client"`
	Model               string          `json:"model"`
	Path                string          `json:"path"`
	Upstream            string          `json:"upstream,omitempty"`
	Status              string          `json:"status"`
	Streaming           bool            `json:"streaming"`
	DurationMS          int             `json:"duration_ms"`
	QueueMS             int             `json:"queue_ms"`
	UpstreamMS          int             `json:"upstream_ms"`
	RequestBody         json.RawMessage `json:"request_body,omitempty"`
	ResponseBody        json.RawMessage `json:"response_body,omitempty"`
	RequestBodyText     string          `json:"request_body_text,omitempty"`
	ResponseBodyText    string          `json:"response_body_text,omitempty"`
	RequestBodyBytes    int             `json:"request_body_bytes"`
	ResponseBodyBytes   int             `json:"response_body_bytes"`
	ResponseJSONValid   *bool           `json:"response_json_valid,omitempty"`
	PromptTokens        int             `json:"prompt_tokens"`
	CompletionTokens    int             `json:"completion_tokens"`
	RequestMessageCount int             `json:"request_message_count"`
	ToolNames           []string        `json:"tool_names,omitempty"`
	ToolErrorCount      int             `json:"tool_error_count"`
	ToolErrors          []string        `json:"tool_errors,omitempty"`
	RawAudit            json.RawMessage `json:"raw_audit,omitempty"`
}

// AppLogSearchParams filters persisted app-log searches.
type AppLogSearchParams struct {
	Q         string
	Level     string
	Client    string
	RequestID string
	Since     *time.Time
	Until     *time.Time
	Limit     int
}

// AppLogSearchRow is a persisted app-log search result.
type AppLogSearchRow struct {
	ID        int64           `json:"id"`
	Time      time.Time       `json:"ts"`
	Level     string          `json:"level"`
	Msg       string          `json:"msg"`
	Fields    json.RawMessage `json:"fields"`
	RawJSON   string          `json:"raw_json"`
	Client    string          `json:"client,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Model     string          `json:"model,omitempty"`
	Path      string          `json:"path,omitempty"`
}

// RequestEventSearchParams filters persisted request-event searches.
type RequestEventSearchParams struct {
	Q          string
	Client     string
	Model      string
	Status     string
	Path       string
	Since      *time.Time
	Until      *time.Time
	ErrorsOnly bool
	Limit      int
}

// RequestEventSearchRow is a persisted request-event search result.
type RequestEventSearchRow struct {
	RequestID           string          `json:"request_id"`
	Time                time.Time       `json:"ts"`
	Client              string          `json:"client"`
	Model               string          `json:"model"`
	Path                string          `json:"path"`
	Upstream            string          `json:"upstream,omitempty"`
	Status              string          `json:"status"`
	Streaming           bool            `json:"streaming"`
	DurationMS          int             `json:"duration_ms"`
	QueueMS             int             `json:"queue_ms"`
	UpstreamMS          int             `json:"upstream_ms"`
	RequestBodyText     string          `json:"request_body_text,omitempty"`
	ResponseBodyText    string          `json:"response_body_text,omitempty"`
	RequestBodyBytes    int             `json:"request_body_bytes"`
	ResponseBodyBytes   int             `json:"response_body_bytes"`
	ResponseJSONValid   *bool           `json:"response_json_valid,omitempty"`
	RequestMessageCount int             `json:"request_message_count"`
	ToolNames           []string        `json:"tool_names,omitempty"`
	ToolErrorCount      int             `json:"tool_error_count"`
	ToolErrors          []string        `json:"tool_errors,omitempty"`
	RawAudit            json.RawMessage `json:"raw_audit"`
}

// SemanticSearchParams filters embedding-backed semantic request search.
type SemanticSearchParams struct {
	Client string
	Model  string
	Status string
	Path   string
	Limit  int
}

// SemanticSearchRow is a semantic search hit joined back to the request event.
type SemanticSearchRow struct {
	RequestID string    `json:"request_id"`
	Time      time.Time `json:"ts"`
	Client    string    `json:"client"`
	Model     string    `json:"model"`
	Path      string    `json:"path"`
	Status    string    `json:"status"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Score     float64   `json:"score"`
}
