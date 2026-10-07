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

	// AnnotationRecipeTriageTaskState is where `factory recipe triage`
	// records triage's state in the issue's sandbox. last-task-state and
	// last-task-type stay the plan's or fix's.
	AnnotationRecipeTriageTaskState = "sandbox.gemini.google.com/recipe-triage-task-state"
)

// TriageTaskState is the state of the triage in a sandbox with
// annotations a.
func TriageTaskState(a map[string]string) string {
	return a[AnnotationRecipeTriageTaskState]
}

// IssueOf returns the issue of repo whose sandbox sb is: by its label,
// else by the fix-<repo>-<N> name, unless factory made it for PR N
// (PRFixSandbox), which no issue has.
func IssueOf(sb *unstructured.Unstructured, repo string) (int, bool) {
	if v := sb.GetLabels()[LabelIssue]; v != "" && sb.GetAnnotations()["repo"] == repo {
		n, err := strconv.Atoi(v)
		return n, err == nil && n > 0
	}
	rest, ok := strings.CutPrefix(sb.GetName(), "fix-"+repo+"-")
	if !ok || sb.GetLabels()[LabelPR] == rest {
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
	return TriageTaskState(a) != "" || a[AnnotationTriageOutput] != "" ||
		a["board.gemini.google.com/triaged-at"] != "" || a[RejectedAnnotation(AnnotationTriageRun)] != ""
}

// OnlyTriaged reports whether sb, an issue's sandbox, has had a triage and
// nothing else in it: no plan or fix to show, rerun or follow up on.
func OnlyTriaged(sb *unstructured.Unstructured) bool {
	return sb.GetAnnotations()[AnnotationTaskType] == "" && HasTriage(sb)
}

// TriageDraft returns the triage draft stored on sb, as a triage: block.
func TriageDraft(sb *unstructured.Unstructured) string {
	return Draft("Triage", sb.GetAnnotations()[AnnotationTriageOutput])
}

// NormalizeTriageDraft returns draft as the triage: block drafts are kept
// in. A Triage task output — whole, or after lines that are not YAML, as
// a harvest that read factory's stderr stored it — becomes its spec; any
// other draft is returned as it is.
func NormalizeTriageDraft(draft string) string {
	doc := draft
	if !strings.HasPrefix(doc, "apiVersion:") {
		i := strings.Index(doc, "\napiVersion:")
		if i < 0 {
			return draft
		}
		doc = doc[i+1:]
	}
	if t := triageFromTaskOutput(doc); t != "" {
		return t
	}
	return draft
}

// TriageState returns triage's task state in sb.
func TriageState(sb *unstructured.Unstructured) string {
	return TriageTaskState(sb.GetAnnotations())
}

// TriageSandbox picks the sandbox holding issue's triage out of
// sandboxes: an issue's sandbox a triage has touched — one with a draft
// first, then one running a triage, should several namespaces have one.
// Nil when there is none.
func TriageSandbox(sandboxes iter.Seq[*unstructured.Unstructured], repo string, issue int) *unstructured.Unstructured {
	rank := func(sb *unstructured.Unstructured) int {
		switch {
		case TriageDraft(sb) != "":
			return 2
		case TriageState(sb) == "Running":
			return 1
		}
		return 0
	}
	var best *unstructured.Unstructured
	for sb := range sandboxes {
		if n, ok := IssueOf(sb, repo); ok && n == issue && HasTriage(sb) {
			if best == nil || rank(sb) > rank(best) {
				best = sb
			}
		}
	}
	return best
}

// PRFixSandbox picks PR pr's fix sandbox of repo out of sandboxes, all in
// one namespace: the fix's that opened it, aliased to it, or the one
// factory made for it, fix-<repo>-<pr>. A PR's recipes run there, but a
// credentials: clone one's (RecipeSandboxName). Pass slices.Values or
// maps.Values.
func PRFixSandbox(sandboxes iter.Seq[*unstructured.Unstructured], repo string, pr int) *unstructured.Unstructured {
	for sb := range sandboxes {
		if strings.HasPrefix(sb.GetName(), "fix-") && sb.GetLabels()[LabelPR] == strconv.Itoa(pr) && sb.GetAnnotations()["repo"] == repo {
			return sb
		}
	}
	return nil
}
