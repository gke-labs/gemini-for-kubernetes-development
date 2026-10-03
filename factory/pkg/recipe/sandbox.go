package recipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// GitHubTokenEnv is every variable a GitHub token travels in; lib.sh's
// dropGitHubTokens unsets the same list for the engine.
var GitHubTokenEnv = []string{
	"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_USER_TOKEN", "GITHUB_BOT_TOKEN",
	"GITHUB_BOT_MANUAL_PAT", "GITHUB_BOT_OAUTH_PAT", "MANUAL_PAT", "OAUTH_PAT",
}

// SandboxExecutor runs uses and run steps in the sandbox, with bash.
type SandboxExecutor struct {
	// StepScript is lib.sh plus the dispatcher that calls one of its
	// functions (tasks.GetRecipeStepScript).
	StepScript []byte
	// TaskDir holds the step script once written.
	TaskDir string
	// RepoDir is where run steps start: the checkout.
	RepoDir string
	// Env is the task's environment, tokens included; Run strips them.
	Env []string
}

// Uses runs NamedSteps[name] from lib.sh with the task's full environment.
// The function name and with values go in as variables, never as source.
func (e *SandboxExecutor) Uses(ctx context.Context, name string, with map[string]string, log io.Writer) error {
	fn, ok := NamedSteps[name]
	if !ok {
		return fmt.Errorf("unknown step %q", name)
	}
	script := filepath.Join(e.TaskDir, "steps", "uses.sh")
	if _, err := os.Stat(script); err != nil {
		if err := os.WriteFile(script, e.StepScript, 0o644); err != nil {
			return err
		}
	}
	env := append(append([]string(nil), e.Env...), "RECIPE_STEP_FUNCTION="+fn)
	for k, v := range with {
		env = append(env, "WITH_"+envName(k)+"="+v)
	}
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = filepath.Dir(e.RepoDir)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = log, log
	return cmd.Run()
}

// Run runs inline shell in the checkout without any GitHub token in its
// environment, inputs as INPUT_<NAME> and the task directory as TASK_DIR.
//
// That is the engine's boundary, and no stronger: the step runs in the
// same pod as the same user, and gh's hosts.yml is on disk. Making the
// token unreachable needs it out of the pod altogether.
func (e *SandboxExecutor) Run(ctx context.Context, script string, inputs map[string]string, log io.Writer) (int, error) {
	env := withoutTokens(e.Env)
	for k, v := range inputs {
		env = append(env, "INPUT_"+envName(k)+"="+v)
	}
	env = append(env, "TASK_DIR="+e.TaskDir)
	cmd := exec.CommandContext(ctx, "bash", "-eo", "pipefail", "-c", script)
	cmd.Dir = e.RepoDir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = log, log
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func withoutTokens(env []string) []string {
	drop := map[string]bool{}
	for _, k := range GitHubTokenEnv {
		drop[k] = true
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return out
}

// envName is k as an environment variable name: issue-url → ISSUE_URL.
func envName(k string) string {
	return strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
}

// ACPSession is an acpd session driven turn by turn: every ask waits for
// the turn to end. acpd spawns the engine with an explicit environment —
// its API key and nothing else — so no GitHub token reaches it.
type ACPSession struct {
	s *acpd.Session
}

// StartACPSession starts the engine in the checkout. The transcript goes to
// <taskDir>/session/stream.ndjson, the same format a research session
// writes, so it can be followed while the task runs.
func StartACPSession(ctx context.Context, engine, model, apiKey, repoDir, taskDir string) (*ACPSession, error) {
	s, err := acpd.StartSession(ctx, acpd.SessionConfig{
		ID:     filepath.Base(taskDir),
		Engine: engine,
		APIKey: apiKey,
		Model:  model,
		CWD:    repoDir,
		Dir:    filepath.Join(taskDir, "session"),
		// Nobody is at the sandbox to answer: the same trust as --yolo.
		Mode:        acpd.GeminiModeYolo,
		AutoApprove: true,
	})
	if err != nil {
		return nil, err
	}
	return &ACPSession{s: s}, nil
}

// Ask sends one turn and returns the agent's reply: the text after its
// last tool call, which is its answer rather than its narration.
func (a *ACPSession) Ask(ctx context.Context, prompt string) (string, error) {
	t := a.s.Transcript()
	offset := t.Size()
	if err := a.s.Prompt(prompt); err != nil {
		return "", err
	}
	var reply strings.Builder
	var partial []byte
	for {
		if !t.Wait(ctx, offset) {
			if ctx.Err() != nil {
				_ = a.s.Cancel()
				return "", ctx.Err()
			}
			return "", fmt.Errorf("the session ended mid-turn")
		}
		data, next, err := t.ReadFrom(offset)
		if err != nil {
			return "", err
		}
		offset = next
		partial = append(partial, data...)
		// Whole lines only: an append can be read half-written.
		for {
			nl := bytes.IndexByte(partial, '\n')
			if nl < 0 {
				break
			}
			line := partial[:nl]
			partial = partial[nl+1:]
			var ev acpd.Event
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			switch ev.Kind {
			case acp.UpdateToolCall:
				reply.Reset()
			case acp.UpdateAgentMessageChunk:
				var u acp.SessionUpdate
				var c acp.ContentBlock
				if json.Unmarshal(ev.Data, &u) == nil && json.Unmarshal(u.Content, &c) == nil && c.Type == "text" {
					reply.WriteString(c.Text)
				}
			case acpd.KindError:
				var e struct{ Message string }
				_ = json.Unmarshal(ev.Data, &e)
				return "", fmt.Errorf("agent: %s", e.Message)
			case acpd.KindTurnEnd:
				var e struct{ StopReason string }
				_ = json.Unmarshal(ev.Data, &e)
				if e.StopReason != "end_turn" {
					return reply.String(), fmt.Errorf("turn stopped: %s", e.StopReason)
				}
				return strings.TrimSpace(reply.String()), nil
			}
		}
	}
}

// Close ends the engine.
func (a *ACPSession) Close() error { return a.s.Close() }
