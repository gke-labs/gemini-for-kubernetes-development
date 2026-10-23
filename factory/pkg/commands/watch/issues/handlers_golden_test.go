package issues

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

var updateGolden = flag.Bool("update", false, "rewrite the issue handlers' golden task files")

// Each issue rule queues the same task, field for field, whatever handler
// queues it: testdata/golden-<case>.yaml holds what the scanner queued
// before its handlers became one (the enqueue time aside, the clock's).
func TestIssueHandlersQueueTheSameTasks(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	issue := func(num int, body string, labelled bool, assignee string) *githubv39.Issue {
		is := &githubv39.Issue{Number: githubv39.Int(num), Body: githubv39.String(body), CreatedAt: &created, UpdatedAt: &updated,
			User: &githubv39.User{Login: githubv39.String("someone")}}
		if labelled {
			is.Labels = []*githubv39.Label{{Name: githubv39.String("factory")}}
		}
		if assignee != "" {
			is.Assignees = []*githubv39.User{{Login: githubv39.String(assignee)}}
		}
		return is
	}
	for _, tc := range []struct {
		name, file string
		issue      *githubv39.Issue
		repo       map[string][]byte
	}{
		{"fix", "task-issue-7-rule-fix.yaml", issue(7, "Fix the thing.", true, ""), nil},
		{"fix-assigned", "task-issue-9-rule-fix.yaml", issue(9, "Fix the other thing.", false, "bot1"), nil},
		{"workflow", "task-workflow-greenfield-issue-12.yaml", issue(12, "Run this: .agents/workflows/greenfield.md", true, ""), nil},
		{"repo-rule", "task-issue-7-rule-triage-2.yaml", issue(7, "Fix the thing.", true, ""),
			map[string][]byte{"triage-2.yaml": []byte("name: triage-2\non: issue\norder: 1\nwhen: {labels: {all: [factory]}}\nrun: {recipe: triage}\n")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := base
			if tc.repo != nil {
				repo, err := rules.LoadFiles(tc.repo)
				if err != nil {
					t.Fatal(err)
				}
				if set, err = rules.Overlay(base, repo); err != nil {
					t.Fatal(err)
				}
			}
			control, err := rules.NewControl(nil, set)
			if err != nil {
				t.Fatal(err)
			}
			fake := &workflowGitHub{contents: map[string]string{".agents/workflows/greenfield.md": "---\nname: greenfield\nmode: workflow\n---\nprompt\n"}}
			s, queue, _ := newScanner(t, Config{TargetAssignee: "bot1", BotUsers: []string{"bot1"}}, Deps{
				GitHub:   fake.client(t),
				Recorder: rules.NewRecorder(filepath.Join(t.TempDir(), "d.jsonl"), 0, nil),
				Rules:    control,
			})
			s.queueTasks(context.Background(), []*githubv39.Issue{tc.issue}, map[int]bool{})
			task := queue.enqueued[tc.file]
			if task == nil {
				t.Fatalf("queued %v, want %s", keys(queue.enqueued), tc.file)
			}
			masked := *task
			masked.EnqueuedAt = time.Time{}
			got, err := yaml.Marshal(struct {
				File   string         `yaml:"file"`
				Task   *api.QueueTask `yaml:"task"`
				Labels []string       `yaml:"labelled,omitempty"`
			}{tc.file, &masked, fake.labels})
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden-"+tc.name+".yaml")
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
