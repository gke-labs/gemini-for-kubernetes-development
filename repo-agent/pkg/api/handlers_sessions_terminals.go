package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
)

// The engine CLIs running in sandboxes' terminal sessions (factory's
// termd: the launcher's agent-<program>, a task's chat-<task>) are rows on
// the Sessions page beside the recipe conversations, with what their
// terminal hears the program doing: needs-you, working, idle, done, error.

// terminalInfo is what of termd's GET /v1/terminals a row needs.
type terminalInfo struct {
	ID      string `json:"id"`
	Program string `json:"program"`
	Created string `json:"created"`
	State   string `json:"state"`
	Message string `json:"message"`
	Title   string `json:"title"`
}

// listTerminals is the pod's daemon's terminal sessions, false when it
// serves none or cannot be reached. A var for the tests.
var listTerminals = func(ctx context.Context, s *Server, pod *corev1.Pod) ([]terminalInfo, bool) {
	base, ok := s.ACPD.Terminals(ctx, pod)
	if !ok {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/terminals", nil)
	if err != nil {
		return nil, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	var out []terminalInfo
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil, false
	}
	return out, true
}

// terminalSessionRows are the rows of the engine terminals on running,
// keyed namespace/sandbox (the member's namespace and every overseer's the
// member may see).
func (s *Server) terminalSessionRows(ctx context.Context, namespace string, running map[string]*corev1.Pod) []SessionRow {
	if s.ACPD == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, researchLiveTimeout)
	defer cancel()
	var (
		mu   sync.Mutex
		rows []SessionRow
		wg   sync.WaitGroup
	)
	for key, pod := range running {
		ns, sandbox, _ := strings.Cut(key, "/")
		wg.Add(1)
		go func() {
			defer wg.Done()
			terms, ok := listTerminals(ctx, s, pod)
			if !ok {
				return
			}
			for _, t := range terms {
				row, ok := terminalRow(namespace, ns, sandbox, t)
				if !ok {
					continue
				}
				mu.Lock()
				rows = append(rows, row)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return rows
}

// terminalRow is a terminal's row: an engine CLI's that is still running,
// not a shell's.
func terminalRow(memberNS, ns, sandbox string, t terminalInfo) (SessionRow, bool) {
	if programEngine(t.Program) == "" || t.State == "exited" {
		return SessionRow{}, false
	}
	row := SessionRow{
		Sandbox:   sandbox,
		Terminal:  t.ID,
		Program:   t.Program,
		Engine:    programEngine(t.Program),
		Label:     t.Program + " terminal",
		Title:     t.Title,
		Doing:     t.Message,
		Pod:       "running",
		Live:      true,
		UpdatedAt: t.Created,
		State:     terminalRowState(t.State),
	}
	if ns != memberNS {
		row.Overseer = strings.TrimPrefix(ns, "overseer-")
	}
	if task, ok := strings.CutPrefix(t.ID, "chat-"); ok {
		row.Chat = task
	}
	return row, true
}

// terminalRowState is a terminal's state as a row's: termd's error is the
// page's failed.
func terminalRowState(state string) string {
	switch state {
	case "needs-you", "working", "idle", "done":
		return state
	case "error":
		return "failed"
	}
	return "idle"
}
