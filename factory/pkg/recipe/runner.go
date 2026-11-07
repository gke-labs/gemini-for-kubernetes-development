package recipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

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
// ErrNoFallback is Runner.Fallback's answer when no model is left to fall
// back to.
var ErrNoFallback = errors.New("no model left to fall back to")

// engineUnavailableRE is an engine's answer that it cannot serve now: its
// model at capacity (gemini's "RPC error [503]: This model is currently
// experiencing high demand"), the key rate limited or out of quota, or the
// model's API out of reach (gemini's "RPC error [500]: exception
// TypeError: fetch failed sending request": its request never got there).
var engineUnavailableRE = regexp.MustCompile(`(?i)RPC error \[(429|503)\]|experiencing high demand|RESOURCE_EXHAUSTED|model is (currently )?overloaded|\b(429 Too Many Requests|503 Service Unavailable)\b`)

// engineTransientRE is an engine's answer that the exchange with its model
// broke - the request never got there (gemini's "RPC error [500]:
// exception TypeError: fetch failed sending request"), or the streamed
// answer was cut off ("RPC error [500]: Incomplete JSON segment at the
// end") - rather than that the model cannot serve: the same model, asked
// again, usually answers. So is a turn whose engine connection dropped
// (acpd's "connection to agent closed") or whose end was lost from the
// transcript (errTurnLost): the conversation is reopened and asked again.
var engineTransientRE = regexp.MustCompile(`(?i)fetch failed sending request|Incomplete JSON segment|\b(ECONNRESET|ETIMEDOUT|EPIPE)\b|socket hang up|connection to agent closed|result was lost from the transcript`)

// EngineUnavailable reports whether a turn failed because the engine could
// not serve it - a state of the service at the moment, which another
// model, or later, gets past - rather than on the turn's own account. A
// broken exchange (EngineTransient) is one too.
func EngineUnavailable(err error) bool {
	return err != nil && EngineUnavailableMessage(err.Error())
}

// EngineUnavailableMessage reports whether a message - an error line of a
// task's log - says the engine could not serve (EngineUnavailable).
func EngineUnavailableMessage(msg string) bool {
	return engineUnavailableRE.MatchString(msg) || engineTransientRE.MatchString(msg)
}

// EngineTransient reports whether a turn failed because the exchange with
// the model broke (engineTransientRE): the turn is asked again of the same
// model before any other is tried.
func EngineTransient(err error) bool {
	return err != nil && engineTransientRE.MatchString(err.Error())
}

// transientRetries is how many times a turn whose exchange broke is asked
// again of the same model before the run falls back to the next one;
// transientBackoff how long it waits before each.
const transientRetries = 2

func transientBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 30 * time.Second
	}
	return 2 * time.Minute
}

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
	// Fallback reopens the conversation the run is in on the next model
	// of its list, when the engine could not serve a turn on this one
	// (EngineUnavailable): the same conversation - loaded, not begun again -
	// and the same checkout, so nothing the run did is lost. It says which
	// model; ErrNoFallback when the list is spent. Nil: no fallback.
	Fallback func(ctx context.Context) (Session, string, error)
	// Reopen reopens the conversation the run is in on the model it is on
	// now, when the exchange with that model broke on a turn
	// (EngineTransient): the turn is asked again there, told it was cut
	// off, before the run falls back to another model. Nil: a broken
	// exchange falls back at once.
	Reopen func(ctx context.Context) (Session, error)
	// Sleep waits d, or until ctx is done: the pause before a turn is
	// asked again (Reopen). Nil: time's.
	Sleep func(ctx context.Context, d time.Duration) error
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
	// StartNamed opens a step's session (Step.Session) called name: a
	// conversation of the run's own beside its main one, on the run's
	// engine. fallback is how far down the run's model list it starts: 0
	// the first model, one more each time the engine could not serve a
	// turn on the last. It says which model, ErrNoFallback when the list
	// is spent. Nil: a recipe with step sessions cannot run.
	StartNamed func(ctx context.Context, name string, fallback int) (Session, string, error)
	// TaskDir is where step logs and captured replies are written.
	TaskDir string
	Inputs  map[string]string
	// Log receives a line per step and everything the steps print.
	Log io.Writer
}

// fallbackPrompt is a turn sent again to the next model after the one
// serving the conversation could not finish it: the request as it was,
// told that another model now continues and that some of it may be done.
func fallbackPrompt(ask string) string {
	return "The model serving this conversation became unavailable before it finished your last request. " +
		"You are a different model continuing the same conversation: what was done so far is in the conversation above and in the checkout. " +
		"Pick up where it left off and complete the request:\n\n" + ask
}

