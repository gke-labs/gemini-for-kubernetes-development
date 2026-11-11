package taskapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acpd"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/termd"
)

// POST /v1/sessions/{id}/handoff {to, task}: an agent session's
// conversation between the session view (acpd's engine) and the engine's
// own CLI in a terminal (termd), one process driving it at a time
// (acpd/handoff.go).
//
//   - to terminal: the session's engine stops (409 while a turn or a
//     permission is in flight, or its task runs), and the CLI starts in
//     terminal handoff-<id>, resuming the same conversation, with the key
//     in X-Engine-Api-Key. Answers {"terminal": id}.
//   - to view: the terminal ends; the next open of the session loads the
//     conversation, with the turns made in the terminal.
//
// task says the session is a task's (its directory is the task's).

// HandoffRequest is the body.
type HandoffRequest struct {
	To   string `json:"to"`
	Task bool   `json:"task,omitempty"`
}

// HandoffResponse is the answer.
type HandoffResponse struct {
	Terminal string `json:"terminal,omitempty"`
}

// handoffProgram is the CLI for an acpd engine's conversation.
func handoffProgram(engine string) (string, bool) {
	switch engine {
	case "gemini", "":
		return "gemini", true
	case "antigravity":
		return "agy", true
	case "claude":
		return "claude", true
	}
	return "", false
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	id, ok := pathName(w, r, "id")
	if !ok {
		return
	}
	var req HandoffRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	term := acpd.HandoffTerminalID(id)
	switch req.To {
	case "terminal":
		rec, err := s.sessions.Release(id, req.Task)
		switch {
		case errors.Is(err, acpd.ErrBusy):
			writeError(w, http.StatusConflict, "the session is busy: wait for its turn to end, or stop it")
			return
		case errors.Is(err, acpd.ErrNoConversation):
			writeError(w, http.StatusConflict, "the session has no conversation to hand off yet")
			return
		case err != nil:
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		program, ok := handoffProgram(rec.Engine)
		if !ok {
			_ = s.sessions.Return(id, req.Task)
			writeError(w, http.StatusBadRequest, fmt.Sprintf("no terminal CLI for engine %s", rec.Engine))
			return
		}
		if _, _, err := s.terminals.Create(termd.CreateRequest{ID: term, Program: program, ResumeID: rec.ACPSessionID, CWD: rec.CWD}, r.Header.Get(acpd.APIKeyHeader)); err != nil {
			// Not left stranded: the view can load it again.
			_ = s.sessions.Return(id, req.Task)
			writeError(w, http.StatusInternalServerError, "starting the terminal: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, HandoffResponse{Terminal: term})
	case "view":
		s.terminals.Delete(term)
		if err := s.sessions.Return(id, req.Task); err != nil && !errors.Is(err, acpd.ErrNotHandedOff) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, HandoffResponse{})
	default:
		writeError(w, http.StatusBadRequest, `to is "terminal" or "view"`)
	}
}
