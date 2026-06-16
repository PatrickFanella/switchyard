# Workflow Creator V1 Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Build a native Switchyard workflow creator where users can define custom ordered pipelines with timers, prompts, steps, and data shapes; Changemaker becomes one reusable workflow template instead of an external service boundary.

**Architecture:** Add persisted workflow definitions and pipeline definitions beside the existing static registry, then make the runtime resolve persisted pipelines before static built-ins. V1 stays linear and safe: fixed executor types, JSON-schema-like input/output shapes stored as JSON, model/prompt steps through the existing `ModelBroker`, and cron/manual triggers through the existing scheduler/trigger model. Changemaker is implemented as a seed/template using the same user-authored pipeline machinery.

**Tech Stack:** Go control plane, Postgres migrations, in-memory store parity, Vite React TypeScript HUD, existing Switchyard `Run`, `RunStep`, `Trigger`, `Artifact`, scheduler, worker, `ModelBroker`, and pipeline executor abstractions.

---

## Product Shape

Workflow Creator V1 supports:

- Custom workflows with metadata: `id`, `name`, `description`, `status`, `owns`, `uses`.
- Custom linear pipelines with ordered steps.
- Step executor types in V1: `internal`, `model`, `http`, `command`.
- Prompt/model steps with templated prompt input.
- Data shape definitions as JSON objects attached to workflow inputs and step outputs.
- Manual and cron triggers for custom workflows.
- Changemaker as a seeded template called `changemaker-daily-digest`.

Workflow Creator V1 explicitly does **not** support:

- Branching/DAG execution.
- Arbitrary plugin code.
- User-defined Go functions.
- Unrestricted shell commands from the UI.
- Deleting built-in Open Pilot/Devlog workflows.

---

## File Structure

### Backend domain and storage

- Modify: `backend/internal/controlplane/pipeline.go`
  - Add persisted pipeline resolver interfaces and a `PipelineSource` marker.
- Modify: `backend/internal/controlplane/types.go`
  - Add `WorkflowDefinition`, `PipelineDefinition`, `PipelineStepDefinition`, and shape structs.
- Modify: `backend/internal/controlplane/store.go`
  - Add MemoryStore CRUD for workflow definitions and pipeline definitions.
- Modify: `backend/internal/controlplane/postgres_store.go`
  - Add Postgres CRUD for definitions.
- Create: `backend/internal/controlplane/migrations/008_workflow_creator.sql`
  - Add definition tables.
- Modify: `backend/internal/controlplane/migrate_test.go`
  - Cover migration and Postgres CRUD.

### Backend API and execution

- Modify: `backend/internal/controlplane/http.go`
  - Add workflow creator CRUD endpoints.
- Modify: `backend/internal/controlplane/executor.go`
  - Add generic execution for persisted pipeline steps.
- Modify: `backend/internal/controlplane/registry.go`
  - Seed Changemaker template as a persisted definition when absent.
- Modify: `backend/internal/controlplane/scheduler.go`
  - Reuse existing cron triggers for persisted workflows; no scheduler rewrite.

### Tests

- Modify: `backend/internal/controlplane/store_test.go`
  - MemoryStore CRUD tests.
- Modify: `backend/internal/controlplane/http_test.go`
  - API tests for create/update/list workflow definitions and triggers.
- Create: `backend/internal/controlplane/workflow_creator_internal_test.go`
  - End-to-end tests for custom pipeline execution and Changemaker template.

### UI and docs

- Modify: `web/src/App.tsx`
  - Add a minimal Workflow Creator panel: list definitions, create from Changemaker template, run manual trigger.
- Modify: `web/src/App.test.tsx`
  - Smoke test creator rendering.
- Modify: `docs/openapi.yaml`
  - Document new endpoints and schemas.
- Modify: `README.md`
  - Document workflow creator and Changemaker-native direction.
- Modify: `.env.example`
  - Remove need for `CHANGEMAKER_BASE_URL` once native template is implemented; keep legacy env as optional compatibility.

---

### Task 1: Persist workflow and pipeline definitions

**Files:**
- Modify: `backend/internal/controlplane/types.go`
- Modify: `backend/internal/controlplane/store.go`
- Modify: `backend/internal/controlplane/postgres_store.go`
- Create: `backend/internal/controlplane/migrations/008_workflow_creator.sql`
- Modify: `backend/internal/controlplane/store_test.go`
- Modify: `backend/internal/controlplane/migrate_test.go`

