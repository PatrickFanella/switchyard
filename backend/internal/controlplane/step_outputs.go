package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Durable named step outputs.
//
// A pipeline step may publish a bounded set of named string outputs. They are
// persisted on the immutable run step record (run_id, position) once the step
// succeeds and are restored from that record on same-run retries, on operator
// resume runs that carry retry_from_run, and after process restarts. Later
// steps reference them in input templates as
//
//	{{steps.<step_id>.outputs.<name>}}
//
// where <step_id> is the pipeline step id in the run's pipeline snapshot and
// <name> matches stepOutputNamePattern. References must point to an earlier
// step; missing or malformed references fail the consuming step before any
// executor side effect and pause the run because the failure is deterministic.

const (
	stepOutputReferencePrefix = "steps."
	stepOutputReferenceInfix  = ".outputs."
	maxStepOutputCount        = 32
	maxStepOutputNameLength   = 64
	defaultMaxStepOutputBytes = 64 << 10
	stepOutputOmittedEvent    = "step.output.omitted"
	stepOutputsRestoredEvent  = "step.outputs.restored"
)

var (
	stepOutputNamePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	stepOutputReferencePattern = regexp.MustCompile(`\{\{\s*steps\.([^{}]*?)\s*\}\}`)
)

// ErrStepOutputReference marks deterministic input-binding failures. The run
// pauses instead of retrying because rerunning cannot produce the reference.
var ErrStepOutputReference = errors.New("step output reference")

type stepOutputReferenceError struct {
	reference string
	reason    string
}

func (e stepOutputReferenceError) Error() string {
	return fmt.Sprintf("unresolved step output reference %q: %s", e.reference, e.reason)
}

func (e stepOutputReferenceError) Unwrap() error { return ErrStepOutputReference }

func (e stepOutputReferenceError) Retryable() bool { return false }

// StepOutputReference is one parsed {{steps.<id>.outputs.<name>}} token.
type StepOutputReference struct {
	StepID string
	Name   string
}

func (r StepOutputReference) String() string {
	return stepOutputReferencePrefix + r.StepID + stepOutputReferenceInfix + r.Name
}

func maxStepOutputBytes() int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_MAX_STEP_OUTPUT_BYTES"))); err == nil && parsed >= 1024 && parsed <= 1<<20 {
		return parsed
	}
	return defaultMaxStepOutputBytes
}

func validStepOutputName(name string) bool {
	return len(name) <= maxStepOutputNameLength && stepOutputNamePattern.MatchString(name) && !sensitiveJSONKey(name)
}

// parseStepOutputReferences returns every steps.* reference in template order.
// Any steps.* token that is not exactly steps.<id>.outputs.<name> is an error so
// typos never silently render as literal text.
func parseStepOutputReferences(template string) ([]StepOutputReference, error) {
	matches := stepOutputReferencePattern.FindAllStringSubmatch(template, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	out := make([]StepOutputReference, 0, len(matches))
	for _, match := range matches {
		body := strings.TrimSpace(match[1])
		stepID, name, ok := strings.Cut(body, stepOutputReferenceInfix)
		stepID, name = strings.TrimSpace(stepID), strings.TrimSpace(name)
		if !ok || stepID == "" || name == "" || strings.Contains(name, ".") || strings.Contains(stepID, " ") {
			return nil, stepOutputReferenceError{reference: stepOutputReferencePrefix + body, reason: "expected steps.<step_id>.outputs.<name>"}
		}
		if !stepOutputNamePattern.MatchString(name) {
			return nil, stepOutputReferenceError{reference: stepOutputReferencePrefix + body, reason: "output name must match " + stepOutputNamePattern.String()}
		}
		out = append(out, StepOutputReference{StepID: stepID, Name: name})
	}
	return out, nil
}

// validatePipelineStepOutputReferences checks every reference in a pipeline
// definition statically: the referenced step must exist and run earlier.
func validatePipelineStepOutputReferences(steps []PipelineStepDefinition) error {
	positions := make(map[string]int, len(steps))
	for _, step := range steps {
		positions[step.ID] = step.Position
	}
	for _, step := range steps {
		keys := make([]string, 0, len(step.Inputs))
		for key := range step.Inputs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if name, ok := strings.CutPrefix(key, internalStepOutputInputPrefix); ok && (step.Executor != "internal" || !validStepOutputName(name)) {
				return fmt.Errorf("%w: step %q input %q does not declare a valid internal step output name", ErrInvalid, step.ID, key)
			}
			references, err := parseStepOutputReferences(step.Inputs[key])
			if err != nil {
				return fmt.Errorf("%w: step %q input %q: %v", ErrInvalid, step.ID, key, err)
			}
			for _, reference := range references {
				position, ok := positions[reference.StepID]
				if !ok {
					return fmt.Errorf("%w: step %q input %q references unknown step %q", ErrInvalid, step.ID, key, reference.StepID)
				}
				if position >= step.Position {
					return fmt.Errorf("%w: step %q input %q references step %q which does not run earlier", ErrInvalid, step.ID, key, reference.StepID)
				}
			}
		}
	}
	return nil
}

