package acpd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// encodeEvent frames an event exactly as acpd's transcript does: one
// JSON object, one newline. The offset arithmetic under test is only
// correct if this framing matches, so the tests build their fixture the
// same way the server builds its file.
func encodeEvent(t *testing.T, seq int64, kind string, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling event data: %v", err)
	}
	line, err := json.Marshal(Event{
		Seq:  seq,
		Time: time.Unix(1700000000+seq, 0).UTC(),
		Kind: kind,
		Data: raw,
	})
	if err != nil {
		t.Fatalf("marshalling event: %v", err)
	}
	return append(line, '\n')
}

// transcriptFixture is a whole transcript plus the offset each event
// ends at, which is what a resuming client is supposed to report.
type transcriptFixture struct {
	bytes   []byte
	kinds   []string
	offsets []int64
}

func buildTranscript(t *testing.T) transcriptFixture {
	t.Helper()
	var f transcriptFixture
	add := func(seq int64, kind string, data any) {
		f.bytes = append(f.bytes, encodeEvent(t, seq, kind, data)...)
		f.kinds = append(f.kinds, kind)
		f.offsets = append(f.offsets, int64(len(f.bytes)))
	}
	add(1, KindUserPrompt, UserPromptData{Text: "what does this repo do?"})
	add(2, "agent_message_chunk", map[string]string{"text": "Reading the module layout"})
	add(3, KindPermissionRequest, PermissionRequest{
		RequestID: "r1",
		Options: []PermissionOption{
			{OptionID: "allow", Name: "Allow", Kind: "allow_once"},
			{OptionID: "deny", Name: "Deny", Kind: "reject_once"},
		},
	})
	add(4, KindPermissionResolved, PermissionResolvedData{RequestID: "r1", Outcome: "selected", OptionID: "allow"})
	add(5, KindTurnEnd, TurnEndData{StopReason: "end_turn"})
	return f
}

