package factorycli

import (
	"iter"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An issue has one sandbox, which its triage, plan and fix share. factory
// labels it with the issue (LabelIssue) and records the repo in the "repo"
// annotation, whatever it names it, so repo-agent finds it by those and
// factory is free to rename it. Sandboxes made before the label carry
// factory's old name for it, fix-<repo>-<N>, and are found by that.
const (
	LabelIssue = "factory.gemini.google.com/issue"
	LabelRepo  = "factory.gemini.google.com/repo"

	// AnnotationTriageTaskState is where factory records triage's state
	// in the issue's sandbox. last-task-state and last-task-type stay the
	// plan's or fix's.
	AnnotationTriageTaskState = "sandbox.gemini.google.com/triage-task-state"
)

// IssueOf returns the issue of repo whose sandbox sb is: by its label,
// else by the fix-<repo>-<N> name.
func IssueOf(sb *unstructured.Unstructured, repo string) (int, bool) {
	if v := sb.GetLabels()[LabelIssue]; v != "" && sb.GetAnnotations()["repo"] == repo {
		n, err := strconv.Atoi(v)
		return n, err == nil && n > 0
	}
	rest, ok := strings.CutPrefix(sb.GetName(), "fix-"+repo+"-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

// IssueSandbox picks issue's sandbox of repo out of sandboxes, all in one
// namespace: the one labelled with it, else the one with the old name.
// Pass slices.Values or maps.Values.
func IssueSandbox(sandboxes iter.Seq[*unstructured.Unstructured], repo string, issue int) *unstructured.Unstructured {
	var byName *unstructured.Unstructured
	for sb := range sandboxes {
		if n, ok := IssueOf(sb, repo); !ok || n != issue {
			continue
		}
		if sb.GetLabels()[LabelIssue] != "" {
			return sb
		}
		byName = sb
	}
	return byName
}

// HasTriage reports whether a triage has run, or is running, in sb, or
// left a draft or a rejection there.
func HasTriage(sb *unstructured.Unstructured) bool {
	a := sb.GetAnnotations()
	return a[AnnotationTriageTaskState] != "" || a[AnnotationTriageDraft] != "" ||
		a["board.gemini.google.com/triaged-at"] != "" || a["board.gemini.google.com/triage-rejected-at"] != ""
}

// OnlyTriaged reports whether sb, an issue's sandbox, has had a triage and
// nothing else in it: no plan or fix to show, rerun or follow up on.
func OnlyTriaged(sb *unstructured.Unstructured) bool {
	return sb.GetAnnotations()[AnnotationTaskType] == "" && HasTriage(sb)
}

// AnnotationTriageDraft holds the board's triage draft. A triage-<repo>-<N>
// sandbox from before triage moved into the issue's sandbox keeps its
// draft in agentDraft (with agentDraftType=triage) instead: in the issue's
// sandbox agentDraft is a review's.
const AnnotationTriageDraft = "board.gemini.google.com/triage-draft"

// TriageDraft returns the triage draft stored on sb, wherever it is.
func TriageDraft(sb *unstructured.Unstructured) string {
	a := sb.GetAnnotations()
	if d := a[AnnotationTriageDraft]; d != "" {
		return d
	}
	if IsLegacyTriageSandbox(sb) || a["agentDraftType"] == "triage" {
		return a["agentDraft"]
	}
	return ""
}

// TriageState returns triage's task state in sb: its own annotation, or
// in a legacy triage-<repo>-<N> sandbox, last-task-state.
func TriageState(sb *unstructured.Unstructured) string {
	a := sb.GetAnnotations()
	if s := a[AnnotationTriageTaskState]; s != "" {
		return s
	}
	if IsLegacyTriageSandbox(sb) {
		return a[AnnotationTaskState]
	}
	return ""
}

// IsLegacyTriageSandbox reports whether sb is a triage-<repo>-<N> sandbox
// from before triage moved into the issue's sandbox.
func IsLegacyTriageSandbox(sb *unstructured.Unstructured) bool {
	return strings.HasPrefix(sb.GetName(), "triage-")
}

// TriageSandbox picks the sandbox holding issue's triage out of one
// namespace's sandboxes: the issue's sandbox once a triage has touched it,
// else a legacy triage-<repo>-<N> one. Nil when there is neither.
func TriageSandbox(sandboxes iter.Seq[*unstructured.Unstructured], repo string, issue int) *unstructured.Unstructured {
	if sb := IssueSandbox(sandboxes, repo, issue); sb != nil && HasTriage(sb) {
		return sb
	}
	legacy := LegacyTriageSandboxName(repo, issue)
	for sb := range sandboxes {
		if sb.GetName() == legacy {
			return sb
		}
	}
	return nil
}
