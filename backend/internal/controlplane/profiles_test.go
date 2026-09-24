package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type profileRecordingExecutor struct {
	model    string
	contract string
}

func (e *profileRecordingExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	return WorkflowExecution{}, nil
}

func (e *profileRecordingExecutor) ExecutePipelineStep(_ context.Context, _ Run, step PipelineStep, _ map[string]any) (PipelineStepExecution, error) {
	e.model = step.Model
	e.contract = step.profileContract
	return PipelineStepExecution{Summary: "recorded"}, nil
}

func writerDraft(expected int, contract string) AgentProfileDraftInput {
	return AgentProfileDraftInput{ExpectedVersion: expected, Name: "Writer", Role: "writer", DefaultModel: "profile-default", AllowedModels: []string{"profile-default"}, AllowedExecutors: []string{"model"}, PromptContract: contract}
}

func TestRunnerEnforcesProfileDefaultsAndAuditMetadata(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "profile-workflow", Kind: "profile-workflow", Name: "Profile Workflow"}); err != nil {
		t.Fatal(err)
	}
	executor := &profileRecordingExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"profile-workflow": executor})
	runner.SetAgentProfileRegistry(StaticAgentProfileRegistry{"writer": {
		ID: "writer", DefaultModel: "profile-default", PromptContract: "Write only grounded content.", AllowedExecutors: []string{"model"}, AllowedModels: []string{"profile-default"},
	}})
	runner.pipelines = StaticPipelineRegistry{"profile-workflow": {ID: "profile-pipeline", WorkflowID: "profile-workflow", Steps: []PipelineStep{{ID: "write", Position: 1, Name: "profile.write", Executor: "model", Profile: "writer"}}}}
	run, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "profile-workflow"})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if executor.model != "profile-default" || executor.contract != "Write only grounded content." {
		t.Fatalf("executor model=%q contract=%q", executor.model, executor.contract)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	correlation := detail.Steps[0].CorrelationIDs
	if correlation["agent_profile_id"] != "writer" || correlation["agent_profile_model"] != "profile-default" || correlation["agent_profile_contract_sha256"] == "" {
		t.Fatalf("profile correlation = %+v", correlation)
	}
	if correlation["agent_profile_version"] != "0" || correlation["agent_profile_source"] != AgentProfileSourceStatic {
		t.Fatalf("static profile correlation = %+v", correlation)
	}
	if !strings.Contains(applyPipelineProfilePrompt(PipelineStep{profileContract: executor.contract}, "request"), "Profile contract") {
		t.Fatal("profile contract was not applied to prompt")
	}
}

func TestWorkflowDefinitionRejectsUnknownOrDisallowedProfiles(t *testing.T) {
	store := NewStore(nil)
	base := WorkflowDefinition{ID: "profile-definition", Name: "Profile Definition", Status: "draft", Pipeline: PipelineDefinition{ID: "profile-definition-pipeline", WorkflowID: "profile-definition", Name: "Profile Definition", Steps: []PipelineStepDefinition{{ID: "step", Position: 1, Name: "step", Executor: "model", Profile: "missing"}}}}
	if _, err := store.UpsertWorkflowDefinition(base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown profile error = %v, want ErrInvalid", err)
	}
	base.Pipeline.Steps[0].Profile = "content-writer"
	base.Pipeline.Steps[0].Executor = "command"
	if _, err := store.UpsertWorkflowDefinition(base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("disallowed executor error = %v, want ErrInvalid", err)
	}
}

