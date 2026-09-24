package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func intPtr(value int) *int { return &value }

// reportShape is a representative contract: a required integer count, a
// required JSON report object with a bounded item list, and an enum status.
func reportShape() JSONShape {
	return JSONShape{
		Type:     "object",
		Required: []string{"count", "report"},
		Properties: map[string]JSONShapeProperty{
			"count":  {Type: "integer"},
			"status": {Type: "string", Enum: []string{"ok", "degraded"}},
			"report": {Type: "object", Required: []string{"title", "items"}, Properties: map[string]JSONShapeProperty{
				"title": {Type: "string", MaxLength: intPtr(20)},
				"items": {Type: "array", MaxItems: intPtr(3), Items: &JSONShapeProperty{Type: "object", Required: []string{"id"}, Properties: map[string]JSONShapeProperty{"id": {Type: "integer"}, "ok": {Type: "boolean"}}}},
			}},
		},
	}
}

func TestOutputShapeProblemsAcceptsSubsetAndRejectsEverythingElse(t *testing.T) {
	if problems := outputShapeProblems(JSONShape{}); problems != nil {
		t.Fatalf("undeclared shape problems = %v", problems)
	}
	if problems := outputShapeProblems(reportShape()); problems != nil {
		t.Fatalf("valid shape problems = %v", problems)
	}
	deep := JSONShapeProperty{Type: "string"}
	for i := 0; i < maxOutputShapeDepth+1; i++ {
		deep = JSONShapeProperty{Type: "array", Items: &deep}
	}
	enum := make([]string, maxOutputShapeEnumValues+1)
	for i := range enum {
		enum[i] = strings.Repeat("v", i+1)
	}
	cases := map[string]struct {
		shape JSONShape
		want  string
	}{
		"top-level string":     {JSONShape{Type: "string"}, `output_shape.type must be "object"`},
		"description only":     {JSONShape{Description: "legacy"}, `output_shape.type must be "object"`},
		"unsupported type":     {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"x": {Type: "null"}}}, `output_shape.properties.x.type "null" is not supported`},
		"undeclared required":  {JSONShape{Type: "object", Required: []string{"missing"}}, `required lists "missing", which is not declared`},
		"invalid output name":  {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"Bad-Name": {Type: "string"}}}, "output_shape.properties.Bad-Name: output name must match"},
		"credential name":      {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"api_token": {Type: "string"}}}, "output_shape.properties.api_token"},
		"enum on number":       {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"n": {Type: "number", Enum: []string{"1"}}}}, "output_shape.properties.n.enum is supported only for type string"},
		"too many enum values": {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"s": {Type: "string", Enum: enum}}}, "enum has 65 values"},
		"maxLength on array":   {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"a": {Type: "array", MaxLength: intPtr(1)}}}, "output_shape.properties.a.maxLength is supported only for type string"},
		"negative maxItems":    {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"a": {Type: "array", MaxItems: intPtr(-1)}}}, "maxItems must be between 0"},
		"items on object":      {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"o": {Type: "object", Items: &JSONShapeProperty{Type: "string"}}}}, "output_shape.properties.o.items is supported only for type array"},
		"properties on string": {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"s": {Type: "string", Properties: map[string]JSONShapeProperty{"x": {Type: "string"}}}}}, "properties and required are supported only for type object"},
		"nested required":      {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"o": {Type: "object", Required: []string{"x"}}}}, `output_shape.properties.o.required lists "x"`},
		"too deep":             {JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"d": deep}}, "nesting exceeds 8 levels"},
	}
	for name, tc := range cases {
		problems := outputShapeProblems(tc.shape)
		if !strings.Contains(strings.Join(problems, "; "), tc.want) {
			t.Errorf("%s: problems = %v, want substring %q", name, problems, tc.want)
		}
	}
}