// pipelineStepOutputs is the resolver view for one run: outputs of steps that
// have already succeeded, keyed by pipeline step id.
type pipelineStepOutputs struct {
	byStepID map[string]map[string]string
	// rejected records steps whose recorded outputs fail the snapshot output
	// contract; references to them fail with the recorded reason.
	rejected map[string]string
}

func newPipelineStepOutputs() *pipelineStepOutputs {
	return &pipelineStepOutputs{byStepID: map[string]map[string]string{}, rejected: map[string]string{}}
}

// accept validates recorded outputs of an already succeeded step against the
// step's snapshot output contract before any later step may consume them.
// Records that do not satisfy the contract are withheld and the violation is
// kept as the reason a downstream reference fails.
func (p *pipelineStepOutputs) accept(step PipelineStep, outputs map[string]string) {
	if p == nil || step.ID == "" {
		return
	}
	if err := enforceStepOutputContract(step, outputs, nil); err != nil {
		delete(p.byStepID, step.ID)
		p.rejected[step.ID] = err.Error()
		return
	}
	p.set(step.ID, outputs)
}

func (p *pipelineStepOutputs) rejection(stepID string) (string, bool) {
	if p == nil {
		return "", false
	}
	reason, ok := p.rejected[stepID]
	return reason, ok
}

func (p *pipelineStepOutputs) set(stepID string, outputs map[string]string) {
	if p == nil || stepID == "" {
		return
	}
	p.byStepID[stepID] = cloneMap(outputs)
	delete(p.rejected, stepID)
}

func (p *pipelineStepOutputs) lookup(reference StepOutputReference) (string, bool) {
	if p == nil {
		return "", false
	}
	outputs, ok := p.byStepID[reference.StepID]
	if !ok {
		return "", false
	}
	value, ok := outputs[reference.Name]
	return value, ok
}

// loadPersistedStepOutputs maps succeeded run step records back to pipeline
// step ids by position so restored outputs use the immutable snapshot identity.
func (p *pipelineStepOutputs) loadPersisted(pipelineSteps []PipelineStep, recorded []RunStep, beforePosition int) int {
	byPosition := make(map[int]PipelineStep, len(pipelineSteps))
	for _, step := range pipelineSteps {
		byPosition[step.Position] = step
	}
	restored := 0
	for _, record := range recorded {
		if record.Status != StepSucceeded || len(record.Outputs) == 0 {
			continue
		}
		if beforePosition > 0 && record.Position >= beforePosition {
			continue
		}
		pipelineStep, ok := byPosition[record.Position]
		if !ok || pipelineStep.Name != record.Name || pipelineStep.Executor != record.Executor {
			continue
		}
		p.accept(pipelineStep, record.Outputs)
		restored++
	}
	return restored
}

