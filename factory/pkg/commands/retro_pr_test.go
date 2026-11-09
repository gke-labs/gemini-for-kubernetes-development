package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
	githubv39 "github.com/google/go-github/v39/github"
)

func TestRetroPROpenedWith(t *testing.T) {
	origWait := retroPRRetryWait
	retroPRRetryWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { retroPRRetryWait = origWait })

	status := http.StatusOK
	body := `[{"html_url":"https://github.com/o/r/pull/42"}]`
	var calls int
	var transientThenOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/o/r/pulls" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("state"); got != "all" {
			t.Errorf("state = %q, want all", got)
		}
		if got := r.URL.Query().Get("head"); got != "bot-fork:retro-pr-7" {
			t.Errorf("head = %q, want bot-fork:retro-pr-7", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if transientThenOK && calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"bad gateway"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")
	c := &taskoutput.Change{Fork: "bot-fork/r", Branch: "retro-pr-7"}
	ctx := context.Background()

	got, err := retroPROpenedWith(ctx, gh, "o", "r", c)
	if err != nil || got != "https://github.com/o/r/pull/42" {
		t.Fatalf("found: %q, %v", got, err)
	}

	calls = 0
	transientThenOK = true
	got, err = retroPROpenedWith(ctx, gh, "o", "r", c)
	if err != nil || got != "https://github.com/o/r/pull/42" || calls != 2 {
		t.Fatalf("transient retry: got=%q err=%v calls=%d", got, err, calls)
	}
	transientThenOK = false

	body = `[]`
	if _, err := retroPROpenedWith(ctx, gh, "o", "r", c); err == nil || !strings.Contains(err.Error(), "no PR was opened") {
		t.Errorf("empty: err = %v", err)
	}

	status = http.StatusBadGateway
	body = `{"message":"bad gateway"}`
	if _, err := retroPROpenedWith(ctx, gh, "o", "r", c); err == nil || !strings.Contains(err.Error(), "bot-fork:retro-pr-7") {
		t.Errorf("502: err = %v", err)
	}
}

func TestParseRetroChangePermanent(t *testing.T) {
	if _, err := parseRetroChange("t1", nil); !errors.Is(err, errPermanentRetroOutput) {
		t.Errorf("nil output err = %v; want errPermanentRetroOutput", err)
	}
	if _, err := parseRetroChange("t2", []byte(":::not yaml")); !errors.Is(err, errPermanentRetroOutput) {
		t.Errorf("bad yaml err = %v; want errPermanentRetroOutput", err)
	}
	nonChangeYAML := []byte("apiVersion: factory.gemini.google.com/v1alpha1\nkind: Triage\ntarget:\n  url: https://github.com/o/r/issues/1\nspec: {}\n")
	if _, err := parseRetroChange("t3", nonChangeYAML); !errors.Is(err, errPermanentRetroOutput) || !strings.Contains(err.Error(), "left no Change") {
		t.Errorf("non-Change err = %v; want errPermanentRetroOutput (left no Change)", err)
	}
	changeYAML := []byte("apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\ntarget:\n  url: https://github.com/o/r\nspec:\n  comment: nothing to change\n")
	c, err := parseRetroChange("t4", changeYAML)
	if err != nil || c == nil || c.Comment != "nothing to change" {
		t.Fatalf("valid Change: c=%+v err=%v", c, err)
	}
}

func TestRetroEntryFailed(t *testing.T) {
	changeYAML := []byte("apiVersion: factory.gemini.google.com/v1alpha1\nkind: Change\ntarget:\n  url: https://github.com/o/r\nspec:\n  comment: nothing to change\n")
	tests := []struct {
		name string
		e    spool.Entry
		data []byte
		err  error
		want bool
	}{
		{name: "running", e: spool.Entry{State: spool.Running}},
		{name: "nonzero exit", e: spool.Entry{State: spool.Exited, ExitCode: "1"}, want: true},
		{name: "read error after exit 0", e: spool.Entry{State: spool.Exited, ExitCode: "0"}, err: errors.New("io")},
		{name: "nil output after exit 0", e: spool.Entry{State: spool.Exited, ExitCode: "0"}, want: true},
		{name: "bad yaml after exit 0", e: spool.Entry{State: spool.Exited, ExitCode: "0"}, data: []byte(":::"), want: true},
		{name: "valid change after exit 0", e: spool.Entry{State: spool.Exited, ExitCode: "0"}, data: changeYAML},
	}
	for _, tc := range tests {
		got := retroEntryFailed("t1", tc.e, func() ([]byte, error) { return tc.data, tc.err })
		if got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}
