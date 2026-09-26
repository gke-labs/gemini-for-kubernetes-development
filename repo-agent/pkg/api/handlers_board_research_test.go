/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// The click writes a claim the controller will act on, and answers with
// the identity of the session and the sandbox that will host it — both
// derivable before anything exists, which is what lets the UI start
// polling straight away.
func TestStartResearchSessionWritesClaim(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var got struct {
		SessionID string `json:"sessionId"`
		Sandbox   string `json:"sandbox"`
		Namespace string `json:"namespace"`
		Repo      string `json:"repo"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad response %q: %v", w.Body.String(), err)
	}
	if got.SessionID == "" {
		t.Fatal("no session id in the response")
	}
	if got.Namespace != "alice" || got.Repo != "repo" {
		t.Errorf("namespace/repo = %q/%q, want alice/repo", got.Namespace, got.Repo)
	}
	// The name must be the one factory will create, or the caller
	// cannot find the sandbox it was promised.
	if want := factorycli.ResearchSandboxName("repo", got.SessionID); got.Sandbox != want {
		t.Errorf("sandbox = %q, want %q", got.Sandbox, want)
	}

	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{}
	if err := json.Unmarshal([]byte(board.GetAnnotations()[annoBoardRequests]), &requests); err != nil {
		t.Fatalf("bad mailbox: %q", board.GetAnnotations()[annoBoardRequests])
	}
	value, ok := requests["research-"+got.SessionID]
	if !ok {
		t.Fatalf("no claim for the session that was returned: %v", requests)
	}
	member, at, found := strings.Cut(value, "|")
	if !found || member != "alice" {
		t.Errorf("claim value = %q, want alice|<RFC3339>", value)
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		// The controller refuses a claim it cannot date: the timestamp
		// is what bounds how long an unserved claim stands.
		t.Errorf("claim timestamp %q does not parse: %v", at, err)
	}
}

// Two clicks are two conversations. The transcript lives on the
// sandbox's disk, so a shared session id would be a shared transcript.
func TestStartResearchSessionMintsADistinctSessionEachTime(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())

	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("call %d: expected 202, got %d: %s", i, w.Code, w.Body.String())
		}
		var got struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if ids[got.SessionID] {
			t.Fatalf("session id %q was minted twice", got.SessionID)
		}
		ids[got.SessionID] = true
	}

	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{}
	_ = json.Unmarshal([]byte(board.GetAnnotations()[annoBoardRequests]), &requests)
	if len(requests) != 2 {
		t.Errorf("mailbox holds %d claims, want both sessions: %v", len(requests), requests)
	}
}

// Same rule as every other kickoff: a board outside the session's
// namespace does not resolve, so no claim is written.
func TestStartResearchSessionForbiddenForNonMember(t *testing.T) {
	board := boardCR()
	board.SetNamespace("board-kcc")
	_, r, _ := boardTestServer(t, map[string]string{}, board)

	req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// A canned exploration is an ordinary session whose first prompt
// someone else typed. The click files it on the claim, because the
// sandbox that will answer it does not exist yet.
func TestStartResearchSessionCarriesTheKickoff(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest("POST", "/board/myboard/research",
		strings.NewReader(`{"kind":"topic","topic":"how does the mailbox get trimmed?"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// The title comes back so the UI can name the row it is about to
	// show, before anything exists to read it from.
	if got.Title != "how does the mailbox get trimmed?" {
		t.Errorf("title = %q", got.Title)
	}

	board, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{}
	_ = json.Unmarshal([]byte(board.GetAnnotations()[annoBoardRequests]), &requests)
	claim, ok := research.DecodeClaim(requests["research-"+got.SessionID])
	if !ok {
		t.Fatalf("claim %q does not decode", requests["research-"+got.SessionID])
	}
	if claim.Kickoff.Kind != research.KindTopic || claim.Kickoff.Topic != "how does the mailbox get trimmed?" {
		t.Errorf("kickoff = %+v", claim.Kickoff)
	}
}

// The plain "new conversation" click still files exactly the claim it
// always did: no third field, nothing to send.
func TestStartResearchSessionWithoutAKickoff(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	board, _ := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
	requests := map[string]string{}
	_ = json.Unmarshal([]byte(board.GetAnnotations()[annoBoardRequests]), &requests)
	for _, value := range requests {
		if strings.Count(value, "|") != 1 {
			t.Errorf("claim %q carries a kickoff nobody asked for", value)
		}
	}
}

// A kind the server cannot render is refused at the door. Defaulting it
// would spend minutes of engine time on the wrong exploration.
func TestStartResearchSessionRejectsAnUnknownKickoff(t *testing.T) {
	for _, body := range []string{`{"kind":"onbaord"}`, `{"kind":"topic"}`, `{"kind":"topic","topic":"  "}`} {
		_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
		req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", body, w.Code, w.Body.String())
		}
		board, _ := dyn.Resource(repoBoardGVR).Namespace("alice").Get(context.Background(), "myboard", v1.GetOptions{})
		if raw := board.GetAnnotations()[annoBoardRequests]; strings.Contains(raw, "research-") {
			t.Errorf("%s: a refused click must file no claim, got %q", body, raw)
		}
	}
}
