package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
)

// On someone else's pull request (alice's, outside the pool): a recipe
// that names a pool runs as one of it (adopt, as: coder); one that runs as
// the PR's author cannot run at all.
func TestSelectUser_SomeoneElsesPR(t *testing.T) {
	w := newTestWatcher(t, t.TempDir())
	w.cfg = &config.FactoryConfig{Roles: map[string]config.RoleConfig{
		"coder":    {Users: []string{"coder-bot"}},
		"reviewer": {Users: []string{"review-bot"}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"number": 7, "user": {"login": "alice"}}`))
	}))
	t.Cleanup(srv.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	w.ghClient = gh
	coordinator := &watcherTaskCoordinator{w: w}

	// The scanner queues it with the PR's author as assignee.
	user, err := coordinator.SelectUser(context.Background(), &api.QueueTask{Type: api.TypePRRule, Recipe: "adopt", Assignee: "alice", Number: 7})
	if err != nil || user != "coder-bot" {
		t.Errorf("adopt on alice's PR: %q, %v; want coder-bot", user, err)
	}
	user, err = coordinator.SelectUser(context.Background(), &api.QueueTask{Type: api.TypePRRule, Recipe: "review", Assignee: "alice", Number: 7})
	if err != nil || user != "review-bot" {
		t.Errorf("review on alice's PR: %q, %v; want review-bot", user, err)
	}
	// Unassigned (selection's own pick): the author it must run as is not in
	// the pool.
	if user, err := w.selectUserForTask(context.Background(), api.TypePRRule, "care-comments", 7); err == nil || !strings.Contains(err.Error(), "not in the configured bot pool") {
		t.Errorf("care-comments (as: author) on alice's PR: %q, %v; want the author error", user, err)
	}
}