// transcriptServer serves a fixed transcript the way acpd does: honour
// ?offset= by slicing, and end the response rather than blocking (the
// follow-forever behaviour is exercised separately).
func transcriptServer(t *testing.T, body []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := int64(0)
		if raw := r.URL.Query().Get("offset"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 0 || parsed > int64(len(body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			offset = parsed
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[offset:])
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

// The offset the client reports must be the byte position in the file,
// because that is the only thing acpd accepts to resume from. If the
// client counted events instead, or forgot the newline, every resume
// would land mid-object.
func TestOffsetsMatchTranscriptBytes(t *testing.T) {
	fixture := buildTranscript(t)
	client := transcriptServer(t, fixture.bytes)

	stream, err := client.Events(context.Background(), "s1", 0, false)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var i int
	for stream.Next() {
		if i >= len(fixture.kinds) {
			t.Fatalf("got more events than the %d written", len(fixture.kinds))
		}
		if got := stream.Event().Kind; got != fixture.kinds[i] {
			t.Errorf("event %d kind = %q, want %q", i, got, fixture.kinds[i])
		}
		if got := stream.Offset(); got != fixture.offsets[i] {
			t.Errorf("event %d offset = %d, want %d", i, got, fixture.offsets[i])
		}
		i++
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if i != len(fixture.kinds) {
		t.Errorf("read %d events, want %d", i, len(fixture.kinds))
	}
	if stream.Offset() != int64(len(fixture.bytes)) {
		t.Errorf("final offset = %d, want %d", stream.Offset(), len(fixture.bytes))
	}
}

// A browser that loses its connection reconnects from the last offset
// it saw. Every event must arrive exactly once across that seam: a
// duplicate reprints the agent, a gap loses it.
func TestResumeFromEveryOffsetIsExactlyOnce(t *testing.T) {
	fixture := buildTranscript(t)
	client := transcriptServer(t, fixture.bytes)

	for cut := range fixture.kinds {
		t.Run(fmt.Sprintf("after_event_%d", cut), func(t *testing.T) {
			// Read up to and including event `cut`, then resume.
			first, err := client.Events(context.Background(), "s1", 0, false)
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			var seen []string
			for i := 0; i <= cut && first.Next(); i++ {
				seen = append(seen, first.Event().Kind)
			}
			resumeAt := first.Offset()
			_ = first.Close()

			if resumeAt != fixture.offsets[cut] {
				t.Fatalf("resume offset = %d, want %d", resumeAt, fixture.offsets[cut])
			}

			second, err := client.Events(context.Background(), "s1", resumeAt, false)
			if err != nil {
				t.Fatalf("Events resume: %v", err)
			}
			defer func() { _ = second.Close() }()
			for second.Next() {
				seen = append(seen, second.Event().Kind)
			}
			if err := second.Err(); err != nil {
				t.Fatalf("resumed stream error: %v", err)
			}

			if len(seen) != len(fixture.kinds) {
				t.Fatalf("saw %d events across the seam, want %d: %v", len(seen), len(fixture.kinds), seen)
			}
			for i, kind := range fixture.kinds {
				if seen[i] != kind {
					t.Errorf("event %d = %q, want %q", i, seen[i], kind)
				}
			}
		})
	}
}

// Payloads have to survive the round trip, or the UI renders an empty
// bubble for a turn that actually said something.
func TestEventPayloadsDecode(t *testing.T) {
	fixture := buildTranscript(t)
	client := transcriptServer(t, fixture.bytes)

	stream, err := client.Events(context.Background(), "s1", 0, false)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var sawPrompt, sawPermission, sawTurnEnd bool
	for stream.Next() {
		event := stream.Event()
		switch event.Kind {
		case KindUserPrompt:
			var data UserPromptData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decoding user_prompt: %v", err)
			}
			if data.Text != "what does this repo do?" {
				t.Errorf("prompt text = %q", data.Text)
			}
			sawPrompt = true
		case KindPermissionRequest:
			var data PermissionRequest
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decoding permission_request: %v", err)
			}
			if data.RequestID != "r1" || len(data.Options) != 2 || data.Options[0].OptionID != "allow" {
				t.Errorf("permission request = %+v", data)
			}
			sawPermission = true
		case KindTurnEnd:
			var data TurnEndData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decoding turn_end: %v", err)
			}
			if data.StopReason != "end_turn" {
				t.Errorf("stopReason = %q", data.StopReason)
			}
			sawTurnEnd = true
		}
	}
	if !sawPrompt || !sawPermission || !sawTurnEnd {
		t.Errorf("missed events: prompt=%v permission=%v turnEnd=%v", sawPrompt, sawPermission, sawTurnEnd)
	}
}

// Kind is an open set. An engine that invents a new sessionUpdate kind
// must not break the stream, or every engine upgrade becomes a
// repo-agent release.
func TestUnknownKindIsCarriedNotRejected(t *testing.T) {
	body := encodeEvent(t, 1, "some_future_kind", map[string]string{"anything": "goes"})
	client := transcriptServer(t, body)

	stream, err := client.Events(context.Background(), "s1", 0, false)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if !stream.Next() {
		t.Fatalf("no event read, err=%v", stream.Err())
	}
	if stream.Event().Kind != "some_future_kind" {
		t.Errorf("kind = %q", stream.Event().Kind)
	}
	if stream.Next() {
		t.Error("unexpected second event")
	}
	if err := stream.Err(); err != nil {
		t.Errorf("stream error: %v", err)
	}
}

// A line cut off mid-write must leave the offset at its first byte.
// Advancing past it would resume inside a JSON object and desynchronise
// the stream for good.
func TestTruncatedFinalLineDoesNotAdvanceOffset(t *testing.T) {
	complete := encodeEvent(t, 1, KindUserPrompt, UserPromptData{Text: "hi"})
	partial := encodeEvent(t, 2, "agent_message_chunk", map[string]string{"text": "half"})
	body := append(append([]byte{}, complete...), partial[:len(partial)/2]...)

	client := transcriptServer(t, body)
	stream, err := client.Events(context.Background(), "s1", 0, false)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if !stream.Next() {
		t.Fatalf("expected the complete event, err=%v", stream.Err())
	}
	if stream.Next() {
		t.Error("returned an event from a truncated line")
	}
	if err := stream.Err(); err != nil {
		t.Errorf("a truncated tail is not an error: %v", err)
	}
	if stream.Offset() != int64(len(complete)) {
		t.Errorf("offset = %d, want %d (the start of the partial line)", stream.Offset(), len(complete))
	}
}

// Corruption in the middle of the file is different from a partial
// tail: there is no offset that recovers from it, so it has to be
// reported rather than skipped.
func TestUndecodableLineIsAnError(t *testing.T) {
	body := append([]byte("{not json}\n"), encodeEvent(t, 2, KindTurnEnd, TurnEndData{})...)
	client := transcriptServer(t, body)

	stream, err := client.Events(context.Background(), "s1", 0, false)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if stream.Next() {
		t.Error("decoded an event from a corrupt line")
	}
	if stream.Err() == nil {
		t.Error("expected an error for a corrupt line")
	}
}

// follow=false is how a one-shot render asks for what exists now. The
// default has to stay follow, since that is what a live view needs.
func TestFollowQueryParam(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL)

	stream, err := client.Events(context.Background(), "s1", 0, false)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	_ = stream.Close()
	if gotQuery != "follow=false" {
		t.Errorf("query = %q, want follow=false", gotQuery)
	}

	stream, err = client.Events(context.Background(), "s1", 42, true)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	_ = stream.Close()
	if gotQuery != "offset=42" {
		t.Errorf("query = %q, want offset=42", gotQuery)
	}
}

