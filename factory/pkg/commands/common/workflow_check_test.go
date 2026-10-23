package common

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
)

// TestCheckWorkflowDefinition pins how a path an issue names is classified.
// The cases that matter most are the convention-named ones: they used to be
// trusted without being fetched, so a missing definition was queued as a run
// that could only fail. A 404 or 410 must now come back WorkflowMissing, a
// failure that will not clear up by itself WorkflowUnusable, and anything that
// may be transient WorkflowUnresolved.
func TestCheckWorkflowDefinition(t *testing.T) {
	var httpSrv *httptest.Server
	httpSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workflows/present.md":
			_, _ = w.Write([]byte("---\nname: present\nmode: workflow\n---\nprompt\n"))
		case "/workflows/redirects-again.md":
			_, _ = w.Write([]byte("---\nname: again\nmode: workflow\nredirect: " + httpSrv.URL + "/workflows/present.md\n---\nmoved again\n"))
		case "/workflows/removed.md":
			w.WriteHeader(http.StatusGone)
		case "/workflows/forbidden.md":
			w.WriteHeader(http.StatusForbidden)
		case "/workflows/rate-limited.md":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/workflows/broken.md":
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer httpSrv.Close()

	files := map[string]string{
		".agents/workflows/present.md":    "---\nname: present\nmode: workflow\n---\nprompt\n",
		".agents/workflows/redirected.md": "---\nname: redirected\nmode: workflow\nredirect: " + httpSrv.URL + "/workflows/gone.md\n---\nmoved\n",
		".agents/workflows/chained.md":    "---\nname: chained\nmode: workflow\nredirect: " + httpSrv.URL + "/workflows/redirects-again.md\n---\nmoved\n",
		".agents/declares-mode.md":        "---\nname: declares\nmode: workflow\n---\nprompt\n",
		".agents/skill.md":                "---\nname: skill\n---\njust a skill\n",
	}
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/repos/test-owner/test-repo/contents/")
		if path == ".agents/workflows/flaky.md" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		content, ok := files[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "file", "content": content})
	}))
	defer ghSrv.Close()

	client := githubv39.NewClient(nil)
	client.BaseURL, _ = url.Parse(ghSrv.URL + "/")
	gh := github.ForRepo(client, "test-owner", "test-repo")

	tests := []struct {
		name    string
		path    string
		want    WorkflowStatus
		wantErr error
	}{
		{name: "convention path that exists", path: ".agents/workflows/present.md", want: WorkflowFound},
		{name: "convention path with leading ./ that exists", path: "./.agents/workflows/present.md", want: WorkflowFound},
		{name: "convention path that does not exist", path: ".agents/workflows/missing.md", want: WorkflowMissing, wantErr: ErrWorkflowNotFound},
		{name: "convention path whose redirect target does not exist", path: ".agents/workflows/redirected.md", want: WorkflowMissing, wantErr: ErrWorkflowNotFound},
		{name: "convention path that fails to fetch", path: ".agents/workflows/flaky.md", want: WorkflowUnresolved},
		{name: "non-convention path that declares workflow mode", path: ".agents/declares-mode.md", want: WorkflowFound},
		{name: "non-convention path that is a plain skill", path: ".agents/skill.md", want: NotAWorkflow},
		// An issue that merely mentions a file that does not exist is not
		// asking for a workflow, so it must not be reported as a missing one.
		{name: "non-convention path that does not exist", path: ".agents/nope.md", want: NotAWorkflow},
		{name: "convention URL that exists", path: httpSrv.URL + "/workflows/present.md", want: WorkflowFound},
		{name: "convention URL that does not exist", path: httpSrv.URL + "/workflows/missing.md", want: WorkflowMissing, wantErr: ErrWorkflowNotFound},
		{name: "convention github.com URL that does not exist", path: "https://github.com/test-owner/test-repo/blob/main/.agents/workflows/missing.md", want: WorkflowMissing, wantErr: ErrWorkflowNotFound},
		{name: "non-convention URL that does not exist", path: httpSrv.URL + "/docs/readme.md", want: NotAWorkflow},
		{name: "convention URL that is gone", path: httpSrv.URL + "/workflows/removed.md", want: WorkflowMissing, wantErr: ErrWorkflowNotFound},
		{name: "convention URL the server refuses", path: httpSrv.URL + "/workflows/forbidden.md", want: WorkflowUnusable, wantErr: ErrWorkflowUnusable},
		{name: "convention path with a chained redirect", path: ".agents/workflows/chained.md", want: WorkflowUnusable, wantErr: ErrWorkflowUnusable},
		{name: "convention URL that is rate limited", path: httpSrv.URL + "/workflows/rate-limited.md", want: WorkflowUnresolved},
		{name: "convention URL with a server error", path: httpSrv.URL + "/workflows/broken.md", want: WorkflowUnresolved},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckWorkflowDefinition(context.Background(), gh, tc.path)
			if got != tc.want {
				t.Errorf("CheckWorkflowDefinition(%q) status = %v, want %v (err: %v)", tc.path, got, tc.want, err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("CheckWorkflowDefinition(%q) err = %v, want one wrapping %v", tc.path, err, tc.wantErr)
			}
			if tc.want == WorkflowUnresolved && (errors.Is(err, ErrWorkflowNotFound) || errors.Is(err, ErrWorkflowUnusable)) {
				t.Errorf("CheckWorkflowDefinition(%q) err = %v reports a permanent failure for a transient one", tc.path, err)
			}
		})
	}
}

// TestCheckWorkflowDefinition_Timeout pins that a workflow URL whose server
// never answers is given up on, and as a failure that may be transient: the
// URL comes from an issue body, and the scanner that checks it must not stall.
func TestCheckWorkflowDefinition_Timeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	old := workflowFetchTimeout
	workflowFetchTimeout = 50 * time.Millisecond
	defer func() { workflowFetchTimeout = old }()

	done := make(chan WorkflowStatus, 1)
	go func() {
		status, _ := CheckWorkflowDefinition(context.Background(), nil, srv.URL+"/workflows/slow.md")
		done <- status
	}()
	select {
	case status := <-done:
		if status != WorkflowUnresolved {
			t.Errorf("status = %v, want WorkflowUnresolved for a fetch that timed out", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CheckWorkflowDefinition did not give up on a server that never answers")
	}
}

// A workflow's cooldown is what its definition declares, the default when
// it declares none (or one that does not parse), and an error when the
// definition cannot be read: an unknown cooldown is not the default.
func TestGetWorkflowCooldown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/two-hours.md":
			_, _ = w.Write([]byte("---\nname: wf\nmode: workflow\ncooldown: 2h\n---\nprompt\n"))
		case "/none.md":
			_, _ = w.Write([]byte("---\nname: wf\nmode: workflow\n---\nprompt\n"))
		case "/garbled.md":
			_, _ = w.Write([]byte("---\nname: wf\nmode: workflow\ncooldown: soon\n---\nprompt\n"))
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	for path, want := range map[string]time.Duration{"/two-hours.md": 2 * time.Hour, "/none.md": DefaultWorkflowCooldown, "/garbled.md": DefaultWorkflowCooldown, "": DefaultWorkflowCooldown} {
		u := path
		if path != "" {
			u = srv.URL + path
		}
		if got, err := GetWorkflowCooldown(context.Background(), nil, u); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", path, got, err, want)
		}
	}
	if got, err := GetWorkflowCooldown(context.Background(), nil, srv.URL+"/unreachable.md"); err == nil {
		t.Errorf("an unreadable definition = %v, no error", got)
	}
}
