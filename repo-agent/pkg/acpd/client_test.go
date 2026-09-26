package acpd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder captures what the client actually put on the wire, which is
// the only thing the server ever sees.
type recorder struct {
	method string
	path   string
	// escapedPath is the path as it arrived. r.URL.Path is already
	// percent-decoded, so it cannot show whether escaping happened.
	escapedPath string
	query       string
	header      http.Header
	body        []byte
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.escapedPath = r.URL.EscapedPath()
		rec.query = r.URL.RawQuery
		rec.header = r.Header.Clone()
		rec.body, _ = io.ReadAll(r.Body)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), rec
}

// The engine key must travel in a header and must not appear in the
// request body. acpd writes conversation bodies to an on-disk
// transcript, so a key that reaches the body has reached the disk.
func TestCreateSessionPutsKeyInHeaderNotBody(t *testing.T) {
	client, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Session{ID: "s1", Engine: EngineGemini, CWD: "/workspaces/repo"})
	})

	got, err := client.CreateSession(context.Background(), CreateSessionRequest{
		ID:     "s1",
		Engine: EngineGemini,
		CWD:    "/workspaces/repo",
	}, "secret-key-value")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got.ID != "s1" {
		t.Errorf("session id = %q, want s1", got.ID)
	}
	if h := rec.header.Get(APIKeyHeader); h != "secret-key-value" {
		t.Errorf("%s = %q, want the key", APIKeyHeader, h)
	}
	if rec.header.Get("Authorization") != "" {
		t.Error("Authorization was set; the engine key is not caller authentication")
	}
	if strings.Contains(string(rec.body), "secret-key-value") {
		t.Errorf("key leaked into the request body: %s", rec.body)
	}
	if rec.method != http.MethodPost || rec.path != "/sessions" {
		t.Errorf("got %s %s, want POST /sessions", rec.method, rec.path)
	}
}

// Failing before the request means a misconfigured caller never causes
// an engine to be spawned without a credential.
func TestCreateSessionRejectsMissingInputs(t *testing.T) {
	called := false
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	if _, err := client.CreateSession(context.Background(), CreateSessionRequest{ID: "s1"}, ""); err == nil {
		t.Error("expected an error for an empty api key")
	}
	if _, err := client.CreateSession(context.Background(), CreateSessionRequest{}, "k"); err == nil {
		t.Error("expected an error for an empty session id")
	}
	if called {
		t.Error("client sent a request it should have rejected locally")
	}
}

// An unset engine is the common case from a UI; defaulting it here keeps
// every caller from repeating the only value that works.
func TestCreateSessionDefaultsEngine(t *testing.T) {
	client, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Session{ID: "s1"})
	})
	if _, err := client.CreateSession(context.Background(), CreateSessionRequest{ID: "s1"}, "k"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var sent CreateSessionRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("decoding sent body: %v", err)
	}
	if sent.Engine != EngineGemini {
		t.Errorf("engine = %q, want %q", sent.Engine, EngineGemini)
	}
}

// A session that has gone is the ordinary end of a conversation whose
// pod went away, not a failure to report loudly. Callers need to tell it
// apart from a broken acpd, so it has to be matchable.
func TestNotFoundIsMatchable(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown session"})
	})

	_, err := client.GetSession(context.Background(), "gone")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false for %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("want an *Error with 404, got %#v", err)
	}
	if !strings.Contains(err.Error(), "unknown session") {
		t.Errorf("server message lost: %v", err)
	}
}

// A 500 must not be mistaken for a missing session: one is worth
// retrying against the same pod, the other is not.
func TestServerErrorIsNotNotFound(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	_, err := client.GetSession(context.Background(), "s1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("a 500 matched ErrNotFound")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("non-JSON error body lost: %v", err)
	}
}

// Each verb has to land on the route acpd actually serves; a typo here
// is a 404 at runtime and nothing at compile time.
func TestRoutesAndMethods(t *testing.T) {
	cases := []struct {
		name       string
		call       func(c *Client) error
		wantMethod string
		wantPath   string
	}{
		{
			name: "prompt",
			call: func(c *Client) error {
				_, err := c.Prompt(context.Background(), "s1", "hello")
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/sessions/s1/prompt",
		},
		{
			name:       "cancel",
			call:       func(c *Client) error { return c.Cancel(context.Background(), "s1") },
			wantMethod: http.MethodPost,
			wantPath:   "/sessions/s1/cancel",
		},
		{
			name:       "delete",
			call:       func(c *Client) error { return c.DeleteSession(context.Background(), "s1") },
			wantMethod: http.MethodDelete,
			wantPath:   "/sessions/s1",
		},
		{
			name: "permission",
			call: func(c *Client) error {
				return c.ResolvePermission(context.Background(), "s1", PermissionResolution{
					RequestID: "r1", OptionID: "allow",
				})
			},
			wantMethod: http.MethodPost,
			wantPath:   "/sessions/s1/permission",
		},
		{
			name:       "health",
			call:       func(c *Client) error { return c.Health(context.Background()) },
			wantMethod: http.MethodGet,
			wantPath:   "/healthz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				// An empty object rather than an empty body: the verbs
				// that decode a reply would otherwise fail on EOF, which
				// would be this table testing the body and not the route.
				_, _ = w.Write([]byte("{}"))
			})
			if err := tc.call(client); err != nil {
				t.Fatalf("call: %v", err)
			}
			if rec.method != tc.wantMethod || rec.path != tc.wantPath {
				t.Errorf("got %s %s, want %s %s", rec.method, rec.path, tc.wantMethod, tc.wantPath)
			}
		})
	}
}

