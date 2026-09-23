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

type JSONShape struct {
	Type        string                       `json:"type"`
	Description string                       `json:"description,omitempty"`
	Properties  map[string]JSONShapeProperty `json:"properties,omitempty"`
	Required    []string                     `json:"required,omitempty"`
}

type JSONShapeProperty struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type WorkflowDefinition struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Status      string             `json:"status"`
	Version     int                `json:"version"`
	Owns        []string           `json:"owns,omitempty"`
	Uses        []string           `json:"uses,omitempty"`
	InputShape  JSONShape          `json:"input_shape,omitempty"`
	Pipeline    PipelineDefinition `json:"pipeline"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type WorkflowDefinitionRollbackInput struct {
	Version int `json:"version"`
}

type PipelineDefinition struct {
	ID          string                   `json:"id"`
	WorkflowID  string                   `json:"workflow_id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Steps       []PipelineStepDefinition `json:"steps"`
}

type PipelineStepDefinition struct {
	ID          string            `json:"id"`
	Position    int               `json:"position"`
	Name        string            `json:"name"`
	Executor    string            `json:"executor"`
	Profile     string            `json:"profile,omitempty"`
	Model       string            `json:"model,omitempty"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	OutputShape JSONShape         `json:"output_shape,omitempty"`
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
	LastRunAt   *time.Time        `json:"last_run_at,omitempty"`
	UpdatedAt   *time.Time        `json:"updated_at,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
}

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
	RunPaused    RunStatus = "paused"
	RunTimedOut  RunStatus = "timed_out"
)

type Run struct {
	ID               string            `json:"id"`
	WorkflowID       string            `json:"workflow_id"`
	TriggerID        string            `json:"trigger_id,omitempty"`
	Status           RunStatus         `json:"status"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	QueuedAt         time.Time         `json:"queued_at"`
	AvailableAt      time.Time         `json:"available_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	ClaimedAt        *time.Time        `json:"claimed_at,omitempty"`
	ClaimedBy        string            `json:"claimed_by,omitempty"`
	LeaseExpiresAt   *time.Time        `json:"lease_expires_at,omitempty"`
	AttemptCount     int               `json:"attempt_count"`
	MaxAttempts      int               `json:"max_attempts"`
	Summary          string            `json:"summary,omitempty"`
	PipelineID       string            `json:"pipeline_id,omitempty"`
	PipelineVersion  string            `json:"pipeline_version,omitempty"`
	PipelineSnapshot *Pipeline         `json:"-"`
}

type RunStepStatus string

const (
	StepQueued    RunStepStatus = "queued"
	StepRunning   RunStepStatus = "running"
	StepSucceeded RunStepStatus = "succeeded"
	StepFailed    RunStepStatus = "failed"
	StepCanceled  RunStepStatus = "canceled"
	StepTimedOut  RunStepStatus = "timed_out"
	StepPaused    RunStepStatus = "paused"
)

type RunStep struct {
	ID             string            `json:"id"`
	RunID          string            `json:"run_id"`
	Position       int               `json:"position"`
	Name           string            `json:"name"`
	Executor       string            `json:"executor"`
	Model          string            `json:"model,omitempty"`
	Status         RunStepStatus     `json:"status"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	OutputSummary  string            `json:"output_summary,omitempty"`
	Error          string            `json:"error,omitempty"`
	AttemptCount   int               `json:"attempt_count"`
	CorrelationIDs map[string]string `json:"correlation_ids,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
}

type CreateRunStepInput struct {
	RunID          string            `json:"run_id"`
	Position       int               `json:"position"`
	Name           string            `json:"name"`
	Executor       string            `json:"executor"`
	Model          string            `json:"model,omitempty"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	CorrelationIDs map[string]string `json:"correlation_ids,omitempty"`
}

type UpdateRunStepInput struct {
	Inputs map[string]string `json:"inputs,omitempty"`
}

type RunEvent struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type Artifact struct {
	ID             string     `json:"id"`
	RunID          string     `json:"run_id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	URI            string     `json:"uri"`
	StorageClass   string     `json:"storage_class"`
	ContentType    string     `json:"content_type,omitempty"`
	ByteSize       int        `json:"byte_size"`
	SHA256         string     `json:"sha256,omitempty"`
	RetentionUntil *time.Time `json:"retention_until,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type CreateArtifactInput struct {
	RunID string `json:"run_id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	URI   string `json:"uri"`
}

type RunDetail struct {
	Run       Run        `json:"run"`
	Steps     []RunStep  `json:"steps"`
	Events    []RunEvent `json:"events"`
	Artifacts []Artifact `json:"artifacts"`
}

type ListRunsPageInput struct {
	Limit         int
	BeforeCreated *time.Time
	BeforeID      string
}

type RunPage struct {
	Runs    []Run
	HasMore bool
}

