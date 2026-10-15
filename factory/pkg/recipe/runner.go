package recipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// Executor runs the steps that are not prompts. The sandbox implements it
// with bash (see SandboxExecutor); tests with a fake.
type Executor interface {
	// Uses runs a named step. inputs arrive as INPUT_<NAME> variables, as
	// a run step's do, and with as WITH_<KEY>.
	Uses(ctx context.Context, name string, with, inputs map[string]string, log io.Writer) error
	// Run runs inline shell, without the GitHub token, and reports its
	// exit status; err is for a step that could not be started at all.
	Run(ctx context.Context, script string, inputs map[string]string, log io.Writer) (exitCode int, err error)
}

// Session is the agent conversation every ask goes to.
type Session interface {
	// Ask sends one turn and returns the agent's reply once the turn ends.
	Ask(ctx context.Context, prompt string) (reply string, err error)
	Close() error
}

// UsageSession is a Session that can say what its last turn used, per
// model, where the engine reports it.
type UsageSession interface {
	TurnUsage() []acpd.ModelUsage
}

// FreshSession is a Session that can say it remembers nothing.
type FreshSession interface {
	Fresh() bool
}

// StepResult is what a step left for later ones to read.
type StepResult struct {
	// Skipped is whether the step's if: was false and it did not run.
	Skipped  bool
	ExitCode int
	// Log is the step's output file in the task directory (uses, run).
	Log string
	// Reply is what the agent answered (ask).
	Reply string
}

// templateData is what an ask is rendered against.
type templateData struct {
	Inputs map[string]string
	Steps  map[string]*StepResult
}

// Runner runs one recipe.
type Runner struct {
	Exec Executor
	// StartSession opens the conversation. Called at the first ask, so a
	// recipe whose prepare steps fail never starts an engine.
	StartSession func(ctx context.Context) (Session, error)
	// Continues is whether StartSession opens a conversation an earlier
	// run left, a recipe's in a session (session: care), rather than one
	// of this run's own.
	Continues bool
	// NewSession opens a conversation of this run's own, as a run that
	// continues none does. With Continues, a conversation too long to take
	// another turn (full) is left for it at the run's first ask: the
	// conversation does not shrink, so every later run in it would fail
	// the same way. Unset, a full conversation fails the run.
	NewSession func(ctx context.Context) (Session, error)
	// Rotated records that the run left the conversation it continued for
	// one of its own (NewSession), so the next run in the session
	// continues that one.
	Rotated func() error
	// TaskDir is where step logs and captured replies are written.
	TaskDir string
	Inputs  map[string]string
	// Log receives a line per step and everything the steps print.
	Log io.Writer
}

