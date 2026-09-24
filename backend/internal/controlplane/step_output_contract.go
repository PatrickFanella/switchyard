package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Step output contracts.
//
// A workflow definition step may declare output_shape. When a run snapshot is
// created the shape is copied into the immutable pipeline snapshot as a
// versioned StepOutputContract, so retries, retry_from_run resumes, restarts,
// and definition rollbacks always validate against the contract the run was
// created with rather than the current definition.
//
// Step outputs are named strings. The top-level shape is always type "object";
// each declared property describes one named output and says how its string
// value is interpreted:
//
//	string   the raw value (maxLength counts Unicode code points, enum)
//	number   JSON number text
//	integer  JSON number text with an integral value
//	boolean  "true" or "false"
//	object   JSON object text, validated recursively
//	array    JSON array text, validated recursively
//
// Nested values use the same keywords: type, description, properties,
// required, items, maxLength, maxItems, and enum (string types only). There is
// no $ref, pattern, format, oneOf/anyOf/allOf, or null type. Undeclared outputs
// and undeclared nested properties are allowed. The subset is intentionally
// small so every accepted keyword is enforced.

const (
	// StepOutputContractVersion is the contract format written into new run
	// snapshots. Runners reject snapshot contracts with any other version.
	StepOutputContractVersion = 1

	stepOutputContractViolatedEvent = "step.output.contract.violated"

	maxOutputShapeDepth          = 8
	maxOutputShapeNodes          = 256
	maxOutputShapeEnumValues     = 64
	maxOutputShapeEnumValueBytes = 1024
	maxOutputShapePropertyName   = 128
	maxOutputShapeBound          = 1 << 20
	maxOutputContractViolations  = 10
)

// ErrStepOutputContract marks step outputs that violate, or steps whose
// snapshot carries an unenforceable, output contract. The failure is
// deterministic for the recorded output, so the run pauses for an operator.
var ErrStepOutputContract = errors.New("step output contract")

// StepOutputContract is the versioned output contract carried in a run's
// immutable pipeline snapshot.
type StepOutputContract struct {
	Version int       `json:"version"`
	Shape   JSONShape `json:"shape"`
}

var outputShapePathIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var outputShapeTypes = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "object": true, "array": true}

// Raw keyword allowlists. The Go structs drop unknown JSON keys, so the HTTP
// write path checks the raw request to reject keywords that would otherwise be
// silently ignored.
var (
	outputShapeTopLevelKeywords = map[string]bool{"type": true, "description": true, "properties": true, "required": true}
	outputShapePropertyKeywords = map[string]bool{"type": true, "description": true, "properties": true, "required": true, "items": true, "maxLength": true, "maxItems": true, "enum": true}
)

type stepOutputContractError struct {
	stepID      string
	version     int
	problems    []string
	unsupported bool
}

func (e stepOutputContractError) Error() string {
	if e.unsupported {
		return fmt.Sprintf("step %q output contract cannot be enforced: %s; update the workflow definition output_shape and activate a new version", e.stepID, strings.Join(e.problems, "; "))
	}
	return fmt.Sprintf("step %q output violates its output contract (v%d): %s", e.stepID, e.version, strings.Join(e.problems, "; "))
}

func (e stepOutputContractError) Unwrap() error { return ErrStepOutputContract }

func (e stepOutputContractError) Retryable() bool { return false }

func outputShapeDeclared(shape JSONShape) bool {
	return strings.TrimSpace(shape.Type) != "" || shape.Description != "" || len(shape.Properties) > 0 || len(shape.Required) > 0
}

// stepOutputContractFromShape converts a definition output_shape into the
// snapshot contract. An undeclared shape yields nil (unvalidated outputs).
func stepOutputContractFromShape(shape JSONShape) *StepOutputContract {
	if !outputShapeDeclared(shape) {
		return nil
	}
	return &StepOutputContract{Version: StepOutputContractVersion, Shape: cloneJSONShape(shape)}
}

func cloneStepOutputContract(contract *StepOutputContract) *StepOutputContract {
	if contract == nil {
		return nil
	}
	out := *contract
	out.Shape = cloneJSONShape(contract.Shape)
	return &out
}

// validatePipelineStepOutputShapes rejects definition output shapes outside
// the supported subset with the step id and the offending schema location.
func validatePipelineStepOutputShapes(steps []PipelineStepDefinition) error {
	for _, step := range steps {
		if problems := outputShapeProblems(step.OutputShape); len(problems) > 0 {
			return fmt.Errorf("%w: step %q output_shape is not a supported output contract: %s", ErrInvalid, step.ID, strings.Join(problems, "; "))
		}
	}
	return nil
}

