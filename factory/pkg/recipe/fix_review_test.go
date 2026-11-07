package recipe

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reviewExec runs fix's review checks for real - whether the step
// committed, and the reviewer's verdict - in a git checkout, with the task
// directory; every other step stands in as done.
type reviewExec struct {
	repo, taskDir string
}

func (e *reviewExec) Uses(context.Context, string, map[string]string, map[string]string, io.Writer) error {
	return nil
}

func (e *reviewExec) Run(ctx context.Context, script string, _ map[string]string, log io.Writer) (int, error) {
	if !strings.HasPrefix(script, "base=") && !strings.HasPrefix(script, "grep ") {
		return 0, nil
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Dir = e.repo
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

var outputRE = regexp.MustCompile(`(/\S+/(?:review|change)\.yaml)`)

// writesOutputs is an agent that, asked to write review.yaml or
// change.yaml, writes body there.
func writesOutputs(t *testing.T, body func(name string) string) func(int, string) {
	return func(_ int, prompt string) {
		if m := outputRE.FindString(prompt); m != "" {
			if err := os.WriteFile(m, []byte(body(filepath.Base(m))), 0o644); err != nil {
				t.Error(err)
			}
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A fix of an issue that wants a review reviews its own change once
// before it pushes: a fresh reviewer (a step session) reviews the step's
// commits alone, from the commit it started on, with the issue's review
// instructions, and the agent addresses what it found. An issue that
// wants none, or a fix that committed nothing, gets no review.
func TestFixReviewRound(t *testing.T) {
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	_, rec, err := Builtin("fix")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		review, verdict string
		commit          bool
		reviewers       int
		addressed       bool
	}{
		{"no review wanted", "false", "changes", true, 0, false},
		{"nothing committed", "true", "changes", false, 0, false},
		{"reviewed clean", "true", "clean", true, 1, false},
		{"reviewed, findings addressed", "true", "changes", true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, taskDir := t.TempDir(), t.TempDir()
			git(t, repo, "init", "-q")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "earlier step")
			base := git(t, repo, "rev-parse", "HEAD")
			for name, body := range map[string]string{"base": base + "\n", "comments.md": ""} {
				if err := os.WriteFile(filepath.Join(taskDir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.commit {
				git(t, repo, "commit", "-q", "--allow-empty", "-m", "this step")
			}
			fixer := &fakeSession{}
			fixer.onAsk = writesOutputs(t, func(string) string { return "change:\n  title: \"t\"\n  body: |\n    Fixes #7\n" })
			var reviewers []*fakeSession
			r := &Runner{
				Exec:         &reviewExec{repo: repo, taskDir: taskDir},
				StartSession: func(context.Context) (Session, error) { return fixer, nil },
				StartNamed: func(_ context.Context, name string, _ int) (Session, string, error) {
					if name != "review" {
						t.Errorf("step session %q", name)
					}
					s := &fakeSession{}
					s.onAsk = writesOutputs(t, func(string) string {
						if tc.verdict == "clean" {
							return "verdict: clean\nfindings: []\n"
						}
						return "verdict: changes\nfindings:\n  - path: a.go\n    problem: p\n    fix: f\n"
					})
					reviewers = append(reviewers, s)
					return s, "m", nil
				},
				TaskDir: taskDir,
				Inputs: map[string]string{
					"issue_url": "https://github.com/o/r/issues/7", "issue_number": "7", "issue_title": "T", "issue_body": "B",
					"disclose": "false", "review": tc.review, "review_instructions": "Use the skill X.",
					"plan": "", "instructions": "", "group_pr": "", "labels": "", "draft": "true",
				},
				Log: io.Discard,
			}
			if err := r.Run(context.Background(), rec); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(reviewers) != tc.reviewers {
				t.Fatalf("%d reviewers, want %d", len(reviewers), tc.reviewers)
			}
			if tc.reviewers > 0 {
				p := reviewers[0].prompts[0]
				if !strings.Contains(p, "git diff "+base+"...HEAD") || !strings.Contains(p, "Use the skill X.") {
					t.Errorf("the reviewer was not given this step's commits and the issue's instructions:\n%s", p)
				}
			}
			addressed := false
			for _, p := range fixer.prompts {
				if strings.Contains(p, "A reviewer looked at your change") {
					addressed = true
				}
			}
			if addressed != tc.addressed {
				t.Errorf("findings addressed: %t, want %t", addressed, tc.addressed)
			}
		})
	}
}