func TestEnforceStepOutputContractReportsActionablePaths(t *testing.T) {
	step := PipelineStep{ID: "report", OutputContract: stepOutputContractFromShape(reportShape())}
	valid := map[string]string{"count": "3", "status": "ok", "report": `{"title":"Weekly","items":[{"id":1,"ok":true},{"id":2.0}]}`, "extra": "undeclared outputs pass"}
	if err := enforceStepOutputContract(step, valid, valid); err != nil {
		t.Fatalf("valid outputs rejected: %v", err)
	}
	cases := map[string]struct {
		outputs map[string]string
		raw     map[string]string
		want    []string
	}{
		"missing required":      {map[string]string{"report": `{"title":"t","items":[]}`}, nil, []string{"$.count: required output is missing"}},
		"malformed json":        {map[string]string{"count": "1", "report": `{"title":`}, nil, []string{"$.report: expected object encoded as JSON text", "invalid JSON"}},
		"trailing data":         {map[string]string{"count": "1", "report": `{"title":"t","items":[]} {}`}, nil, []string{"unexpected data after the JSON value"}},
		"integer text":          {map[string]string{"count": "three", "report": `{"title":"t","items":[]}`}, nil, []string{`$.count: expected integer encoded as JSON text, got "three"`}},
		"non-integral":          {map[string]string{"count": "1.5", "report": `{"title":"t","items":[]}`}, nil, []string{"$.count: expected integer, got non-integral number 1.5"}},
		"enum":                  {map[string]string{"count": "1", "status": "broken", "report": `{"title":"t","items":[]}`}, nil, []string{`$.status: "broken" is not one of the allowed enum values`}},
		"nested type":           {map[string]string{"count": "1", "report": `{"title":7,"items":[{"id":"x"}]}`}, nil, []string{"$.report.title: expected string, got number", "$.report.items[0].id: expected integer, got string"}},
		"nested required":       {map[string]string{"count": "1", "report": `{"items":[{}]}`}, nil, []string{"$.report.title: required property is missing", "$.report.items[0].id: required property is missing"}},
		"maxLength":             {map[string]string{"count": "1", "report": `{"title":"` + strings.Repeat("é", 21) + `","items":[]}`}, nil, []string{"$.report.title: string has 21 characters; maxLength is 20"}},
		"maxItems":              {map[string]string{"count": "1", "report": `{"title":"t","items":[{"id":1},{"id":2},{"id":3},{"id":4}]}`}, nil, []string{"$.report.items: array has 4 items; maxItems is 3"}},
		"wrong container":       {map[string]string{"count": "1", "report": `[1]`}, nil, []string{"$.report: expected object, got array"}},
		"null item":             {map[string]string{"count": "1", "report": `{"title":"t","items":[null]}`}, nil, []string{"$.report.items[0]: expected object, got null"}},
		"oversized and omitted": {map[string]string{"count": "1"}, map[string]string{"count": "1", "report": strings.Repeat("x", defaultMaxStepOutputBytes+1)}, []string{"$.report: required output was omitted because its 65537-byte value exceeds the 65536-byte step output limit"}},
	}
	for name, tc := range cases {
		err := enforceStepOutputContract(step, tc.outputs, tc.raw)
		if !errors.Is(err, ErrStepOutputContract) {
			t.Errorf("%s: error = %v, want ErrStepOutputContract", name, err)
			continue
		}
		var classified interface{ Retryable() bool }
		if !errors.As(err, &classified) || classified.Retryable() {
			t.Errorf("%s: contract violation must be non-retryable", name)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error = %q, want substring %q", name, err.Error(), want)
			}
		}
		if !strings.Contains(err.Error(), `step "report" output violates its output contract (v1)`) {
			t.Errorf("%s: error lacks step identity: %q", name, err.Error())
		}
	}

	booleans := PipelineStep{ID: "flag", OutputContract: stepOutputContractFromShape(JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"flag": {Type: "boolean"}, "ratio": {Type: "number"}}})}
	if err := enforceStepOutputContract(booleans, map[string]string{"flag": "true", "ratio": "-1.5e3"}, nil); err != nil {
		t.Fatalf("boolean/number outputs rejected: %v", err)
	}
	if err := enforceStepOutputContract(booleans, map[string]string{"flag": "yes", "ratio": "NaN"}, nil); err == nil || !strings.Contains(err.Error(), `$.flag: expected boolean text "true" or "false", got "yes"`) || !strings.Contains(err.Error(), "$.ratio: expected number encoded as JSON text") {
		t.Fatalf("boolean/number violation = %v", err)
	}
	// Undeclared shapes and pre-contract snapshots stay unvalidated.
	if err := enforceStepOutputContract(PipelineStep{ID: "legacy"}, map[string]string{"anything": "{"}, nil); err != nil {
		t.Fatalf("nil contract error = %v", err)
	}
}

