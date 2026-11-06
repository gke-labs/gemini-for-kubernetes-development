package recipe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
)

// DaemonSession is the task's session on the daemon's task server rather
// than in the task's own process: the same engine, transcript and
// directory as ACPSession, but the daemon's to keep. Anybody can watch it
// while the task runs, and once the task has ended a member can open it
// again and the agent still has the conversation (session/load).
type DaemonSession struct {
	base  string
	id    string
	token string
	hc    *http.Client
	// created is whether this task started the engine, and so ends it: a
	// run continuing a session leaves one it found open to the member
	// using it.
	created bool
	// fresh is whether the session it created remembers nothing: no
	// conversation to load, as after a run that asked nothing.
	fresh bool
	// usage is the last turn's (TurnUsage).
	usage []acpd.ModelUsage
}

// Fresh reports whether the agent starts from nothing, so a run
// continuing a session sends the recipe's context as a first run would.
func (d *DaemonSession) Fresh() bool { return d.fresh }

// StartDaemonSession creates the task's session on the task server at
// base (http://127.0.0.1:<port>/v1), named after the task, as the task:
// token is the one the daemon gave it.
func StartDaemonSession(ctx context.Context, base, token, engine, model, apiKey, repoDir, taskDir string, env map[string]string) (*DaemonSession, error) {
	d := newDaemonSession(base, filepath.Base(taskDir), token)
	if err := d.create(ctx, engine, model, apiKey, repoDir, env); err != nil {
		return nil, err
	}
	d.created = true
	return d, nil
}

// OpenDaemonSession is a run's way in to the session it continues,
// session: the daemon holds it for the run, whose token is token. Open
// already — a member is talking in it — it is used as it is, unless a
// turn is in flight, which refuses the run before it sends anything. Otherwise it is created, which loads the conversation.
func OpenDaemonSession(ctx context.Context, base, token, session, engine, model, apiKey, repoDir string, env map[string]string) (*DaemonSession, error) {
	d := newDaemonSession(base, session, token)
	var state struct {
		Busy bool `json:"busy"`
	}
	err := d.call(ctx, http.MethodGet, "/sessions/"+session, nil, http.StatusOK, &state)
	var status statusError
	switch {
	case err == nil && state.Busy:
		return nil, fmt.Errorf("session %s is busy: a turn is in flight; run again once it has ended", session)
	case err == nil:
		return d, nil
	case errors.As(err, &status) && status.code == http.StatusNotFound:
	default:
		return nil, err
	}
	if err := d.create(ctx, engine, model, apiKey, repoDir, env); err != nil {
		return nil, err
	}
	d.created = true
	return d, nil
}

func newDaemonSession(base, id, token string) *DaemonSession {
	return &DaemonSession{base: strings.TrimSuffix(base, "/"), id: id, token: token, hc: &http.Client{}}
}

