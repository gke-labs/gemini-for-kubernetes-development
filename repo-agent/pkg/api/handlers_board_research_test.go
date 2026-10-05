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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/research"
)

// The click files a Request the controller will act on, and answers
// with the identity of the session and the sandbox that will host it —
// both derivable before anything exists, which is what lets the UI
// start polling straight away.
const aQuestionBody = `{"kind":"topic","topic":"where does the retry loop live?"}`

func TestStartResearchSessionFilesRequest(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(aQuestionBody))
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

	filed := theRequest(t, dyn, "alice")
	if filed.Spec.Verb != boardv1alpha1.VerbResearch || filed.Spec.Board != "myboard" {
		t.Errorf("filed %+v, want a research click on myboard", filed.Spec)
	}
	if filed.Spec.Research == nil || filed.Spec.Research.SessionID != got.SessionID {
		t.Errorf("request is not for the session that was returned: %+v", filed.Spec.Research)
	}
	// The executor namespace, never a token: the controller fetches the
	// member's credential from this namespace at launch time.
	if filed.Spec.Member != "alice" {
		t.Errorf("member = %q, want alice", filed.Spec.Member)
	}
	// The click time is the object's own creation stamp, which is what
	// bounds how long an unserved click stands.
	if filed.Status.Phase != "" {
		t.Errorf("a fresh click must have no phase, got %q", filed.Status.Phase)
	}
}

// The canned openings are handed over as text, rendered for the board's
// repository, so the pane can put one in the member's box instead of
// running it behind a button.
func TestGetResearchPromptsRendersThemForTheBoardsRepo(t *testing.T) {
	_, r, _ := boardTestServer(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest("GET", "/board/myboard/research/prompts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got struct {
		Prompts map[string]string `json:"prompts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad response %q: %v", w.Body.String(), err)
	}
	for _, kind := range []string{research.KindOnboard, research.KindActivity} {
		text := got.Prompts[kind]
		if text == "" {
			t.Fatalf("no %s prompt in %v", kind, got.Prompts)
		}
		// Rendered, not the template: this text goes straight into a box
		// the member sends, so an unrendered action would reach the
		// engine verbatim.
		if strings.Contains(text, "{{") || strings.Contains(text, "<no value>") {
			t.Errorf("%s prompt was handed over unrendered:\n%s", kind, text)
		}
		if !strings.Contains(text, "repo") {
			t.Errorf("%s prompt does not name the board's repository:\n%s", kind, text)
		}
	}
	// What the member will see it called in the rail, since the box is
	// sent as an ordinary question and the title is derived from it.
	if title := research.Truncate(got.Prompts[research.KindOnboard]); title != "Overview of the repo" {
		t.Errorf("the overview would be listed as %q", title)
	}
}

// The prompts are the board's, so they are behind the board's access
// check like everything else on that path.
func TestGetResearchPromptsRefusesABoardYouCannotSee(t *testing.T) {
	_, r, _ := boardTestServer(t, map[string]string{}, boardCR())

	req, _ := http.NewRequest("GET", "/board/someone-elses/research/prompts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// Two clicks are two conversations. The transcript lives on the
// sandbox's disk, so a shared session id would be a shared transcript.
func TestStartResearchSessionMintsADistinctSessionEachTime(t *testing.T) {
	_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())

	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(aQuestionBody))
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

	if filed := filedRequests(t, dyn, "alice"); len(filed) != 2 {
		t.Errorf("filed %d requests, want one per session: %+v", len(filed), filed)
	}
}

// Same rule as every other kickoff: a board outside the session's
// namespace does not resolve, so no Request is filed.
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
// someone else typed. The click files it on the Request, because the
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

	kickoff := theRequest(t, dyn, "alice").Spec.Research
	if kickoff == nil || kickoff.SessionID != got.SessionID {
		t.Fatalf("no request for the session that was returned: %+v", kickoff)
	}
	if kickoff.Kind != research.KindTopic || kickoff.Topic != "how does the mailbox get trimmed?" {
		t.Errorf("kickoff = %+v", kickoff)
	}
}

// A conversation starts with a question: the recipe asks it as the
// task, so a click without one is refused and files nothing.
func TestStartResearchSessionWithoutAQuestionIsRefused(t *testing.T) {
	for _, body := range []string{``, `{}`} {
		_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
		req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400: %s", body, w.Code, w.Body.String())
		}
		if filed := filedRequests(t, dyn, "alice"); len(filed) != 0 {
			t.Errorf("%q: a refused click must file nothing, got %+v", body, filed)
		}
	}
}

// A kind the server cannot render is refused at the door. Defaulting it
// would spend minutes of engine time on the wrong exploration.
func TestStartResearchSessionRejectsAnUnknownKickoff(t *testing.T) {
	for _, body := range []string{`{"kind":"onboarding"}`, `{"kind":"topic"}`, `{"kind":"topic","topic":"  "}`} {
		_, r, dyn := boardTestServer(t, map[string]string{}, boardCR())
		req, _ := http.NewRequest("POST", "/board/myboard/research", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", body, w.Code, w.Body.String())
		}
		if filed := filedRequests(t, dyn, "alice"); len(filed) != 0 {
			t.Errorf("%s: a refused click must file nothing, got %+v", body, filed)
		}
	}
}