func TestCheckStepOutputContractRejectsUnenforceableSnapshots(t *testing.T) {
	if err := checkStepOutputContract(PipelineStep{ID: "legacy"}); err != nil {
		t.Fatalf("nil contract = %v", err)
	}
	future := PipelineStep{ID: "future", OutputContract: &StepOutputContract{Version: 2, Shape: JSONShape{Type: "object"}}}
	if err := checkStepOutputContract(future); !errors.Is(err, ErrStepOutputContract) || !strings.Contains(err.Error(), "contract version 2 is not supported by this runtime (supports 1)") {
		t.Fatalf("future version = %v", err)
	}
	unsupported := PipelineStep{ID: "legacy-shape", OutputContract: &StepOutputContract{Version: 1, Shape: JSONShape{Type: "string"}}}
	if err := checkStepOutputContract(unsupported); !errors.Is(err, ErrStepOutputContract) || !strings.Contains(err.Error(), `step "legacy-shape" output contract cannot be enforced`) || !strings.Contains(err.Error(), "update the workflow definition output_shape") {
		t.Fatalf("unsupported shape = %v", err)
	}
}

func TestPipelineSnapshotCarriesVersionedOutputContract(t *testing.T) {
	definition := WorkflowDefinition{ID: "contract", Pipeline: PipelineDefinition{ID: "contract", Name: "Contract", Steps: []PipelineStepDefinition{
		{ID: "plain", Position: 1, Name: "plain", Executor: "internal"},
		{ID: "typed", Position: 2, Name: "typed", Executor: "internal", OutputShape: reportShape()},
	}}}
	pipeline := pipelineFromDefinition(definition)
	if pipeline.Steps[0].OutputContract != nil {
		t.Fatalf("undeclared shape produced a contract: %+v", pipeline.Steps[0].OutputContract)
	}
	contract := pipeline.Steps[1].OutputContract
	if contract == nil || contract.Version != StepOutputContractVersion || contract.Shape.Properties["report"].Properties["items"].Items == nil {
		t.Fatalf("typed contract = %+v", contract)
	}
	// The snapshot is independent of the definition it came from.
	definition.Pipeline.Steps[1].OutputShape.Properties["report"].Properties["items"].Items.Required[0] = "mutated"
	cloned := clonePipeline(pipeline)
	cloned.Steps[1].OutputContract.Shape.Required[0] = "mutated"
	if pipeline.Steps[1].OutputContract.Shape.Required[0] != "count" || pipeline.Steps[1].OutputContract.Shape.Properties["report"].Properties["items"].Items.Required[0] != "id" {
		t.Fatalf("snapshot contract aliases its source: %+v", pipeline.Steps[1].OutputContract.Shape)
	}
	encoded, err := json.Marshal(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(encoded), `"output_contract"`) != 1 || !strings.Contains(string(encoded), `"output_contract":{"version":1,"shape":{"type":"object"`) {
		t.Fatalf("snapshot JSON = %s", encoded)
	}
	// Steps without contracts keep the pre-contract JSON, so their pipeline
	// version is unchanged.
	plain := Pipeline{ID: "p", Steps: []PipelineStep{{ID: "a", Position: 1, Name: "a", Executor: "internal"}}}
	if encoded, _ := json.Marshal(plain); strings.Contains(string(encoded), "output_contract") {
		t.Fatalf("plain snapshot JSON = %s", encoded)
	}
}

func TestWorkflowDefinitionRejectsUnsupportedOutputShapes(t *testing.T) {
	store := NewStore(nil)
	base := func(shape JSONShape) WorkflowDefinition {
		return WorkflowDefinition{ID: "shapes", Name: "Shapes", Status: "draft", Pipeline: PipelineDefinition{ID: "shapes", WorkflowID: "shapes", Name: "Shapes", Steps: []PipelineStepDefinition{{ID: "emit", Position: 1, Name: "emit", Executor: "internal", OutputShape: shape}}}}
	}
	_, err := store.UpsertWorkflowDefinition(base(JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"x": {Type: "date"}}}))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `step "emit" output_shape is not a supported output contract: output_shape.properties.x.type "date" is not supported`) {
		t.Fatalf("unsupported shape error = %v", err)
	}
	if _, err := store.UpsertWorkflowDefinition(base(reportShape())); err != nil {
		t.Fatalf("valid shape rejected: %v", err)
	}
	if _, err := store.UpsertWorkflowDefinition(base(JSONShape{})); err != nil {
		t.Fatalf("undeclared shape rejected: %v", err)
	}
}