- [ ] **Step 1: Add failing MemoryStore CRUD test**

Add this test to `backend/internal/controlplane/store_test.go`:

```go
func TestStoreWorkflowDefinitionsCRUD(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	definition := WorkflowDefinition{
		ID:          "daily-digest",
		Name:        "Daily Digest",
		Description: "Generate a daily digest from a prompt.",
		Status:      "draft",
		InputShape:  JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"topic": {Type: "string", Description: "Digest topic"}}, Required: []string{"topic"}},
		Pipeline: PipelineDefinition{
			ID:         "daily-digest-pipeline",
			WorkflowID: "daily-digest",
			Name:       "Daily Digest Pipeline",
			Steps: []PipelineStepDefinition{
				{ID: "build-prompt", Position: 1, Name: "digest.build-prompt", Executor: "internal", Inputs: map[string]string{"template": "Write about {{topic}}"}},
				{ID: "generate", Position: 2, Name: "digest.generate", Executor: "model", Profile: "content-writer", Model: "${model}"},
			},
		},
	}

	created, err := store.UpsertWorkflowDefinition(definition)
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}
	if created.ID != "daily-digest" || created.Pipeline.Steps[1].Executor != "model" {
		t.Fatalf("created = %+v", created)
	}

	fetched, err := store.WorkflowDefinition("daily-digest")
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if fetched.InputShape.Properties["topic"].Type != "string" {
		t.Fatalf("fetched = %+v", fetched)
	}

	list, err := store.ListWorkflowDefinitions()
	if err != nil {
		t.Fatalf("ListWorkflowDefinitions() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != "daily-digest" {
		t.Fatalf("list = %+v", list)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
cd backend && go test ./internal/controlplane -run TestStoreWorkflowDefinitionsCRUD -v
```

Expected: compile failure for undefined `WorkflowDefinition`, `JSONShape`, and store methods.

- [ ] **Step 3: Add domain types**

Add these types to `backend/internal/controlplane/types.go`:

```go
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
	Owns        []string           `json:"owns,omitempty"`
	Uses        []string           `json:"uses,omitempty"`
	InputShape  JSONShape          `json:"input_shape,omitempty"`
	Pipeline    PipelineDefinition `json:"pipeline"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
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
```

- [ ] **Step 4: Extend Store interface and MemoryStore**

Add to `Store` in `backend/internal/controlplane/store.go`:

```go
	UpsertWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error)
	WorkflowDefinition(id string) (WorkflowDefinition, error)
	ListWorkflowDefinitions() ([]WorkflowDefinition, error)
```

Add a field to `MemoryStore`:

```go
	workflowDefinitions map[string]WorkflowDefinition
```

Initialize it in `NewStore`:

```go
		workflowDefinitions: map[string]WorkflowDefinition{},
