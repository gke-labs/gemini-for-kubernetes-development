package prs

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
	"gopkg.in/yaml.v3"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// One evaluation of a pull request does the same, call for call and write
// for write, whatever carries out the engine's decisions:
// testdata/walk-<case>.yaml holds what the scanner did before its write
// rules were carried out by the walk (the GitHub calls in order, the
// labels added, the task queued, the decisions recorded).
func TestEvaluate_WalkGolden(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	// inactive made to hold on any pool PR (the fixture's activity is
	// always now), still on its history.
	alwaysInactive := func(t *testing.T) rules.Set {
		var inactive rules.Rule
		for _, r := range base {
			if r.Name == "inactive" {
				inactive = *r
			}
		}
		inactive.When.InactiveFor = rules.Duration{Duration: time.Nanosecond}
		set, err := rules.Overlay(base, []*rules.Rule{&inactive})
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	no := false
	cases := []struct {
		name   string
		set    func(*testing.T) rules.Set
		f      *prFixture
		active bool
	}{
		{name: "ready", f: &prFixture{body: "Fixes #7"}},
		{name: "inherit-labels", f: &prFixture{body: "Fixes #7", refLabels: []string{"area/kcc", "factory/review"}}},
		{name: "inherit-stop", f: &prFixture{body: "Fixes #7", refLabels: []string{"factory/stop"}}},
		{name: "inactive", set: alwaysInactive, f: &prFixture{body: "Fixes #7"}},
		{name: "task-active", f: &prFixture{body: "Fixes #7", refLabels: []string{"area/kcc"}}, active: true},
		{name: "ci", f: &prFixture{failing: true}},
		{name: "pending", f: &prFixture{pending: true}},
		{name: "conflict", f: &prFixture{mergeable: &no}},
		{name: "comments", f: &prFixture{comments: []*githubv39.IssueComment{{
			ID: githubv39.Int64(5), Body: githubv39.String("please rename this"),
			User:              &githubv39.User{Login: githubv39.String("alice")},
			AuthorAssociation: githubv39.String("MEMBER"),
			CreatedAt:         &[]time.Time{time.Now().Add(time.Hour)}[0],
		}}}},
		{name: "ready-already", f: &prFixture{readyForHuman: true}},
	}
	sha := regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z?`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := base
			if tc.set != nil {
				set = tc.set(t)
			}
			control, err := rules.NewControl(nil, set)
			if err != nil {
				t.Fatal(err)
			}
			f := tc.f
			f.num, f.headSHA, f.updated = 30, "sha-30", time.Now()
			dir := t.TempDir()
			record := filepath.Join(dir, "d.jsonl")
			s, queue := newTestScanner(t, filepath.Join(dir, "q"), testOpts{
				GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "watcher", TriggerLabel: "factory",
				AllowlistedUsers: []string{"alice"}, Recorder: rules.NewRecorder(record, 0, nil), Rules: control,
			})
			if tc.active {
				if err := queue.Enqueue(api.RuleTaskFile(true, 30, "ci"), &api.QueueTask{Type: api.TypePRRule, Number: 30, Rule: "ci", Recipe: "care-ci"}); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			listed := f.listingIssueLocked()
			f.mu.Unlock()
			s.evaluate(context.Background(), listed)

			var decided []string
			gate := ""
			if fh, err := os.Open(record); err == nil {
				sc := bufio.NewScanner(fh)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				for sc.Scan() {
					var rec rules.Record
					if json.Unmarshal(sc.Bytes(), &rec) == nil {
						decided = append(decided, rec.Decided...)
						if rec.Gate != "" {
							gate = rec.Gate
						}
					}
				}
				fh.Close()
			}
			var queued []string
			for _, d := range []string{"incoming", "processing"} {
				entries, _ := os.ReadDir(filepath.Join(dir, "q", d))
				for _, e := range entries {
					queued = append(queued, d+"/"+e.Name())
				}
			}
			slices.Sort(queued)
			f.mu.Lock()
			calls := slices.Clone(f.calls)
			added := slices.Clone(f.added)
			f.mu.Unlock()
			for i, c := range calls {
				calls[i] = sha.ReplaceAllString(c, "<time>")
			}
			got, err := yaml.Marshal(map[string]any{
				"calls": calls, "added": added, "queued": queued, "decided": decided, "gate": gate,
			})
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "walk-"+tc.name+".yaml")
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
				t.Errorf("evaluated\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// A repository's write rule is carried out where its order and its facts
// put it: before inactive on the PR's own facts - so on a PR whose task is
// active too, where the evaluation stops before the history - and after
// the care rules once everything is read.
func TestEvaluate_RepositoryWriteRulesByOrder(t *testing.T) {
	base, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := rules.LoadFiles(map[string][]byte{
		"early.yaml": []byte("name: early\non: pr\norder: 3\nwhen: {author: pool}\nrun: {label: [seen]}\n"),
		"late.yaml":  []byte("name: late\non: pr\norder: 45\nwhen: {author: pool, checks: green}\nrun: {label: [green]}\n"),
	})
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
	for _, tc := range []struct {
		name   string
		active bool
		want   []string
	}{
		// The task active: early, on the listing and the PR alone; no
		// history or checks read.
		{"task active", true, []string{"seen"}},
		// Nothing active, green: early first, late with ready at the end.
		{"green", false, []string{"seen", "green", "factory/ready-for-human"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &prFixture{num: 40, headSHA: "sha-40", updated: time.Now()}
			dir := t.TempDir()
			s, queue := newTestScanner(t, dir, testOpts{
				GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "watcher", TriggerLabel: "factory",
				Recorder: rules.NewRecorder(filepath.Join(dir, "d.jsonl"), 0, nil), Rules: control,
			})
			if tc.active {
				if err := queue.Enqueue(api.RuleTaskFile(true, 40, "ci"), &api.QueueTask{Type: api.TypePRRule, Number: 40, Rule: "ci", Recipe: "care-ci"}); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			listed := f.listingIssueLocked()
			f.mu.Unlock()
			s.evaluate(context.Background(), listed)
			f.mu.Lock()
			defer f.mu.Unlock()
			if !slices.Equal(f.added, tc.want) {
				t.Errorf("added %v, want %v", f.added, tc.want)
			}
			if tc.active && slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "/commits") }) {
				t.Errorf("the history or checks read on a PR whose task is active: %v", f.calls)
			}
		})
	}
}

// A PR closed between the listing and the read (a human closing it with a
// comment) is closed: no task for the comment, nothing more read, the
// record gated.
func TestEvaluate_ClosedSinceTheListing(t *testing.T) {
	set, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	control, err := rules.NewControl(nil, set)
	if err != nil {
		t.Fatal(err)
	}
	f := &prFixture{num: 41, headSHA: "sha-41", updated: time.Now(), closed: true, comments: []*githubv39.IssueComment{{
		ID: githubv39.Int64(9), Body: githubv39.String("Closing as a duplicate"),
		User:              &githubv39.User{Login: githubv39.String("alice")},
		AuthorAssociation: githubv39.String("MEMBER"),
		CreatedAt:         &[]time.Time{time.Now().Add(time.Hour)}[0],
	}}}
	dir := t.TempDir()
	var rec rules.Record
	s, _ := newTestScanner(t, dir, testOpts{
		GitHub: f.start(t), BotUsers: []string{"bot1"}, GitHubLogin: "watcher", TriggerLabel: "factory",
		AllowlistedUsers: []string{"alice"}, Recorder: rules.NewSinkRecorder(func(r rules.Record) { rec = r }), Rules: control,
	})
	f.mu.Lock()
	listed := f.listingIssueLocked()
	f.mu.Unlock()
	s.evaluate(context.Background(), listed)
	if entries, _ := os.ReadDir(filepath.Join(dir, "incoming")); len(entries) > 0 {
		t.Errorf("queued %s for a closed PR", entries[0].Name())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "/comments") || strings.Contains(c, "/commits") }) {
		t.Errorf("read on after the PR was found closed: %v", f.calls)
	}
	if !rec.Item.Closed || len(rec.Decided) > 0 {
		t.Errorf("record: closed %t, decided %v", rec.Item.Closed, rec.Decided)
	}
}
