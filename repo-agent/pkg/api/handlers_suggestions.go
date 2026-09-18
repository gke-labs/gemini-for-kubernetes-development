package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

// Repo suggestions for onboarding: the repos the member actually works in,
// so the add-board box can offer them instead of demanding a pasted URL.
// Signals, in rank order (live-calibrated against real accounts):
//  1. issues/PRs involving the member in the last 90 days, by frequency —
//     the strongest "works in" signal, and the only one that reliably
//     catches org repos the member contributes to without owning;
//  2. recent activity events (a short window — ~100 events is days for an
//     active user — but fresher than search indexing);
//  3. the member's own non-fork repos by last push.
// Forks are dropped when the upstream (same repo name, different owner)
// is already suggested — boards want the upstream. Computed lazily and
// cached; the UI prefetches on page load so the list is warm by the first
// click into the box.

type repoSuggestion struct {
	FullName string `json:"fullName"`
	URL      string `json:"url"`
}

var repoSuggestionCache = struct {
	sync.Mutex
	entries map[string]repoSuggestionEntry
}{entries: map[string]repoSuggestionEntry{}}

type repoSuggestionEntry struct {
	suggestions []repoSuggestion
	expires     time.Time
}

const maxRepoSuggestions = 15

// suggestionNow is injectable for tests (the involvement-search query
// embeds a date).
var suggestionNow = time.Now

func (s *Server) getRepoSuggestions(c *gin.Context) {
	ctx := c.Request.Context()
	namespace := s.Auth.GetNamespaceFromContext(c)

	repoSuggestionCache.Lock()
	if e, ok := repoSuggestionCache.entries[namespace]; ok && time.Now().Before(e.expires) {
		repoSuggestionCache.Unlock()
		c.JSON(http.StatusOK, e.suggestions)
		return
	}
	repoSuggestionCache.Unlock()

	token, err := s.memberToken(ctx, namespace)
	if err != nil {
		// Not onboarded yet: nothing to suggest, nothing to block.
		c.JSON(http.StatusOK, []repoSuggestion{})
		return
	}
	// Detached from the request: the browser's prefetch is fire-and-forget
	// and gets aborted on re-renders/reloads — a canceled request must not
	// cancel the GitHub calls (live failure: every signal died with
	// "context canceled" and the empty result was cached for an hour).
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	gh := githubClientForToken(bg, token)

	suggestions, complete := collectRepoSuggestions(bg, gh, namespace)

	// A complete answer holds for an hour; a partial one (some signal
	// errored) retries soon — never park a degraded verdict long-term.
	ttl := time.Hour
	if !complete {
		ttl = 2 * time.Minute
	}
	repoSuggestionCache.Lock()
	repoSuggestionCache.entries[namespace] = repoSuggestionEntry{suggestions: suggestions, expires: time.Now().Add(ttl)}
	repoSuggestionCache.Unlock()
	c.JSON(http.StatusOK, suggestions)
}

// collectRepoSuggestions returns the ranked suggestions and whether every
// signal answered (false = degraded, cache briefly).
func collectRepoSuggestions(ctx context.Context, gh *github.Client, login string) ([]repoSuggestion, bool) {
	log := klog.FromContext(ctx)
	complete := true
	var ranked []string
	counted := map[string]bool{}

	// Frequency-ranked block: append names in first-seen order, then rank
	// a block by count before folding it into the final order.
	rankBlock := func(names []string) {
		counts := map[string]int{}
		var order []string
		for _, name := range names {
			if name == "" || counted[name] {
				continue
			}
			if counts[name] == 0 {
				order = append(order, name)
			}
			counts[name]++
		}
		sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
		for _, name := range order {
			counted[name] = true
			ranked = append(ranked, name)
		}
	}

	// 1. Involvement search, last 90 days.
	cutoff := suggestionNow().AddDate(0, -3, 0).Format("2006-01-02")
	query := fmt.Sprintf("involves:%s updated:>%s", login, cutoff)
	if result, _, err := gh.Search.Issues(ctx, query, &github.SearchOptions{ListOptions: github.ListOptions{PerPage: 100}}); err != nil {
		log.Info("repo suggestions: involvement search unavailable", "user", login, "err", err)
		complete = false
	} else {
		var names []string
		for _, issue := range result.Issues {
			names = append(names, repoFullNameFromAPIURL(issue.GetRepositoryURL()))
		}
		rankBlock(names)
	}

	// 2. Recent activity events.
	if events, _, err := gh.Activity.ListEventsPerformedByUser(ctx, login, false, &github.ListOptions{PerPage: 100}); err != nil {
		log.Info("repo suggestions: events unavailable", "user", login, "err", err)
		complete = false
	} else {
		var names []string
		for _, ev := range events {
			names = append(names, ev.GetRepo().GetName())
		}
		rankBlock(names)
	}

	// 3. The member's own non-fork repos by last push. Deliberately
	// affiliation=owner: sorting all *accessible* repos by push activity
	// just surfaces whichever big org repos are busiest.
	if repos, _, err := gh.Repositories.List(ctx, "", &github.RepositoryListOptions{
		Affiliation: "owner",
		Sort:        "pushed",
		ListOptions: github.ListOptions{PerPage: 30},
	}); err != nil {
		log.Info("repo suggestions: repo list unavailable", "user", login, "err", err)
		complete = false
	} else {
		for _, repo := range repos {
			name := repo.GetFullName()
			if name == "" || counted[name] || repo.GetFork() {
				continue
			}
			counted[name] = true
			ranked = append(ranked, name)
		}
	}

	// Fork heuristic: the member's copy of a repo whose upstream (same
	// repo name, another owner) is also suggested is almost certainly the
	// fork — boards want the upstream.
	upstreamNamed := map[string]bool{}
	for _, name := range ranked {
		if !strings.HasPrefix(name, login+"/") {
			upstreamNamed[shortRepoName(name)] = true
		}
	}
	suggestions := []repoSuggestion{}
	for _, name := range ranked {
		if strings.HasPrefix(name, login+"/") && upstreamNamed[shortRepoName(name)] {
			continue
		}
		suggestions = append(suggestions, repoSuggestion{
			FullName: name,
			URL:      "https://github.com/" + name,
		})
		if len(suggestions) >= maxRepoSuggestions {
			break
		}
	}
	return suggestions, complete
}

// repoFullNameFromAPIURL turns https://api.github.com/repos/org/repo into
// org/repo.
func repoFullNameFromAPIURL(u string) string {
	if idx := strings.Index(u, "/repos/"); idx >= 0 {
		return u[idx+len("/repos/"):]
	}
	return ""
}

func shortRepoName(fullName string) string {
	if idx := strings.LastIndex(fullName, "/"); idx >= 0 {
		return fullName[idx+1:]
	}
	return fullName
}