```

Implement methods:

```go
func (s *MemoryStore) UpsertWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	definition.ID = strings.TrimSpace(definition.ID)
	if definition.ID == "" || strings.TrimSpace(definition.Name) == "" || len(definition.Pipeline.Steps) == 0 {
		return WorkflowDefinition{}, ErrInvalid
	}
	now := s.now()
	if existing, ok := s.workflowDefinitions[definition.ID]; ok {
		definition.CreatedAt = existing.CreatedAt
	} else {
		definition.CreatedAt = now
	}
	definition.UpdatedAt = now
	s.workflowDefinitions[definition.ID] = cloneWorkflowDefinition(definition)
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) WorkflowDefinition(id string) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) ListWorkflowDefinitions() ([]WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkflowDefinition, 0, len(s.workflowDefinitions))
	for _, definition := range s.workflowDefinitions {
		out = append(out, cloneWorkflowDefinition(definition))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
```

Add a clone helper in `store.go`:

```go
func cloneWorkflowDefinition(definition WorkflowDefinition) WorkflowDefinition {
	definition.Owns = append([]string(nil), definition.Owns...)
	definition.Uses = append([]string(nil), definition.Uses...)
	definition.InputShape = cloneJSONShape(definition.InputShape)
	definition.Pipeline.Steps = append([]PipelineStepDefinition(nil), definition.Pipeline.Steps...)
	for i := range definition.Pipeline.Steps {
		definition.Pipeline.Steps[i].Inputs = cloneStringMap(definition.Pipeline.Steps[i].Inputs)
		definition.Pipeline.Steps[i].OutputShape = cloneJSONShape(definition.Pipeline.Steps[i].OutputShape)
	}
	return definition
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
```

- [ ] **Step 5: Add Postgres migration**

Create `backend/internal/controlplane/migrations/008_workflow_creator.sql`:

```sql
CREATE TABLE IF NOT EXISTS workflow_definitions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'draft',
  owns JSONB NOT NULL DEFAULT '[]'::jsonb,
  uses JSONB NOT NULL DEFAULT '[]'::jsonb,
  input_shape JSONB NOT NULL DEFAULT '{}'::jsonb,
  pipeline JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS workflow_definitions_status_idx ON workflow_definitions(status);
```

- [ ] **Step 6: Implement Postgres CRUD**

Add methods to `backend/internal/controlplane/postgres_store.go` using `json.Marshal`/`json.Unmarshal`. Use this SQL for upsert:

```sql
INSERT INTO workflow_definitions (id, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
ON CONFLICT (id) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  status = EXCLUDED.status,
  owns = EXCLUDED.owns,
  uses = EXCLUDED.uses,
  input_shape = EXCLUDED.input_shape,
  pipeline = EXCLUDED.pipeline,
  updated_at = now()
RETURNING id, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at
```

- [ ] **Step 7: Add migration test**

In `backend/internal/controlplane/migrate_test.go`, extend the migration assertion to check that `workflow_definitions` exists after running migrations:

```go
var exists bool
if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'workflow_definitions')`).Scan(&exists); err != nil {
	t.Fatalf("check workflow_definitions table: %v", err)
}
if !exists {
	t.Fatal("workflow_definitions table was not created")
}
```

- [ ] **Step 8: Run tests**

Run:

```bash
cd backend && go test ./internal/controlplane
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/controlplane/types.go backend/internal/controlplane/store.go backend/internal/controlplane/postgres_store.go backend/internal/controlplane/store_test.go backend/internal/controlplane/migrate_test.go backend/internal/controlplane/migrations/008_workflow_creator.sql
git commit -m "feat: persist workflow creator definitions"
```

---

### Task 2: Resolve persisted pipelines before static pipelines

**Files:**
- Modify: `backend/internal/controlplane/pipeline.go`
- Modify: `backend/internal/controlplane/executor.go`
- Create: `backend/internal/controlplane/workflow_creator_internal_test.go`

- [ ] **Step 1: Write failing resolver test**

Create `backend/internal/controlplane/workflow_creator_internal_test.go`:

```go
package controlplane

import (
	"testing"
	"time"
)

func TestStoreBackedPipelineRegistryPrefersWorkflowDefinition(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	_, err := store.UpsertWorkflowDefinition(WorkflowDefinition{
		ID:     "custom-digest",
		Name:   "Custom Digest",
		Status: "active",
		Pipeline: PipelineDefinition{ID: "custom-digest-pipeline", WorkflowID: "custom-digest", Name: "Custom Digest", Steps: []PipelineStepDefinition{
			{ID: "generate", Position: 1, Name: "custom.generate", Executor: "model", Profile: "content-writer", Model: "${model}"},
		}},
	})
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}

	registry := NewStoreBackedPipelineRegistry(store, DefaultPipelineRegistry())
	pipeline, ok := registry.PipelineForWorkflow("custom-digest")
	if !ok {
		t.Fatal("expected custom pipeline")
	}
	if pipeline.ID != "custom-digest-pipeline" || pipeline.Steps[0].Name != "custom.generate" {
		t.Fatalf("pipeline = %+v", pipeline)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

```bash
cd backend && go test ./internal/controlplane -run TestStoreBackedPipelineRegistryPrefersWorkflowDefinition -v
```

Expected: compile failure for `NewStoreBackedPipelineRegistry`.

- [ ] **Step 3: Add store-backed registry**

Add to `backend/internal/controlplane/pipeline.go`:

```go
type StoreBackedPipelineRegistry struct {
	store    Store
	fallback PipelineRegistry
}

func NewStoreBackedPipelineRegistry(store Store, fallback PipelineRegistry) StoreBackedPipelineRegistry {
	return StoreBackedPipelineRegistry{store: store, fallback: fallback}
}

func (r StoreBackedPipelineRegistry) PipelineForWorkflow(workflowID string) (Pipeline, bool) {
	if r.store != nil {
		definition, err := r.store.WorkflowDefinition(workflowID)
		if err == nil && strings.EqualFold(definition.Status, "active") {
			return pipelineFromDefinition(definition), true
		}
	}
	return r.fallback.PipelineForWorkflow(workflowID)
}

func (r StoreBackedPipelineRegistry) ListPipelines() []Pipeline {
	seen := map[string]bool{}
	out := []Pipeline{}
	if r.store != nil {
		definitions, err := r.store.ListWorkflowDefinitions()
		if err == nil {
			for _, definition := range definitions {
				if strings.EqualFold(definition.Status, "active") {
					pipeline := pipelineFromDefinition(definition)
					out = append(out, pipeline)
					seen[pipeline.WorkflowID] = true
				}
			}
		}
	}
	for _, pipeline := range r.fallback.ListPipelines() {
		if !seen[pipeline.WorkflowID] {
			out = append(out, pipeline)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func pipelineFromDefinition(definition WorkflowDefinition) Pipeline {
	steps := make([]PipelineStep, 0, len(definition.Pipeline.Steps))
	for _, step := range definition.Pipeline.Steps {
		steps = append(steps, PipelineStep{ID: step.ID, Position: step.Position, Name: step.Name, Executor: step.Executor, Profile: step.Profile, Model: step.Model, Inputs: cloneStringMap(step.Inputs)})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	return Pipeline{ID: definition.Pipeline.ID, WorkflowID: definition.ID, Name: definition.Pipeline.Name, Description: definition.Pipeline.Description, Steps: steps}
}
```

Add `strings` to the `pipeline.go` import list.

- [ ] **Step 4: Wire runner to store-backed registry**

In the runner constructor path in `backend/internal/controlplane/executor.go`, replace direct `DefaultPipelineRegistry()` usage with:

```go
registry := NewStoreBackedPipelineRegistry(store, DefaultPipelineRegistry())
```

Use this registry wherever the runner resolves pipelines.

- [ ] **Step 5: Run tests**

```bash
cd backend && go test ./internal/controlplane
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/controlplane/pipeline.go backend/internal/controlplane/executor.go backend/internal/controlplane/workflow_creator_internal_test.go
git commit -m "feat: resolve workflow creator pipelines"
```

---

### Task 3: Add workflow creator HTTP API

**Files:**
- Modify: `backend/internal/controlplane/http.go`
- Modify: `backend/internal/controlplane/http_test.go`
- Modify: `docs/openapi.yaml`

- [ ] **Step 1: Add failing API test**

Add to `backend/internal/controlplane/http_test.go`:

```go
func TestAPIWorkflowDefinitionCRUD(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	server := newTestServer(store)

	body := `{"id":"custom-digest","name":"Custom Digest","description":"Daily custom digest","status":"active","input_shape":{"type":"object","properties":{"topic":{"type":"string"}},"required":["topic"]},"pipeline":{"id":"custom-digest-pipeline","workflow_id":"custom-digest","name":"Custom Digest","steps":[{"id":"generate","position":1,"name":"custom.generate","executor":"model","profile":"content-writer","model":"${model}"}]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/workflow-definitions/custom-digest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/workflow-definitions", nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "custom-digest") {
		t.Fatalf("GET status = %d body = %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

```bash
cd backend && go test ./internal/controlplane -run TestAPIWorkflowDefinitionCRUD -v
```

Expected: 404 for `/api/workflow-definitions`.

- [ ] **Step 3: Add handlers**

In `backend/internal/controlplane/http.go`, register:

```go
router.HandleFunc("GET /api/workflow-definitions", h.listWorkflowDefinitions)
router.HandleFunc("PUT /api/workflow-definitions/{id}", h.upsertWorkflowDefinition)
router.HandleFunc("GET /api/workflow-definitions/{id}", h.getWorkflowDefinition)
```

Implement:

```go
func (h *HTTPHandler) listWorkflowDefinitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := h.store.ListWorkflowDefinitions()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definitions": definitions})
}

func (h *HTTPHandler) getWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	definition, err := h.store.WorkflowDefinition(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": definition})
}

func (h *HTTPHandler) upsertWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	var definition WorkflowDefinition
	if err := json.NewDecoder(r.Body).Decode(&definition); err != nil {
		writeError(w, ErrInvalid)
		return
	}
	definition.ID = r.PathValue("id")
	created, err := h.store.UpsertWorkflowDefinition(definition)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": created})
}
```

- [ ] **Step 4: Document OpenAPI schemas**

Add to `docs/openapi.yaml`:

```yaml
  /api/workflow-definitions:
    get:
      summary: List workflow creator definitions
      responses:
        '200':
          description: Workflow definitions
  /api/workflow-definitions/{id}:
    get:
      summary: Get a workflow creator definition
    put:
      summary: Create or update a workflow creator definition
```

Add schemas for `WorkflowDefinition`, `PipelineDefinition`, `PipelineStepDefinition`, and `JSONShape` matching the Go types.

- [ ] **Step 5: Run tests**

```bash
cd backend && go test ./internal/controlplane
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/controlplane/http.go backend/internal/controlplane/http_test.go docs/openapi.yaml
git commit -m "feat: add workflow creator api"
```

---

### Task 4: Execute generic persisted pipeline steps

**Files:**
- Modify: `backend/internal/controlplane/executor.go`
- Modify: `backend/internal/controlplane/workflow_creator_internal_test.go`

- [ ] **Step 1: Add failing execution test**

Add to `backend/internal/controlplane/workflow_creator_internal_test.go`:

```go
func TestRunnerExecutesCustomWorkflowModelStep(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"message\":{\"content\":\"draft body\"}}]}\n\n"))
	}))
	t.Cleanup(model.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", model.URL)
	t.Setenv("DEVLOG_MODEL", "gpt-test")

	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	_, err := store.UpsertWorkflowDefinition(WorkflowDefinition{
		ID:     "custom-digest",
		Name:   "Custom Digest",
		Status: "active",
		Pipeline: PipelineDefinition{ID: "custom-digest-pipeline", WorkflowID: "custom-digest", Name: "Custom Digest", Steps: []PipelineStepDefinition{
			{ID: "generate", Position: 1, Name: "custom.generate", Executor: "model", Profile: "content-writer", Model: "${model}", Inputs: map[string]string{"prompt": "Write a digest about {{topic}}"}},
		}},
	})
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}

	runner := NewRunner(store)
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "custom-digest", Inputs: map[string]string{"topic": "Switchyard"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != RunSucceeded || len(detail.Steps) != 1 || detail.Steps[0].OutputSummary != "draft body" {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

```bash
cd backend && go test ./internal/controlplane -run TestRunnerExecutesCustomWorkflowModelStep -v
```

Expected: failure because generic custom workflow steps are not handled.

- [ ] **Step 3: Add generic step execution path**

In `executor.go`, add a fallback for workflow definitions when no specialized executor exists. The generic executor should support:

```go
switch step.Executor {
case "internal":
	// attach inputs as JSON artifact or render template into state
case "model":
	// render step.Inputs["prompt"] with run inputs, call ModelBroker.Generate, attach response artifact
case "http":
	// V1: disabled unless step input allow_http=true; otherwise fail with ErrInvalid
case "command":
	// Reuse command policy; no shell expansion
default:
	return PipelineStepExecution{}, ErrInvalid
}
```

Add a helper:

```go
func renderWorkflowTemplate(template string, inputs map[string]string) string {
	output := template
	for key, value := range inputs {
		output = strings.ReplaceAll(output, "{{"+key+"}}", value)
	}
	return output
}
```

For `model`, create a `ModelRequest`:

```go
request := ModelRequest{
	Model:  resolveWorkflowModel(run, step.Model),
	Prompt: renderWorkflowTemplate(step.Inputs["prompt"], run.Inputs),
}
```

Return a `PipelineStepExecution` with:

```go
Summary: response.Content,
Artifacts: []CreateArtifactInput{{Name: step.ID + ".md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(response.Content)}},
CorrelationIDs: response.CorrelationIDs,
```

- [ ] **Step 4: Run tests**

```bash
cd backend && go test ./internal/controlplane
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/controlplane/executor.go backend/internal/controlplane/workflow_creator_internal_test.go
git commit -m "feat: execute custom workflow pipelines"
```

---

### Task 5: Seed Changemaker as a workflow creator template

**Files:**
- Modify: `backend/internal/controlplane/registry.go`
- Modify: `backend/internal/controlplane/workflow_creator_internal_test.go`
- Modify: `README.md`

- [ ] **Step 1: Add failing template seed test**

Add to `workflow_creator_internal_test.go`:

```go
func TestSeedDefaultsCreatesChangemakerWorkflowTemplate(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	definition, err := store.WorkflowDefinition("changemaker-daily-digest")
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if definition.Status != "active" || definition.Pipeline.Steps[0].Executor != "internal" || definition.Pipeline.Steps[1].Executor != "model" {
		t.Fatalf("definition = %+v", definition)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

```bash
cd backend && go test ./internal/controlplane -run TestSeedDefaultsCreatesChangemakerWorkflowTemplate -v
```

Expected: `ErrNotFound`.

- [ ] **Step 3: Seed the template**

In `registry.go`, after workflows/triggers are seeded, upsert:

```go
changemakerTemplate := WorkflowDefinition{
	ID:          "changemaker-daily-digest",
	Name:        "Changemaker Daily Digest",
	Description: "Generate a timed narrative digest from a configurable prompt.",
	Status:      "active",
	Owns:        []string{"prompt", "draft artifact", "timer"},
	Uses:        []string{"model-broker"},
	InputShape: JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{
		"topic":  {Type: "string", Description: "Digest topic or scope"},
		"prompt": {Type: "string", Description: "Writer prompt"},
	}, Required: []string{"topic", "prompt"}},
	Pipeline: PipelineDefinition{ID: "changemaker-daily-digest-pipeline", WorkflowID: "changemaker-daily-digest", Name: "Changemaker Daily Digest", Steps: []PipelineStepDefinition{
		{ID: "prepare", Position: 1, Name: "changemaker.prepare", Executor: "internal", Inputs: map[string]string{"artifact": "changemaker-input.json"}},
		{ID: "generate", Position: 2, Name: "changemaker.generate", Executor: "model", Profile: "content-writer", Model: "${model}", Inputs: map[string]string{"prompt": "{{prompt}}\n\nTopic: {{topic}}"}},
		{ID: "attach", Position: 3, Name: "changemaker.attach-draft", Executor: "internal", Inputs: map[string]string{"artifact": "changemaker-draft.md"}},
	}},
}
if _, err := store.UpsertWorkflowDefinition(changemakerTemplate); err != nil {
	return err
}
```

Add a trigger:

```go
{ID: "daily-changemaker-native", WorkflowID: "changemaker-daily-digest", Type: TriggerCron, Name: "Daily Changemaker Native", Description: "Native workflow-creator Changemaker template.", Enabled: false, Schedule: "0 9 * * *"}
```

- [ ] **Step 4: Document Changemaker direction**

Update `README.md` to state:

```markdown
Changemaker is now represented as a native workflow-creator template (`changemaker-daily-digest`). `CHANGEMAKER_BASE_URL` remains a legacy adapter fallback only; new scheduled content workflows should be authored as Switchyard workflow definitions.
```

- [ ] **Step 5: Run tests**

```bash
cd backend && go test ./internal/controlplane
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/controlplane/registry.go backend/internal/controlplane/workflow_creator_internal_test.go README.md
git commit -m "feat: seed changemaker workflow template"
```

---

### Task 6: Minimal Workflow Creator UI

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/App.test.tsx`

- [ ] **Step 1: Add failing UI smoke test**

In `web/src/App.test.tsx`, add an assertion that the app contains a Workflow Creator section after render:

```tsx
expect(document.body.textContent).toContain('Workflow Creator')
expect(document.body.textContent).toContain('Changemaker Daily Digest')
```

- [ ] **Step 2: Run test and verify it fails**

```bash
pnpm --dir web run test
```

Expected: FAIL because the UI does not render workflow definitions.

- [ ] **Step 3: Add API fetch type**

In `web/src/App.tsx`, add:

```ts
type WorkflowDefinition = {
  id: string;
  name: string;
  description: string;
  status: string;
  input_shape?: { type?: string; required?: string[] };
  pipeline: { id: string; name: string; steps: { id: string; position: number; name: string; executor: string; profile?: string; model?: string }[] };
};
```

Add state:

```ts
const [workflowDefinitions, setWorkflowDefinitions] = useState<WorkflowDefinition[]>([]);
```

In the dashboard load function, fetch:

```ts
fetchJSON<{ workflow_definitions: WorkflowDefinition[] }>('/api/workflow-definitions')
```

Set `workflowDefinitions` to the returned array.

- [ ] **Step 4: Render panel**

Add a HUD panel near workflow cards:

```tsx
<section className="hud-panel p-4">
  <p className="hud-label text-[hsl(var(--faint))]">Workflow Creator</p>
  <h2 className="mt-2 text-xl font-semibold text-[hsl(var(--ink))]">Custom pipelines</h2>
  <div className="mt-4 grid gap-3">
    {workflowDefinitions.map((definition) => (
      <article key={definition.id} className="border border-[hsl(var(--border))] p-3">
        <div className="flex items-center justify-between gap-3">
          <h3 className="font-semibold text-[hsl(var(--ink))]">{definition.name}</h3>
          <span className="hud-chip px-2 py-1 text-[0.62rem]">{definition.status}</span>
        </div>
        <p className="mt-2 text-sm text-[hsl(var(--muted))]">{definition.description}</p>
        <p className="mt-2 text-xs text-[hsl(var(--faint))]">{definition.pipeline.steps.length} steps · {definition.pipeline.steps.map((step) => step.executor).join(' → ')}</p>
      </article>
    ))}
  </div>
</section>
```

- [ ] **Step 5: Run UI tests**

```bash
pnpm --dir web run test
pnpm --dir web run lint
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/App.tsx web/src/App.test.tsx
git commit -m "feat: show workflow creator templates"
```

---

### Task 7: Full verification and deployment

**Files:**
- Modify: `README.md`
- Modify: `.env.example`
- Modify: `docker-compose.yml` only if env wiring changes are needed.

- [ ] **Step 1: Run full verification**

```bash
make verify
make switchyard-smoke
git diff --check
```

Expected: all pass.

- [ ] **Step 2: Build and deploy**

```bash
docker compose up -d --build
```

Expected: API, web, postgres, opencode/OCQ/llama-line compatibility services remain healthy while native Changemaker no longer requires `CHANGEMAKER_BASE_URL`.

- [ ] **Step 3: Post-deploy checks**

```bash
curl -fsS http://127.0.0.1:28080/healthz
curl -fsS http://127.0.0.1:28080/api/workflow-definitions
curl -fsS http://127.0.0.1:28080/api/pipelines
curl -fsS http://127.0.0.1:28080/api/triggers
```

Expected:

- `/healthz` returns `{"ok":true}`.
- `/api/workflow-definitions` includes `changemaker-daily-digest`.
- `/api/pipelines` includes the persisted Changemaker template pipeline.
- `/api/triggers` includes `daily-changemaker-native`.

- [ ] **Step 4: Commit docs/env updates**

```bash
git add README.md .env.example docker-compose.yml docs/openapi.yaml
git commit -m "docs: document workflow creator v1"
```

---

## Self-Review

### Spec coverage

- Timer: covered through persisted cron trigger seeding and existing scheduler reuse.
- Prompt: covered through model step `Inputs["prompt"]` and template rendering.
- Step: covered through persisted ordered `PipelineStepDefinition` records.
- Data shape: covered through `JSONShape` on workflow input and step output.
- Changemaker as one workflow in larger creator: covered by `changemaker-daily-digest` template using the same persisted pipeline machinery.
- Avoid special Changemaker service dependency: covered by native template and legacy fallback language.

### Placeholder scan

The plan intentionally avoids `TBD`, unspecified validation, and vague “write tests” steps. Each task includes concrete files, code snippets, commands, and expected results.

### Type consistency

The plan uses consistent names:

- `WorkflowDefinition`
- `PipelineDefinition`
- `PipelineStepDefinition`
- `JSONShape`
- `JSONShapeProperty`
- `NewStoreBackedPipelineRegistry`
- `changemaker-daily-digest`