// create starts the session's engine, named after its task, with env
// added to its environment.
func (d *DaemonSession) create(ctx context.Context, engine, model, apiKey, repoDir string, env map[string]string) error {
	body, err := json.Marshal(map[string]any{
		"id":     d.id,
		"task":   d.id,
		"engine": engine,
		"model":  model,
		"cwd":    repoDir,
		// Nobody is at the sandbox to answer: the same trust as --yolo.
		"mode":        acpd.GeminiModeYolo,
		"autoApprove": true,
		"env":         env,
	})
	if err != nil {
		return err
	}
	resp, err := d.send(ctx, http.MethodPost, "/sessions", bytes.NewReader(body), apiKey)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("starting the task's session: %s", failure(resp))
	}
	var created struct {
		Loaded bool `json:"loaded"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return fmt.Errorf("reading the task's session: %w", err)
	}
	d.fresh = !created.Loaded
	return nil
}

// Ask sends one turn and returns the agent's reply, as ACPSession.Ask.
func (d *DaemonSession) Ask(ctx context.Context, prompt string) (string, error) {
	// From where the transcript is now: the task is the only one driving
	// the session, so nothing else lands between this and the prompt.
	var state struct {
		Offset int64 `json:"offset"`
	}
	if err := d.call(ctx, http.MethodGet, "/sessions/"+d.id, nil, http.StatusOK, &state); err != nil {
		return "", err
	}
	text, err := json.Marshal(map[string]string{"text": prompt})
	if err != nil {
		return "", err
	}
	if err := d.call(ctx, http.MethodPost, "/sessions/"+d.id+"/prompt", text, http.StatusAccepted, nil); err != nil {
		return "", err
	}

	resp, err := d.send(ctx, http.MethodGet, fmt.Sprintf("/sessions/%s/events?offset=%d", d.id, state.Offset), nil, "")
	if err != nil {
		return "", d.cancelled(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("following the task's session: %s", failure(resp))
	}
	return d.follow(ctx, resp.Body)
}

// Liveness while a turn is followed: how long after the last event, or
// after a line that is not a whole event, the session's own state is
// asked; and how long a busy session may send nothing before the turn is
// given up as stalled. Variables, for tests.
var (
	turnCheckAfter     = 10 * time.Minute
	turnBadLineGrace   = 30 * time.Second
	turnStalledAfter   = 45 * time.Minute
	turnCheckPollEvery = time.Minute
)

// errTurnLost is a turn whose session finished it, but whose end the task
// could not read: the transcript line that carried it was cut short (a
// full disk, KCC #15081) or never written. The turn is asked again
// (EngineTransient), told it was interrupted.
var errTurnLost = errors.New("the session ended the turn, but its result was lost from the transcript")

// follow reads the turn's events from body until its end. The transcript
// is the only channel, so the session's own state is asked as well when it
// falls silent or sends a line that is no whole event: a session that is
// no longer busy, or gone, ended the turn without the task seeing how; one
// still busy but silent for turnStalledAfter (and not retrying a model
// call) is stalled. Either fails the turn rather than waiting forever.
func (d *DaemonSession) follow(ctx context.Context, body io.Reader) (string, error) {
	type read struct {
		line []byte
		err  error
	}
	lines := make(chan read)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		r := bufio.NewReader(body)
		for {
			line, err := r.ReadBytes('\n')
			select {
			case lines <- read{line, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var turn turn
	// tools are the agent's tool calls still running: a build or a test
	// suite runs silent for as long as it takes (validate-prereqs.sh, ~40
	// minutes), so a turn with one open is never stalled; the task's
	// timeout bounds it.
	tools := map[string]bool{}
	last := time.Now()
	checkAt := last.Add(turnCheckAfter)
	poll := time.NewTicker(turnCheckPollEvery)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", d.cancelled(ctx, ctx.Err())
		case rd := <-lines:
			if rd.err != nil {
				if ctx.Err() != nil {
					return "", d.cancelled(ctx, ctx.Err())
				}
				// Whole lines only; the stream ends when the session does.
				return "", fmt.Errorf("the session ended mid-turn")
			}
			last = time.Now()
			checkAt = last.Add(turnCheckAfter)
			if !json.Valid(bytes.TrimSpace(rd.line)) {
				// Not a whole event: a write cut short, perhaps the one
				// that ended the turn. Ask the session soon.
				checkAt = last.Add(turnBadLineGrace)
				continue
			}
			trackTool(tools, rd.line)
			if reply, done, err := turn.event(rd.line); done {
				d.usage = turn.usage
				return reply, err
			}
		case now := <-poll.C:
			if now.Before(checkAt) {
				continue
			}
			checkAt = now.Add(turnCheckAfter)
			var state struct {
				Busy     bool `json:"busy"`
				Retrying *struct {
					Model string `json:"model"`
				} `json:"retrying"`
			}
			err := d.call(ctx, http.MethodGet, "/sessions/"+d.id, nil, http.StatusOK, &state)
			var status statusError
			switch {
			case errors.As(err, &status) && status.code == http.StatusNotFound:
				return "", fmt.Errorf("%w (the session is gone)", errTurnLost)
			case err != nil:
				continue // the daemon unreachable for now: ask again later
			case !state.Busy:
				return "", errTurnLost
			case state.Retrying == nil && len(tools) == 0 && now.Sub(last) >= turnStalledAfter:
				stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = d.call(stopCtx, http.MethodPost, "/sessions/"+d.id+"/cancel", nil, http.StatusNoContent, nil)
				cancel()
				return "", fmt.Errorf("the turn stalled: no event for %s", now.Sub(last).Round(time.Minute))
			}
		}
	}
}

// TurnUsage is what the last turn used, per model, where the engine said.
func (d *DaemonSession) TurnUsage() []acpd.ModelUsage { return d.usage }

// cancelled stops the turn the task gave up on, and returns err.
func (d *DaemonSession) cancelled(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = d.call(stop, http.MethodPost, "/sessions/"+d.id+"/cancel", nil, http.StatusNoContent, nil)
	return err
}

// trackTool keeps tools, the turn's tool calls still running, up to date
// with one transcript event.
func trackTool(tools map[string]bool, line []byte) {
	var ev struct {
		Kind string `json:"kind"`
		Data struct {
			ToolCallID string `json:"toolCallId"`
			Status     string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Data.ToolCallID == "" {
		return
	}
	switch ev.Kind {
	case "tool_call", "tool_call_update":
		if ev.Data.Status == "completed" || ev.Data.Status == "failed" {
			delete(tools, ev.Data.ToolCallID)
		} else {
			tools[ev.Data.ToolCallID] = true
		}
	}
}

// Close ends the engine, if this task started it. The transcript and the
// record stay in the task's directory, for whoever continues the
// conversation.
func (d *DaemonSession) Close() error {
	if !d.created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := d.call(ctx, http.MethodDelete, "/sessions/"+d.id, nil, http.StatusNoContent, nil)
	var status statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil // already gone: the task was cancelled
	}
	return err
}

type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

// call sends body and expects want, decoding the answer into out.
func (d *DaemonSession) call(ctx context.Context, method, path string, body []byte, want int, out any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	resp, err := d.send(ctx, method, path, r, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return statusError{code: resp.StatusCode, msg: fmt.Sprintf("%s %s: %s", method, path, failure(resp))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (d *DaemonSession) send(ctx context.Context, method, path string, body io.Reader, apiKey string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(acpd.TaskTokenHeader, d.token)
	if apiKey != "" {
		req.Header.Set(acpd.APIKeyHeader, apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return d.hc.Do(req)
}

// failure is the status and the server's error message, never the
// request: that carried the API key.
func failure(resp *http.Response) string {
	var e struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return fmt.Sprintf("%s: %s", resp.Status, e.Error)
	}
	return resp.Status
}