// outputShapeProblems returns every reason the shape falls outside the
// supported subset. An undeclared shape has no problems.
func outputShapeProblems(shape JSONShape) []string {
	if !outputShapeDeclared(shape) {
		return nil
	}
	var problems []string
	if shape.Type != "object" {
		problems = append(problems, fmt.Sprintf("output_shape.type must be \"object\" (the map of named outputs), got %q", shape.Type))
	}
	if len(shape.Properties) > maxStepOutputCount {
		problems = append(problems, fmt.Sprintf("output_shape declares %d outputs; limit is %d", len(shape.Properties), maxStepOutputCount))
	}
	names := sortedShapeKeys(shape.Properties)
	for _, name := range names {
		if !validStepOutputName(name) {
			problems = append(problems, fmt.Sprintf("output_shape.properties.%s: output name must match %s, be at most %d characters, and not look like a credential", name, stepOutputNamePattern.String(), maxStepOutputNameLength))
		}
	}
	problems = append(problems, requiredProblems("output_shape", shape.Required, shape.Properties)...)
	nodes := 0
	for _, name := range names {
		problems = append(problems, propertyShapeProblems("output_shape.properties."+name, shape.Properties[name], 1, &nodes)...)
	}
	if nodes > maxOutputShapeNodes {
		problems = append(problems, fmt.Sprintf("output_shape declares %d schema nodes; limit is %d", nodes, maxOutputShapeNodes))
	}
	return capProblems(problems)
}

func requiredProblems(location string, required []string, properties map[string]JSONShapeProperty) []string {
	var problems []string
	seen := map[string]bool{}
	for _, name := range required {
		if seen[name] {
			problems = append(problems, fmt.Sprintf("%s.required lists %q more than once", location, name))
			continue
		}
		seen[name] = true
		if _, ok := properties[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s.required lists %q, which is not declared in properties", location, name))
		}
	}
	return problems
}

func propertyShapeProblems(location string, property JSONShapeProperty, depth int, nodes *int) []string {
	*nodes++
	if depth > maxOutputShapeDepth {
		return []string{fmt.Sprintf("%s: nesting exceeds %d levels", location, maxOutputShapeDepth)}
	}
	var problems []string
	if !outputShapeTypes[property.Type] {
		problems = append(problems, fmt.Sprintf("%s.type %q is not supported; use string, number, integer, boolean, object, or array", location, property.Type))
	}
	if len(property.Enum) > 0 {
		if property.Type != "string" {
			problems = append(problems, fmt.Sprintf("%s.enum is supported only for type string", location))
		}
		if len(property.Enum) > maxOutputShapeEnumValues {
			problems = append(problems, fmt.Sprintf("%s.enum has %d values; limit is %d", location, len(property.Enum), maxOutputShapeEnumValues))
		}
		seen := map[string]bool{}
		for _, value := range property.Enum {
			if len(value) > maxOutputShapeEnumValueBytes {
				problems = append(problems, fmt.Sprintf("%s.enum value exceeds %d bytes", location, maxOutputShapeEnumValueBytes))
			}
			if seen[value] {
				problems = append(problems, fmt.Sprintf("%s.enum lists %q more than once", location, value))
			}
			seen[value] = true
		}
	}
	if property.MaxLength != nil {
		if property.Type != "string" {
			problems = append(problems, fmt.Sprintf("%s.maxLength is supported only for type string", location))
		}
		if *property.MaxLength < 0 || *property.MaxLength > maxOutputShapeBound {
			problems = append(problems, fmt.Sprintf("%s.maxLength must be between 0 and %d", location, maxOutputShapeBound))
		}
	}
	if property.MaxItems != nil {
		if property.Type != "array" {
			problems = append(problems, fmt.Sprintf("%s.maxItems is supported only for type array", location))
		}
		if *property.MaxItems < 0 || *property.MaxItems > maxOutputShapeBound {
			problems = append(problems, fmt.Sprintf("%s.maxItems must be between 0 and %d", location, maxOutputShapeBound))
		}
	}
	if property.Items != nil {
		if property.Type != "array" {
			problems = append(problems, fmt.Sprintf("%s.items is supported only for type array", location))
		}
		problems = append(problems, propertyShapeProblems(location+".items", *property.Items, depth+1, nodes)...)
	}
	if len(property.Properties) > 0 || len(property.Required) > 0 {
		if property.Type != "object" {
			problems = append(problems, fmt.Sprintf("%s.properties and required are supported only for type object", location))
		}
		problems = append(problems, requiredProblems(location, property.Required, property.Properties)...)
		for _, name := range sortedShapeKeys(property.Properties) {
			if name == "" || len(name) > maxOutputShapePropertyName {
				problems = append(problems, fmt.Sprintf("%s.properties: property names must be 1-%d bytes", location, maxOutputShapePropertyName))
				continue
			}
			problems = append(problems, propertyShapeProblems(location+".properties."+name, property.Properties[name], depth+1, nodes)...)
		}
	}
	return problems
}