func TestAPIWorkflowDefinitionOutputShapeValidation(t *testing.T) {
	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "shapes", Kind: "shapes", Name: "Shapes"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewAPI(store, NewRunner(store)).Handler())
	t.Cleanup(srv.Close)
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(payload)
	}
	definition := func(shape string) string {
		return `{"name":"Shapes","status":"draft","pipeline":{"id":"shapes","workflow_id":"shapes","name":"Shapes","steps":[{"id":"emit","position":1,"name":"emit","executor":"internal","inputs":{"output.count":"1"},"output_shape":` + shape + `}]}}`
	}
	rejected := map[string]string{
		`{"type":"object","properties":{"count":{"type":"string","pattern":"^[0-9]+$"}}}`:                  `output_shape.properties.count: unsupported keyword \"pattern\"`,
		`{"type":"object","properties":{"count":{"$ref":"https://example.test/s.json"}}}`:                  `output_shape.properties.count: unsupported keyword \"$ref\"`,
		`{"type":"object","additionalProperties":false}`:                                                   `output_shape: unsupported keyword \"additionalProperties\"`,
		`{"type":"object","properties":{"list":{"type":"array","items":{"type":"string","minLength":1}}}}`: `output_shape.properties.list.items: unsupported keyword \"minLength\"`,
		`{"type":"array"}`: `output_shape.type must be \"object\"`,
		`{"type":"object","properties":{"count":{"type":"integer"}},"required":["missing"]}`: `output_shape.required lists \"missing\"`,
	}
	for shape, want := range rejected {
		status, body := do(http.MethodPut, "/api/workflow-definitions/shapes", definition(shape))
		if status != http.StatusBadRequest || !strings.Contains(body, `step \"emit\" output_shape is not a supported output contract`) || !strings.Contains(body, want) {
			t.Errorf("shape %s: status = %d body = %s, want %q", shape, status, body, want)
		}
	}
	status, body := do(http.MethodPut, "/api/workflow-definitions/shapes", definition(`{"type":"object","required":["count"],"properties":{"count":{"type":"integer","description":"items"},"tags":{"type":"array","maxItems":4,"items":{"type":"string","enum":["a","b"],"maxLength":8}}}}`))
	if status != http.StatusOK {
		t.Fatalf("valid PUT status = %d body = %s", status, body)
	}
	var envelope struct {
		Definition WorkflowDefinition `json:"workflow_definition"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	tags := envelope.Definition.Pipeline.Steps[0].OutputShape.Properties["tags"]
	if tags.MaxItems == nil || *tags.MaxItems != 4 || tags.Items == nil || tags.Items.MaxLength == nil || *tags.Items.MaxLength != 8 || len(tags.Items.Enum) != 2 {
		t.Fatalf("saved output shape = %+v", envelope.Definition.Pipeline.Steps[0].OutputShape)
	}
	if status, body := do(http.MethodPost, "/api/workflow-definitions/shapes/activate", ""); status != http.StatusOK {
		t.Fatalf("activate status = %d body = %s", status, body)
	}

	// A version stored before contracts were enforced cannot be restored.
	store.mu.Lock()
	legacy := cloneWorkflowDefinition(store.workflowDefinitionVersions["shapes"][1])
	legacy.Version = 99
	legacy.Pipeline.Steps[0].OutputShape = JSONShape{Type: "string"}
	store.workflowDefinitionVersions["shapes"][99] = legacy
	store.mu.Unlock()
	status, body = do(http.MethodPost, "/api/workflow-definitions/shapes/rollback", `{"version":99}`)
	if status != http.StatusBadRequest || !strings.Contains(body, `output_shape.type must be \"object\"`) {
		t.Fatalf("legacy rollback status = %d body = %s", status, body)
	}
}