// Run runs the recipe's steps in order and stops at the first failure.
func (r *Runner) Run(ctx context.Context, rec *Recipe) (err error) {
	steps := rec.Start.Steps
	if err := os.MkdirAll(filepath.Join(r.TaskDir, "steps"), 0o755); err != nil {
		return err
	}
	data := templateData{Inputs: r.Inputs, Steps: map[string]*StepResult{}}
	if data.Inputs == nil {
		data.Inputs = map[string]string{}
	}
	var session Session
	defer func() {
		if session != nil {
			_ = session.Close()
		}
	}()
	contextSent := false
	// asks is how many asks the run has sent; a full conversation is left
	// at the first only, when it holds nothing of this run's.
	asks := 0
	rotated := false

	for i, step := range steps {
		label := step.Label(i)
		if step.If != "" {
			run, cerr := condition(label, step.If, data, r.TaskDir)
			if cerr != nil {
				fmt.Fprintf(r.Log, "::failed %s: %v\n", label, cerr)
				return fmt.Errorf("step %s: %w", label, cerr)
			}
			if !run {
				fmt.Fprintf(r.Log, "::skip %s (%s)\n", label, step.Kind())
				if step.ID != "" {
					data.Steps[step.ID] = &StepResult{Skipped: true}
				}
				continue
			}
		}
		fmt.Fprintf(r.Log, "::step %s (%s)\n", label, step.Kind())
		res := &StepResult{}

		switch step.Kind() {
		case "uses", "run":
			logPath := filepath.Join(r.TaskDir, "steps", fmt.Sprintf("%02d-%s.log", i+1, sanitize(label)))
			f, ferr := os.Create(logPath)
			if ferr != nil {
				return ferr
			}
			out := io.MultiWriter(r.Log, f)
			res.Log = logPath
			if step.Kind() == "uses" {
				err = r.Exec.Uses(ctx, step.Uses, step.With, r.Inputs, out)
				if err != nil {
					res.ExitCode = 1
				}
			} else {
				res.ExitCode, err = r.Exec.Run(ctx, step.Run, r.Inputs, out)
				if err == nil && res.ExitCode != 0 && !step.ContinueOnError {
					err = fmt.Errorf("exited %d", res.ExitCode)
				}
			}
			f.Close()

		case "ask":
			var prompt string
			prompt, err = render(label, step.Ask, data, r.TaskDir)
			if err != nil {
				break
			}
			if session == nil {
				if session, err = r.StartSession(ctx); err != nil {
					session = nil
					err = fmt.Errorf("starting the agent session: %w", err)
					break
				}
				if f, ok := session.(FreshSession); ok && f.Fresh() {
					contextSent = false
				}
			}
			ask := prompt
			if !contextSent && rec.Context != "" {
				prompt = rec.Context + "\n\n---\n\n" + prompt
			}
			contextSent = true
			asks++
			res.Reply, err = session.Ask(ctx, prompt)
			r.addUsage(label, session)
			if asks == 1 && r.Continues && !rotated && r.NewSession != nil && conversationFull(err, res.Reply, session) {
				// The conversation the run continued is too long for the
				// engine to take another turn: it refused without the model
				// writing a word. Leave it for one of the run's own, sent
				// the recipe's context as a first run is; the run takes the
				// PR as it is now, so nothing it needs is lost.
				fmt.Fprintf(r.Log, "::warning %s: the conversation it continues is full (%v); continuing in a new one\n", label, err)
				_ = session.Close()
				rotated = true
				if session, err = r.NewSession(ctx); err != nil {
					session = nil
					err = fmt.Errorf("starting a new agent session: %w", err)
					break
				}
				if r.Rotated != nil {
					if rerr := r.Rotated(); rerr != nil {
						fmt.Fprintf(r.Log, "::warning recording the new conversation: %v\n", rerr)
					}
				}
				prompt = ask
				if rec.Context != "" {
					prompt = rec.Context + "\n\n---\n\n" + ask
				}
				res.Reply, err = session.Ask(ctx, prompt)
				r.addUsage(label, session)
			}
			if err == nil && step.Capture != "" {
				err = os.WriteFile(filepath.Join(r.TaskDir, step.Capture), []byte(res.Reply), 0o644)
			}
			if err == nil && step.Expects != "" {
				err = r.expect(ctx, session, label, step.Expects)
			}
		}

		if step.ID != "" {
			data.Steps[step.ID] = res
		}
		if err != nil {
			fmt.Fprintf(r.Log, "::failed %s: %v\n", label, err)
			return fmt.Errorf("step %s: %w", label, err)
		}
	}
	fmt.Fprintf(r.Log, "::done %s\n", rec.Name)
	return nil
}

// addUsage records what session's last turn used beside the task's other
// usage, for the harvest (usagereport): a turn that failed used tokens too.
func (r *Runner) addUsage(label string, session Session) {
	if u, ok := session.(UsageSession); ok {
		if uerr := AddUsage(r.TaskDir, u.TurnUsage()); uerr != nil {
			fmt.Fprintf(r.Log, "::warning recording %s's token usage: %v\n", label, uerr)
		}
	}
}

// conversationFull reports whether a turn ended as one sent to a
// conversation too long to take it does: stopped for max_tokens with
// nothing written, no reply and no output tokens. A turn that wrote and
// then ran out of tokens is a reply too long, which a new conversation
// would not mend.
func conversationFull(err error, reply string, session Session) bool {
	var stop *StopError
	if !errors.As(err, &stop) || stop.Reason != "max_tokens" || strings.TrimSpace(reply) != "" {
		return false
	}
	if u, ok := session.(UsageSession); ok {
		for _, m := range u.TurnUsage() {
			if m.Output > 0 {
				return false
			}
		}
	}
	return true
}