type RetentionResult struct {
	Runs                    int   `json:"runs"`
	ModelRequests           int   `json:"model_requests"`
	Artifacts               int   `json:"artifacts"`
	ArtifactBytes           int64 `json:"artifact_bytes"`
	ArtifactBytesUnmeasured int   `json:"artifact_bytes_unmeasured"`
	WebhookReceipts         int   `json:"webhook_receipts"`
}

type ArtifactReportInput struct {
	Start time.Time
	End   time.Time
}

type ArtifactDailyBucket struct {
	Date                  string `json:"date"`
	Created               int64  `json:"created"`
	CreatedBytes          int64  `json:"created_bytes"`
	Pruned                int64  `json:"pruned"`
	PrunedBytes           int64  `json:"pruned_bytes"`
	PrunedBytesUnmeasured int64  `json:"pruned_bytes_unmeasured"`
	NetChange             int64  `json:"net_change"`
	NetBytes              int64  `json:"net_bytes"`
}

type ArtifactReport struct {
	WindowStart                             time.Time             `json:"window_start"`
	WindowEnd                               time.Time             `json:"window_end"`
	Bucket                                  string                `json:"bucket"`
	ObservationStartedAt                    time.Time             `json:"observation_started_at"`
	ObservationRetainedFloor                int64                 `json:"observation_retained_floor"`
	ObservationRetainedFloorBytes           int64                 `json:"observation_retained_floor_bytes"`
	ObservationRetainedFloorBytesUnmeasured int64                 `json:"observation_retained_floor_bytes_unmeasured"`
	Retained                                int64                 `json:"retained"`
	RetainedBytes                           int64                 `json:"retained_bytes"`
	RetainedBytesUnmeasured                 int64                 `json:"retained_bytes_unmeasured"`
	CreatedTotal                            int64                 `json:"created_total"`
	CreatedBytesTotal                       int64                 `json:"created_bytes_total"`
	PrunedTotal                             int64                 `json:"pruned_total"`
	PrunedBytesTotal                        int64                 `json:"pruned_bytes_total"`
	PrunedBytesUnmeasuredTotal              int64                 `json:"pruned_bytes_unmeasured_total"`
	DeclaredRetentionMissing                int64                 `json:"declared_retention_missing"`
	DeclaredRetentionExpired                int64                 `json:"declared_retention_expired"`
	DeclaredExpiredBytes                    int64                 `json:"declared_retention_expired_bytes"`
	DeclaredExpiredBytesUnmeasured          int64                 `json:"declared_retention_expired_bytes_unmeasured"`
	RetentionSemantics                      string                `json:"retention_semantics"`
	Buckets                                 []ArtifactDailyBucket `json:"buckets"`
}

type RuntimeMetrics struct {
	RunStatusCounts                 map[string]int
	ModelRequestStatusCounts        map[string]int
	ModelLatencyCount               int
	ModelLatencyMilliseconds        float64
	ModelLatencyMaxMilliseconds     float64
	ArtifactObservationStartedAt    time.Time
	ArtifactsRetained               int64
	ArtifactBytesRetained           int64
	ArtifactsCreated                int64
	ArtifactBytesCreated            int64
	ArtifactsPruned                 int64
	ArtifactBytesPruned             int64
	ArtifactBytesRetainedUnmeasured int64
	ArtifactBytesPrunedUnmeasured   int64
	WebhookReceiptsRetained         int64
	WebhookReceiptsPruned           int64
}

type CreateRunInput struct {
	WorkflowID                 string            `json:"workflow_id"`
	TriggerID                  string            `json:"trigger_id,omitempty"`
	Inputs                     map[string]string `json:"inputs,omitempty"`
	MaxAttempts                int               `json:"max_attempts,omitempty"`
	PipelineSnapshot           *Pipeline         `json:"-"`
	OpenPilotTarget            *OpenPilotTarget  `json:"-"`
	OpenPilotFreshSessionRunID string            `json:"-"`
	OpenPilotFeedbackRunID     string            `json:"-"`
	OpenPilotFeedback          string            `json:"-"`
}

type WebhookAuthKey struct {
	ID        string
	NotBefore time.Time
	NotAfter  time.Time
}

type WebhookAuthMetadata struct {
	TriggerID string
	Revision  string
	Keys      []WebhookAuthKey
}

type WebhookAuthConfig struct {
	Owner            string
	Generation       int64
	Digest           string
	ReceiptRetention time.Duration
	Triggers         []WebhookAuthMetadata
}

type AdmitWebhookInput struct {
	TriggerID      string
	DeliveryID     string
	BodySHA256     string
	AuthGeneration int64
	AuthRevision   string
	KeyID          string
	Inputs         map[string]string
	InputTypes     map[string]string
}

type WebhookAdmission struct {
	Run      Run  `json:"run"`
	Replayed bool `json:"replayed"`
}
