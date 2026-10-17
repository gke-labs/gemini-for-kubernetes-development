package prs

import (
	"context"
	"net/url"
	"slices"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/scan/scantest"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

func seedPRs(repo *scantest.Repo) {
	now := time.Now()
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	repo.Put(scantest.Item{Number: 30, PR: true, Assignees: []string{"bot1"}, Author: "bot1", Updated: ago(5)})
	repo.Put(scantest.Item{Number: 31, PR: true, Assignees: []string{"bot1"}, Author: "bot1", Updated: ago(15)})
	repo.Put(scantest.Item{Number: 32, PR: true, Assignees: []string{"bot1"}, Author: "bot1", Updated: ago(25)}) // off bot1's first page
	repo.Put(scantest.Item{Number: 33, Assignees: []string{"bot1"}, Author: "alice", Updated: ago(1)})           // an issue, on bot1's page
	repo.Put(scantest.Item{Number: 34, PR: true, Labels: []string{"overseer"}, Author: "bot2", Updated: ago(35)})
	repo.Put(scantest.Item{Number: 35, PR: true, Assignees: []string{"bot2"}, Labels: []string{"overseer"}, Author: "bot2", Updated: ago(45)})
	repo.Put(scantest.Item{Number: 36, PR: true, Author: "carol", Updated: ago(2)}) // not the watcher's
	repo.Put(scantest.Item{Number: 37, PR: true, State: "closed", Assignees: []string{"bot1"}, Author: "bot1", Updated: ago(1)})
}

func prNumbers(items []*githubv39.Issue) []int {
	var out []int
	for _, it := range items {
		out = append(out, it.GetNumber())
	}
	slices.Sort(out)
	return out
}

// With the shared scan on, the pull request scanner's sweep and fast pass get
// the same candidates as from its own listings.
func TestSharedScanServesTheSameCandidates(t *testing.T) {
	candidates := func(shared bool) (sweep, fast []int, calls int) {
		repo := scantest.NewRepo("o", "r")
		seedPRs(repo)
		srv := repo.Server()
		defer srv.Close()
		gh := githubv39.NewClient(nil)
		gh.BaseURL, _ = url.Parse(srv.URL + "/")
		client := github.ForRepo(gh, "o", "r")
		cfg := Config{TriggerLabel: "overseer", BotUsers: []string{"bot1", "bot2"}, GitHubLogin: "watcher", ScanLimit: 3}
		deps := Deps{GitHub: client}
		if shared {
			deps.Listings = scan.New(scan.Config{Repo: "o/r", TriggerLabel: "overseer", BotUsers: cfg.BotUsers, GitHubLogin: "watcher"}, client)
		}
		s := New(cfg, deps)
		ctx := context.Background()
		sw, err := s.scanCandidates(ctx)
		if err != nil {
			t.Fatal(err)
		}
		fa, err := s.scanAssigned(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return prNumbers(sw), prNumbers(fa), repo.ListCalls()
	}
	offSweep, offFast, offCalls := candidates(false)
	onSweep, onFast, onCalls := candidates(true)
	if !slices.Equal(offSweep, onSweep) {
		t.Errorf("sweep candidates differ: own %v, shared %v", offSweep, onSweep)
	}
	if !slices.Equal(offFast, onFast) {
		t.Errorf("fast candidates differ: own %v, shared %v", offFast, onFast)
	}
	if !slices.Equal(offSweep, []int{30, 31, 32, 34, 35}) || slices.Contains(offFast, 32) {
		t.Errorf("the scenario is not what it should be: sweep %v, fast %v", offSweep, offFast)
	}
	t.Logf("listing calls for a sweep and a fast pass: own listings %d, shared scan %d", offCalls, onCalls)
}
