package prs

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

var updateGolden = flag.Bool("update", false, "rewrite the handlers' golden task files")

// queueFor queues the task the engine decided for rule on the pull request,
// as the evaluation does, and reports whether it completed.
func queueFor(ctx context.Context, s *Scanner, pc *prContext, rule string, checks prCheckAnalysis, comments prCommentAnalysis) bool {
	pc.checks, pc.comments = checks, comments
	return s.queueRuleTask(ctx, pc, rule)
}

// Each PR rule queues the same task, field for field, whatever handler
// queues it: testdata/golden-<rule>.yaml holds what the handlers queued
// before they became one (the enqueue time aside, which is the clock's).
func TestHandlersQueueTheSameTasks(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := rules.LoadFiles(map[string][]byte{"preview.yaml": []byte("name: preview\non: pr\norder: 35\nwhen: {checks: green}\nrun: {recipe: summarize, apply: comment}\n")})
	if err != nil {
		t.Fatal(err)
	}
	set, err := rules.Overlay(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	commit := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	failed := commit.Add(10 * time.Minute)
	green := commit.Add(20 * time.Minute)
	comment := commit.Add(30 * time.Minute)
	pr := &githubv39.PullRequest{
		Number: githubv39.Int(10), Body: githubv39.String("## Review instructions\n- check the docs\n"),
		Base:      &githubv39.PullRequestBranch{Ref: githubv39.String("main")},
		CreatedAt: &commit, UpdatedAt: &comment,
	}
	prIssue := &githubv39.Issue{Number: githubv39.Int(10), CreatedAt: &commit, UpdatedAt: &comment,
		Labels: []*githubv39.Label{{Name: githubv39.String("factory")}, {Name: githubv39.String("factory/review")}}}
	checks := prCheckAnalysis{
		hasFailure: true, earliestFailureTime: failed, earliestFailureName: "unit", earliestFailureConclusion: "failure", failedCount: 2,
		checkRuns: []*githubv39.CheckRun{{Name: githubv39.String("unit"), CompletedAt: &githubv39.Timestamp{Time: green}}},
	}
	comments := prCommentAnalysis{hasNewComments: true, oldestCommentTime: comment, oldestCommentAuthor: "alice", oldestCommentType: "review comment", oldestCommentID: 42}

	for _, name := range []string{"comments", "comments-retry", "conflict", "ci", "review", "preview"} {
		rule, retry := strings.CutSuffix(name, "-retry")
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, queue := newTestScanner(t, dir, testOpts{BotUsers: []string{"bot1"}, TriggerLabel: "factory", Rules: control})
			comments := comments
			if retry {
				// Again after a run that failed on this head: no new
				// feedback, the last run's trigger and retry count.
				comments = prCommentAnalysis{}
				last := &api.QueueTask{Type: api.TypePRComments, Number: 10, RetryCount: 1, TriggerEventTime: failed, Status: api.StatusFailed}
				if err := queue.Enqueue("task-pr-10-comments.yaml", last); err != nil {
					t.Fatal(err)
				}
				fn, task, err := queue.ClaimNextCandidate()
				if err != nil || fn == "" {
					t.Fatalf("claim: %v", err)
				}
				if err := queue.StartTask(fn, task); err != nil {
					t.Fatal(err)
				}
				if err := queue.FailTask(fn, task, "boom"); err != nil {
					t.Fatal(err)
				}
			}
			r := set.Named(rule)
			s.engineCare = []rules.Decision{{Rule: rule, Kind: rules.OnPR, Number: 10, Outcome: rules.OutcomeRun, Recipe: r.Run.Recipe, Apply: r.Run.Apply, Inputs: r.Run.Inputs, Phase: r.Policy.Phase}}
			s.rec = s.startRecording(prIssue)
			pc := &prContext{pr: pr, prIssue: prIssue, headSHA: "0123456789abcdef", shortSHA: "0123456", lastCommitTime: commit,
				taskAssignee: "bot1", prURL: "https://github.com/test-owner/test-repo/pull/10",
				refIssues: &refIssues{loaded: true, resolved: []*githubv39.Issue{{Body: githubv39.String("Review: and the tests.\n")}}}}
			if !queueFor(context.Background(), s, pc, rule, checks, comments) {
				t.Fatal("not completed")
			}
			var file string
			var data []byte
			for _, fn := range []string{"task-pr-10-comments.yaml", "task-pr-10-iterate.yaml", "task-pr-10-investigate.yaml", "task-pr-10-review.yaml", api.RuleTaskFile(true, 10, rule)} {
				if b, err := os.ReadFile(filepath.Join(dir, "incoming", fn)); err == nil {
					file, data = fn, b
				}
			}
			if data == nil {
				t.Fatal("nothing queued")
			}
			var task api.QueueTask
			if err := yaml.Unmarshal(data, &task); err != nil {
				t.Fatal(err)
			}
			// Stamped when queued, as the runner would have named it from
			// the task: the enqueue clock masked, as the time is.
			unstamped := task
			unstamped.RunName = ""
			if want := api.RunNameOf(&unstamped); task.RunName != want {
				t.Errorf("run name %q, want %q", task.RunName, want)
			}
			task.RunName = strings.TrimSuffix(task.RunName, fmt.Sprintf("-%d", task.EnqueuedAt.Unix())) + "-<enqueued>"
			task.EnqueuedAt = time.Time{}
			got, err := yaml.Marshal(struct {
				File string         `yaml:"file"`
				Task *api.QueueTask `yaml:"task"`
			}{file, &task})
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden-"+name+".yaml")
			if *updateGolden {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("queued\n%s\nwant\n%s", got, want)
			}
		})
	}
}