// interruptedPrompt is a turn asked again of the same model after the
// exchange with it broke partway: told so, and that some of it may be
// done.
func interruptedPrompt(ask string) string {
	return "Your last request was interrupted by a connection error before you finished it. " +
		"What you did so far is in the conversation above and in the checkout. " +
		"Continue from where you stopped and complete the request:\n\n" + ask
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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
	// named are the run's step sessions (Step.Session), by name.
	named := map[string]*namedSession{}
	defer func() {
		if session != nil {
			_ = session.Close()
		}
		for _, ns := range named {
			_ = ns.s.Close()
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
			if step.Session != "" {
				var s Session
				s, res.Reply, err = r.askNamed(ctx, label, step.Session, prompt, named)
				if err == nil && step.Capture != "" {
					err = os.WriteFile(filepath.Join(r.TaskDir, step.Capture), []byte(res.Reply), 0o644)
				}
				if err == nil && step.Expects != "" {
					err = r.expect(ctx, s, label, step.Expects)
				}
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
			// The exchange with the model broke (the request never got
			// there, the answer was cut off): the same model is asked
			// again, in the same conversation, told it was interrupted - a
			// few times, a while apart - before another is tried. The
			// engine could not serve the turn (its model at capacity, the
			// key out of quota), or the same model still fails: the same
			// conversation goes on with the next model of the run's list,
			// told where it is, until one answers or none is left.
			retries := 0
			for err != nil {
				failed := err
				if EngineTransient(err) && r.Reopen != nil && retries < transientRetries {
					retries++
					wait := transientBackoff(retries)
					fmt.Fprintf(r.Log, "::warning %s: the exchange with the model broke (%v); asking the same model again in %s (%d of %d)\n", label, failed, wait, retries, transientRetries)
					if serr := r.sleep(ctx, wait); serr != nil {
						err = fmt.Errorf("%w; waiting to ask again: %w", failed, serr)
						break
					}
					_ = session.Close()
					var rerr error
					if session, rerr = r.Reopen(ctx); rerr != nil {
						session = nil
						err = fmt.Errorf("%w; reopening the conversation: %w", failed, rerr)
						break
					}
					prompt = interruptedPrompt(ask)
					if f, ok := session.(FreshSession); ok && f.Fresh() && rec.Context != "" {
						prompt = rec.Context + "\n\n---\n\n" + ask
					}
					res.Reply, err = session.Ask(ctx, prompt)
					r.addUsage(label, session)
					continue
				}
				if !EngineUnavailable(err) || r.Fallback == nil {
					break
				}
				retries = 0
				_ = session.Close()
				var model string
				var ferr error
				if session, model, ferr = r.Fallback(ctx); ferr != nil {
					session = nil
					if !errors.Is(ferr, ErrNoFallback) {
						err = fmt.Errorf("%w; falling back to another model: %w", failed, ferr)
					}
					break
				}
				fmt.Fprintf(r.Log, "::warning %s: the engine could not serve the turn (%v); continuing the conversation on %s\n", label, failed, model)
				prompt = fallbackPrompt(ask)
				if f, ok := session.(FreshSession); ok && f.Fresh() && rec.Context != "" {
					// The conversation could not be loaded: a new one,
					// told the recipe's context as a first turn is.
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
			if reason := requiresHuman(err); reason != "" {
				r.markRequiresHuman(label, reason)
			}
			var stop *StopError
			if errors.As(err, &stop) {
				r.markStopReason(label, stop.Reason)
			}
			return fmt.Errorf("step %s: %w", label, err)
		}
	}
	fmt.Fprintf(r.Log, "::done %s\n", rec.Name)
	return nil
}

// namedSession is a step session (Step.Session) of a run, and how far
// down the run's model list it is.
type namedSession struct {
	s        Session
	fallback int
}

// askNamed sends prompt to the step session called name, starting it on
// its first ask. It is retried as the run's conversation is: a broken
// exchange asked again a few times, a while apart, then the next model
// when the engine cannot serve the turn. Unlike the run's conversation, a
// step session is started again rather than reloaded, and the ask sent
// again whole: what it said before is gone, which an ask that stands on
// its own - a reviewer's - does not miss.
func (r *Runner) askNamed(ctx context.Context, label, name, prompt string, named map[string]*namedSession) (Session, string, error) {
	if r.StartNamed == nil {
		return nil, "", fmt.Errorf("this run cannot open the step session %q", name)
	}
	ns := named[name]
	if ns == nil {
		s, _, err := r.StartNamed(ctx, name, 0)
		if err != nil {
			return nil, "", fmt.Errorf("starting the %s session: %w", name, err)
		}
		ns = &namedSession{s: s}
		named[name] = ns
	}
	reply, err := ns.s.Ask(ctx, prompt)
	r.addUsage(label, ns.s)
	retries := 0
	for err != nil {
		failed := err
		switch {
		case EngineTransient(err) && retries < transientRetries:
			retries++
			wait := transientBackoff(retries)
			fmt.Fprintf(r.Log, "::warning %s: the exchange with the %s session's model broke (%v); starting it again in %s (%d of %d)\n", label, name, failed, wait, retries, transientRetries)
			if serr := r.sleep(ctx, wait); serr != nil {
				return ns.s, reply, fmt.Errorf("%w; waiting to ask again: %w", failed, serr)
			}
		case EngineUnavailable(err):
			retries = 0
			ns.fallback++
		default:
			return ns.s, reply, err
		}
		_ = ns.s.Close()
		s, model, serr := r.StartNamed(ctx, name, ns.fallback)
		if serr != nil {
			delete(named, name)
			if errors.Is(serr, ErrNoFallback) {
				return nil, reply, failed
			}
			return nil, reply, fmt.Errorf("%w; starting the %s session again: %w", failed, name, serr)
		}
		ns.s = s
		if EngineUnavailable(failed) {
			fmt.Fprintf(r.Log, "::warning %s: the engine could not serve the %s session's turn (%v); starting it again on %s\n", label, name, failed, model)
		}
		reply, err = ns.s.Ask(ctx, prompt)
		r.addUsage(label, ns.s)
	}
	return ns.s, reply, nil
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
		// hasLabel is whether a comma-separated list of labels (an issue's,
		// issue_labels) has name, case-insensitively; "*/suffix" is suffix
		// under any prefix (overseer/review, kcc/review).
		"hasLabel": hasLabel,
	}
}

func hasLabel(labels, name string) bool {
	suffix, anyPrefix := strings.CutPrefix(name, "*/")
	for _, l := range strings.Split(labels, ",") {
		l = strings.TrimSpace(l)
		if anyPrefix {
			if _, s, ok := strings.Cut(l, "/"); ok && strings.EqualFold(s, suffix) {
				return true
			}
		} else if l != "" && strings.EqualFold(l, name) {
			return true
		}
	}
	return false
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