// contractWorkflow registers an active two-step internal definition whose
// first step publishes output.count and output.report from run inputs and
// whose second step consumes them.
func contractWorkflow(t *testing.T, store *MemoryStore, shape JSONShape) *Runner {
	t.Helper()
	if err := store.UpsertWorkflow(Workflow{ID: "contract", Kind: "contract", Name: "Contract"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "contract", Name: "Contract", Status: "active", Pipeline: PipelineDefinition{ID: "contract", WorkflowID: "contract", Name: "Contract", Steps: []PipelineStepDefinition{
		{ID: "produce", Position: 1, Name: "contract.produce", Executor: "internal", Inputs: map[string]string{"output.count": "{{count}}", "output.report": "{{report}}"}, OutputShape: shape},
		{ID: "consume", Position: 2, Name: "contract.consume", Executor: "internal", Inputs: map[string]string{"summary": "count={{steps.produce.outputs.count}} report={{steps.produce.outputs.report}}"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	return NewRunner(store, MapExecutorRegistry{"contract": genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}})
}

func stepsByName(t *testing.T, detail RunDetail) map[string]RunStep {
	t.Helper()
	out := map[string]RunStep{}
	for _, step := range detail.Steps {
		out[step.Name] = step
	}
	return out
}

func hasEvent(detail RunDetail, eventType, substring string) bool {
	for _, event := range detail.Events {
		if event.Type == eventType && strings.Contains(event.Message, substring) {
			return true
		}
	}
	return false
}

const validReport = `{"title":"Weekly","items":[{"id":1}]}`

func TestRunnerEnforcesOutputContractBeforeDownstreamConsumption(t *testing.T) {
	store := NewStore(nil)
	runner := contractWorkflow(t, store, reportShape())

	ok, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "2", "report": validReport}})
	if err != nil || ok.Status != RunSucceeded {
		t.Fatalf("valid run = %+v, %v", ok, err)
	}
	okDetail, err := store.RunDetail(ok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contract := okDetail.Run.PipelineSnapshot.Steps[0].OutputContract; contract == nil || contract.Version != StepOutputContractVersion || contract.Shape.Required[0] != "count" {
		t.Fatalf("snapshot contract = %+v", contract)
	}
	if consume := stepsByName(t, okDetail)["contract.consume"]; consume.OutputSummary != "count=2 report="+validReport {
		t.Fatalf("consume summary = %q", consume.OutputSummary)
	}

	cases := map[string]struct {
		inputs map[string]string
		want   string
	}{
		"malformed":        {map[string]string{"count": "2", "report": `{"title":`}, "$.report: expected object encoded as JSON text"},
		"wrong type":       {map[string]string{"count": "two", "report": validReport}, `$.count: expected integer encoded as JSON text, got "two"`},
		"missing required": {map[string]string{"count": "2", "report": `{"items":[]}`}, "$.report.title: required property is missing"},
	}
	for name, tc := range cases {
		run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: tc.inputs})
		if err != nil {
			t.Fatalf("%s: RunOnce() error = %v", name, err)
		}
		if run.Status != RunPaused || !strings.Contains(run.Summary, `step "produce" output violates its output contract (v1)`) || !strings.Contains(run.Summary, tc.want) {
			t.Errorf("%s: run = %+v", name, run)
			continue
		}
		detail, err := store.RunDetail(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		steps := stepsByName(t, detail)
		produce := steps["contract.produce"]
		if produce.Status != StepFailed || !strings.Contains(produce.Error, tc.want) || len(produce.Outputs) != 0 {
			t.Errorf("%s: produce step = %+v", name, produce)
		}
		if _, consumed := steps["contract.consume"]; consumed {
			t.Errorf("%s: downstream step ran after a contract violation", name)
		}
		if !hasEvent(detail, stepOutputContractViolatedEvent, tc.want) {
			t.Errorf("%s: violation event missing: %+v", name, detail.Events)
		}
	}
}

func TestRunnerReportsOversizedRequiredOutputAsContractViolation(t *testing.T) {
	t.Setenv("SWITCHYARD_MAX_STEP_OUTPUT_BYTES", "1024")
	store := NewStore(nil)
	runner := contractWorkflow(t, store, JSONShape{Type: "object", Required: []string{"count", "report"}, Properties: map[string]JSONShapeProperty{"count": {Type: "integer"}, "report": {Type: "string"}}})
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "2", "report": strings.Repeat("x", 1500)}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunPaused || !strings.Contains(run.Summary, "$.report: required output was omitted because its 1500-byte value exceeds the 1024-byte step output limit") {
		t.Fatalf("run = %+v", run)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvent(detail, stepOutputOmittedEvent, "produce.report (1500 bytes exceeds the 1024-byte step output limit)") {
		t.Fatalf("omission event missing: %+v", detail.Events)
	}
}

func TestRunnerKeepsSnapshotContractSemanticsAcrossResumeAndRollback(t *testing.T) {
	store := NewStore(nil)
	runner := contractWorkflow(t, store, reportShape())
	// The consumer fails the first time so the run can be resumed from it.
	failConsumer := true
	runner.executors = MapExecutorRegistry{"contract": flakyConsumerExecutor{inner: genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}, fail: &failConsumer}}
	source, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "2", "report": validReport}})
	if err != nil || source.Status != RunFailed {
		t.Fatalf("source run = %+v, %v", source, err)
	}
	failConsumer = false
	sourceDetail, err := store.RunDetail(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	consume := stepsByName(t, sourceDetail)["contract.consume"]

	// Tighten the current definition after the run was created. The resume
	// must keep validating against its own snapshot, not the new definition.
	stricter := reportShape()
	stricter.Properties["count"] = JSONShapeProperty{Type: "string", Enum: []string{"never"}}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "contract", Name: "Contract", Status: "active", Pipeline: PipelineDefinition{ID: "contract", WorkflowID: "contract", Name: "Contract", Steps: []PipelineStepDefinition{
		{ID: "produce", Position: 1, Name: "contract.produce", Executor: "internal", Inputs: map[string]string{"output.count": "{{count}}"}, OutputShape: stricter},
		{ID: "consume", Position: 2, Name: "contract.consume", Executor: "internal", Inputs: map[string]string{"summary": "count={{steps.produce.outputs.count}}"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: retryRunInputsFromStep(sourceDetail, consume), PipelineSnapshot: sourceDetail.Run.PipelineSnapshot})
	if err != nil || resumed.Status != RunSucceeded {
		t.Fatalf("resumed run = %+v, %v", resumed, err)
	}
	resumedDetail, err := store.RunDetail(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stepsByName(t, resumedDetail)["contract.consume"].OutputSummary; got != "count=2 report="+validReport {
		t.Fatalf("resumed consumer summary = %q", got)
	}
	if !hasEvent(resumedDetail, stepOutputsRestoredEvent, "restored outputs of 1 succeeded step(s) before position 2 from run "+source.ID) {
		t.Fatalf("restore event missing: %+v", resumedDetail.Events)
	}
	if contract := resumedDetail.Run.PipelineSnapshot.Steps[0].OutputContract; contract.Shape.Properties["count"].Type != "integer" {
		t.Fatalf("resumed run adopted the current definition's contract: %+v", contract)
	}
	// A fresh run uses the current (stricter) definition and fails on the same value.
	fresh, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "2"}})
	if err != nil || fresh.Status != RunPaused || !strings.Contains(fresh.Summary, `$.count: "2" is not one of the allowed enum values`) {
		t.Fatalf("fresh run under the new definition = %+v, %v", fresh, err)
	}
	// Rolling the definition back restores the original contract for new runs;
	// the paused run above keeps the stricter contract it was created with.
	if _, err := store.RollbackWorkflowDefinition("contract", 1); err != nil {
		t.Fatalf("RollbackWorkflowDefinition() error = %v", err)
	}
	rolledBack, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "2", "report": validReport}})
	if err != nil || rolledBack.Status != RunSucceeded {
		t.Fatalf("run after rollback = %+v, %v", rolledBack, err)
	}
	freshDetail, err := store.RunDetail(fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: freshDetail.Run.Inputs, PipelineSnapshot: freshDetail.Run.PipelineSnapshot})
	if err != nil || retried.Status != RunPaused || !strings.Contains(retried.Summary, "is not one of the allowed enum values") {
		t.Fatalf("retry of the stricter run after rollback = %+v, %v", retried, err)
	}
}

