package spool

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// EngineLogFile is where a task's agent session keeps the engine's stderr,
// relative to the task directory (acpd writes it in the session's dir).
const EngineLogFile = "session/engine.stderr.log"

// EngineRetries is how often the engine's model calls failed and were
// retried, by HTTP status, as the engine logged them. A task that is slow
// for no visible reason is usually one whose engine is retrying: a 429 is
// a rate limit or an exhausted quota, a 503 an overloaded model.
type EngineRetries struct {
	Statuses map[string]int `json:"statuses"`
	// QuotaExceeded is set when a 429 said the quota is used up: retrying
	// will not help, somebody has to raise it or change the key.
	QuotaExceeded bool `json:"quota_exceeded,omitempty"`
}

// Total is the number of retries.
func (r *EngineRetries) Total() int {
	n := 0
	for _, c := range r.Statuses {
		n += c
	}
	return n
}

// engineRetry is a failed call in an engine's log: gemini-cli writes
// "Attempt 3 failed with status 429. Retrying with backoff...".
var engineRetry = regexp.MustCompile(`Attempt \d+ failed with status (\d{3})`)

// ScanEngineLog counts the retries in an engine log, nil when there are
// none or no log.
func ScanEngineLog(path string) *EngineRetries {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := &EngineRetries{Statuses: map[string]int{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := engineRetry.FindStringSubmatch(line); m != nil {
			r.Statuses[m[1]]++
		}
		if strings.Contains(line, "exceeded your current quota") {
			r.QuotaExceeded = true
		}
	}
	if len(r.Statuses) == 0 && !r.QuotaExceeded {
		return nil
	}
	return r
}

// engineLogs remembers each log's counts until it changes, so listing the
// tasks, which a caller polls, reads a log only when it has grown.
var engineLogs = struct {
	sync.Mutex
	m map[string]engineLogScan
}{m: map[string]engineLogScan{}}

type engineLogScan struct {
	size    int64
	mod     time.Time
	retries *EngineRetries
}

// engineRetries is ScanEngineLog for a task directory, cached.
func engineRetries(taskDir string) *EngineRetries {
	path := filepath.Join(taskDir, EngineLogFile)
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	engineLogs.Lock()
	defer engineLogs.Unlock()
	if c, ok := engineLogs.m[path]; ok && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.retries
	}
	r := ScanEngineLog(path)
	engineLogs.m[path] = engineLogScan{size: fi.Size(), mod: fi.ModTime(), retries: r}
	return r
}