// A missing session must fail at open rather than look like an empty
// conversation, which is what a bare io.EOF would look like.
func TestEventsOnMissingSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown session"})
	}))
	t.Cleanup(srv.Close)

	_, err := New(srv.URL).Events(context.Background(), "gone", 0, true)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false for %v", err)
	}
}

// Follow must unwind when the consumer goes away and report how far it
// got, so a reconnect does not replay what was already delivered.
func TestFollowStopsOnConsumerError(t *testing.T) {
	fixture := buildTranscript(t)
	client := transcriptServer(t, fixture.bytes)

	stopAfter := errors.New("consumer gone")
	var seen int
	offset, err := client.Follow(context.Background(), "s1", 0, func(Event) error {
		seen++
		if seen == 2 {
			return stopAfter
		}
		return nil
	})
	if !errors.Is(err, stopAfter) {
		t.Errorf("err = %v, want the consumer error", err)
	}
	if seen != 2 {
		t.Errorf("consumed %d events, want 2", seen)
	}
	if offset != fixture.offsets[1] {
		t.Errorf("offset = %d, want %d", offset, fixture.offsets[1])
	}
}

// A live view is cancelled by the user navigating away. That has to
// surface as the context's error, not as a clean end that would look
// like the agent finished its turn.
func TestFollowOnCancelReportsContextError(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server cannot flush")
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		_, _ = w.Write(encodeEvent(t, 1, KindUserPrompt, UserPromptData{Text: "hi"}))
		flusher.Flush()
		once.Do(func() { close(release) })
		<-r.Context().Done() // hold the stream open like a quiet session
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-release
		cancel()
	}()

	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = New(srv.URL).Follow(ctx, "s1", 0, func(Event) error { return nil })
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Follow did not return after the context was cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestEventsRejectsNegativeOffset(t *testing.T) {
	if _, err := New("http://example.invalid").Events(context.Background(), "s1", -1, true); err == nil {
		t.Error("expected an error for a negative offset")
	}
}
