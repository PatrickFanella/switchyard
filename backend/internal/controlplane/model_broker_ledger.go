package controlplane

import "time"

const (
	ModelBrokerRequestQueued          = "queued"
	ModelBrokerRequestRunning         = "running"
	ModelBrokerRequestCancelRequested = "cancel_requested"
	ModelBrokerRequestCanceled        = "canceled"
	ModelBrokerRequestCompleted       = "completed"
	ModelBrokerRequestFailed          = "failed"
)

type ModelBrokerRequest struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Model             string            `json:"model"`
	Path              string            `json:"path"`
	Client            string            `json:"client"`
	ClientPriority    int               `json:"client_priority"`
	RequestBody       string            `json:"request_body,omitempty"`
	ResponseBody      string            `json:"response_body,omitempty"`
	Error             string            `json:"error,omitempty"`
	CorrelationIDs    map[string]string `json:"correlation_ids,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
	UpdatedAt         time.Time         `json:"updated_at"`
	CancelRequestedAt *time.Time        `json:"cancel_requested_at,omitempty"`
}

type CreateModelBrokerRequestInput struct {
	ID          string
	Model       string
	Path        string
	Client      string
	Priority    int
	RequestBody string
}

type FinishModelBrokerRequestInput struct {
	Status         string
	ResponseBody   string
	Error          string
	CorrelationIDs map[string]string
}

type ListModelBrokerRequestsFilter struct {
	Statuses      []string
	Query         string
	Limit         int
	BeforeCreated *time.Time
	BeforeID      string
	SinceCreated  *time.Time
}

type ModelBrokerFeedback struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id"`
	Rating    int       `json:"rating"`
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateModelBrokerFeedbackInput struct {
	RequestID string `json:"request_id"`
	Rating    int    `json:"rating"`
	Comment   string `json:"comment,omitempty"`
}

func cloneModelBrokerRequest(in ModelBrokerRequest) ModelBrokerRequest {
	in.CorrelationIDs = cloneMap(in.CorrelationIDs)
	if in.StartedAt != nil {
		v := *in.StartedAt
		in.StartedAt = &v
	}
	if in.FinishedAt != nil {
		v := *in.FinishedAt
		in.FinishedAt = &v
	}
	if in.CancelRequestedAt != nil {
		v := *in.CancelRequestedAt
		in.CancelRequestedAt = &v
	}
	return in
}
