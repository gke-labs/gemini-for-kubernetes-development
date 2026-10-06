package factorycli

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const reviewTaskOutput = `apiVersion: factory.gemini.google.com/v1alpha1
kind: Review
target:
  url: https://github.com/o/repo/pull/7
  commit: 0123abcd
source:
  sandbox: review-repo-7
  task: recipe-review-20261005-120000-0001
  recipe: review
spec:
  body: Check the nil case.
`

// The script's apply proves it was handed the Review the run wrote.
const reviewFactory = `recipe) echo "Running recipe review..." ;;
sandbox) echo "Waiting for sandbox pod review-repo-7 to become ready..." >&2; cat <<'DOC'
` + reviewTaskOutput + `DOC
;;
apply) grep -q "kind: Review" "$3" && echo "Posted the pending review" ;;
*) exit 9 ;;`

// The review runs as a recipe, is read back by run name and posted as the
// pending review: a result without an error is a review on GitHub.
func TestStartReviewRunsTheRecipeAndPosts(t *testing.T) {
	bin, argsLog := fakeFactory(t, reviewFactory)
	p := &fakeProber{probe: TaskProbe{State: ProbeNone}}
	r := &Runner{Binary: bin, Prober: p, running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartReview("alice/review-repo-7", ReviewOptions{
		Namespace: "alice", SandboxName: "review-repo-7", PRURL: "https://github.com/o/repo/pull/7",
		RunName: "review/b/7/1", Engine: "claude", Image: "img:1", Instructions: []string{"skip vendor/", ""},
	})
	res := waitResult(t, r, "alice/review-repo-7")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	if !strings.Contains(res.Output, reviewBanner+"\n"+reviewTaskOutput) || !strings.Contains(res.Output, "Posted the pending review") {
		t.Errorf("output = %q, want the Review and what apply said", res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 3 {
		t.Fatalf("commands run = %q, want recipe, task output, apply", args)
	}
	for _, want := range []string{"--url https://github.com/o/repo/pull/7", "--namespace alice", "--abort-on-cancel=false", "--engine claude", "--image img:1", "--instruction skip vendor/ "} {
		if !strings.HasPrefix(args[0], "recipe review --run-name review/b/7/1 ") || !strings.Contains(args[0], want) {
			t.Errorf("recipe args = %q, want %q", args[0], want)
		}
	}
	if strings.Count(args[0], "--instruction") != 1 {
		t.Errorf("recipe args = %q: an empty instruction is not passed", args[0])
	}
	if !strings.HasPrefix(args[1], "sandbox task output review-repo-7 --namespace alice --run-name review/b/7/1") {
		t.Errorf("output args = %q", args[1])
	}
	if !strings.HasPrefix(args[2], "apply -f ") || !strings.Contains(args[2], "--action post-review") {
		t.Errorf("apply args = %q", args[2])
	}
	if p.taskType != "" {
		t.Errorf("probed %q: a review is resumed by its run name", p.taskType)
	}
}

// A post that fails fails the review, with what apply said.
func TestStartReviewPostFailure(t *testing.T) {
	bin, _ := fakeFactory(t, strings.Replace(reviewFactory, `apply) grep -q "kind: Review" "$3" && echo "Posted the pending review" ;;`,
		`apply) echo "Error: you already have a pending review on this PR"; exit 1 ;;`, 1))
	r := &Runner{Binary: bin, running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartReview("alice/review-repo-7", ReviewOptions{Namespace: "alice", SandboxName: "review-repo-7", PRURL: "https://github.com/o/repo/pull/7", RunName: "review/b/7/1"})
	res := waitResult(t, r, "alice/review-repo-7")
	if res.Err == nil || !strings.Contains(res.Output, "Error: you already have a pending review") {
		t.Fatalf("want apply's failure, got %v\n%s", res.Err, res.Output)
	}
}

// Update review revises in the review's session and posts what it writes.
func TestStartRevisePostsAReview(t *testing.T) {
	bin, argsLog := fakeFactory(t, reviewFactory)
	r := &Runner{Binary: bin, running: map[string]struct{}{}, results: map[string]Result{}}
	r.StartRevise("alice/revise-review-repo-7", ReviseOptions{
		Namespace: "alice", SandboxName: "review-repo-7", Revise: "review", Session: "recipe-review-1",
		RunName: "revise/b/review-repo-7/review/1", PostReview: true,
	})
	res := waitResult(t, r, "alice/revise-review-repo-7")
	if res.Err != nil {
		t.Fatalf("run: %v\n%s", res.Err, res.Output)
	}
	args := readArgs(t, argsLog)
	if len(args) != 3 || !strings.HasPrefix(args[0], "recipe revise review-repo-7 review ") || !strings.HasPrefix(args[2], "apply -f ") {
		t.Fatalf("commands run = %q, want revise, task output, apply", args)
	}
}

func TestReviewPROf(t *testing.T) {
	sb := func(name, repo string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetName(name)
		if repo != "" {
			u.SetAnnotations(map[string]string{"repo": repo})
		}
		return u
	}
	for _, tc := range []struct {
		sb   *unstructured.Unstructured
		want int
	}{
		{sb(ReviewSandboxName("repo", 7), "repo"), 7},
		{sb("review-repo-7", ""), 7},
		{sb("review-repo-extra-7", "repo-extra"), 0},
		{sb("review-repo-7", "other"), 0},
		{sb("factory-pr-repo-7", "repo"), 0},
		{sb("recipe-repo-7", "repo"), 0},
	} {
		n, ok := ReviewPROf(tc.sb, "repo")
		if (tc.want > 0) != ok || n != tc.want {
			t.Errorf("ReviewPROf(%s) = %d, %v; want %d", tc.sb.GetName(), n, ok, tc.want)
		}
	}
}
