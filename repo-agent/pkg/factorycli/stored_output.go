package factorycli

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The board keeps a run's task output on its sandbox whole, under one
// annotation per recorded run (OutputAnnotation): the draft is its spec,
// edits rewrite the spec, and factory apply takes it as it is. What has
// been applied of it is one more annotation per run (AppliedAnnotation):
// action → when. A new output of the run replaces both.
const boardAnnotationPrefix = "board.gemini.google.com/"

// The stored outputs, and what was applied of them, of the runs the board
// keeps drafts of: OutputAnnotation and AppliedAnnotation of their run
// annotations.
const (
	AnnotationTriageOutput  = boardAnnotationPrefix + "recipe-triage-output"
	AnnotationTriageApplied = boardAnnotationPrefix + "recipe-triage-applied"
	AnnotationPlanOutput    = boardAnnotationPrefix + "plan-output"
	AnnotationPlanApplied   = boardAnnotationPrefix + "plan-applied"
	// A research sandbox's Notes, the output of its Save notes revise.
	AnnotationNotesOutput  = boardAnnotationPrefix + "research-output"
	AnnotationNotesApplied = boardAnnotationPrefix + "research-applied"
)

// RunTaskType is the task type a run annotation is recorded for: what an
// apply names the run by.
func RunTaskType(runKey string) string {
	return strings.TrimSuffix(strings.TrimPrefix(runKey, runAnnotationPrefix), runAnnotationSuffix)
}

// RunAnnotation is the annotation a run of taskType is recorded under.
func RunAnnotation(taskType string) string {
	return runAnnotationPrefix + taskType + runAnnotationSuffix
}

// OutputAnnotation is where the board stores the task output of the run
// recorded under runKey.
func OutputAnnotation(runKey string) string {
	return boardAnnotationPrefix + RunTaskType(runKey) + "-output"
}

// AppliedAnnotation is where the board stamps the actions applied to the
// task output of the run recorded under runKey.
func AppliedAnnotation(runKey string) string {
	return boardAnnotationPrefix + RunTaskType(runKey) + "-applied"
}

// TaskOutputKind is a task output's kind, "" when doc is none.
func TaskOutputKind(doc string) string {
	var m struct {
		Kind string `yaml:"kind"`
	}
	if yaml.Unmarshal([]byte(doc), &m) != nil {
		return ""
	}
	return m.Kind
}

// ApplyDoc is a stored task output as factory apply takes it: as stored,
// edits included, with task as its source task and targetURL as its
// target when it names none, and spec's fields set over its spec's.
func ApplyDoc(doc, task, targetURL string, spec map[string]string) (string, error) {
	kind := TaskOutputKind(doc)
	root := headerNode(kind, doc)
	if root == nil {
		return "", fmt.Errorf("there is no task output to apply")
	}
	if m, _ := parseTaskOutputMeta(kind, doc); m.Source.Task == "" {
		setKey(root, "source", mapping("task", task))
	}
	if !hasTarget(root) {
		setKey(root, "target", mapping("url", targetURL))
	}
	if len(spec) > 0 {
		specNode := mapValue(root, "spec")
		if specNode == nil || specNode.Kind != yaml.MappingNode {
			return "", fmt.Errorf("the %s has no spec", kind)
		}
		for _, k := range slices.Sorted(maps.Keys(spec)) {
			setKey(specNode, k, scalar(spec[k]))
		}
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("encoding the %s: %w", kind, err)
	}
	return string(out), nil
}

// Applied is the actions applied to a stored output, from its applied
// annotation (appliedKey): action → RFC3339 time. Empty when none, or
// when the stamp does not parse.
func Applied(annotations map[string]string, appliedKey string) map[string]string {
	var applied map[string]string
	if s := annotations[appliedKey]; s != "" {
		_ = json.Unmarshal([]byte(s), &applied)
	}
	return applied
}

// IsApplied reports whether action was applied to a stored output.
func IsApplied(annotations map[string]string, appliedKey, action string) bool {
	return Applied(annotations, appliedKey)[action] != ""
}

// MarkApplied stamps action as applied at at, in annotations.
func MarkApplied(annotations map[string]string, appliedKey, action string, at time.Time) {
	applied := Applied(annotations, appliedKey)
	if applied == nil {
		applied = map[string]string{}
	}
	applied[action] = at.UTC().Format(time.RFC3339)
	b, _ := json.Marshal(applied)
	annotations[appliedKey] = string(b)
}

// Draft is the draft in a stored task output of kind, in the shape the
// board edits it in: a Triage's spec as a triage: block, a Plan's or
// Notes' markdown. "" for no output, or one of another kind.
func Draft(kind, doc string) string {
	switch kind {
	case "Triage":
		return triageFromTaskOutput(doc)
	case "Plan", "Notes":
		return markdownFromTaskOutput(kind, doc)
	}
	return ""
}

// PlanDraft is the plan draft stored in annotations, or "".
func PlanDraft(annotations map[string]string) string {
	return Draft("Plan", annotations[AnnotationPlanOutput])
}

// NotesDraft is the notes draft stored in annotations, or "".
func NotesDraft(annotations map[string]string) string {
	return Draft("Notes", annotations[AnnotationNotesOutput])
}

// WithDraft is a stored task output of kind with draft, as edited, as its
// spec. A Plan's or Notes' other spec fields stay.
func WithDraft(kind, doc, draft string) (string, error) {
	root := headerNode(kind, doc)
	if root == nil {
		return "", fmt.Errorf("there is no %s to edit", strings.ToLower(kind))
	}
	spec, err := draftSpec(kind, draft)
	if err != nil {
		return "", err
	}
	specNode := &yaml.Node{}
	if err := specNode.Encode(spec); err != nil {
		return "", fmt.Errorf("encoding the %s's spec: %w", kind, err)
	}
	if kind != "Triage" {
		if old := mapValue(root, "spec"); old != nil && old.Kind == yaml.MappingNode {
			setKey(old, "markdown", mapValue(specNode, "markdown"))
			specNode = old
		}
	}
	setKey(root, "spec", specNode)
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("encoding the %s: %w", kind, err)
	}
	return string(out), nil
}

// mapValue is key's value in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