// validateRawOutputShapeKeywords walks the raw definition JSON and rejects
// schema keywords outside the supported subset (for example $ref, pattern, or
// oneOf) that the typed decoder would otherwise drop without enforcing.
func validateRawOutputShapeKeywords(body []byte) error {
	var raw struct {
		Pipeline struct {
			Steps []map[string]json.RawMessage `json:"steps"`
		} `json:"pipeline"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil // the typed decoder reports malformed JSON
	}
	for _, step := range raw.Pipeline.Steps {
		shape, ok := step["output_shape"]
		if !ok || bytes.Equal(bytes.TrimSpace(shape), []byte("null")) {
			continue
		}
		var stepID string
		_ = json.Unmarshal(step["id"], &stepID)
		var problems []string
		rawShapeKeywordProblems("output_shape", shape, outputShapeTopLevelKeywords, 0, &problems)
		if len(problems) > 0 {
			return fmt.Errorf("%w: step %q output_shape is not a supported output contract: %s", ErrInvalid, stepID, strings.Join(capProblems(problems), "; "))
		}
	}
	return nil
}

func rawShapeKeywordProblems(location string, raw json.RawMessage, allowed map[string]bool, depth int, problems *[]string) {
	if depth > maxOutputShapeDepth+1 {
		return
	}
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		*problems = append(*problems, fmt.Sprintf("%s must be a JSON object", location))
		return
	}
	for _, key := range sortedRawKeys(node) {
		if !allowed[key] {
			*problems = append(*problems, fmt.Sprintf("%s: unsupported keyword %q", location, key))
		}
	}
	if properties, ok := node["properties"]; ok {
		var children map[string]json.RawMessage
		if err := json.Unmarshal(properties, &children); err != nil {
			*problems = append(*problems, fmt.Sprintf("%s.properties must be a JSON object", location))
		} else {
			for _, name := range sortedRawKeys(children) {
				rawShapeKeywordProblems(location+".properties."+name, children[name], outputShapePropertyKeywords, depth+1, problems)
			}
		}
	}
	if items, ok := node["items"]; ok && allowed["items"] {
		rawShapeKeywordProblems(location+".items", items, outputShapePropertyKeywords, depth+1, problems)
	}
}

// checkStepOutputContract verifies that a snapshot contract can be enforced
// before the step runs, so an unenforceable contract never causes a side
// effect. Nil contracts (undeclared shapes and pre-contract snapshots) pass.
func checkStepOutputContract(step PipelineStep) error {
	contract := step.OutputContract
	if contract == nil {
		return nil
	}
	if contract.Version != StepOutputContractVersion {
		return stepOutputContractError{stepID: step.ID, version: contract.Version, unsupported: true, problems: []string{fmt.Sprintf("contract version %d is not supported by this runtime (supports %d)", contract.Version, StepOutputContractVersion)}}
	}
	if !outputShapeDeclared(contract.Shape) {
		return nil
	}
	if problems := outputShapeProblems(contract.Shape); len(problems) > 0 {
		return stepOutputContractError{stepID: step.ID, version: contract.Version, unsupported: true, problems: problems}
	}
	return nil
}

// enforceStepOutputContract validates the bounded, redacted outputs a
// downstream step would consume. raw is the executor's unbounded output map and
// is used only to explain a missing required output that was omitted for size.
func enforceStepOutputContract(step PipelineStep, bounded, raw map[string]string) error {
	if err := checkStepOutputContract(step); err != nil {
		return err
	}
	contract := step.OutputContract
	if contract == nil || !outputShapeDeclared(contract.Shape) {
		return nil
	}
	var problems []string
	limit := maxStepOutputBytes()
	for _, name := range contract.Shape.Required {
		if _, ok := bounded[name]; ok {
			continue
		}
		if value, ok := raw[name]; ok && len(value) > limit {
			problems = append(problems, fmt.Sprintf("$.%s: required output was omitted because its %d-byte value exceeds the %d-byte step output limit", name, len(value), limit))
			continue
		}
		problems = append(problems, fmt.Sprintf("$.%s: required output is missing", name))
	}
	for _, name := range sortedShapeKeys(contract.Shape.Properties) {
		value, ok := bounded[name]
		if !ok {
			continue
		}
		problems = append(problems, outputValueProblems("$."+name, contract.Shape.Properties[name], value)...)
	}
	if len(problems) > 0 {
		return stepOutputContractError{stepID: step.ID, version: contract.Version, problems: capProblems(problems)}
	}
	return nil
}

// outputValueProblems interprets a named string output according to its
// declared type.
func outputValueProblems(path string, property JSONShapeProperty, value string) []string {
	switch property.Type {
	case "string":
		return stringValueProblems(path, property, value)
	case "boolean":
		if value != "true" && value != "false" {
			return []string{fmt.Sprintf("%s: expected boolean text \"true\" or \"false\", got %s", path, describeText(value))}
		}
		return nil
	case "number", "integer", "object", "array":
		decoded, err := decodeOutputJSON(value)
		if err != nil {
			return []string{fmt.Sprintf("%s: expected %s encoded as JSON text, got %s (%v)", path, property.Type, describeText(value), err)}
		}
		return jsonValueProblems(path, property, decoded)
	default:
		return []string{fmt.Sprintf("%s: unsupported type %q", path, property.Type)}
	}
}

func decodeOutputJSON(value string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("invalid JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return decoded, nil
}

func jsonValueProblems(path string, property JSONShapeProperty, value any) []string {
	switch property.Type {
	case "string":
		text, ok := value.(string)
		if !ok {
			return []string{typeMismatch(path, "string", value)}
		}
		return stringValueProblems(path, property, text)
	case "boolean":
		if _, ok := value.(bool); !ok {
			return []string{typeMismatch(path, "boolean", value)}
		}
	case "number":
		if _, ok := value.(json.Number); !ok {
			return []string{typeMismatch(path, "number", value)}
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return []string{typeMismatch(path, "integer", value)}
		}
		if !integralNumber(number) {
			return []string{fmt.Sprintf("%s: expected integer, got non-integral number %s", path, number.String())}
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return []string{typeMismatch(path, "object", value)}
		}
		var problems []string
		for _, name := range property.Required {
			if _, ok := object[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s: required property is missing", childPath(path, name)))
			}
		}
		for _, name := range sortedShapeKeys(property.Properties) {
			child, ok := object[name]
			if !ok {
				continue
			}
			problems = append(problems, jsonValueProblems(childPath(path, name), property.Properties[name], child)...)
		}
		return problems
	case "array":
		items, ok := value.([]any)
		if !ok {
			return []string{typeMismatch(path, "array", value)}
		}
		var problems []string
		if property.MaxItems != nil && len(items) > *property.MaxItems {
			problems = append(problems, fmt.Sprintf("%s: array has %d items; maxItems is %d", path, len(items), *property.MaxItems))
		}
		if property.Items != nil {
			for index, item := range items {
				problems = append(problems, jsonValueProblems(path+"["+strconv.Itoa(index)+"]", *property.Items, item)...)
				if len(problems) > maxOutputContractViolations {
					break
				}
			}
		}
		return problems
	default:
		return []string{fmt.Sprintf("%s: unsupported type %q", path, property.Type)}
	}
	return nil
}

func stringValueProblems(path string, property JSONShapeProperty, value string) []string {
	var problems []string
	if property.MaxLength != nil {
		if length := utf8.RuneCountInString(value); length > *property.MaxLength {
			problems = append(problems, fmt.Sprintf("%s: string has %d characters; maxLength is %d", path, length, *property.MaxLength))
		}
	}
	if len(property.Enum) > 0 {
		allowed := false
		for _, candidate := range property.Enum {
			if candidate == value {
				allowed = true
				break
			}
		}
		if !allowed {
			problems = append(problems, fmt.Sprintf("%s: %s is not one of the allowed enum values", path, describeText(value)))
		}
	}
	return problems
}

func integralNumber(number json.Number) bool {
	if _, err := number.Int64(); err == nil {
		return true
	}
	value, err := number.Float64()
	return err == nil && !math.IsInf(value, 0) && value == math.Trunc(value)
}

func typeMismatch(path, want string, value any) string {
	return fmt.Sprintf("%s: expected %s, got %s", path, want, jsonTypeName(value))
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// describeText quotes a short prefix of an output value. Values are already
// redacted before validation; the prefix keeps error messages bounded.
func describeText(value string) string {
	const limit = 40
	if value == "" {
		return "empty text"
	}
	if utf8.RuneCountInString(value) <= limit {
		return strconv.Quote(value)
	}
	runes := []rune(value)
	return strconv.Quote(string(runes[:limit])) + "…"
}

func childPath(path, name string) string {
	if outputShapePathIdentifier.MatchString(name) {
		return path + "." + name
	}
	return path + "[" + strconv.Quote(name) + "]"
}

func capProblems(problems []string) []string {
	if len(problems) <= maxOutputContractViolations {
		return problems
	}
	out := append([]string(nil), problems[:maxOutputContractViolations]...)
	return append(out, fmt.Sprintf("and %d more", len(problems)-maxOutputContractViolations))
}

func sortedShapeKeys(properties map[string]JSONShapeProperty) []string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedRawKeys(node map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
