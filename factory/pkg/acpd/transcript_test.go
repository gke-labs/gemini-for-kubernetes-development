package acpd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTranscriptAppendAndReadFromOffset(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	if err := tr.AppendValue(KindUserPrompt, map[string]string{"text": "first"}); err != nil {
		t.Fatalf("append first: %v", err)
	}
	afterFirst := tr.Size()
	if afterFirst == 0 {
		t.Fatal("size did not grow after append")
	}

	if err := tr.AppendValue(KindUserPrompt, map[string]string{"text": "second"}); err != nil {
		t.Fatalf("append second: %v", err)
	}

	// Reading from the offset handed back after the first event must yield
	// the second and only the second. This is the reconnect contract: a
	// browser resuming at its last offset sees exactly what it missed.
	chunk, next, err := tr.ReadFrom(afterFirst)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if next != tr.Size() {
		t.Errorf("next offset = %d, want %d", next, tr.Size())
	}
	if strings.Contains(string(chunk), "first") {
		t.Errorf("chunk from offset %d replayed an already-read event: %s", afterFirst, chunk)
	}
	if !strings.Contains(string(chunk), "second") {
		t.Errorf("chunk missing the new event: %s", chunk)
	}
	if got := bytes.Count(chunk, []byte("\n")); got != 1 {
		t.Errorf("chunk has %d lines, want 1: %s", got, chunk)
	}
}

func TestTranscriptEventsAreWellFormedNDJSON(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	for i := 0; i < 3; i++ {
		if err := tr.AppendValue("agent_message_chunk", map[string]int{"n": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	chunk, _, err := tr.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(chunk), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d is not valid JSON: %v (%s)", i, err, line)
		}
		// Sequence numbers must be dense and 1-based; the UI uses them to
		// tell "nothing happened" from "we dropped something".
		if ev.Seq != int64(i+1) {
			t.Errorf("line %d has seq %d, want %d", i, ev.Seq, i+1)
		}
		if ev.Time.IsZero() {
			t.Errorf("line %d has no timestamp", i)
		}
	}
}

func TestTranscriptReadPastEndIsEmpty(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	if err := tr.AppendValue(KindUserPrompt, map[string]string{"text": "only"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	chunk, next, err := tr.ReadFrom(tr.Size())
	if err != nil {
		t.Fatalf("ReadFrom at EOF: %v", err)
	}
	if len(chunk) != 0 {
		t.Errorf("expected no bytes at EOF, got %q", chunk)
	}
	if next != tr.Size() {
		t.Errorf("next = %d, want %d", next, tr.Size())
	}
}

func TestTranscriptWaitWakesOnAppend(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	woke := make(chan bool, 1)
	go func() { woke <- tr.Wait(ctx, 0) }()

	// Give the waiter time to block, then append. A Wait that took the
	// size before the wake channel would miss this and hang.
	time.Sleep(20 * time.Millisecond)
	if err := tr.AppendValue(KindUserPrompt, map[string]string{"text": "hello"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	select {
	case ok := <-woke:
		if !ok {
			t.Error("Wait reported nothing to read after an append")
		}
	case <-ctx.Done():
		t.Fatal("Wait did not wake within 5s of an append")
	}
}

func TestTranscriptWaitReturnsWhenDataAlreadyPresent(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	if err := tr.AppendValue(KindUserPrompt, map[string]string{"text": "already here"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !tr.Wait(ctx, 0) {
		t.Error("Wait blocked despite data being available at offset 0")
	}
}

func TestTranscriptWaitReleasesOnClose(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}

	released := make(chan bool, 1)
	go func() { released <- tr.Wait(context.Background(), 0) }()

	time.Sleep(20 * time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case ok := <-released:
		if ok {
			t.Error("Wait reported readable data on a closed transcript")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not release a blocked follower")
	}
}

func TestTranscriptWaitHonoursContext(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan bool, 1)
	go func() { done <- tr.Wait(ctx, 0) }()

	select {
	case ok := <-done:
		if ok {
			t.Error("Wait reported data after its context expired")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait ignored its context")
	}
}

// Concurrent appends and follows are the normal case: the agent streams
// while the browser reads. Run under -race, this is what catches a
// transcript that is only accidentally thread-safe.
func TestTranscriptConcurrentAppendAndFollow(t *testing.T) {
	tr, err := OpenTranscript(t.TempDir())
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	defer tr.Close()

	const events = 50
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < events; i++ {
			if err := tr.AppendValue("agent_message_chunk", map[string]int{"n": i}); err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
		}
	}()

	var seen int
	var offset int64
	for seen < events {
		if !tr.Wait(ctx, offset) {
			break
		}
		chunk, next, err := tr.ReadFrom(offset)
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		seen += bytes.Count(chunk, []byte("\n"))
		offset = next
	}
	wg.Wait()

	if seen != events {
		t.Errorf("follower saw %d events, want %d", seen, events)
	}
}

func TestOpenTranscriptResumesExistingFile(t *testing.T) {
	dir := t.TempDir()

	first, err := OpenTranscript(dir)
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	if err := first.AppendValue(KindUserPrompt, map[string]string{"text": "before restart"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	sizeBefore := first.Size()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A restarted acpd must append, not truncate: the session is gone but
	// its history is what the user still has.
	second, err := OpenTranscript(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	if second.Size() != sizeBefore {
		t.Errorf("reopened size = %d, want %d (file was truncated)", second.Size(), sizeBefore)
	}
	chunk, _, err := second.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if !strings.Contains(string(chunk), "before restart") {
		t.Error("history from before the restart was lost")
	}
}
