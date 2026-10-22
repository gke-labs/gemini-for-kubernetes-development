package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
)

func TestWarmScriptSources(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/warm.sh":
			_, _ = w.Write([]byte("go build ./...\n"))
		case "/big.sh":
			_, _ = w.Write([]byte(strings.Repeat("x", warmScriptMaxBytes+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := warmScriptClient
	warmScriptClient = srv.Client()
	defer func() { warmScriptClient = old }()

	file := filepath.Join(t.TempDir(), "warm.sh")
	if err := os.WriteFile(file, []byte("make\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	in, prov, err := warmScript(ctx, config.WarmWorkspaceConfig{Script: file})
	if err != nil || in["script"] != "make\n" || prov != "inline" {
		t.Fatalf("script: %v %q %v", in, prov, err)
	}
	in, prov, err = warmScript(ctx, config.WarmWorkspaceConfig{ScriptURL: srv.URL + "/warm.sh"})
	if err != nil || in["script"] != "go build ./...\n" || !strings.HasPrefix(prov, "url "+srv.URL+"/warm.sh sha256:") {
		t.Fatalf("scriptURL: %v %q %v", in, prov, err)
	}
	in, prov, err = warmScript(ctx, config.WarmWorkspaceConfig{ScriptPath: "dev/tasks/warm"})
	if err != nil || in["script_path"] != "dev/tasks/warm" || in["script"] != "" || prov != "path dev/tasks/warm" {
		t.Fatalf("scriptPath: %v %q %v", in, prov, err)
	}
	for _, url := range []string{srv.URL + "/missing.sh", srv.URL + "/big.sh", strings.Replace(srv.URL, "https", "http", 1) + "/warm.sh"} {
		if _, _, err := warmScript(ctx, config.WarmWorkspaceConfig{ScriptURL: url}); err == nil {
			t.Errorf("scriptURL %s: no error", url)
		}
	}
}

func TestTheCatalogHasNoWarm(t *testing.T) {
	builtins, err := builtinCatalog()
	infos := mergeCatalog(builtins, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Name == "warm" {
			t.Fatal("the board catalog offers the warm recipe")
		}
	}
}

// A failed warm run starts again after six hours, or after the interval
// when that is shorter: a day's wait would let the last good snapshot
// run out.
func TestWarmRetryDue(t *testing.T) {
	failed := time.Date(2026, 10, 16, 12, 23, 29, 0, time.UTC)
	for name, tc := range map[string]struct {
		after    time.Duration
		interval time.Duration
		want     bool
	}{
		"5h59m, daily":  {5*time.Hour + 59*time.Minute, 24 * time.Hour, false},
		"6h, daily":     {6 * time.Hour, 24 * time.Hour, true},
		"1h, hourly":    {time.Hour, time.Hour, true},
		"59m, hourly":   {59 * time.Minute, time.Hour, false},
		"a day, weekly": {24 * time.Hour, 7 * 24 * time.Hour, true},
	} {
		if got := warmRetryDue(failed, failed.Add(tc.after), tc.interval); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