// expect checks that the agent wrote the file an ask told it to, asking
// once more if it did not.
func (r *Runner) expect(ctx context.Context, session Session, label, name string) error {
	path := filepath.Join(r.TaskDir, name)
	if nonEmptyFile(path) {
		return nil
	}
	fmt.Fprintf(r.Log, "::warning %s: the agent wrote no %s; asking again\n", label, name)
	_, err := session.Ask(ctx, fmt.Sprintf("You have not written %s. Write it now, whole, as asked, then say in a line that it is written.", path))
	r.addUsage(label, session)
	if err != nil {
		return err
	}
	if !nonEmptyFile(path) {
		return fmt.Errorf("the agent wrote no %s", name)
	}
	return nil
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// RenderAgentEnv is AgentEnv for a run with inputs: each value
// rendered against them, as an ask is. One that renders empty is left
// unset rather than set empty: CLOUDSDK_CORE_PROJECT="" is not "no
// project" to gcloud but an error.
func (r *Recipe) RenderAgentEnv(inputs map[string]string) (map[string]string, error) {
	if len(r.AgentEnv) == 0 {
		return nil, nil
	}
	data := templateData{Inputs: inputs, Steps: map[string]*StepResult{}}
	out := make(map[string]string, len(r.AgentEnv))
	for k, v := range r.AgentEnv {
		val, err := render("agent-env "+k, v, data, "")
		if err != nil {
			return nil, err
		}
		if val != "" {
			out[k] = val
		}
	}
	return out, nil
}

// CheckRender renders every ask with inputs, as far as can
// be done before anything has run (step results and files empty), so an
// ask reading an input its target does not set — an issue's, on a
// repository — fails before a sandbox is made rather than in it.
func (r *Recipe) CheckRender(inputs map[string]string) error {
	if _, err := r.RenderAgentEnv(inputs); err != nil {
		return err
	}
	for part, steps := range map[string][]Step{"start": r.Start.Steps} {
		data := templateData{Inputs: inputs, Steps: map[string]*StepResult{}}
		for _, s := range steps {
			if s.ID != "" {
				data.Steps[s.ID] = &StepResult{}
			}
		}
		for i, s := range steps {
			if s.If != "" {
				if _, err := condition(s.Label(i), s.If, data, ""); err != nil {
					return fmt.Errorf("%s: step %s: %w", part, s.Label(i), err)
				}
			}
			if s.Kind() != "ask" {
				continue
			}
			if _, err := render(s.Label(i), s.Ask, data, ""); err != nil {
				return fmt.Errorf("%s: step %s: %w", part, s.Label(i), err)
			}
		}
	}
	return nil
}

// condition renders a step's if:, which must come out true or false, so a
// mistyped condition fails the run rather than skipping the step.
func condition(label, text string, data templateData, taskDir string) (bool, error) {
	out, err := render(label+":if", text, data, taskDir)
	if err != nil {
		return false, fmt.Errorf("if: %w", err)
	}
	switch strings.TrimSpace(out) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("if: %q is neither true nor false", strings.TrimSpace(out))
}

// render fills one ask. `file "name"` reads a file a step left in the task
// directory, so a turn can quote a log without the recipe inlining it.
func render(label, text string, data templateData, taskDir string) (string, error) {
	t, err := template.New(label).Option("missingkey=error").Funcs(templateFuncs(taskDir)).Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			b[i] = '-'
		}
	}
	return string(b)
}

// templateFuncs are the functions a recipe's templates (asks, if:,
// agent-env) may call. With no taskDir (CheckRender, Validate) file reads
// nothing.
func templateFuncs(taskDir string) template.FuncMap {
	return template.FuncMap{
		"file": func(name string) (string, error) {
			if !safeFileName(name) {
				return "", fmt.Errorf("file %q must be a plain file name", name)
			}
			if taskDir == "" {
				return "", nil // CheckRender: nothing has run
			}
			b, err := os.ReadFile(filepath.Join(taskDir, name))
			return string(b), err
		},
		// output is where an ask tells the agent to write a result: a
		// file of the task directory, by its absolute path. The agent
		// writes it, not the runner, so a later turn of the conversation
		// (a member's, in Continue session) can write it again.
		"output": func(name string) (string, error) {
			if !safeFileName(name) {
				return "", fmt.Errorf("output %q must be a plain file name", name)
			}
			if taskDir == "" {
				return name, nil // CheckRender: nothing has run
			}
			return filepath.Join(taskDir, name), nil
		},
		"resourcePrefix": ResourcePrefix,
	}
}

// ResourcePrefix is the project-unique identity of everything a run
// creates in the cloud: the repository's short name, then the run's,
// shortName(repo)-run. Deliberately dumb — no stutter collapsing (it
// would be renaming the user cannot control, and it can alias two runs
// onto one prefix). Stutter is prevented at the source: the UI shows the
// prefix so nobody hand-types it.
func ResourcePrefix(repo, run string) string {
	return shortName(repo) + "-" + run
}

// shortName is a repository's short name: the initials of a hyphenated
// one (in-cluster-storage → ics), else the name, cut to 8.
func shortName(repo string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(repo)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if words := strings.Split(slug, "-"); len(words) >= 2 {
		var initials strings.Builder
		for _, w := range words {
			if w != "" {
				initials.WriteByte(w[0])
			}
		}
		return initials.String()
	}
	if len(slug) > 8 {
		return slug[:8]
	}
	return slug
}
