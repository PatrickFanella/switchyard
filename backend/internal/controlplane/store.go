package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrInvalid         = errors.New("invalid input")
	ErrQueueFull       = errors.New("queue capacity exceeded")
	ErrAlreadyClaimed  = errors.New("open-pilot issue already claimed")
	ErrConflict        = errors.New("state conflict")
	ErrStaleRunAttempt = fmt.Errorf("stale run attempt: %w", ErrConflict)
)

type Store interface {
	WithContext(context.Context) Store
	Ready(context.Context) error
	UpsertWorkflowDefinition(WorkflowDefinition) (WorkflowDefinition, error)
	WorkflowDefinition(string) (WorkflowDefinition, error)
	ListWorkflowDefinitions() ([]WorkflowDefinition, error)
	ListWorkflowDefinitionVersions(string) ([]WorkflowDefinition, error)
	ActivateWorkflowDefinition(string) (WorkflowDefinition, error)
	RollbackWorkflowDefinition(string, int) (WorkflowDefinition, error)
	AgentProfileIdentity(string) (AgentProfileIdentity, error)
	ListAgentProfileIdentities() ([]AgentProfileIdentity, error)
	ActiveAgentProfile(string) (AgentProfileVersion, error)
	ListActiveAgentProfiles() ([]AgentProfileVersion, error)
	AgentProfileVersion(string, int) (AgentProfileVersion, error)
	ListAgentProfileVersions(string) ([]AgentProfileVersion, error)
	SaveAgentProfileDraft(string, AgentProfileDraftInput, string) (AgentProfileVersion, error)
	ActivateAgentProfileVersion(string, AgentProfileActivationInput, string) (AgentProfileVersion, error)
	RollbackAgentProfile(string, AgentProfileActivationInput, string) (AgentProfileVersion, error)
	ListAgentProfileAudit(string, int) ([]AgentProfileAuditEntry, error)
	UpsertWorkflow(Workflow) error
	UpsertTrigger(Trigger) error
	OpenPilotIssueState(string, string) (OpenPilotIssueState, error)
	OpenPilotIssueStateForTarget(OpenPilotTarget) (OpenPilotIssueState, error)
	UpsertOpenPilotIssueState(OpenPilotIssueState) (OpenPilotIssueState, error)
	TransitionOpenPilotIssueState(string, string, OpenPilotPhase) (OpenPilotIssueState, error)
	ClaimAndCreateOpenPilotResumeRun(string, string, CreateRunInput) (OpenPilotIssueState, Run, error)
	ClaimAndCreateOpenPilotInitialRun(string, string, CreateRunInput) (OpenPilotIssueState, Run, error)
	BindOpenPilotIssueStateAffinity(string, string, OpenPilotTarget) (OpenPilotIssueState, error)
	AdoptOpenPilotRunAffinity(string, string, int, *OpenPilotAdmissionGate) (Run, OpenPilotIssueState, error)
	HandleOpenPilotCIFailure(OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, error)
	ReconcileOpenPilotCIRepairTerminal(string, ...*OpenPilotAdmissionGate) (bool, error)
	ReconcileOpenPilotCIRepairTerminals(int, ...*OpenPilotAdmissionGate) (int, error)
	ReconcileOpenPilotMergeTerminal(string, ...*OpenPilotAdmissionGate) (bool, error)
	ReconcileOpenPilotMergeTerminals(int, ...*OpenPilotAdmissionGate) (int, error)
	ListOpenPilotIssueStates() []OpenPilotIssueState
	ListOpenPilotIssueStatesForAudit(int) ([]OpenPilotIssueState, error)
	OpenPilotRolloutStatus() (OpenPilotRolloutStatus, error)
	ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput) (OpenPilotRolloutStatus, error)
	ListWorkflows() []Workflow
	ListTriggers() []Trigger
	Trigger(string) (Trigger, bool)
	SetTriggerEnabled(string, bool) (Trigger, error)
	UpdateTriggerScheduleState(string, *time.Time, *time.Time, string) (Trigger, error)
	ReplaceWebhookAuth(context.Context, WebhookAuthConfig) error
	RevokeWebhookAuth(context.Context, string, int64, string) error
	AdmitWebhook(AdmitWebhookInput) (WebhookAdmission, error)
	AdmitWebhookContext(context.Context, AdmitWebhookInput) (WebhookAdmission, error)
	CreateRun(CreateRunInput) (Run, error)
	CreateRunContext(context.Context, CreateRunInput) (Run, error)
	StartRun(string) (Run, error)
	ClaimNextRun(string, time.Duration) (Run, error)
	ExtendRunLease(string, string, int, time.Duration) (Run, error)
	RecoverLeasedRuns(time.Time, ...*OpenPilotAdmissionGate) (int, int, error)
	RequeueFailedRun(string, time.Time, string) (Run, error)
	FinishRun(string, RunStatus, string) (Run, error)
	FinishClaimedRun(string, string, int, RunStatus, string) (Run, error)
	CreateRunStep(CreateRunStepInput) (RunStep, error)
	StartRunStep(string) (RunStep, error)
	UpdateRunStep(string, UpdateRunStepInput) (RunStep, error)
	FinishRunStep(string, RunStepStatus, string, string, map[string]string) (RunStep, error)
	MergeRunStepCorrelationIDs(string, map[string]string) (RunStep, error)
	RecordRunStepBackendDecision(string, string, BackendDecision) error
	RetryRunStep(string) (RunStep, error)
	RecoverRunStep(string) (RunStep, error)
	CancelRunStep(string) (RunStep, error)
	ListRunSteps(string) []RunStep
	RunStep(string) (RunStep, error)
	AddEvent(string, string, string) (RunEvent, error)
	AddArtifact(CreateArtifactInput) (Artifact, error)
	ListRuns() []Run
	ListRunsPage(ListRunsPageInput) (RunPage, error)
	ListRunsPageContext(context.Context, ListRunsPageInput) (RunPage, error)
	RunDetail(string) (RunDetail, error)
	RunDetailContext(context.Context, string) (RunDetail, error)
	CreateModelBrokerRequest(CreateModelBrokerRequestInput) (ModelBrokerRequest, error)
	BeginModelBrokerRequest(CreateModelBrokerRequestInput) (ModelBrokerRequest, bool, error)
	AdmitModelBrokerRequest(CreateModelBrokerRequestInput, int) (ModelBrokerRequest, bool, error)
	ClaimNextModelBrokerRequest() (ModelBrokerRequest, error)
	TransitionModelBrokerRequest(string, TransitionModelBrokerRequestInput) (ModelBrokerRequest, error)
	StartModelBrokerRequest(string) (ModelBrokerRequest, error)
	FinishModelBrokerRequest(string, FinishModelBrokerRequestInput) (ModelBrokerRequest, error)
	MergeModelBrokerRequestCorrelationIDs(string, map[string]string) (ModelBrokerRequest, error)
	CancelModelBrokerRequest(string) (ModelBrokerRequest, error)
	CancelAllModelBrokerRequests() (int, error)
	RequestCancelModelBrokerRequests(map[string]string) ([]ModelBrokerRequest, error)
	ModelBrokerQueueSnapshot(int) (ModelBrokerQueueSnapshot, error)
	ListModelBrokerRequests(ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error)
	ListModelBrokerRequestsContext(context.Context, ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error)
	ModelBrokerRequest(string) (ModelBrokerRequest, error)
	ModelBrokerRequestContext(context.Context, string) (ModelBrokerRequest, error)
	AddModelBrokerFeedback(CreateModelBrokerFeedbackInput) (ModelBrokerFeedback, error)
	ListModelBrokerFeedback(int) ([]ModelBrokerFeedback, error)
	PruneRetention(time.Time) (RetentionResult, error)
	ArtifactReport(context.Context, ArtifactReportInput) (ArtifactReport, error)
	SearchEvidence(context.Context, EvidenceSearchInput) (EvidenceSearchPage, error)
	RuntimeMetrics() (RuntimeMetrics, error)
	RecordDevlogDeliveryApproval(DevlogDeliveryApproval) (DevlogDeliveryApproval, error)
	DevlogDeliveryApproval(string) (DevlogDeliveryApproval, error)
	ListDevlogDeliveryApprovals(string) ([]DevlogDeliveryApproval, error)
	BeginDevlogDelivery(BeginDevlogDeliveryInput) (DevlogDeliveryReceipt, bool, error)
	FinishDevlogDelivery(FinishDevlogDeliveryInput) (DevlogDeliveryReceipt, error)
	DevlogDeliveryReceipt(string) (DevlogDeliveryReceipt, error)
	ListDevlogDeliveryReceipts(string) ([]DevlogDeliveryReceipt, error)
}