// The offset a prompt returns is what a follower resumes from, and it is
// the position AFTER the user_prompt event acpd just wrote. Dropping it
// would make the UI re-read the message it only just sent.
func TestPromptReturnsResumeOffset(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"offset":4096}`))
	})
	offset, err := client.Prompt(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if offset != 4096 {
		t.Errorf("offset = %d, want 4096", offset)
	}
}

// A session id reaches acpd in the URL path, so one containing a slash
// would otherwise silently address a different route.
func TestSessionIDIsEscaped(t *testing.T) {
	client, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := client.Cancel(context.Background(), "a/b"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if rec.escapedPath != "/sessions/a%2Fb/cancel" {
		t.Errorf("escaped path = %q, want /sessions/a%%2Fb/cancel", rec.escapedPath)
	}
}

// Local validation keeps malformed calls off the wire, where they would
// only turn into a 400 the caller has to interpret anyway.
func TestLocalValidation(t *testing.T) {
	called := false
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	if _, err := client.Prompt(context.Background(), "s1", ""); err == nil {
		t.Error("expected an error for empty prompt text")
	}
	if err := client.ResolvePermission(context.Background(), "s1", PermissionResolution{}); err == nil {
		t.Error("expected an error for a missing requestId")
	}
	if err := client.ResolvePermission(context.Background(), "s1", PermissionResolution{RequestID: "r1"}); err == nil {
		t.Error("expected an error when neither optionId nor cancelled is set")
	}
	if called {
		t.Error("client sent a request it should have rejected locally")
	}
}

// Cancelling is a legitimate answer and must not be mistaken for the
// incomplete case the validation above rejects.
func TestCancelledPermissionIsValid(t *testing.T) {
	client, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	err := client.ResolvePermission(context.Background(), "s1", PermissionResolution{
		RequestID: "r1", Cancelled: true,
	})
	if err != nil {
		t.Fatalf("ResolvePermission: %v", err)
	}
	var sent PermissionResolution
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if !sent.Cancelled || sent.RequestID != "r1" {
		t.Errorf("sent %+v, want cancelled r1", sent)
	}
}

func TestListSessions(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Session{{ID: "s1"}, {ID: "s2", Busy: true}})
	})
	got, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 || got[0].ID != "s1" || !got[1].Busy {
		t.Errorf("got %+v", got)
	}
}

// A base URL from configuration may well carry a trailing slash, and
// "//sessions" is not the route acpd registered.
func TestBaseURLTrailingSlash(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	if err := New(srv.URL + "///").Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if rec.path != "/healthz" {
		t.Errorf("path = %q, want /healthz", rec.path)
	}
}

func TestNewForPodIP(t *testing.T) {
	c := NewForPodIP("10.1.2.3")
	want := "http://10.1.2.3:49984"
	if c.baseURL != want {
		t.Errorf("baseURL = %q, want %q", c.baseURL, want)
	}
}

func TestSetMode(t *testing.T) {
	client, rec := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Session{
			ID:             "s1",
			Mode:           ModeYolo,
			AvailableModes: []SessionMode{{ID: ModeDefault, Name: "Default"}, {ID: ModeYolo, Name: "YOLO"}},
		})
	})

	got, err := client.SetMode(context.Background(), "s1", ModeYolo)
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/sessions/s1/mode" {
		t.Errorf("got %s %s, want POST /sessions/s1/mode", rec.method, rec.path)
	}
	if !strings.Contains(string(rec.body), `"mode":"yolo"`) {
		t.Errorf("body = %s, want the mode in it", rec.body)
	}
	if got.Mode != ModeYolo {
		t.Errorf("mode = %q, want yolo", got.Mode)
	}
	// The set the UI builds its switcher from. Dropping it here would
	// leave the browser with a mode it cannot change back.
	if len(got.AvailableModes) != 2 {
		t.Errorf("availableModes = %+v, want both", got.AvailableModes)
	}
}

// An empty mode is caught before the request: acpd would answer 400, but
// a caller that meant "leave it alone" should not be making the call.
func TestSetModeRejectsAnEmptyMode(t *testing.T) {
	called := false
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	if _, err := client.SetMode(context.Background(), "s1", ""); err == nil {
		t.Error("expected an error for an empty mode")
	}
	if called {
		t.Error("an empty mode reached the server")
	}
}

// Research sessions run auto-approving. The canned openings are fired by
// the controller with nothing attached, so a permission prompt there is
// not friction but a turn that blocks and is then cancelled.
func TestResearchModeIsAutoApproving(t *testing.T) {
	if ResearchMode != ModeYolo {
		t.Errorf("ResearchMode = %q, want %q", ResearchMode, ModeYolo)
	}
}
