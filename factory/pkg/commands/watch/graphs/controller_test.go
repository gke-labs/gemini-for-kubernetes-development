package graphs

import (
	"context"
	"slices"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/graph/graphtest"
)

const plan = "**Implementation plan** · v1\n\n```yaml graph\nsteps:\n  - {id: a, title: A, body: a}\n  - {id: b, after: [a], title: B, body: b}\n```\n\n<!-- factory:comment key=plan v=1 -->"

func controller(f *graphtest.Fake, dryRun bool) *Controller {
	return New(Config{TriggerLabel: "overseer", GitHubLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}, DryRun: dryRun}, Deps{GitHub: f})
}

func childOf(f *graphtest.Fake, parent int, step string) int {
	for n, is := range f.Issues {
		if m, ok := graph.ParseChildMarker(is.Body); ok && m.Parent == parent && m.Step == step {
			return n
		}
	}
	return 0
}

// A sweep moves the labelled parents, and finds a planned issue whose
// graph started (its start block posted by the implement task's bot) and
// labels it a graph parent. A planned issue whose graph has not started is
// left alone.
func TestSweep(t *testing.T) {
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "Started", Body: "x", Author: "alice", Open: true, Labels: []string{"overseer/planned"}}
	f.Comment(100, "hopper-coder-bot", "NONE", plan)
	f.Comment(100, "hopper-coder-bot", "NONE", graph.Start{Source: graph.SourcePlan}.StartComment())
	f.Issues[101] = &graphtest.Issue{Title: "Planned only", Body: "x", Author: "alice", Open: true, Labels: []string{"overseer/planned"}}
	f.Comment(101, "hopper-coder-bot", "NONE", plan)
	f.Issues[102] = &graphtest.Issue{Title: "Not ours", Body: "x", Author: "alice", Open: true}

	controller(f, false).SyncOnce(context.Background())
	if childOf(f, 100, "a") == 0 || !slices.Contains(f.Issues[100].Labels, "overseer/graph") {
		t.Errorf("#100: a=#%d, labels %v", childOf(f, 100, "a"), f.Issues[100].Labels)
	}
	if childOf(f, 101, "a") != 0 || slices.Contains(f.Issues[101].Labels, "overseer/graph") || len(f.Issues[101].Comments) != 1 {
		t.Errorf("#101 (not started) was touched: %+v", f.Issues[101])
	}
	if len(f.Issues[102].Comments) != 0 {
		t.Error("#102 (no label) was touched")
	}
}

// A sweep leaves a stopped parent alone - no child opened, nothing
// written, not even its status - and moves it again once the stop label
// is off.
func TestSweepSkipsStoppedParents(t *testing.T) {
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "Stopped", Body: "x", Author: "alice", Open: true, Labels: []string{"overseer/graph", "overseer/stop"}}
	f.Comment(100, "hopper-coder-bot", "NONE", plan)
	f.Comment(100, "hopper-coder-bot", "NONE", graph.Start{Source: graph.SourcePlan}.StartComment())
	before := f.Writes

	controller(f, false).SyncOnce(context.Background())
	if childOf(f, 100, "a") != 0 || f.Writes != before {
		t.Errorf("a stopped parent was swept: a=#%d, %d writes", childOf(f, 100, "a"), f.Writes-before)
	}

	f.Issues[100].Labels = []string{"overseer/graph"}
	controller(f, false).SyncOnce(context.Background())
	if childOf(f, 100, "a") == 0 {
		t.Error("the parent was not swept once its stop label was off")
	}
}

// countingFake counts the lists of one account's issues a sweep asks for.
type countingFake struct {
	*graphtest.Fake
	byCreator int
}

func (f *countingFake) ListIssues(ctx context.Context, opts *githubv39.IssueListByRepoOptions) ([]*githubv39.Issue, *githubv39.Response, error) {
	if opts != nil && opts.Creator != "" {
		f.byCreator++
	}
	return f.Fake.ListIssues(ctx, opts)
}

// A sweep lists the bot's recent issues once, not once per parent, and
// every parent still moves.
func TestSweepListsBotIssuesOnce(t *testing.T) {
	f := &countingFake{Fake: graphtest.New()}
	for _, n := range []int{100, 101, 102} {
		f.Issues[n] = &graphtest.Issue{Title: "Started", Body: "x", Author: "alice", Open: true, Labels: []string{"overseer/graph"}}
		f.Comment(n, "hopper-coder-bot", "NONE", plan)
		f.Comment(n, "hopper-coder-bot", "NONE", graph.Start{Source: graph.SourcePlan}.StartComment())
	}
	c := New(Config{TriggerLabel: "overseer", GitHubLogin: "watcher", Pool: []string{"hopper-coder-bot", "watcher"}}, Deps{GitHub: f})

	c.SyncOnce(context.Background())
	if f.byCreator != 1 {
		t.Errorf("the bot's issues were listed %d times in one sweep, want 1", f.byCreator)
	}
	for _, n := range []int{100, 101, 102} {
		if childOf(f.Fake, n, "a") == 0 {
			t.Errorf("#%d got no child", n)
		}
	}

	// The next sweep lists them afresh.
	c.SyncOnce(context.Background())
	if f.byCreator != 2 {
		t.Errorf("after two sweeps the bot's issues were listed %d times, want 2", f.byCreator)
	}
}

func TestSweepDryRun(t *testing.T) {
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "Started", Body: "x", Author: "alice", Open: true, Labels: []string{"overseer/planned"}}
	f.Comment(100, "hopper-coder-bot", "NONE", plan)
	f.Comment(100, "hopper-coder-bot", "NONE", graph.Start{Source: graph.SourcePlan}.StartComment())
	before := f.Writes
	controller(f, true).SyncOnce(context.Background())
	if f.Writes != before {
		t.Errorf("a dry run wrote %d times", f.Writes-before)
	}
}

// A closed child wakes its parent; an issue with no graph marker wakes
// nothing.
func TestNudge(t *testing.T) {
	c := controller(graphtest.New(), false)
	child := &githubv39.Issue{Number: githubv39.Int(201), Body: githubv39.String("x\n\n" + graph.ChildInfo{Parent: 100, Source: "plan", Step: "a", Depth: 1}.Marker())}
	c.NudgeLinkedWorkflows(context.Background(), child)
	c.NudgeLinkedWorkflows(context.Background(), &githubv39.Issue{Number: githubv39.Int(5), Body: githubv39.String("no marker")})
	if got := len(c.wake); got != 1 {
		t.Fatalf("%d wakes, want 1", got)
	}
	if n := <-c.wake; n != 100 {
		t.Errorf("woke #%d, want #100", n)
	}
}

// A paused (draining) watcher syncs nothing.
func TestPaused(t *testing.T) {
	f := graphtest.New()
	f.Issues[100] = &graphtest.Issue{Title: "x", Body: "```factory-graph\nsource: plan\n```", Author: "alice", Open: true, Labels: []string{"overseer/graph"}}
	c := New(Config{TriggerLabel: "overseer", GitHubLogin: "watcher"}, Deps{GitHub: f, Paused: func() bool { return true }})
	before := f.Writes
	c.SyncOnce(context.Background())
	if f.Writes != before {
		t.Error("a draining watcher synced")
	}
}
