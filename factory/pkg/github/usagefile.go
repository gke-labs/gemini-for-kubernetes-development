package github

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// A task's child factory process spends GitHub's rate limit too, as the
// watcher's token or its bot's, out of the watch's sight. The watch names
// a file (EnvUsageFile) in the child's environment; the child appends
// what it counted to it (ReportUsage), and the watch reads that into its
// own counts (IngestUsage).

// EnvUsageFile is the file a process appends its GitHub usage to, as JSON
// lines (ReportUsage); unset, it reports none.
const EnvUsageFile = "FACTORY_GITHUB_USAGE_FILE"

// usageLine is one report: the series counted since the last.
type usageLine struct {
	At         time.Time                       `json:"at"`
	Series     []Series                        `json:"series"`
	RateLimits map[string]map[string]RateLimit `json:"rateLimits,omitempty"` // by token id
	TokenNames map[string]string               `json:"tokenNames,omitempty"`
}

// ReportUsage appends the process's GitHub usage to EnvUsageFile every
// interval, what was counted since the last report, and once more when
// stop is called: a long run is seen while it runs, a short one at its end.
// A process without EnvUsageFile reports nothing.
func ReportUsage(every time.Duration) (stop func()) {
	path := os.Getenv(EnvUsageFile)
	if path == "" {
		return func() {}
	}
	var mu sync.Mutex
	prev := UsageSnapshot{Since: processUsage.since, At: processUsage.since}
	flush := func() {
		mu.Lock()
		defer mu.Unlock()
		now := ProcessUsage()
		d := now.Minus(prev)
		if d.Total == 0 {
			return
		}
		if appendUsage(path, usageLine{At: now.At, Series: d.Series, RateLimits: now.rate, TokenNames: now.TokenNames}) == nil {
			prev = now
		}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				flush()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			flush()
		})
	}
}

// appendUsage appends one line in one write: lines written by processes at
// once do not interleave.
func appendUsage(path string, l usageLine) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

var (
	ingestMu      sync.Mutex
	ingestOffsets = map[string]int64{}
)

// IngestUsage adds the reports appended to path since it last read it
// (ReportUsage) to the process's counts. A line not yet complete waits
// for the next read; a line that does not parse is skipped.
func IngestUsage(path string) error {
	ingestMu.Lock()
	defer ingestMu.Unlock()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	off := ingestOffsets[path]
	if fi, err := f.Stat(); err == nil && fi.Size() < off {
		off = 0 // truncated: read it anew
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return nil
	}
	for _, raw := range bytes.Split(b[:end], []byte("\n")) {
		var l usageLine
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &l) != nil {
			continue
		}
		processUsage.add(l.Series, l.RateLimits, l.TokenNames)
	}
	ingestOffsets[path] = off + int64(end) + 1
	return nil
}
