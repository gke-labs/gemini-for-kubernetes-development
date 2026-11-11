package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestLabelEventsAndCollaborator(t *testing.T) {
	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r/issues":
			if got := r.URL.Query().Get("labels"); got != "overseer/retro" {
				t.Errorf("labels = %q, want overseer/retro", got)
			}
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[{"number":9,"state":"closed","pull_request":{"url":"u"}}]`))
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues?page=2>; rel="next"`, serverURL))
			_, _ = w.Write([]byte(`[{"number":7,"state":"open"}]`))
		case "/repos/o/r/issues/7/events":
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write([]byte(`[
					{"id":3,"event":"labeled","label":{"name":"Other"},"created_at":"2026-11-09T12:00:00Z"},
					{"id":4,"event":"labeled","label":{"name":"OVERSEER/RETRO"},"actor":{"login":"alice"},"created_at":"2026-11-09T11:00:00Z"}
				]`))
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/7/events?page=2>; rel="next"`, serverURL))
			_, _ = w.Write([]byte(`[
				{"id":1,"event":"labeled","label":{"name":"overseer/retro"},"actor":{"login":"bob"},"created_at":"2026-11-09T10:00:00Z"}
			]`))
		case "/repos/o/r/issues/8/events":
			_, _ = w.Write([]byte(`[{"id":1,"event":"closed","created_at":"2026-11-09T10:00:00Z"}]`))
		case "/repos/o/r/collaborators/alice":
			w.WriteHeader(http.StatusNoContent)
		case "/repos/o/r/collaborators/mallory":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	c := ForRepo(gh, "o", "r")
	ctx := context.Background()

	items, err := c.LabelledItems(ctx, "overseer/retro")
	if err != nil || len(items) != 2 || items[0].GetNumber() != 7 || items[1].GetNumber() != 9 {
		t.Fatalf("LabelledItems: %v, %+v", err, items)
	}
	ev, err := c.LastLabeled(ctx, 7, "overseer/retro")
	if err != nil || ev == nil || ev.GetID() != 4 || ev.GetActor().GetLogin() != "alice" {
		t.Fatalf("LastLabeled #7: %v, %+v", err, ev)
	}
	if none, err := c.LastLabeled(ctx, 8, "overseer/retro"); err != nil || none != nil {
		t.Errorf("LastLabeled #8: %v, %+v", err, none)
	}
	if ok, err := c.IsCollaborator(ctx, "alice"); err != nil || !ok {
		t.Errorf("alice collaborator: %t, %v", ok, err)
	}
	if ok, err := c.IsCollaborator(ctx, "mallory"); err != nil || ok {
		t.Errorf("mallory collaborator: %t, %v", ok, err)
	}
	if _, err := (&Client{}).LabelledItems(ctx, "x"); !errors.Is(err, errNoClient) {
		t.Errorf("unready client err = %v, want errNoClient", err)
	}
}
