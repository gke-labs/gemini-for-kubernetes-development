package recipe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
)

// Executor runs the steps that are not prompts. The sandbox implements it
// with bash (see SandboxExecutor); tests with a fake.
type Executor interface {
	// Uses runs a named step. with arrives as WITH_<KEY> variables.
	Uses(ctx context.Context, name string, with map[string]string, log io.Writer) error
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

// StepResult is what a step left for later ones to read.
type StepResult struct {
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
	// TaskDir is where step logs and captured replies are written.
	TaskDir string
	Inputs  map[string]string
	// Revise selects the part to run: "" for start, else a revise's id.
	// A revise asks in a session that already has the recipe's context,
	// so it is not sent again.
	Revise string
	// Log receives a line per step and everything the steps print.
	Log io.Writer
}

// Run runs the selected part's steps in order and stops at the first
// failure.
func (r *Runner) Run(ctx context.Context, rec *Recipe) (err error) {
	steps, err := rec.Steps(r.Revise)
	if err != nil {
		return err
	}
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
	contextSent := r.Revise != ""

	for i, step := range steps {
		label := step.Label(i)
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
				err = r.Exec.Uses(ctx, step.Uses, step.With, out)
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
			if !contextSent && rec.Context != "" {
				prompt = rec.Context + "\n\n---\n\n" + prompt
			}
			contextSent = true
			if session == nil {
				if session, err = r.StartSession(ctx); err != nil {
					session = nil
					err = fmt.Errorf("starting the agent session: %w", err)
					break
				}
			}
			res.Reply, err = session.Ask(ctx, prompt)
			if err == nil && step.Capture != "" {
				err = os.WriteFile(filepath.Join(r.TaskDir, step.Capture), []byte(res.Reply), 0o644)
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

// render fills one ask. `file "name"` reads a file a step left in the task
// directory, so a turn can quote a log without the recipe inlining it.
func render(label, text string, data templateData, taskDir string) (string, error) {
	t, err := template.New(label).Option("missingkey=error").Funcs(template.FuncMap{
		"file": func(name string) (string, error) {
			if !safeFileName(name) {
				return "", fmt.Errorf("file %q must be a plain file name", name)
			}
			b, err := os.ReadFile(filepath.Join(taskDir, name))
			return string(b), err
		},
	}).Parse(text)
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
