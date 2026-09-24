package controlplane

import (
	"errors"
	"strings"
	"testing"
)

func TestParseStepOutputReferences(t *testing.T) {
	references, err := parseStepOutputReferences("Body: {{steps.fetch.outputs.body}} and {{ steps.fetch.outputs.status }} plus {{topic}}")
	if err != nil {
		t.Fatalf("parseStepOutputReferences() error = %v", err)
	}
	if len(references) != 2 || references[0] != (StepOutputReference{StepID: "fetch", Name: "body"}) || references[1] != (StepOutputReference{StepID: "fetch", Name: "status"}) {
		t.Fatalf("references = %+v", references)
	}
	for _, template := range []string{"{{steps.fetch}}", "{{steps.fetch.body}}", "{{steps.fetch.outputs.Body}}", "{{steps.fetch.outputs.a.b}}", "{{steps..outputs.body}}", "{{steps.fetch.outputs.}}"} {
		if _, err := parseStepOutputReferences(template); !errors.Is(err, ErrStepOutputReference) {
			t.Errorf("parseStepOutputReferences(%q) error = %v, want ErrStepOutputReference", template, err)
		}
	}
}

func TestResolvePipelineStepInputsIsDeterministicAndFailsClearly(t *testing.T) {
	outputs := newPipelineStepOutputs()
	outputs.set("fetch", map[string]string{"body": "payload", "status": "200"})
	step := PipelineStep{ID: "summarize", Position: 2, Inputs: map[string]string{"prompt": "{{steps.fetch.outputs.body}} ({{steps.fetch.outputs.status}}) {{steps.fetch.outputs.body}}", "plain": "{{topic}}"}}
	resolved, err := resolvePipelineStepInputs(step, outputs)
	if err != nil {
		t.Fatalf("resolvePipelineStepInputs() error = %v", err)
	}
	if resolved["prompt"] != "payload (200) payload" || resolved["plain"] != "{{topic}}" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if step.Inputs["prompt"] != "{{steps.fetch.outputs.body}} ({{steps.fetch.outputs.status}}) {{steps.fetch.outputs.body}}" {
		t.Fatal("resolution mutated the pipeline step inputs")
	}

	missing := PipelineStep{ID: "summarize", Position: 2, Inputs: map[string]string{"prompt": "{{steps.fetch.outputs.missing}}"}}
	_, err = resolvePipelineStepInputs(missing, outputs)
	if !errors.Is(err, ErrStepOutputReference) || !strings.Contains(err.Error(), `unresolved step output reference "steps.fetch.outputs.missing"`) {
		t.Fatalf("missing reference error = %v", err)
	}
	var classified interface{ Retryable() bool }
	if !errors.As(err, &classified) || classified.Retryable() {
		t.Fatalf("missing reference must be non-retryable: %v", err)
	}
	self := PipelineStep{ID: "fetch", Position: 1, Inputs: map[string]string{"url": "{{steps.fetch.outputs.url}}"}}
	if _, err := resolvePipelineStepInputs(self, outputs); !errors.Is(err, ErrStepOutputReference) {
		t.Fatalf("self reference error = %v", err)
	}
	if _, err := resolvePipelineStepInputs(PipelineStep{ID: "x", Inputs: map[string]string{"prompt": "{{steps.fetch.outputs.body}}"}}, newPipelineStepOutputs()); !errors.Is(err, ErrStepOutputReference) {
		t.Fatalf("unknown step error = %v", err)
	}
}

