package acpd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
)

func TestChooseAuthMethod(t *testing.T) {
	gemini := acp.AuthMethod{ID: "gemini-api-key", Name: "API key"}
	oauth := acp.AuthMethod{ID: "oauth-personal", Name: "Sign in"}

	tests := []struct {
		name          string
		advertised    []acp.AuthMethod
		requested     string
		engineDefault string
		want          string
	}{
		{
			// An agent advertising nothing needs no authenticate call, and
			// some agents error if you make one anyway.
			name:          "no methods advertised skips authenticate",
			advertised:    nil,
			engineDefault: "gemini-api-key",
			want:          "",
		},
		{
			name:          "caller's choice wins when offered",
			advertised:    []acp.AuthMethod{gemini, oauth},
			requested:     "oauth-personal",
			engineDefault: "gemini-api-key",
			want:          "oauth-personal",
		},
		{
			// A caller naming something the agent does not offer must not
			// be passed through: authenticate would fail on an unknown id.
			name:          "unofferred request falls back to the engine default",
			advertised:    []acp.AuthMethod{gemini},
			requested:     "nonexistent",
			engineDefault: "gemini-api-key",
			want:          "gemini-api-key",
		},
		{
			name:          "engine default used when caller names nothing",
			advertised:    []acp.AuthMethod{gemini, oauth},
			engineDefault: "gemini-api-key",
			want:          "gemini-api-key",
		},
		{
			name:          "sole method used even when it is not the default",
			advertised:    []acp.AuthMethod{oauth},
			engineDefault: "gemini-api-key",
			want:          "oauth-personal",
		},
		{
			// Several offered, none of them ours: picking one risks
			// starting an OAuth flow that blocks on a browser nobody is
			// watching, so pick nothing and let the call fail loudly.
			name:          "ambiguous choice picks nothing",
			advertised:    []acp.AuthMethod{oauth, {ID: "enterprise-sso"}},
			engineDefault: "gemini-api-key",
			want:          "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseAuthMethod(tc.advertised, tc.requested, tc.engineDefault)
			if got != tc.want {
				t.Errorf("chooseAuthMethod(%v, %q, %q) = %q, want %q",
					tc.advertised, tc.requested, tc.engineDefault, got, tc.want)
			}
		})
	}
}

func TestStartSessionRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  SessionConfig
	}{
		{
			// Fail at create rather than hang on a handshake that will
			// never complete, which is why claude is absent from Engines
			// instead of half-wired.
			name: "unknown engine",
			cfg:  SessionConfig{ID: "s", Engine: "claude", APIKey: "k", CWD: "/tmp"},
		},
		{
			name: "missing api key",
			cfg:  SessionConfig{ID: "s", Engine: "gemini", CWD: "/tmp"},
		},
		{
			name: "missing cwd",
			cfg:  SessionConfig{ID: "s", Engine: "gemini", APIKey: "k"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Dir = t.TempDir()
			sess, err := StartSession(t.Context(), tc.cfg)
			if err == nil {
				sess.Close()
				t.Fatal("expected an error, got a session")
			}
		})
	}
}

func TestGeminiEngineIsConfiguredForACP(t *testing.T) {
	engine, ok := Engines["gemini"]
	if !ok {
		t.Fatal("gemini engine is not registered")
	}
	// ACP carries no credential (acp.AuthenticateRequest names a method
	// only), so the engine must declare the env var its key travels in.
	if engine.APIKeyEnv == "" {
		t.Error("gemini engine declares no APIKeyEnv; the key would never reach the process")
	}
	var hasACPFlag bool
	for _, arg := range engine.Args {
		if arg == "--acp" {
			hasACPFlag = true
		}
	}
	if !hasACPFlag {
		t.Errorf("gemini engine args %v do not put the CLI in ACP mode", engine.Args)
	}
	// Without this the checkout is an untrusted folder, and gemini
	// refuses every approval mode but prompting — which is the whole
	// point of a session nobody is sitting in front of.
	var trusts bool
	for _, kv := range engine.Env {
		if kv == "GEMINI_CLI_TRUST_WORKSPACE=true" {
			trusts = true
		}
	}
	if !trusts {
		t.Errorf("gemini engine env %v does not trust the workspace", engine.Env)
	}
}

func TestAntigravityEngineIsConfiguredForACP(t *testing.T) {
	engine, ok := Engines["antigravity"]
	if !ok {
		t.Fatal("antigravity engine is not registered")
	}
	if engine.APIKeyEnv != "GEMINI_API_KEY" {
		t.Errorf("APIKeyEnv = %q; agy_acp_server's gemini-api-key method reads GEMINI_API_KEY", engine.APIKeyEnv)
	}
	// The server also advertises oauth methods that block for minutes
	// on a browser sign-in. The engine default is what keeps
	// chooseAuthMethod on the key.
	advertised := []acp.AuthMethod{{ID: "oauth-personal"}, {ID: "oauth-business"}, {ID: "gemini-api-key"}, {ID: "agent-platform"}}
	if got := chooseAuthMethod(advertised, "", engine.AuthMethodID); got != "gemini-api-key" {
		t.Errorf("chooseAuthMethod picked %q, want gemini-api-key", got)
	}
	var ipv6 bool
	for _, arg := range engine.Args {
		if arg == "--enforce_kernel_ipv6_support=false" {
			ipv6 = true
		}
	}
	if !ipv6 {
		t.Errorf("antigravity args %v: without --enforce_kernel_ipv6_support=false the server aborts in a pod", engine.Args)
	}
	if !filepath.IsAbs(engine.Command) {
		t.Errorf("Command %q is not absolute; the server is installed under /opt, not on PATH", engine.Command)
	}
}

// An engine that relaunches itself (gemini runs a child node process) ends
// with its session: the child as well as the process acpd started, which
// otherwise went on running on the conversation after a handoff.
func TestCloseEndsTheEnginesChildren(t *testing.T) {
	registerFakeEngine(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The agent runs under a shell that stays its parent (the trailing
	// command keeps sh from exec'ing it), as gemini's relaunch does.
	Engines["fake-relaunch"] = Engine{
		Command:      "sh",
		Args:         []string{"-c", `python3 "$0"; :`, Engines["fake"].Args[0]},
		APIKeyEnv:    "FAKE_KEY",
		AuthMethodID: "fake-auth",
		Env:          []string{"FAKE_PIDFILE=" + pidFile},
	}
	t.Cleanup(func() { delete(Engines, "fake-relaunch") })

	sess, err := StartSession(t.Context(), SessionConfig{ID: "s", Engine: "fake-relaunch", APIKey: "k", CWD: t.TempDir(), Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the agent wrote no pid: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid %q: %v", raw, err)
	}
	if err := sess.Close(); err != nil {
		t.Logf("Close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(child, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(child, syscall.SIGKILL)
			t.Fatalf("the engine's child %d outlived Close", child)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
