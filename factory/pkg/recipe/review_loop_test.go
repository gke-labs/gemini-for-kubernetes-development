package recipe

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// loopExec runs review-loop's own checks for real - the verdict greps and
// the lease update, in a shell, in the task directory - and stands in for
// the rest: the PR's checkout (git) succeeds, and each push "pushes" a new
// head, as push.json says.
type loopExec struct {
	taskDir string
	ran     []string
	pushes  int
}

func (e *loopExec) Uses(_ context.Context, name string, _, _ map[string]string, _ io.Writer) error {
	e.ran = append(e.ran, "uses:"+name)
	if name == "push" {
		e.pushes++
		return os.WriteFile(filepath.Join(e.taskDir, "push.json"),
			[]byte(fmt.Sprintf(`{"fork":"bot/repo","branch":"b","base":"base0","head":"head%d"}`+"\n", e.pushes)), 0o644)
	}
	return nil
}

func (e *loopExec) Run(ctx context.Context, script string, _ map[string]string, log io.Writer) (int, error) {
	e.ran = append(e.ran, "run")
	if !strings.HasPrefix(script, "grep ") && !strings.HasPrefix(script, "head=") {
		return 0, nil // the checkout and the sleep
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "TASK_DIR="+e.taskDir)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

var outputPathRE = regexp.MustCompile(`(/\S+/review-\d\.yaml)`)

// reviewer is a step session that, asked for its findings, writes the
// next verdict of its round to the file the ask names.
type reviewer struct {
	verdict string
	prompts []string
}

func (s *reviewer) Ask(_ context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if m := outputPathRE.FindString(prompt); m != "" {
		body := "verdict: clean\nfindings: []\n"
		if s.verdict == "changes" {
			body = "verdict: changes\nfindings:\n  - path: a.go\n    problem: p\n    fix: f\n"
		}
		if err := os.WriteFile(m, []byte(body), 0o644); err != nil {
			return "", err
		}
		return "written", nil
	}
	return "reviewed", nil
}

func (s *reviewer) Close() error { return nil }

// runReviewLoop runs the built-in review-loop with reviewers whose rounds
// say verdicts (in order), and reports what the fixer was asked, the
// pushes, the reviewers started and the lease left.
func runReviewLoop(t *testing.T, verdicts ...string) (fixerAsks int, pushes int, reviewers []string, lease string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	_, rec, err := Builtin("review-loop")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"base": "base0\n", "pr": "7", "lease": "head0"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ex := &loopExec{taskDir: dir}
	fixer := &fakeSession{}
	round := 0
	r := &Runner{
		Exec:         ex,
		StartSession: func(context.Context) (Session, error) { return fixer, nil },
		StartNamed: func(_ context.Context, name string, _ int) (Session, string, error) {
			reviewers = append(reviewers, name)
			v := "clean"
			if round < len(verdicts) {
				v = verdicts[round]
			}
			round++
			return &reviewer{verdict: v}, "m", nil
		},
		TaskDir: dir,
		Inputs: map[string]string{
			"repo_owner": "o", "repo_name": "r", "pr_title": "T", "pr_body": "B",
			"instructions": "be strict", "disclose": "false",
		},
		Log: io.Discard,
	}
	if err := r.Run(context.Background(), rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	l, _ := os.ReadFile(filepath.Join(dir, "lease"))
	return len(fixer.prompts), ex.pushes, reviewers, string(l)
}

// The loop stops at the first clean review: a clean first round fixes and
// pushes nothing; each round that finds something is fixed and pushed, the
// next push leased against the head it pushed; after a third round's
// findings it fixes, pushes and stops.
func TestReviewLoopRounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verdicts  []string
		fixes     int
		pushes    int
		reviewers string
		lease     string
	}{
		{"clean at once", []string{"clean"}, 0, 0, "review-1", "head0"},
		{"clean after one fix", []string{"changes", "clean"}, 1, 1, "review-1,review-2", "head1"},
		{"clean after two fixes", []string{"changes", "changes", "clean"}, 2, 2, "review-1,review-2,review-3", "head2"},
		{"findings to the end", []string{"changes", "changes", "changes"}, 3, 3, "review-1,review-2,review-3", "head2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixes, pushes, reviewers, lease := runReviewLoop(t, tc.verdicts...)
			if fixes != tc.fixes || pushes != tc.pushes || strings.Join(reviewers, ",") != tc.reviewers || lease != tc.lease {
				t.Errorf("fixes %d, pushes %d, reviewers %v, lease %q; want %d, %d, %s, %q",
					fixes, pushes, reviewers, lease, tc.fixes, tc.pushes, tc.reviewers, tc.lease)
			}
		})
	}
}

// Each round's reviewer is fresh (a session of its own), reads the change
// against the PR's base, gets the review instructions, and - after the
// first round - the earlier findings to check.
func TestReviewLoopReviewerPrompts(t *testing.T) {
	_, rec, err := Builtin("review-loop")
	if err != nil {
		t.Fatal(err)
	}
	if rec.SessionName() != "care" {
		t.Errorf("the fixer continues %q, want the PR's care conversation", rec.SessionName())
	}
	var sessions []string
	for _, s := range rec.Start.Steps {
		if s.Session != "" && (len(sessions) == 0 || sessions[len(sessions)-1] != s.Session) {
			sessions = append(sessions, s.Session)
		}
		if s.Session != "" && strings.Contains(s.Ask, "pull request") && strings.Contains(s.Ask, "git diff") {
			if !strings.Contains(s.Ask, `{{ file "base" }}...HEAD`) || !strings.Contains(s.Ask, ".Inputs.instructions") {
				t.Errorf("a review ask does not read the change against the base with the instructions:\n%s", s.Ask)
			}
		}
	}
	if strings.Join(sessions, ",") != "review-1,review-2,review-3" {
		t.Errorf("reviewer sessions %v, want a fresh one per round", sessions)
	}
}
