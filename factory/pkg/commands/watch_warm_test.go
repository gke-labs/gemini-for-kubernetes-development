package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	infos, err := builtinRecipeInfos()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Name == "warm" {
			t.Fatal("the board catalog offers the warm recipe")
		}
	}
}