func TestRunnerValidatesRestoredOutputsBeforeConsumption(t *testing.T) {
	store := NewStore(nil)
	runner := contractWorkflow(t, store, reportShape())
	failConsumer := true
	runner.executors = MapExecutorRegistry{"contract": flakyConsumerExecutor{inner: genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}, fail: &failConsumer}}
	source, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "2", "report": validReport}})
	if err != nil || source.Status != RunFailed {
		t.Fatalf("source run = %+v, %v", source, err)
	}
	failConsumer = false
	sourceDetail, err := store.RunDetail(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps := stepsByName(t, sourceDetail)
	// Simulate a record that no longer satisfies the snapshot contract, such as
	// one written before enforcement; it must not reach the consumer.
	if _, err := store.UpdateRunStep(steps["contract.produce"].ID, UpdateRunStepInput{Outputs: map[string]string{"count": "many", "report": validReport}}); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: retryRunInputsFromStep(sourceDetail, steps["contract.consume"]), PipelineSnapshot: sourceDetail.Run.PipelineSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != RunPaused || !strings.Contains(resumed.Summary, `unresolved step output reference "steps.produce.outputs.count": recorded outputs fail the producing step's output contract`) || !strings.Contains(resumed.Summary, `$.count: expected integer encoded as JSON text, got "many"`) {
		t.Fatalf("resumed run = %+v", resumed)
	}
}

