package prs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// adoptionServer serves the issues named in issues, 404s everything else, and
// records the labels added to each pull request.
//
// The 404 is as much a fixture as the issues are: references are parsed out of
// branch names and titles as well as bodies, so an adoption pass routinely asks
// about numbers that were never issues.
func adoptionServer(t *testing.T, issues map[int]*githubv39.Issue) (*githubv39.Client, func() map[int][]string) {
	t.Helper()

	var mu sync.Mutex
	added := make(map[int][]string)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path

		if r.Method == http.MethodPost && strings.HasSuffix(path, "/labels") {
			parts := strings.Split(strings.TrimSuffix(path, "/labels"), "/")
			num, _ := strconv.Atoi(parts[len(parts)-1])

			body, _ := io.ReadAll(r.Body)
			var labels []string
			_ = json.Unmarshal(body, &labels)

			mu.Lock()
			added[num] = append(added[num], labels...)
			mu.Unlock()

			_ = json.NewEncoder(w).Encode([]*githubv39.Label{})
			return
		}

		if strings.Contains(path, "/issues/") {
			parts := strings.Split(path, "/")
			num, _ := strconv.Atoi(parts[len(parts)-1])
			if issue, ok := issues[num]; ok {
				_ = json.NewEncoder(w).Encode(issue)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
			return
		}

		_ = json.NewEncoder(w).Encode([]interface{}{})
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")

	return gh, func() map[int][]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[int][]string, len(added))
		for k, v := range added {
			out[k] = append([]string(nil), v...)
		}
		return out
	}
}

func labelsOf(names ...string) []*githubv39.Label {
	var labels []*githubv39.Label
	for _, name := range names {
		labels = append(labels, &githubv39.Label{Name: stringPtr(name)})
	}
	return labels
}

// TestAdoptOrphanedBotPRs_InheritsFromTriggeredParent covers the case that
// stranded PR #1523: a fix pull request opened by the pool that never received
// a label, because the process that would have applied one was killed while the
// task ran on detached.
func TestAdoptOrphanedBotPRs_InheritsFromTriggeredParent(t *testing.T) {
	gh, added := adoptionServer(t, map[int]*githubv39.Issue{
		1364: {Number: githubv39.Int(1364), Labels: labelsOf("overseer", "overseer/review")},
	})
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"ada-coder-bot"},
		TriggerLabel: "overseer",
	})

	s.adoptOrphanedBotPRs(context.Background(), []*githubv39.PullRequest{{
		Number: githubv39.Int(1523),
		User:   &githubv39.User{Login: stringPtr("ada-coder-bot")},
		Head:   &githubv39.PullRequestBranch{Ref: stringPtr("issue-1364-1790016435")},
		Body:   stringPtr("Fixes #1364"),
	}})

	got := added()[1523]
	want := map[string]bool{"overseer": true, "overseer/review": true}
	if len(got) != len(want) {
		t.Fatalf("labels added to PR #1523 = %v, want %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected label %q added to PR #1523", name)
		}
	}
}

// TestAdoptOrphanedBotPRs_SkipsUntriggeredParent is the safety valve: a bot
// account opens pull requests that are none of the watcher's business, and the
// only thing telling them apart from a fix is whether the repository labelled
// the issue behind them.
func TestAdoptOrphanedBotPRs_SkipsUntriggeredParent(t *testing.T) {
	gh, added := adoptionServer(t, map[int]*githubv39.Issue{
		42: {Number: githubv39.Int(42), Labels: labelsOf("kind/bug")},
	})
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"ada-coder-bot"},
		TriggerLabel: "overseer",
	})

	s.adoptOrphanedBotPRs(context.Background(), []*githubv39.PullRequest{{
		Number: githubv39.Int(100),
		User:   &githubv39.User{Login: stringPtr("ada-coder-bot")},
		Body:   stringPtr("Fixes #42"),
	}})

	if labels := added()[100]; len(labels) != 0 {
		t.Errorf("adopted a PR whose parent issue carries no trigger label, adding %v", labels)
	}
}