// OpenPilotCIRepairRequest contains the optimistic claim identity and the only
// caller-prepared values accepted by the atomic CI repair handoff. The store
// derives the child identity, cursor, and pipeline from locked durable state.
type OpenPilotCIRepairRequest struct {
	Target               OpenPilotTarget
	Repo                 string
	Issue                string
	ParentRunID          string
	ExpectedWorkerID     string
	ExpectedAttemptCount int
	PullRequestID        int
	PullRequestNumber    int
	HeadBranch           string
	FailedHeadSHA        string
	Feedback             string
	ChildRun             CreateRunInput
}

type OpenPilotCIRepairResult struct {
	State     OpenPilotIssueState
	ParentRun Run
	ChildRun  *Run
	Attempt   int
	Queued    bool
	Exhausted bool
	Replayed  bool
}

type MemoryStore struct {
	mu                         sync.RWMutex
	now                        func() time.Time
	workflowDefinitions        map[string]WorkflowDefinition
	workflowDefinitionVersions map[string]map[int]WorkflowDefinition
	agentProfiles              map[string]AgentProfileIdentity
	agentProfileVersions       map[string]map[int]AgentProfileVersion
	agentProfileAudit          map[string][]AgentProfileAuditEntry
	workflows                  map[string]Workflow
	triggers                   map[string]Trigger
	openPilotIssueStates       map[string]OpenPilotIssueState
	openPilotReservations      map[string][]string
	openPilotRollout           OpenPilotRolloutStatus
	runs                       map[string]Run
	steps                      map[string]RunStep
	events                     map[string][]RunEvent
	artifacts                  map[string][]Artifact
	modelBrokerRequests        map[string]ModelBrokerRequest
	modelBrokerFeedback        []ModelBrokerFeedback
	compatibilityUsage         map[string]CompatibilityUsageDaily
	artifactDaily              map[string]ArtifactDailyBucket
	artifactObservationStarted time.Time
	artifactsCreated           int64
	artifactBytesCreated       int64
	artifactsPruned            int64
	artifactBytesPruned        int64
	webhookAuth                map[string]WebhookAuthMetadata
	webhookReceipts            map[string]webhookReceipt
	webhookAuthOwner           string
	webhookAuthGeneration      int64
	webhookAuthDigest          string
	webhookReceiptRetention    time.Duration
	webhookReceiptsPruned      int64
	devlogApprovals            map[string]DevlogDeliveryApproval
	devlogReceipts             map[string]DevlogDeliveryReceipt
}

func NewStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	created := now().UTC()
	return &MemoryStore{now: now, workflowDefinitions: map[string]WorkflowDefinition{}, workflowDefinitionVersions: map[string]map[int]WorkflowDefinition{}, agentProfiles: map[string]AgentProfileIdentity{}, agentProfileVersions: map[string]map[int]AgentProfileVersion{}, agentProfileAudit: map[string][]AgentProfileAuditEntry{}, workflows: map[string]Workflow{}, triggers: map[string]Trigger{}, openPilotIssueStates: map[string]OpenPilotIssueState{}, openPilotReservations: map[string][]string{}, openPilotRollout: OpenPilotRolloutStatus{Mode: "legacy", ProofTargets: []OpenPilotProofTarget{}, UpdatedAt: created}, runs: map[string]Run{}, steps: map[string]RunStep{}, events: map[string][]RunEvent{}, artifacts: map[string][]Artifact{}, modelBrokerRequests: map[string]ModelBrokerRequest{}, modelBrokerFeedback: []ModelBrokerFeedback{}, compatibilityUsage: map[string]CompatibilityUsageDaily{}, artifactDaily: map[string]ArtifactDailyBucket{}, artifactObservationStarted: created, webhookAuth: map[string]WebhookAuthMetadata{}, webhookReceipts: map[string]webhookReceipt{}}
}

