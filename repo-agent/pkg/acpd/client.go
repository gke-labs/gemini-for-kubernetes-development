package acpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client talks to one sandbox's acpd.
//
// One Client per sandbox, not one per session: acpd is a per-pod server
// and the base URL is the pod. It is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
	// stream is separate because it must have no timeout; see
	// defaultTimeout.
	stream *http.Client
}

// defaultTimeout bounds the short request/response calls. It does not
// apply to Events, which is long-lived by design and uses a client with
// no timeout at all — a follower on a quiet session is idle for as long
// as the user is thinking, and cutting it off would look like a crash.
const defaultTimeout = 30 * time.Second

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the transport, for tests and for callers that
// need their own dialer. It replaces the event-stream client too, so a
// caller passing one with a Timeout will cut its own streams off — pass
// a zero Timeout unless that is what you want.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		c.http = h
		c.stream = h
	}
}

// New returns a Client for a base URL such as http://10.1.2.3:49984.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: trimSlash(baseURL),
		http:    &http.Client{Timeout: defaultTimeout},
		stream:  &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewForPodIP returns a Client dialling a sandbox pod directly.
//
// Pod IP rather than the <sandbox>-lb Service because acpd's port is not
// on that Service, and adding it would change the manifest shared by
// every sandbox type. A pod IP goes stale when the pod restarts, but an
// acpd restart already loses the session, so the staleness costs nothing
// that was not lost anyway.
func NewForPodIP(ip string, opts ...Option) *Client {
	return New(fmt.Sprintf("http://%s:%d", ip, DefaultPort), opts...)
}

// Error is a non-2xx reply from acpd.
type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("acpd: http %d", e.StatusCode)
	}
	return fmt.Sprintf("acpd: http %d: %s", e.StatusCode, e.Message)
}

// ErrNotFound is matched by errors.Is for a 404, which is how a caller
// distinguishes "this session is gone" — the normal end of a
// conversation whose pod went away — from a real failure.
var ErrNotFound = errors.New("acpd: session not found")

func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

// Health reports whether acpd is serving. Used to wait out the gap
// between the pod going Ready and the server binding its port.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil, "")
}

// CreateSession starts a conversation and spawns the engine.
//
// apiKey is the engine credential. It is sent once, in a header, and
// acpd drops it as soon as the engine child is running — so it must be
// supplied again on any later create, including the create that follows
// an acpd restart. The caller already reads the member Secret, so
// re-supplying is cheap; holding the key in the session would not be.
func (c *Client) CreateSession(ctx context.Context, req CreateSessionRequest, apiKey string) (*Session, error) {
	if apiKey == "" {
		// Caught here rather than at the server so that a
		// misconfigured caller fails before an engine is spawned.
		return nil, errors.New("acpd: engine api key is required")
	}
	if req.ID == "" {
		return nil, errors.New("acpd: session id is required")
	}
	if req.Engine == "" {
		req.Engine = EngineGemini
	}
	var out Session
	if err := c.do(ctx, http.MethodPost, "/sessions", req, &out, apiKey); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListSessions returns every session this acpd is holding.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var out []Session
	if err := c.do(ctx, http.MethodGet, "/sessions", nil, &out, ""); err != nil {
		return nil, err
	}
	return out, nil
}

// GetSession returns one session, or an error matching ErrNotFound.
func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	var out Session
	if err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil, &out, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSession stops the engine and releases the session. The
// transcript file survives on the PVC until the sandbox does.
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(id), nil, nil, "")
}

// Prompt sends one turn and returns as soon as acpd has accepted it.
//
// It does not wait for the answer: the reply arrives as events on the
// transcript. A prompt sent while a turn is already in flight is
// rejected rather than queued, so callers that dispatch from a UI should
// check Session.Busy or handle the error.
func (c *Client) Prompt(ctx context.Context, id, text string) error {
	if text == "" {
		return errors.New("acpd: prompt text is required")
	}
	body := struct {
		Text string `json:"text"`
	}{Text: text}
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/prompt", body, nil, "")
}

// ResolvePermission answers a permission request the engine is blocked
// on. Until this lands the engine's turn makes no progress.
func (c *Client) ResolvePermission(ctx context.Context, id string, res PermissionResolution) error {
	if res.RequestID == "" {
		return errors.New("acpd: permission requestId is required")
	}
	if res.OptionID == "" && !res.Cancelled {
		return errors.New("acpd: permission needs an optionId or cancelled")
	}
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/permission", res, nil, "")
}

// Cancel interrupts the turn in flight. ACP models cancellation as a
// notification, so the turn ends with a cancelled stopReason rather than
// the call failing — expect a turn_end event, not silence.
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/cancel", nil, nil, "")
}

// do issues one short request. A nil out discards the body.
func (c *Client) do(ctx context.Context, method, path string, in, out any, apiKey string) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("acpd: encoding request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("acpd: building request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set(APIKeyHeader, apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("acpd: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp)
	}
	if out == nil {
		// Drain so the connection can be reused rather than dropped.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("acpd: decoding %s %s: %w", method, path, err)
	}
	return nil
}

// statusError turns a non-2xx reply into an *Error, preferring acpd's
// own {"error": ...} message over the raw body.
func statusError(resp *http.Response) error {
	// Bounded: an error body is a short JSON object, and an unbounded
	// read here would let a wedged server consume the caller's memory.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := ""
	var decoded struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &decoded) == nil && decoded.Error != "" {
		msg = decoded.Error
	} else {
		msg = string(bytes.TrimSpace(raw))
	}
	return &Error{StatusCode: resp.StatusCode, Message: msg}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// eventsURL builds the follow URL for a session.
func (c *Client) eventsURL(id string, offset int64, follow bool) string {
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	if !follow {
		q.Set("follow", "false")
	}
	u := c.baseURL + "/sessions/" + url.PathEscape(id) + "/events"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}