func TestRunnerLeavesUncontractedAndLegacySnapshotsUnvalidated(t *testing.T) {
	store := NewStore(nil)
	runner := contractWorkflow(t, store, JSONShape{})
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "not a number", "report": "{"}})
	if err != nil || run.Status != RunSucceeded {
		t.Fatalf("uncontracted run = %+v, %v", run, err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.PipelineSnapshot.Steps[0].OutputContract != nil {
		t.Fatalf("undeclared shape produced a snapshot contract")
	}

	// A snapshot created before contracts existed has no output_contract even
	// though the definition now declares one; it keeps its original semantics.
	contracted := NewStore(nil)
	runner = contractWorkflow(t, contracted, reportShape())
	pipeline, ok := runner.pipelines.PipelineForWorkflow("contract")
	if !ok {
		t.Fatal("pipeline missing")
	}
	legacy := clonePipeline(pipeline)
	for i := range legacy.Steps {
		legacy.Steps[i].OutputContract = nil
	}
	run, err = runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "not a number", "report": "{"}, PipelineSnapshot: &legacy})
	if err != nil || run.Status != RunSucceeded {
		t.Fatalf("legacy snapshot run = %+v, %v", run, err)
	}
}

func TestRunnerFailsUnenforceableSnapshotContractBeforeSideEffects(t *testing.T) {
	store := NewStore(nil)
	runner := contractWorkflow(t, store, reportShape())
	calls := 0
	runner.executors = MapExecutorRegistry{"contract": countingExecutor{inner: genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}, calls: &calls}}
	pipeline, _ := runner.pipelines.PipelineForWorkflow("contract")
	legacy := clonePipeline(pipeline)
	legacy.Steps[0].OutputContract = &StepOutputContract{Version: 1, Shape: JSONShape{Type: "string"}}
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "contract", Inputs: map[string]string{"count": "1", "report": validReport}, PipelineSnapshot: &legacy})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunPaused || !strings.Contains(run.Summary, `step "produce" output contract cannot be enforced`) || calls != 0 {
		t.Fatalf("run = %+v, executor calls = %d", run, calls)
	}
}

// flakyConsumerExecutor fails the consumer step while *fail is true.
type flakyConsumerExecutor struct {
	inner genericWorkflowExecutor
	fail  *bool
}

func (f flakyConsumerExecutor) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	return f.inner.Execute(ctx, run)
}

func (f flakyConsumerExecutor) ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error) {
	if step.ID == "consume" && *f.fail {
		return PipelineStepExecution{}, errors.New("consumer unavailable")
	}
	return f.inner.ExecutePipelineStep(ctx, run, step, state)
}

type countingExecutor struct {
	inner genericWorkflowExecutor
	calls *int
}

func (c countingExecutor) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	return c.inner.Execute(ctx, run)
}

func (c countingExecutor) ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error) {
	*c.calls++
	return c.inner.ExecutePipelineStep(ctx, run, step, state)
}