// TestAdoptOrphanedBotPRs_SkipsPRsTheScanAlreadySees keeps the pass to the gap
// it exists to close. A labelled or assigned pull request is listed by the scan
// and inherits its labels during evaluation; adopting it here would spend the
// parent issue reads twice for the same answer.
func TestAdoptOrphanedBotPRs_SkipsPRsTheScanAlreadySees(t *testing.T) {
	gh, added := adoptionServer(t, map[int]*githubv39.Issue{
		7: {Number: githubv39.Int(7), Labels: labelsOf("overseer", "overseer/review")},
	})
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"ada-coder-bot"},
		TriggerLabel: "overseer",
	})

	s.adoptOrphanedBotPRs(context.Background(), []*githubv39.PullRequest{
		{
			Number: githubv39.Int(101),
			User:   &githubv39.User{Login: stringPtr("ada-coder-bot")},
			Labels: labelsOf("overseer"),
			Body:   stringPtr("Fixes #7"),
		},
		{
			Number:    githubv39.Int(102),
			User:      &githubv39.User{Login: stringPtr("ada-coder-bot")},
			Assignees: []*githubv39.User{{Login: stringPtr("ada-coder-bot")}},
			Body:      stringPtr("Fixes #7"),
		},
	})

	for _, num := range []int{101, 102} {
		if labels := added()[num]; len(labels) != 0 {
			t.Errorf("PR #%d is already visible to the scan but was adopted, adding %v", num, labels)
		}
	}
}

// TestAdoptOrphanedBotPRs_SkipsForeignAuthors holds the line the whole pass
// depends on: the watcher can only work on branches it can push to, so a pull
// request from outside the pool must not be pulled into its candidate set.
func TestAdoptOrphanedBotPRs_SkipsForeignAuthors(t *testing.T) {
	gh, added := adoptionServer(t, map[int]*githubv39.Issue{
		7: {Number: githubv39.Int(7), Labels: labelsOf("overseer")},
	})
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"ada-coder-bot"},
		TriggerLabel: "overseer",
	})

	s.adoptOrphanedBotPRs(context.Background(), []*githubv39.PullRequest{{
		Number: githubv39.Int(103),
		User:   &githubv39.User{Login: stringPtr("some-outside-contributor")},
		Body:   stringPtr("Fixes #7"),
	}})

	if labels := added()[103]; len(labels) != 0 {
		t.Errorf("adopted a PR authored outside the bot pool, adding %v", labels)
	}
}

// TestAdoptOrphanedBotPRs_SkipsBelowMinNumber checks the adoption pass honours
// the same floor as evaluation, so a deployment pointed at recent pull requests
// does not start labelling the back catalogue.
func TestAdoptOrphanedBotPRs_SkipsBelowMinNumber(t *testing.T) {
	gh, added := adoptionServer(t, map[int]*githubv39.Issue{
		7: {Number: githubv39.Int(7), Labels: labelsOf("overseer")},
	})
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"ada-coder-bot"},
		TriggerLabel: "overseer",
		MinNumber:    500,
	})

	s.adoptOrphanedBotPRs(context.Background(), []*githubv39.PullRequest{{
		Number: githubv39.Int(104),
		User:   &githubv39.User{Login: stringPtr("ada-coder-bot")},
		Body:   stringPtr("Fixes #7"),
	}})

	if labels := added()[104]; len(labels) != 0 {
		t.Errorf("adopted a PR below MinNumber, adding %v", labels)
	}
}

// TestAdoptOrphanedBotPRs_ToleratesUnreadableReferences checks that a reference
// which is not an issue does not stop the adoption. Branch names and titles are
// mined for numbers too, so a pull request number or a stale reference in the
// set is ordinary rather than exceptional.
func TestAdoptOrphanedBotPRs_ToleratesUnreadableReferences(t *testing.T) {
	gh, added := adoptionServer(t, map[int]*githubv39.Issue{
		1364: {Number: githubv39.Int(1364), Labels: labelsOf("overseer")},
	})
	s, _ := newTestScanner(t, t.TempDir(), testOpts{
		GitHub:       gh,
		BotUsers:     []string{"ada-coder-bot"},
		TriggerLabel: "overseer",
	})

	s.adoptOrphanedBotPRs(context.Background(), []*githubv39.PullRequest{{
		Number: githubv39.Int(1523),
		User:   &githubv39.User{Login: stringPtr("ada-coder-bot")},
		Body:   stringPtr("Fixes #1364. Supersedes #999999, see also #888888."),
	}})

	if labels := added()[1523]; len(labels) != 1 || labels[0] != "overseer" {
		t.Errorf("labels added to PR #1523 = %v, want [overseer]", labels)
	}
}