func TestBoundStepOutputsEnforcesNamesSizesAndRedaction(t *testing.T) {
	t.Setenv("SWITCHYARD_MAX_STEP_OUTPUT_BYTES", "1024")
	bounded, omitted, err := boundStepOutputs("fetch", map[string]string{"body": strings.Repeat("x", 1025), "status": "200", "note": "Authorization: Bearer abc.def-123 done"})
	if err != nil {
		t.Fatalf("boundStepOutputs() error = %v", err)
	}
	if _, ok := bounded["body"]; ok || bounded["status"] != "200" {
		t.Fatalf("bounded = %+v", bounded)
	}
	if bounded["note"] != "Authorization: Bearer [REDACTED] done" {
		t.Fatalf("note = %q", bounded["note"])
	}
	if len(omitted) != 1 || !strings.Contains(omitted[0], "fetch.body (1025 bytes exceeds the 1024-byte step output limit)") {
		t.Fatalf("omitted = %+v", omitted)
	}
	for _, name := range []string{"api_token", "Password", "1abc", "has-dash", "with space", strings.Repeat("a", 65)} {
		if _, _, err := boundStepOutputs("fetch", map[string]string{name: "v"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("boundStepOutputs(%q) error = %v, want ErrInvalid", name, err)
		}
	}
	tooMany := map[string]string{}
	for i := 0; i <= maxStepOutputCount; i++ {
		tooMany["o"+strings.Repeat("a", i)] = "v"
	}
	if _, _, err := boundStepOutputs("fetch", tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many outputs error = %v", err)
	}
	if got, _, err := boundStepOutputs("fetch", nil); err != nil || got != nil {
		t.Fatalf("nil outputs = %+v, %v", got, err)
	}
}

func TestWorkflowDefinitionRejectsInvalidStepOutputReferences(t *testing.T) {
	store := NewStore(nil)
	base := func(steps ...PipelineStepDefinition) WorkflowDefinition {
		return WorkflowDefinition{ID: "bindings", Name: "Bindings", Status: "draft", Pipeline: PipelineDefinition{ID: "bindings", WorkflowID: "bindings", Name: "Bindings", Steps: steps}}
	}
	fetch := PipelineStepDefinition{ID: "fetch", Position: 1, Name: "fetch", Executor: "http", Inputs: map[string]string{"allow_http": "true", "url": "https://example.test"}}
	cases := map[string]WorkflowDefinition{
		"unknown step":     base(fetch, PipelineStepDefinition{ID: "use", Position: 2, Name: "use", Executor: "internal", Inputs: map[string]string{"summary": "{{steps.nope.outputs.body}}"}}),
		"later step":       base(PipelineStepDefinition{ID: "use", Position: 1, Name: "use", Executor: "internal", Inputs: map[string]string{"summary": "{{steps.fetch.outputs.body}}"}}, PipelineStepDefinition{ID: "fetch", Position: 2, Name: "fetch", Executor: "http"}),
		"self reference":   base(PipelineStepDefinition{ID: "use", Position: 1, Name: "use", Executor: "internal", Inputs: map[string]string{"summary": "{{steps.use.outputs.summary}}"}}),
		"malformed":        base(fetch, PipelineStepDefinition{ID: "use", Position: 2, Name: "use", Executor: "internal", Inputs: map[string]string{"summary": "{{steps.fetch.body}}"}}),
		"secret output":    base(PipelineStepDefinition{ID: "use", Position: 1, Name: "use", Executor: "internal", Inputs: map[string]string{"output.api_token": "x"}}),
		"invalid name":     base(PipelineStepDefinition{ID: "use", Position: 1, Name: "use", Executor: "internal", Inputs: map[string]string{"output.Bad-Name": "x"}}),
		"non-internal out": base(PipelineStepDefinition{ID: "use", Position: 1, Name: "use", Executor: "model", Inputs: map[string]string{"prompt": "x", "output.extra": "x"}}),
	}
	for name, definition := range cases {
		if _, err := store.UpsertWorkflowDefinition(definition); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: UpsertWorkflowDefinition() error = %v, want ErrInvalid", name, err)
		}
	}
	valid := base(fetch, PipelineStepDefinition{ID: "use", Position: 2, Name: "use", Executor: "internal", Inputs: map[string]string{"summary": "{{steps.fetch.outputs.status}}", "output.digest": "{{steps.fetch.outputs.body_sha256}}"}})
	if _, err := store.UpsertWorkflowDefinition(valid); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
}
