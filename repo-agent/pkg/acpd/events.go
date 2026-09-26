package acpd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// EventStream is a live read of one session's transcript.
//
// The unit of resumption is a byte offset, not an event index, because
// that is the contract acpd's transcript exposes and the one envd's tail
// already uses. Offset reports the position after the event just
// returned, so a caller that stores it and reconnects sees each event
// exactly once across a dropped connection.
type EventStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
	offset int64
	event  Event
	err    error
}

// Events opens a stream of transcript events from offset.
//
// With follow true the stream stays open and Next blocks waiting for the
// agent; the only ways it ends are the context being cancelled, the
// session being deleted, or the pod going away. With follow false it
// returns what is already on disk and then ends, which is what a
// one-shot render of a finished conversation wants.
//
// Pass offset 0 for the whole conversation from the beginning, or the
// Offset from a previous stream (or from Session.Offset) to resume.
func (c *Client) Events(ctx context.Context, id string, offset int64, follow bool) (*EventStream, error) {
	if offset < 0 {
		return nil, errors.New("acpd: offset must not be negative")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.eventsURL(id, offset, follow), nil)
	if err != nil {
		return nil, fmt.Errorf("acpd: building events request: %w", err)
	}

	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("acpd: opening events for %s: %w", id, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, statusError(resp)
	}

	return &EventStream{
		body: resp.Body,
		// The reader is generously sized: a tool_call event carrying a
		// file diff is not small, and a line longer than the buffer
		// would otherwise have to be stitched back together.
		reader: bufio.NewReaderSize(resp.Body, 64<<10),
		offset: offset,
	}, nil
}

// Next advances to the next event, returning false when the stream ends
// or fails. Check Err to tell those apart.
func (s *EventStream) Next() bool {
	if s.err != nil {
		return false
	}
	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if len(line) > 0 && errors.Is(err, io.EOF) {
				// A line without its terminator: the server was cut off
				// mid-write. Dropping it leaves offset pointing at its
				// first byte, so a reconnect re-reads it whole rather
				// than resuming into the middle of a JSON object.
				return false
			}
			if !errors.Is(err, io.EOF) {
				s.err = fmt.Errorf("acpd: reading events: %w", err)
			}
			return false
		}

		consumed := int64(len(line))
		trimmed := line[:len(line)-1]
		if len(trimmed) == 0 {
			// Blank lines are not events. Still count the bytes, or the
			// resume offset drifts behind the file.
			s.offset += consumed
			continue
		}

		var event Event
		if err := json.Unmarshal(trimmed, &event); err != nil {
			s.err = fmt.Errorf("acpd: undecodable transcript line at offset %d: %w", s.offset, err)
			return false
		}
		s.offset += consumed
		s.event = event
		return true
	}
}

// Event returns the event Next just read.
func (s *EventStream) Event() Event { return s.event }

// Offset is where to resume: the position after the current event.
func (s *EventStream) Offset() int64 { return s.offset }

// Err reports why the stream stopped, or nil if it simply ended. A
// cancelled context surfaces here as the context's error.
func (s *EventStream) Err() error { return s.err }

// Close releases the connection. Safe to call more than once.
func (s *EventStream) Close() error {
	if s.body == nil {
		return nil
	}
	body := s.body
	s.body = nil
	return body.Close()
}

// Follow streams events to fn until the stream ends, fn returns an
// error, or ctx is cancelled. It returns the offset reached, so a caller
// that reconnects after a failure does not replay what fn already saw.
//
// An error from fn stops the stream and is returned as-is; use it to
// unwind when the consumer (a websocket to a browser, say) has gone.
func (c *Client) Follow(ctx context.Context, id string, offset int64, fn func(Event) error) (int64, error) {
	stream, err := c.Events(ctx, id, offset, true)
	if err != nil {
		return offset, err
	}
	defer func() { _ = stream.Close() }()

	for stream.Next() {
		if err := fn(stream.Event()); err != nil {
			return stream.Offset(), err
		}
	}
	if err := stream.Err(); err != nil {
		return stream.Offset(), err
	}
	// A clean end with a live context means the session went away rather
	// than the caller losing interest; report the context's own error
	// otherwise so a cancel is not mistaken for the agent finishing.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stream.Offset(), ctxErr
	}
	return stream.Offset(), nil
}
