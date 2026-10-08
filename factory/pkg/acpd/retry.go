package acpd

import (
	"bytes"
	"io"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// KindEngineRetry marks a model call the engine retried, at the point in
// the conversation it happened. Its payload is an EngineRetry.
//
// Only what the engine says on stderr: gemini logs the calls that failed
// before the answer began streaming (rate limits, quota, an overloaded
// model), which is what keeps a session silent for minutes. A call that
// fails mid-answer is retried without a word on stderr, so it leaves no
// marker.
const KindEngineRetry = "engine_retry"

// EngineRetry is one retried model call.
type EngineRetry struct {
	// Status is the HTTP status that failed the call ("429", "503"),
	// "5xx" when the engine did not say which, "" when it said nothing.
	Status  string    `json:"status,omitempty"`
	Attempt int       `json:"attempt"`
	Time    time.Time `json:"time"`
}

// RetryParser recognises an engine's stderr line about a retried model
// call, or returns false.
type RetryParser func(line string) (EngineRetry, bool)

// geminiRetry is gemini-cli's retryWithBackoff logging, in its forms:
//
//	Attempt 3 failed with status 429. Retrying with backoff...
//	Attempt 3 failed with status 429. Retrying after explicit delay of 2000ms...
//	Attempt 3 failed with 429 error (no Retry-After header). Retrying with backoff...
//	Attempt 3 failed with 5xx error. Retrying with backoff...
//	Attempt 3 failed. Retrying with backoff...
var geminiRetryLine = regexp.MustCompile(`Attempt (\d+) failed(?: with (?:status (\d{3})|(\d{3}|5xx) error))?[^.]*\. Retrying`)

func geminiRetry(line string) (EngineRetry, bool) {
	m := geminiRetryLine.FindStringSubmatch(line)
	if m == nil {
		return EngineRetry{}, false
	}
	n, _ := strconv.Atoi(m[1])
	return EngineRetry{Status: m[2] + m[3], Attempt: n}, true
}

// maxStderrLine is how much of a stderr line is kept for recognising it.
// The engine dumps whole API errors, JSON and stack included; the retry
// sentence is at the start of its line.
const maxStderrLine = 4 << 10

// lineWriter passes everything written to it to w, and every line, cut to
// maxStderrLine, to onLine.
type lineWriter struct {
	w      io.Writer
	onLine func(string)

	mu      sync.Mutex
	partial []byte
	long    bool
}

func (l *lineWriter) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	l.mu.Lock()
	defer l.mu.Unlock()
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		chunk := rest
		if i >= 0 {
			chunk = rest[:i]
		}
		if room := maxStderrLine - len(l.partial); room > 0 {
			l.partial = append(l.partial, chunk[:min(room, len(chunk))]...)
		}
		if i < 0 {
			break
		}
		l.onLine(string(l.partial))
		l.partial = l.partial[:0]
		rest = rest[i+1:]
	}
	return n, err
}

// onEngineLine records a retried model call: a marker in the transcript,
// and the session's state until the engine says anything else.
func (s *Session) onEngineLine(line string) {
	if s.parseRetry == nil {
		return
	}
	r, ok := s.parseRetry(line)
	if !ok {
		return
	}
	r.Time = time.Now().UTC()
	s.mu.Lock()
	s.retry = &r
	s.mu.Unlock()
	_ = s.transcript.AppendValue(KindEngineRetry, r)
}

// Retrying is the model call the engine is retrying now: the last one it
// logged, while the turn has not moved on since. Nil otherwise.
func (s *Session) Retrying() *EngineRetry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.busy || s.retry == nil {
		return nil
	}
	r := *s.retry
	return &r
}

// clearRetry is called whenever the engine is heard from again.
func (s *Session) clearRetry() {
	s.mu.Lock()
	s.retry = nil
	s.mu.Unlock()
}
