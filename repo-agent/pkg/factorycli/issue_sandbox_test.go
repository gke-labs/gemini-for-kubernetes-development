package factorycli

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func testSandbox(name string, labels, annotations map[string]string) *unstructured.Unstructured {
	sb := &unstructured.Unstructured{}
	sb.SetName(name)
	sb.SetLabels(labels)
	sb.SetAnnotations(annotations)
	return sb
}

func TestIssueOf(t *testing.T) {
	for _, c := range []struct {
		sb     *unstructured.Unstructured
		want   int
		wantOK bool
	}{
		{testSandbox("fix-repo-7", nil, nil), 7, true},
		{testSandbox("fix-repo-x", nil, nil), 0, false},
		{testSandbox("fix-repo2-7", nil, nil), 0, false},
		{testSandbox("triage-repo-7", nil, nil), 0, false},
		// Labelled: the name does not matter, the repo does.
		{testSandbox("repo-7", map[string]string{LabelIssue: "7"}, map[string]string{"repo": "repo"}), 7, true},
		{testSandbox("repo-7", map[string]string{LabelIssue: "7"}, map[string]string{"repo": "other"}), 0, false},
	} {
		if n, ok := IssueOf(c.sb, "repo"); n != c.want || ok != c.wantOK {
			t.Errorf("IssueOf(%s) = %d, %v; want %d, %v", c.sb.GetName(), n, ok, c.want, c.wantOK)
		}
	}
}

func TestIssueSandboxPrefersTheLabel(t *testing.T) {
	byName := testSandbox("fix-repo-7", nil, nil)
	labelled := testSandbox("repo-7", map[string]string{LabelIssue: "7"}, map[string]string{"repo": "repo"})
	other := testSandbox("fix-repo-8", nil, nil)
	if got := IssueSandbox(slices.Values([]*unstructured.Unstructured{byName, labelled, other}), "repo", 7); got != labelled {
		t.Errorf("IssueSandbox = %v, want the labelled one", got.GetName())
	}
	if got := IssueSandbox(slices.Values([]*unstructured.Unstructured{byName, other}), "repo", 7); got != byName {
		t.Errorf("IssueSandbox = %v, want fix-repo-7", got)
	}
	if got := IssueSandbox(slices.Values([]*unstructured.Unstructured{other}), "repo", 7); got != nil {
		t.Errorf("IssueSandbox = %s, want nil", got.GetName())
	}
}

func TestTriageSandbox(t *testing.T) {
	untouched := testSandbox("fix-repo-7", map[string]string{LabelIssue: "7"}, map[string]string{"repo": "repo"})
	triaged := testSandbox("fix-repo-7", map[string]string{LabelIssue: "7"},
		map[string]string{"repo": "repo", AnnotationRecipeTriageTaskState: "Running"})

	if got := TriageSandbox(slices.Values([]*unstructured.Unstructured{untouched, triaged}), "repo", 7); got != triaged {
		t.Errorf("issue sandbox with a triage: got %s, want it", got.GetName())
	}
	if got := TriageSandbox(slices.Values([]*unstructured.Unstructured{untouched}), "repo", 7); got != nil {
		t.Errorf("no triage anywhere: got %s, want nil", got.GetName())
	}
	if !OnlyTriaged(triaged) || OnlyTriaged(untouched) {
		t.Error("OnlyTriaged wrong")
	}
	a := triaged.GetAnnotations()
	a[AnnotationTaskType] = "plan"
	triaged.SetAnnotations(a)
	if OnlyTriaged(triaged) {
		t.Error("OnlyTriaged with a plan in it")
	}
}

func TestTriageDraftAndState(t *testing.T) {
	for _, c := range []struct {
		sb           *unstructured.Unstructured
		draft, state string
	}{
		// The issue's sandbox: its own keys; agentDraft there is a review's.
		{testSandbox("fix-repo-7", nil, map[string]string{
			AnnotationTriageDraft: "new", AnnotationRecipeTriageTaskState: "Completed",
			"agentDraft": "review", AnnotationTaskState: "Running",
		}), "new", "Completed"},
		{testSandbox("fix-repo-7", nil, map[string]string{"agentDraft": "review", AnnotationTaskState: "Running"}), "", ""},
	} {
		if d := TriageDraft(c.sb); d != c.draft {
			t.Errorf("TriageDraft(%s %v) = %q, want %q", c.sb.GetName(), c.sb.GetAnnotations(), d, c.draft)
		}
		if s := TriageState(c.sb); s != c.state {
			t.Errorf("TriageState(%s %v) = %q, want %q", c.sb.GetName(), c.sb.GetAnnotations(), s, c.state)
		}
	}
}

func TestRunningClaims(t *testing.T) {
	for _, c := range []struct {
		name             string
		annotations      map[string]string
		prefix           string
		own, side, other bool
	}{
		{"plan running", map[string]string{AnnotationTaskState: "Running", AnnotationTaskType: "plan"}, "plan", true, false, false},
		{"plan sees triage", map[string]string{AnnotationTaskState: "Completed", AnnotationTaskType: "plan", AnnotationRecipeTriageTaskState: "Running"}, "plan", false, false, true},
		{"triage running", map[string]string{AnnotationTaskState: "Completed", AnnotationTaskType: "plan", AnnotationRecipeTriageTaskState: "Running"}, "recipe-triage", true, true, false},
		{"triage sees plan", map[string]string{AnnotationTaskState: "Running", AnnotationTaskType: "plan"}, "recipe-triage", false, false, true},
		{"idle", map[string]string{AnnotationTaskState: "Completed", AnnotationTaskType: "plan"}, "plan", false, false, false},
	} {
		own, side, other := runningClaims(c.annotations, c.prefix)
		if own != c.own || side != c.side || other != c.other {
			t.Errorf("%s: runningClaims = %v %v %v, want %v %v %v", c.name, own, side, other, c.own, c.side, c.other)
		}
	}
}