// resolvePipelineStepInputs substitutes every steps.* reference in the step's
// inputs. It is deterministic: the same persisted outputs always yield the same
// rendered inputs, and any missing reference fails before execution.
func resolvePipelineStepInputs(step PipelineStep, outputs *pipelineStepOutputs) (map[string]string, error) {
	if len(step.Inputs) == 0 {
		return step.Inputs, nil
	}
	keys := make([]string, 0, len(step.Inputs))
	for key := range step.Inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	resolved := make(map[string]string, len(step.Inputs))
	for _, key := range keys {
		template := step.Inputs[key]
		references, err := parseStepOutputReferences(template)
		if err != nil {
			return nil, fmt.Errorf("step %q input %q: %w", step.ID, key, err)
		}
		values := make(map[string]string, len(references))
		for _, reference := range references {
			if reference.StepID == step.ID {
				return nil, fmt.Errorf("step %q input %q: %w", step.ID, key, stepOutputReferenceError{reference: reference.String(), reason: "a step cannot reference its own outputs"})
			}
			if reason, rejected := outputs.rejection(reference.StepID); rejected {
				return nil, fmt.Errorf("step %q input %q: %w", step.ID, key, stepOutputReferenceError{reference: reference.String(), reason: "recorded outputs fail the producing step's output contract: " + reason})
			}
			value, ok := outputs.lookup(reference)
			if !ok {
				return nil, fmt.Errorf("step %q input %q: %w", step.ID, key, stepOutputReferenceError{reference: reference.String(), reason: "no succeeded step output with that name is recorded for this run"})
			}
			values[reference.String()] = value
		}
		if len(values) == 0 {
			resolved[key] = template
			continue
		}
		resolved[key] = stepOutputReferencePattern.ReplaceAllStringFunc(template, func(token string) string {
			match := stepOutputReferencePattern.FindStringSubmatch(token)
			if len(match) != 2 {
				return token
			}
			value, ok := values[stepOutputReferencePrefix+strings.TrimSpace(match[1])]
			if !ok {
				return token
			}
			return value
		})
	}
	return resolved, nil
}

// boundStepOutputs enforces the persisted output contract before storage:
// valid names, no credential-shaped names, at most maxStepOutputCount entries,
// and per-value byte limits. Oversized values are dropped and reported through
// the returned omission messages so a downstream reference fails clearly
// instead of receiving truncated data. Values are redacted like other persisted
// evidence.
func boundStepOutputs(stepID string, outputs map[string]string) (map[string]string, []string, error) {
	if len(outputs) == 0 {
		return nil, nil, nil
	}
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > maxStepOutputCount {
		return nil, nil, fmt.Errorf("%w: step %q publishes %d outputs; limit is %d", ErrInvalid, stepID, len(names), maxStepOutputCount)
	}
	limit := maxStepOutputBytes()
	bounded := make(map[string]string, len(names))
	var omitted []string
	for _, name := range names {
		if !validStepOutputName(name) {
			return nil, nil, fmt.Errorf("%w: step %q output name %q is not allowed", ErrInvalid, stepID, name)
		}
		value := outputs[name]
		if len(value) > limit {
			omitted = append(omitted, fmt.Sprintf("%s.%s (%d bytes exceeds the %d-byte step output limit)", stepID, name, len(value), limit))
			continue
		}
		bounded[name] = RedactForLog(value)
	}
	return bounded, omitted, nil
}

// validatePersistedStepOutputs is the store-side guard so no caller can persist
// outputs that violate the contract.
func validatePersistedStepOutputs(outputs map[string]string) error {
	if len(outputs) > maxStepOutputCount {
		return ErrInvalid
	}
	limit := maxStepOutputBytes()
	for name, value := range outputs {
		if !validStepOutputName(name) || len(value) > limit {
			return ErrInvalid
		}
	}
	return nil
}

func redactStepOutputs(outputs map[string]string) map[string]string {
	if outputs == nil {
		return nil
	}
	out := make(map[string]string, len(outputs))
	for name, value := range outputs {
		out[name] = RedactForLog(value)
	}
	return out
}

func stepOutputDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// textStepOutput returns the value only when it is valid UTF-8 text; binary
// payloads stay in artifacts and are described by their size and digest.
func textStepOutput(value []byte) (string, bool) {
	if !utf8.Valid(value) {
		return "", false
	}
	return string(value), true
}
