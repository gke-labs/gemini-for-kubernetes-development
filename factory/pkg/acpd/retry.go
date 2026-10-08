package acpd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
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
	// QuotaExceeded is set once the engine said the quota is used up:
	// retrying will not help, somebody has to raise it or change the key.
	QuotaExceeded bool `json:"quota_exceeded,omitempty"`
}

// RetryParser recognises an engine's stderr line about a retried model
// call (Attempt > 0), or one saying the quota is exceeded (Attempt 0,
// QuotaExceeded), or returns false. It is the only engine-specific part:
// acpd keeps the counts (see RetriesFile) and the transcript markers.
type RetryParser func(line string) (EngineRetry, bool)

// RetriesFile is the session's retried model calls so far, by status,
// beside its engine log: an EngineRetries, replaced on every change. The
// task server reads it to show a task's retries without knowing its
// engine.
const RetriesFile = "engine-retries.json"

// EngineRetries is the content of RetriesFile.
type EngineRetries struct {
	// Statuses counts the retries by EngineRetry.Status, "unknown" when
	// the engine did not say.
	Statuses      map[string]int `json:"statuses"`
	QuotaExceeded bool           `json:"quota_exceeded,omitempty"`
}

// geminiRetry is gemini-cli's retryWithBackoff logging, in its forms:
//
//	Attempt 3 failed with status 429. Retrying with backoff...
//	Attempt 3 failed with status 429. Retrying after explicit delay of 2000ms...
//	Attempt 3 failed with 429 error (no Retry-After header). Retrying with backoff...
//	Attempt 3 failed with 5xx error. Retrying with backoff...
//	Attempt 3 failed. Retrying with backoff...
//
// and the API error it dumps after a 429 whose quota is used up, which
// says "You exceeded your current quota".
var geminiRetryLine = regexp.MustCompile(`Attempt (\d+) failed(?: with (?:status (\d{3})|(\d{3}|5xx) error))?[^.]*\. Retrying`)

func geminiRetry(line string) (EngineRetry, bool) {
	m := geminiRetryLine.FindStringSubmatch(line)
	if m == nil {
		if strings.Contains(line, "exceeded your current quota") {
			return EngineRetry{QuotaExceeded: true}, true
		}
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
// the session's state until the engine says anything else, and the counts
// in RetriesFile. A quota line marks the counts and the call being
// retried, without a marker of its own.
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
	if r.Attempt > 0 {
		status := r.Status
		if status == "" {
			status = "unknown"
		}
		s.retries.Statuses[status]++
		s.retry = &r
	}
	if r.QuotaExceeded {
		s.retries.QuotaExceeded = true
		if s.retry != nil {
			s.retry.QuotaExceeded = true
		}
	}
	data, _ := json.Marshal(s.retries)
	s.mu.Unlock()
	if err := replaceFile(s.dir, RetriesFile, data); err != nil {
		klog.Background().Error(err, "writing engine retries", "session", s.ID)
	}
	if r.Attempt > 0 {
		_ = s.transcript.AppendValue(KindEngineRetry, r)
	}
}

// loadRetries starts the counts from the ones a previous engine in the
// same session directory left, as its log is appended to as well.
func loadRetries(dir string) EngineRetries {
	r := EngineRetries{}
	if data, err := os.ReadFile(filepath.Join(dir, RetriesFile)); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	if r.Statuses == nil {
		r.Statuses = map[string]int{}
	}
	return r
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
