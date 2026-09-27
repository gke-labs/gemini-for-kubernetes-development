package acpd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/acp"
)

// createAuto posts a session with autoApprove set either way. Separate
// from create, which fixes the field at its zero value.
func createAuto(t *testing.T, ts *httptest.Server, id string, autoApprove bool) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"engine":"fake","autoApprove":%t}`, id, autoApprove)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set(APIKeyHeader, "k")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	return resp
}

// permissionStream is an open event stream with a prompt already sent on
// it, which is the only order that works: a turn that asks for permission
// will not end until someone answers, so the test has to be reading while
// it decides what to answer.
type permissionStream struct {
	t       *testing.T
	scanner *bufio.Scanner
}

func promptForPermission(t *testing.T, ts *httptest.Server, id, text string) *permissionStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)

	head, err := ts.Client().Get(fmt.Sprintf("%s/sessions/%s", ts.URL, id))
	if err != nil {
		cancel()
		t.Fatalf("GET session: %v", err)
	}
	from := decodeSession(t, head, http.StatusOK).Offset

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/sessions/%s/events?offset=%d", ts.URL, id, from), nil)
	if err != nil {
		cancel()
		t.Fatalf("building stream request: %v", err)
	}
	stream, err := ts.Client().Do(streamReq)
	if err != nil {
		cancel()
		t.Fatalf("GET events: %v", err)
	}

	promptResp, err := ts.Client().Post(fmt.Sprintf("%s/sessions/%s/prompt", ts.URL, id),
		"application/json", strings.NewReader(fmt.Sprintf(`{"text":%q}`, text)))
	if err != nil {
		cancel()
		t.Fatalf("POST prompt: %v", err)
	}
	promptResp.Body.Close()
	if promptResp.StatusCode != http.StatusAccepted {
		cancel()
		t.Fatalf("prompt returned %d", promptResp.StatusCode)
	}

	t.Cleanup(func() {
		stream.Body.Close()
		cancel()
	})
	return &permissionStream{t: t, scanner: bufio.NewScanner(stream.Body)}
}

// next returns the following event of one of the given kinds, failing the
// test if the stream ends or the deadline passes first. Waiting for a
// named kind rather than counting lines keeps the test indifferent to
// events the engine may add between the ones it is about.
func (p *permissionStream) next(kinds ...string) Event {
	p.t.Helper()
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	for p.scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(p.scanner.Bytes(), &ev); err != nil {
			p.t.Fatalf("stream line is not an Event: %v (%s)", err, p.scanner.Text())
		}
		if want[ev.Kind] {
			return ev
		}
	}
	p.t.Fatalf("stream ended without any of %v", kinds)
	return Event{}
}

// resolvedEvent is the KindPermissionResolved payload, which acpd writes
// as a map. Spelling it out here is the test's way of pinning the field
// names a client reads.
type resolvedEvent struct {
	RequestID string `json:"requestId"`
	Outcome   string `json:"outcome"`
	OptionID  string `json:"optionId"`
	Reason    string `json:"reason"`
}

func decodeInto[T any](t *testing.T, ev Event) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(ev.Data, &out); err != nil {
		t.Fatalf("decoding %s payload: %v (%s)", ev.Kind, err, ev.Data)
	}
	return out
}

func answerPermission(t *testing.T, ts *httptest.Server, id, body string) int {
	t.Helper()
	resp, err := ts.Client().Post(fmt.Sprintf("%s/sessions/%s/permission", ts.URL, id),
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST permission: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A session created with autoApprove must not stop for a permission
// request, because the caller has already said nobody will answer it.
// gemini asks even in yolo — its shell tool screens commands for tokens
// taken from earlier tool output before it looks at the approval mode —
// so this is the only thing standing between an unattended research turn
// and being cancelled by the permission timeout.
func TestAnAutoApprovedSessionAnswersItsOwnPermissionRequests(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	created := decodeSession(t, createAuto(t, ts, "s1", true), http.StatusCreated)
	if !created.AutoApprove {
		t.Error("create did not report the session as auto-approved")
	}

	stream := promptForPermission(t, ts, "s1", "!ask")

	req := decodeInto[PermissionRequest](t, stream.next(KindPermissionRequest))
	// Recorded, not skipped: the transcript is the account of what the
	// agent did, and a tool call that would have been asked about belongs
	// in it whether or not anyone was there to be asked.
	if req.RequestID == "" {
		t.Error("the request was not written to the transcript with an id")
	}

	res := decodeInto[resolvedEvent](t, stream.next(KindPermissionResolved))
	if res.RequestID != req.RequestID {
		t.Errorf("resolved %q, want %q", res.RequestID, req.RequestID)
	}
	if res.OptionID != "proceed_once" {
		t.Errorf("answered with %q, want proceed_once", res.OptionID)
	}
	if !strings.Contains(res.Reason, "auto-approved") {
		t.Errorf("resolution reason = %q, want it to say who answered", res.Reason)
	}

	// Nothing is left pending: a browser attaching to one of these is a
	// spectator, and an answer it sends is stale by construction.
	if got := answerPermission(t, ts, "s1", `{"requestId":"`+req.RequestID+`","optionId":"proceed_once"}`); got != http.StatusConflict {
		t.Errorf("answering an auto-approved request returned %d, want 409", got)
	}

	// And the engine got the answer, not merely the transcript.
	echo := stream.next("agent_message_chunk")
	if !strings.Contains(string(echo.Data), "permission:selected/proceed_once") {
		t.Errorf("engine saw %s", echo.Data)
	}
	stream.next(KindTurnEnd)
}

// Without autoApprove the request must still reach a human, which is the
// behaviour every interactive session depends on.
func TestAPermissionRequestWaitsForTheUserWhenNobodySaidOtherwise(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	created := decodeSession(t, createAuto(t, ts, "s1", false), http.StatusCreated)
	if created.AutoApprove {
		t.Fatal("session reported itself auto-approved when the create did not ask for it")
	}

	stream := promptForPermission(t, ts, "s1", "!ask")
	req := decodeInto[PermissionRequest](t, stream.next(KindPermissionRequest))

	if got := answerPermission(t, ts, "s1", `{"requestId":"`+req.RequestID+`","optionId":"proceed_always"}`); got != http.StatusNoContent {
		t.Fatalf("answering a pending request returned %d, want 204", got)
	}

	res := decodeInto[resolvedEvent](t, stream.next(KindPermissionResolved))
	if res.OptionID != "proceed_always" {
		t.Errorf("recorded %q, want the option the user picked", res.OptionID)
	}
	if res.Reason != "" {
		t.Errorf("reason = %q, want none: a user answered this one", res.Reason)
	}
	echo := stream.next("agent_message_chunk")
	if !strings.Contains(string(echo.Data), "permission:selected/proceed_always") {
		t.Errorf("engine saw %s", echo.Data)
	}
}

// An auto-approved session answers what it can and no more. A request
// that offers only refusals is not one acpd may decide on its own: the
// caller said nobody would be answering, not that everything is allowed.
func TestAutoApproveLeavesARequestItCannotAllowToTheUser(t *testing.T) {
	registerFakeEngine(t)
	_, ts := newTestServer(t)

	decodeSession(t, createAuto(t, ts, "s1", true), http.StatusCreated)

	stream := promptForPermission(t, ts, "s1", "!ask deny")
	req := decodeInto[PermissionRequest](t, stream.next(KindPermissionRequest))

	// Still pending — which is exactly what a 200 here proves, since an
	// already-answered request is a 409.
	if got := answerPermission(t, ts, "s1", `{"requestId":"`+req.RequestID+`","cancelled":true}`); got != http.StatusNoContent {
		t.Fatalf("answering returned %d, want 204 — the request should have been left pending", got)
	}

	res := decodeInto[resolvedEvent](t, stream.next(KindPermissionResolved))
	if res.Outcome != "cancelled" {
		t.Errorf("outcome = %q, want cancelled", res.Outcome)
	}
}

func TestAllowOnceOptionPrefersTheNarrowestAllow(t *testing.T) {
	once := acp.PermissionOption{OptionID: "a", Kind: acp.PermissionAllowOnce}
	always := acp.PermissionOption{OptionID: "b", Kind: acp.PermissionAllowAlways}
	reject := acp.PermissionOption{OptionID: "c", Kind: acp.PermissionRejectOnce}

	tests := []struct {
		name    string
		options []acp.PermissionOption
		want    string
		wantOK  bool
	}{
		// Order on the wire must not decide it: gemini lists "allow for
		// this session" before "allow".
		{"always listed first", []acp.PermissionOption{always, once, reject}, "a", true},
		{"only always", []acp.PermissionOption{always, reject}, "b", true},
		{"nothing allows", []acp.PermissionOption{reject}, "", false},
		{"no options at all", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := allowOnceOption(tc.options)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("got (%q, %t), want (%q, %t)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