func (s *MemoryStore) Ready(context.Context) error { return nil }
func (s *MemoryStore) WithContext(ctx context.Context) Store {
	fence, present, err := runAttemptFenceFromContext(ctx)
	return &memoryStoreContext{MemoryStore: s, fence: fence, fencePresent: present, fenceErr: err}
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// normalizeWorkflowDefinition validates profile references against the
// effective registry (active persisted versions, then shipped defaults).
func normalizeWorkflowDefinition(definition WorkflowDefinition, profiles AgentProfileRegistry) (WorkflowDefinition, error) {
	if profiles == nil {
		profiles = DefaultAgentProfileRegistry()
	}
	definition = cloneWorkflowDefinition(definition)
	definition.ID, definition.Name = strings.TrimSpace(definition.ID), strings.TrimSpace(definition.Name)
	definition.Description, definition.Status = strings.TrimSpace(definition.Description), strings.TrimSpace(definition.Status)
	definition.Pipeline.ID, definition.Pipeline.WorkflowID = strings.TrimSpace(definition.Pipeline.ID), strings.TrimSpace(definition.Pipeline.WorkflowID)
	definition.Pipeline.Name, definition.Pipeline.Description = strings.TrimSpace(definition.Pipeline.Name), strings.TrimSpace(definition.Pipeline.Description)
	if definition.ID == "" || definition.Name == "" || definition.Pipeline.ID == "" || definition.Pipeline.Name == "" || len(definition.Pipeline.Steps) == 0 {
		return WorkflowDefinition{}, ErrInvalid
	}
	if definition.Status == "" {
		definition.Status = "draft"
	}
	if definition.Status != "draft" && definition.Status != "active" {
		return WorkflowDefinition{}, ErrInvalid
	}
	if definition.Owns == nil {
		definition.Owns = []string{}
	}
	if definition.Uses == nil {
		definition.Uses = []string{}
	}
	if definition.Pipeline.WorkflowID == "" {
		definition.Pipeline.WorkflowID = definition.ID
	}
	if definition.Pipeline.WorkflowID != definition.ID {
		return WorkflowDefinition{}, ErrInvalid
	}
	seenIDs, seenPositions := map[string]bool{}, map[int]bool{}
	for i := range definition.Pipeline.Steps {
		step := &definition.Pipeline.Steps[i]
		step.ID, step.Name, step.Executor = strings.TrimSpace(step.ID), strings.TrimSpace(step.Name), strings.TrimSpace(step.Executor)
		step.Profile, step.Model = strings.TrimSpace(step.Profile), strings.TrimSpace(step.Model)
		if step.ID == "" || step.Position < 1 || step.Name == "" || step.Executor == "" || seenIDs[step.ID] || seenPositions[step.Position] {
			return WorkflowDefinition{}, ErrInvalid
		}
		if step.Profile != "" {
			profile, ok := profiles.Profile(step.Profile)
			if !ok || !profileAllowsExecutor(profile, step.Executor) {
				return WorkflowDefinition{}, ErrInvalid
			}
			if step.Model != "" && step.Model != "${model}" && !profileAllowsModel(profile, step.Model) {
				return WorkflowDefinition{}, ErrInvalid
			}
		}
		seenIDs[step.ID], seenPositions[step.Position] = true, true
	}
	for position := 1; position <= len(definition.Pipeline.Steps); position++ {
		if !seenPositions[position] {
			return WorkflowDefinition{}, ErrInvalid
		}
	}
	if err := validatePipelineStepOutputReferences(definition.Pipeline.Steps); err != nil {
		return WorkflowDefinition{}, err
	}
	if err := validatePipelineStepOutputShapes(definition.Pipeline.Steps); err != nil {
		return WorkflowDefinition{}, err
	}
	return definition, nil
}

func cloneJSONShape(shape JSONShape) JSONShape {
	shape.Required = append([]string(nil), shape.Required...)
	shape.Properties = cloneJSONShapeProperties(shape.Properties)
	return shape
}

func cloneJSONShapeProperties(properties map[string]JSONShapeProperty) map[string]JSONShapeProperty {
	if properties == nil {
		return nil
	}
	out := make(map[string]JSONShapeProperty, len(properties))
	for key, value := range properties {
		out[key] = cloneJSONShapeProperty(value)
	}
	return out
}

func cloneJSONShapeProperty(property JSONShapeProperty) JSONShapeProperty {
	property.Properties = cloneJSONShapeProperties(property.Properties)
	property.Required = append([]string(nil), property.Required...)
	property.Enum = append([]string(nil), property.Enum...)
	if property.Items != nil {
		items := cloneJSONShapeProperty(*property.Items)
		property.Items = &items
	}
	if property.MaxLength != nil {
		value := *property.MaxLength
		property.MaxLength = &value
	}
	if property.MaxItems != nil {
		value := *property.MaxItems
		property.MaxItems = &value
	}
	return property
}

func cloneWorkflowDefinition(definition WorkflowDefinition) WorkflowDefinition {
	definition.Owns, definition.Uses = append([]string(nil), definition.Owns...), append([]string(nil), definition.Uses...)
	definition.InputShape = cloneJSONShape(definition.InputShape)
	definition.Pipeline.Steps = append([]PipelineStepDefinition(nil), definition.Pipeline.Steps...)
	for i := range definition.Pipeline.Steps {
		definition.Pipeline.Steps[i].Inputs = cloneMap(definition.Pipeline.Steps[i].Inputs)
		definition.Pipeline.Steps[i].OutputShape = cloneJSONShape(definition.Pipeline.Steps[i].OutputShape)
	}
	return definition
}