func TestAgentProfileDraftRejectsInvalidExecutorsAndModels(t *testing.T) {
	store := NewStore(nil)
	valid := writerDraft(0, "Write only grounded content.")
	cases := map[string]func(AgentProfileDraftInput) AgentProfileDraftInput{
		"unknown executor": func(d AgentProfileDraftInput) AgentProfileDraftInput {
			d.AllowedExecutors = []string{"shell"}
			return d
		},
		"empty executors":         func(d AgentProfileDraftInput) AgentProfileDraftInput { d.AllowedExecutors = nil; return d },
		"default outside allowed": func(d AgentProfileDraftInput) AgentProfileDraftInput { d.DefaultModel = "other-model"; return d },
		"model with whitespace": func(d AgentProfileDraftInput) AgentProfileDraftInput {
			d.AllowedModels = []string{"bad model"}
			return d
		},
		"placeholder model": func(d AgentProfileDraftInput) AgentProfileDraftInput {
			d.AllowedModels = []string{"${model}"}
			d.DefaultModel = ""
			return d
		},
		"empty contract": func(d AgentProfileDraftInput) AgentProfileDraftInput { d.PromptContract = "  "; return d },
		"oversized contract": func(d AgentProfileDraftInput) AgentProfileDraftInput {
			d.PromptContract = strings.Repeat("x", maxAgentProfileContractLength+1)
			return d
		},
		"empty role": func(d AgentProfileDraftInput) AgentProfileDraftInput { d.Role = ""; return d },
		"oversized note": func(d AgentProfileDraftInput) AgentProfileDraftInput {
			d.Note = strings.Repeat("n", maxAgentProfileNoteLength+1)
			return d
		},
		"oversized default model": func(d AgentProfileDraftInput) AgentProfileDraftInput {
			d.DefaultModel = strings.Repeat("m", 129)
			d.AllowedModels = nil
			return d
		},
	}
	for name, mutate := range cases {
		if _, err := store.SaveAgentProfileDraft("writer", mutate(valid), "token:admin"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	for _, id := range []string{"", "Writer", "bad id", "-leading", strings.Repeat("a", 65)} {
		if _, err := store.SaveAgentProfileDraft(id, valid, "token:admin"); !errors.Is(err, ErrInvalid) {
			t.Errorf("id %q: error = %v, want ErrInvalid", id, err)
		}
	}
	if _, err := store.AgentProfileIdentity("writer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid drafts must not create an identity, got %v", err)
	}
	saved, err := store.SaveAgentProfileDraft("writer", valid, "token:admin")
	if err != nil {
		t.Fatalf("valid draft error = %v", err)
	}
	if saved.Version != 1 || saved.Status != AgentProfileStatusDraft || saved.Source != AgentProfileSourcePersisted || saved.PromptContractSHA256 != profileContractHash(saved.AgentProfile) {
		t.Fatalf("saved draft = %+v", saved)
	}
	if saved.Provenance == nil || saved.Provenance.Origin != AgentProfileOriginOperator || saved.Provenance.Actor != "token:admin" {
		t.Fatalf("draft provenance = %+v", saved.Provenance)
	}
	// Executor and model normalization: case-folded and de-duplicated.
	normalized, err := normalizeAgentProfileDraft("writer", AgentProfileDraftInput{Name: "W", Role: "r", AllowedExecutors: []string{" Model ", "model", "opencode"}, AllowedModels: []string{"a", "A", "b"}, PromptContract: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(normalized.AllowedExecutors, ",") != "model,opencode" || strings.Join(normalized.AllowedModels, ",") != "a,b" {
		t.Fatalf("normalized = %+v", normalized)
	}
}

func TestAgentProfileConcurrentEditsConflictOnExpectedVersion(t *testing.T) {
	store := NewStore(nil)
	if _, err := store.SaveAgentProfileDraft("writer", writerDraft(0, "v1"), "a"); err != nil {
		t.Fatal(err)
	}
	// A second editor who saw the pre-save state loses.
	if _, err := store.SaveAgentProfileDraft("writer", writerDraft(0, "stale"), "b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected_version error = %v, want ErrConflict", err)
	}
	if _, err := store.SaveAgentProfileDraft("writer", writerDraft(2, "ahead"), "b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("future expected_version error = %v, want ErrConflict", err)
	}
	// Racing saves with the same base: exactly one wins.
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.SaveAgentProfileDraft("writer", writerDraft(1, "race"), "racer")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected race error %v", err)
		}
	}
	if wins != 1 || conflicts != 7 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	identity, err := store.AgentProfileIdentity("writer")
	if err != nil {
		t.Fatal(err)
	}
	if identity.LatestVersion != 2 || identity.DraftVersion != 2 || identity.ActiveVersion != 0 {
		t.Fatalf("identity = %+v", identity)
	}
	versions, err := store.ListAgentProfileVersions("writer")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Status != AgentProfileStatusSuperseded {
		t.Fatalf("versions = %+v", versions)
	}
	// Only drafts activate; a superseded draft and an unknown version are rejected.
	if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: 1}, "a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("activate superseded error = %v, want ErrConflict", err)
	}
	if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: 9}, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate missing error = %v, want ErrNotFound", err)
	}
	if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: 2}, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: 2}, "a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-activate error = %v, want ErrConflict", err)
	}
}

