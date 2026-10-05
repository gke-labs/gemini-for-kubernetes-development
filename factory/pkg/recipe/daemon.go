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
}

// StartDaemonSession creates the task's session on the task server at
// base (http://127.0.0.1:<port>/v1), named after the task, as the task:
// token is the one the daemon gave it.
func StartDaemonSession(ctx context.Context, base, token, engine, model, apiKey, repoDir, taskDir string) (*DaemonSession, error) {
	id := filepath.Base(taskDir)
	d := &DaemonSession{base: strings.TrimSuffix(base, "/"), id: id, token: token, hc: &http.Client{}}
	body, err := json.Marshal(map[string]any{
		"id":     id,
		"task":   id,
		"engine": engine,
		"model":  model,
		"cwd":    repoDir,
		// Nobody is at the sandbox to answer: the same trust as --yolo.
		"mode":        acpd.GeminiModeYolo,
		"autoApprove": true,
	})
	if err != nil {
		return nil, err
	}
	resp, err := d.send(ctx, http.MethodPost, "/sessions", bytes.NewReader(body), apiKey)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("starting the task's session: %s", failure(resp))
	}
	return d, nil
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
	var turn turn
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if ctx.Err() != nil {
				return "", d.cancelled(ctx, ctx.Err())
			}
			// Whole lines only; the stream ends when the session does.
			return "", fmt.Errorf("the session ended mid-turn")
		}
		if reply, done, err := turn.event(line); done {
			return reply, err
		}
	}
}

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

// Close ends the engine. The transcript and the record stay in the task's
// directory, for whoever continues the conversation.
func (d *DaemonSession) Close() error {
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
