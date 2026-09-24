package controlplane

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Grounded model steps.
//
// A generic model step becomes grounded when it declares the input
// grounding_item_count, normally bound to a source step's item_count output.
// With zero items the model is not called: the step succeeds with an explicit
// no-sources notice and generated=false, so a run never produces a draft that
// implies unseen knowledge. With items, the response is checked for citations
// of the form [S1] against grounding_source_ids and the result is published as
// outputs so later steps can show which claims cite collected material.

const (
	modelGroundingItemCountInput = "grounding_item_count"
	modelGroundingSourceIDsInput = "grounding_source_ids"
	modelGenerationSkippedEvent  = "model.generation.skipped"

	// GroundedModelNoSourcesNotice is the content of a grounded model step
	// that had no collected source items.
	GroundedModelNoSourcesNotice = "No sources collected. The model was not called, so this run has no generated draft."
)

var (
	sourceCitationGroupPattern = regexp.MustCompile(`\[([^\[\]]{1,200})\]`)
	sourceCitationIDPattern    = regexp.MustCompile(`^S[1-9][0-9]{0,5}$`)
)

type modelGrounding struct {
	itemCount int
	sourceIDs map[string]bool
}

func parseModelGrounding(stepID string, inputs map[string]string) (modelGrounding, bool, error) {
	raw, ok := inputs[modelGroundingItemCountInput]
	if !ok {
		return modelGrounding{}, false, nil
	}
	count, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || count < 0 {
		return modelGrounding{}, true, sourceCollectionFailure{reason: "model step \"" + stepID + "\": " + modelGroundingItemCountInput + " " + strconv.Quote(raw) + " must be a non-negative integer", retryable: false}
	}
	grounding := modelGrounding{itemCount: count, sourceIDs: map[string]bool{}}
	for _, id := range splitCSV(inputs[modelGroundingSourceIDsInput]) {
		grounding.sourceIDs[id] = true
	}
	return grounding, true, nil
}

// outputs reports the grounding outputs for a response.
func (g modelGrounding) outputs(content string, generated bool) map[string]string {
	cited, unknown := g.citations(content)
	return map[string]string{
		"generated":            strconv.FormatBool(generated),
		"grounding_item_count": strconv.Itoa(g.itemCount),
		"cited_sources":        strings.Join(cited, ","),
		"unknown_citations":    strings.Join(unknown, ","),
	}
}

// citations returns the distinct source IDs cited as [S1] or [S1, S2], split
// into IDs that were collected and IDs that match no collected item.
func (g modelGrounding) citations(content string) ([]string, []string) {
	seen := map[string]bool{}
	var cited, unknown []string
	for _, group := range sourceCitationGroupPattern.FindAllStringSubmatch(content, -1) {
		for _, part := range strings.FieldsFunc(group[1], func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			if !sourceCitationIDPattern.MatchString(part) || seen[part] {
				continue
			}
			seen[part] = true
			if g.sourceIDs[part] {
				cited = append(cited, part)
			} else {
				unknown = append(unknown, part)
			}
		}
	}
	sortSourceIDs(cited)
	sortSourceIDs(unknown)
	return cited, unknown
}

func sortSourceIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		left, _ := strconv.Atoi(strings.TrimPrefix(ids[i], "S"))
		right, _ := strconv.Atoi(strings.TrimPrefix(ids[j], "S"))
		return left < right
	})
}

func skippedGroundedModelStep(step PipelineStep, model string, grounding modelGrounding) PipelineStepExecution {
	content := GroundedModelNoSourcesNotice
	outputs := map[string]string{
		"content":        content,
		"content_bytes":  strconv.Itoa(len(content)),
		"content_sha256": stepOutputDigest(content),
		"model":          model,
		"artifact_name":  step.ID + ".md",
	}
	for name, value := range grounding.outputs("", false) {
		outputs[name] = value
	}
	return PipelineStepExecution{
		Summary:   "Skipped model generation: no sources collected",
		Artifacts: []CreateArtifactInput{{Name: step.ID + ".md", Kind: "markdown", URI: inlineArtifactURI("text/markdown;charset=utf-8", content)}},
		Inputs:    map[string]string{"model": model, modelGroundingItemCountInput: "0", "model_called": "false"},
		Events:    []WorkflowExecutionEvent{{Type: modelGenerationSkippedEvent, Message: "step " + step.ID + ": no source items were collected; the model was not called"}},
		Outputs:   outputs,
	}
}