func TestAgentProfileActivationKeepsHistoryImmutableAndInFlightRunsUnchanged(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewStore(func() time.Time { now = now.Add(time.Second); return now })
	if err := store.UpsertWorkflow(Workflow{ID: "profile-workflow", Kind: "profile-workflow", Name: "Profile Workflow"}); err != nil {
		t.Fatal(err)
	}
	executor := &profileRecordingExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"profile-workflow": executor})
	runner.pipelines = StaticPipelineRegistry{"profile-workflow": {ID: "profile-pipeline", WorkflowID: "profile-workflow", Steps: []PipelineStep{{ID: "write", Position: 1, Name: "profile.write", Executor: "model", Profile: "writer"}}}}

	v1, err := store.SaveAgentProfileDraft("writer", writerDraft(0, "contract v1"), "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: v1.Version, Reason: "first"}, "token:admin"); err != nil {
		t.Fatal(err)
	}
	queued, err := runner.Enqueue(CreateRunInput{WorkflowID: "profile-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if queued.PipelineSnapshot == nil || queued.PipelineSnapshot.Profiles["writer"].Version != 1 || queued.PipelineSnapshot.Profiles["writer"].PromptContract != "contract v1" {
		t.Fatalf("run snapshot did not capture the active profile: %+v", queued.PipelineSnapshot)
	}
	shapeVersion := pipelineVersion(*queued.PipelineSnapshot)
	withoutProfiles := clonePipeline(*queued.PipelineSnapshot)
	withoutProfiles.Profiles = nil
	if pipelineVersion(withoutProfiles) != shapeVersion {
		t.Fatal("pipeline_version must exclude the profile snapshot")
	}

	v2, err := store.SaveAgentProfileDraft("writer", writerDraft(1, "contract v2"), "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: v2.Version, Reason: "second"}, "token:admin"); err != nil {
		t.Fatal(err)
	}
	effective, ok := runner.effectiveAgentProfiles().Profile("writer")
	if !ok || effective.Version != 2 || effective.PromptContract != "contract v2" || effective.Status != AgentProfileStatusActive {
		t.Fatalf("effective profile = %+v", effective)
	}

	started, err := store.StartRun(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ExecuteRun(context.Background(), started); err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	if executor.contract != "contract v1" {
		t.Fatalf("in-flight run used contract %q, want the snapshot from before activation", executor.contract)
	}
	detail, err := store.RunDetail(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	correlation := detail.Steps[0].CorrelationIDs
	if correlation["agent_profile_version"] != "1" || correlation["agent_profile_source"] != AgentProfileSourcePersisted || correlation["agent_profile_contract_sha256"] != v1.PromptContractSHA256 || correlation["agent_profile_origin"] != AgentProfileOriginOperator {
		t.Fatalf("step correlation = %+v", correlation)
	}

	// A new run picks up version 2.
	fresh, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "profile-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if executor.contract != "contract v2" {
		t.Fatalf("new run contract = %q", executor.contract)
	}
	freshDetail, _ := store.RunDetail(fresh.ID)
	if freshDetail.Steps[0].CorrelationIDs["agent_profile_version"] != "2" {
		t.Fatalf("fresh correlation = %+v", freshDetail.Steps[0].CorrelationIDs)
	}

	// Historical bodies are immutable; only lifecycle status changed.
	versions, err := store.ListAgentProfileVersions("writer")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[1].Version != 1 || versions[1].PromptContract != "contract v1" || versions[1].Status != AgentProfileStatusSuperseded || versions[1].ActivatedAt == nil || versions[1].PromptContractSHA256 != v1.PromptContractSHA256 || versions[1].CreatedAt != v1.CreatedAt {
		t.Fatalf("history = %+v", versions)
	}
	// Mutating a returned copy must not leak into the store.
	versions[1].AllowedExecutors[0] = "command"
	again, _ := store.AgentProfileVersion("writer", 1)
	if again.AllowedExecutors[0] != "model" {
		t.Fatal("stored version body was mutated through a returned copy")
	}

	audit, err := store.ListAgentProfileAudit("writer", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 4 || audit[0].Action != AgentProfileAuditActivated || audit[0].Version != 2 || audit[0].PreviousActiveVersion != 1 || audit[0].Reason != "second" || audit[0].Actor != "token:admin" || audit[3].Action != AgentProfileAuditDraftSaved {
		t.Fatalf("audit = %+v", audit)
	}
}

func TestAgentProfileRollbackRestoresHistoricalBodyAsNewVersion(t *testing.T) {
	store := NewStore(nil)
	for i, contract := range []string{"v1", "v2"} {
		if _, err := store.SaveAgentProfileDraft("writer", writerDraft(i, contract), "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ActivateAgentProfileVersion("writer", AgentProfileActivationInput{Version: i + 1}, "a"); err != nil {
			t.Fatal(err)
		}
	}
	// Leave an unactivated draft in place; rollback must not touch it.
	if _, err := store.SaveAgentProfileDraft("writer", writerDraft(2, "draft v3"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RollbackAgentProfile("writer", AgentProfileActivationInput{Version: 3}, "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rollback to never-activated draft error = %v, want ErrInvalid", err)
	}
	if _, err := store.RollbackAgentProfile("writer", AgentProfileActivationInput{Version: 2}, "a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rollback to current active error = %v, want ErrConflict", err)
	}
	restored, err := store.RollbackAgentProfile("writer", AgentProfileActivationInput{Version: 1, Reason: "regression"}, "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 4 || restored.Status != AgentProfileStatusActive || restored.PromptContract != "v1" || restored.Provenance == nil || restored.Provenance.Origin != AgentProfileOriginRollback || restored.Provenance.RolledBackFromVersion != 1 || restored.ActivatedAt == nil {
		t.Fatalf("restored = %+v", restored)
	}
	identity, _ := store.AgentProfileIdentity("writer")
	if identity.ActiveVersion != 4 || identity.LatestVersion != 4 || identity.DraftVersion != 3 {
		t.Fatalf("identity after rollback = %+v", identity)
	}
	original, _ := store.AgentProfileVersion("writer", 1)
	previous, _ := store.AgentProfileVersion("writer", 2)
	draft, _ := store.AgentProfileVersion("writer", 3)
	if original.Status != AgentProfileStatusSuperseded || previous.Status != AgentProfileStatusSuperseded || draft.Status != AgentProfileStatusDraft {
		t.Fatalf("statuses = %s %s %s", original.Status, previous.Status, draft.Status)
	}
	active, err := store.ActiveAgentProfile("writer")
	if err != nil || active.Version != 4 {
		t.Fatalf("active = %+v err=%v", active, err)
	}
	audit, _ := store.ListAgentProfileAudit("writer", 1)
	if len(audit) != 1 || audit[0].Action != AgentProfileAuditRolledBack || audit[0].PreviousActiveVersion != 2 || audit[0].Reason != "regression" {
		t.Fatalf("audit = %+v", audit)
	}
}

func TestAgentProfileStaticDefaultsRemainBackwardCompatible(t *testing.T) {
	store := NewStore(nil)
	registry := NewStoreBackedAgentProfileRegistry(store, DefaultAgentProfileRegistry())
	static, ok := registry.Profile("content-writer")
	if !ok || static.Version != 0 || static.Source != AgentProfileSourceStatic || static.Status != AgentProfileStatusStatic || static.PromptContractSHA256 == "" {
		t.Fatalf("static profile = %+v", static)
	}
	if got := len(registry.ListProfiles()); got != 4 {
		t.Fatalf("static list size = %d", got)
	}
	// A run snapshot created before profile snapshots existed still resolves.
	if err := store.UpsertWorkflow(Workflow{ID: "legacy", Kind: "legacy", Name: "Legacy"}); err != nil {
		t.Fatal(err)
	}
	executor := &profileRecordingExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"legacy": executor})
	legacy := Pipeline{ID: "legacy-pipeline", WorkflowID: "legacy", Steps: []PipelineStep{{ID: "write", Position: 1, Name: "legacy.write", Executor: "model", Profile: "content-writer"}}}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: "legacy", PipelineSnapshot: &legacy, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if run.PipelineSnapshot.Profiles != nil {
		t.Fatal("legacy snapshot must stay without profiles")
	}
	started, _ := store.StartRun(run.ID)
	if _, err := runner.ExecuteRun(context.Background(), started); err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	if executor.contract != static.PromptContract {
		t.Fatalf("legacy run contract = %q", executor.contract)
	}

	// Persisting a version of a shipped profile records static provenance and
	// replaces the default only after activation.
	draft, err := store.SaveAgentProfileDraft("content-writer", AgentProfileDraftInput{Name: "Content Writer", Role: "writer", DefaultModel: "gpt-5.4", AllowedExecutors: []string{"model"}, PromptContract: "Persisted contract."}, "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Provenance.Origin != AgentProfileOriginStaticDefault || draft.Provenance.StaticContractSHA256 != static.PromptContractSHA256 {
		t.Fatalf("draft provenance = %+v", draft.Provenance)
	}
	if still, _ := registry.Profile("content-writer"); still.Source != AgentProfileSourceStatic {
		t.Fatalf("draft must not serve: %+v", still)
	}
	if _, err := store.ActivateAgentProfileVersion("content-writer", AgentProfileActivationInput{Version: 1}, "token:admin"); err != nil {
		t.Fatal(err)
	}
	if now, _ := registry.Profile("content-writer"); now.Source != AgentProfileSourcePersisted || now.PromptContract != "Persisted contract." {
		t.Fatalf("persisted profile did not replace the default: %+v", now)
	}
	if got := len(registry.ListProfiles()); got != 4 {
		t.Fatalf("list must merge persisted and static without duplicates, got %d", got)
	}
	identity, _ := store.AgentProfileIdentity("content-writer")
	if !identity.StaticDefault {
		t.Fatalf("identity = %+v", identity)
	}

	// Workflow definitions accept static, persisted-active, and reject draft-only profiles.
	definition := WorkflowDefinition{ID: "profile-definition", Name: "Profile Definition", Pipeline: PipelineDefinition{ID: "p", WorkflowID: "profile-definition", Name: "P", Steps: []PipelineStepDefinition{{ID: "step", Position: 1, Name: "step", Executor: "model", Profile: "operator-reviewer"}}}}
	if _, err := store.UpsertWorkflowDefinition(definition); err != nil {
		t.Fatalf("static reference error = %v", err)
	}
	definition.Pipeline.Steps[0].Profile = "brand-new"
	if _, err := store.UpsertWorkflowDefinition(definition); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown persisted profile error = %v", err)
	}
	if _, err := store.SaveAgentProfileDraft("brand-new", writerDraft(0, "new"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(definition); !errors.Is(err, ErrInvalid) {
		t.Fatalf("draft-only profile must not be referenceable, got %v", err)
	}
	if _, err := store.ActivateAgentProfileVersion("brand-new", AgentProfileActivationInput{Version: 1}, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(definition); err != nil {
		t.Fatalf("active persisted reference error = %v", err)
	}
}

func TestAgentProfileActivationRejectsBreakingCurrentConsumers(t *testing.T) {
	store := NewStore(nil)
	// devlog-summarizer is used by the shipped devlog pipeline with the model executor.
	if _, err := store.SaveAgentProfileDraft("devlog-summarizer", AgentProfileDraftInput{Name: "Devlog", Role: "summarizer", AllowedExecutors: []string{"opencode"}, PromptContract: "narrow"}, "a"); err != nil {
		t.Fatal(err)
	}
	target, _ := store.AgentProfileVersion("devlog-summarizer", 1)
	if err := validateAgentProfileConsumers(store, target.AgentProfile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("narrowing away the model executor error = %v, want ErrInvalid", err)
	}
	// An active workflow definition pins a model; a closed allowed_models list without it is rejected.
	if _, err := store.SaveAgentProfileDraft("pinned", writerDraft(0, "c"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion("pinned", AgentProfileActivationInput{Version: 1}, "a"); err != nil {
		t.Fatal(err)
	}
	definition := WorkflowDefinition{ID: "pinned-definition", Name: "Pinned", Status: "active", Pipeline: PipelineDefinition{ID: "p", WorkflowID: "pinned-definition", Name: "P", Steps: []PipelineStepDefinition{{ID: "step", Position: 1, Name: "step", Executor: "model", Profile: "pinned", Model: "profile-default"}}}}
	if _, err := store.UpsertWorkflowDefinition(definition); err != nil {
		t.Fatal(err)
	}
	narrowed := writerDraft(1, "c2")
	narrowed.DefaultModel, narrowed.AllowedModels = "other", []string{"other"}
	if _, err := store.SaveAgentProfileDraft("pinned", narrowed, "a"); err != nil {
		t.Fatal(err)
	}
	next, _ := store.AgentProfileVersion("pinned", 2)
	if err := validateAgentProfileConsumers(store, next.AgentProfile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("narrowing away a pinned model error = %v, want ErrInvalid", err)
	}
	compatible := writerDraft(2, "c3")
	compatible.AllowedModels = []string{"profile-default", "other"}
	if _, err := store.SaveAgentProfileDraft("pinned", compatible, "a"); err != nil {
		t.Fatal(err)
	}
	wider, _ := store.AgentProfileVersion("pinned", 3)
	if err := validateAgentProfileConsumers(store, wider.AgentProfile); err != nil {
		t.Fatalf("widening error = %v", err)
	}
}

func TestAttachPipelineProfileSnapshotClonesAndSkipsUnknown(t *testing.T) {
	pipeline := Pipeline{Steps: []PipelineStep{{ID: "a", Profile: "content-writer"}, {ID: "b", Profile: "missing"}, {ID: "c"}}}
	attachPipelineProfileSnapshot(&pipeline, DefaultAgentProfileRegistry())
	if len(pipeline.Profiles) != 1 || pipeline.Profiles["content-writer"].ID != "content-writer" {
		t.Fatalf("profiles = %+v", pipeline.Profiles)
	}
	cloned := clonePipeline(pipeline)
	cloned.Profiles["content-writer"].AllowedExecutors[0] = "command"
	if pipeline.Profiles["content-writer"].AllowedExecutors[0] != "model" {
		t.Fatal("clonePipeline must deep-copy profile bodies")
	}
	attachPipelineProfileSnapshot(nil, DefaultAgentProfileRegistry())
	attachPipelineProfileSnapshot(&pipeline, nil)
}
