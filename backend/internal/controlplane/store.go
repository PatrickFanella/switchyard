package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrInvalid        = errors.New("invalid input")
	ErrQueueFull      = errors.New("queue capacity exceeded")
	ErrAlreadyClaimed = errors.New("open-pilot issue already claimed")
	ErrConflict       = errors.New("state conflict")
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
	UpsertWorkflow(Workflow) error
	UpsertTrigger(Trigger) error
	OpenPilotIssueState(string, string) (OpenPilotIssueState, error)
	UpsertOpenPilotIssueState(OpenPilotIssueState) (OpenPilotIssueState, error)
	TransitionOpenPilotIssueState(string, string, OpenPilotPhase) (OpenPilotIssueState, error)
	ClaimOpenPilotIssueStateForResume(string, string, map[string]string) (OpenPilotIssueState, error)
	ClaimAndCreateOpenPilotResumeRun(string, string, CreateRunInput) (OpenPilotIssueState, Run, error)
	ClaimAndCreateOpenPilotInitialRun(string, string, CreateRunInput) (OpenPilotIssueState, Run, error)
	ListOpenPilotIssueStates() []OpenPilotIssueState
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
	ExtendRunLease(string, string, time.Duration) (Run, error)
	RecoverLeasedRuns(time.Time) (int, int, error)
	RequeueFailedRun(string, time.Time, string) (Run, error)
	FinishRun(string, RunStatus, string) (Run, error)
	FinishClaimedRun(string, string, RunStatus, string) (Run, error)
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
	RuntimeMetrics() (RuntimeMetrics, error)
}

type MemoryStore struct {
	mu                         sync.RWMutex
	now                        func() time.Time
	workflowDefinitions        map[string]WorkflowDefinition
	workflowDefinitionVersions map[string]map[int]WorkflowDefinition
	workflows                  map[string]Workflow
	triggers                   map[string]Trigger
	openPilotIssueStates       map[string]OpenPilotIssueState
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
}

func NewStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, workflowDefinitions: map[string]WorkflowDefinition{}, workflowDefinitionVersions: map[string]map[int]WorkflowDefinition{}, workflows: map[string]Workflow{}, triggers: map[string]Trigger{}, openPilotIssueStates: map[string]OpenPilotIssueState{}, runs: map[string]Run{}, steps: map[string]RunStep{}, events: map[string][]RunEvent{}, artifacts: map[string][]Artifact{}, modelBrokerRequests: map[string]ModelBrokerRequest{}, modelBrokerFeedback: []ModelBrokerFeedback{}, compatibilityUsage: map[string]CompatibilityUsageDaily{}, artifactDaily: map[string]ArtifactDailyBucket{}, artifactObservationStarted: now().UTC(), webhookAuth: map[string]WebhookAuthMetadata{}, webhookReceipts: map[string]webhookReceipt{}}
}

func (s *MemoryStore) Ready(context.Context) error       { return nil }
func (s *MemoryStore) WithContext(context.Context) Store { return s }

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

func normalizeWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error) {
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
			profile, ok := DefaultAgentProfileRegistry().Profile(step.Profile)
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
	return definition, nil
}

func cloneJSONShape(shape JSONShape) JSONShape {
	shape.Required = append([]string(nil), shape.Required...)
	if shape.Properties != nil {
		properties := make(map[string]JSONShapeProperty, len(shape.Properties))
		for key, value := range shape.Properties {
			properties[key] = value
		}
		shape.Properties = properties
	}
	return shape
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
