/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tokenusage

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Traces are the agents' session transcripts (acpd's stream.ndjson), one
// per task, harvested with the task's usage. The collector only passes
// them through: a trace PUT is spooled on the PVC, the flusher uploads it
// to the archive and deletes it, and the trace-index log (sealed,
// archived and replayed like the records log) indexes what the archive
// holds. The collector never serves a trace's content; readers go
// to the archive.
//
// In the archive, under the collector's prefix:
//
//	traces/<owner>/<repo>/<pr-N|issue-N|repo>/<sandbox>/<task>/stream.ndjson.gz

// TraceRecord is the index entry of one archived trace. Key is
// "<sandbox>/<task>"; a newer copy of the same task's trace replaces it.
type TraceRecord struct {
	Key      string `json:"key"`
	Repo     string `json:"repo,omitempty"`
	Sandbox  string `json:"sandbox"`
	Task     string `json:"task"`
	TaskType string `json:"taskType,omitempty"`
	Issue    int    `json:"issue,omitempty"`
	PR       int    `json:"pr,omitempty"`
	// Bytes and SHA256 are of the trace as archived, after redaction and
	// before compression.
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// Object is the trace's name in the archive, under the prefix.
	Object     string `json:"object,omitempty"`
	ArchivedAt string `json:"archivedAt,omitempty"`
}

// TraceFilter selects trace records. Zero values mean "no filter".
type TraceFilter struct {
	Repo    string
	Issue   int
	PR      int
	Sandbox string
}

// logTraces is the index log's name. It is not "traces", where the trace
// objects themselves are: startup lists the log's segments by prefix.
const logTraces = "trace-index"

var (
	// maxTraceBytes bounds one trace; maxSpoolBytes bounds the traces
	// waiting on the PVC, so that an archive outage fills neither the
	// PVC the usage logs share nor memory.
	maxTraceBytes int64 = 256 << 20
	maxSpoolBytes int64 = 512 << 20

	// ErrNoArchive refuses a trace when there is no archive to send it to:
	// the PVC alone would only fill up.
	ErrNoArchive = errors.New("traces need an archive (GCS_BUCKET)")
	// ErrSpoolFull refuses a trace while the spool is full.
	ErrSpoolFull = errors.New("the trace spool is full")
	// ErrTraceTooLarge refuses a trace over maxTraceBytes.
	ErrTraceTooLarge = errors.New("trace too large")

	traceName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

	// secretPatterns are credential shapes masked in a trace before it
	// touches the PVC. Tokens are kept out of the engine's environment
	// (#1695, #1696); this is the backstop for what leaks anyway.
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
		regexp.MustCompile(`ya29\.[0-9A-Za-z_-]{20,}`),
		regexp.MustCompile(`sk-ant-[0-9A-Za-z_-]{20,}`),
	}
)

// redact masks the credential shapes in line.
func redact(line []byte) []byte {
	for _, re := range secretPatterns {
		line = re.ReplaceAll(line, []byte("[REDACTED]"))
	}
	return line
}

// traceObject is where a trace goes in the archive.
func traceObject(rec TraceRecord) string {
	subject := "repo"
	switch {
	case rec.PR > 0:
		subject = SubjectKey("pr", rec.PR)
	case rec.Issue > 0:
		subject = SubjectKey("issue", rec.Issue)
	}
	return fmt.Sprintf("traces/%s/%s/%s/%s/stream.ndjson.gz", rec.Repo, subject, rec.Sandbox, rec.Task)
}

func (s *Store) traceSpoolDir() string { return filepath.Join(s.root, "traces") }

// PutTraceContent redacts a trace and spools it for the flusher, with rec
// (repo, sandbox, task, issue, pr) to index it by. A trace whose content
// the archive already holds is not spooled again.
func (s *Store) PutTraceContent(rec TraceRecord, content io.Reader) error {
	if s.archive == nil {
		return ErrNoArchive
	}
	if !traceName.MatchString(rec.Sandbox) || !traceName.MatchString(rec.Task) {
		return fmt.Errorf("invalid sandbox %q or task %q", rec.Sandbox, rec.Task)
	}
	if !repoName.MatchString(rec.Repo) {
		return fmt.Errorf("invalid repo %q", rec.Repo)
	}
	rec.Key = rec.Sandbox + "/" + rec.Task
	if spooled, err := dirSize(s.traceSpoolDir()); err != nil {
		return err
	} else if spooled >= maxSpoolBytes {
		return ErrSpoolFull
	}

	// Each PUT is an entry of its own, made in a temporary directory and
	// renamed into place whole, so the flusher never sees half of one.
	// Entries are named by time: the flusher uploads them in order, and
	// the last copy of a task's trace wins.
	tmp, err := os.MkdirTemp(s.traceSpoolDir()+".tmp", "put-")
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(s.traceSpoolDir()+".tmp", 0o755); err != nil {
			return err
		}
		tmp, err = os.MkdirTemp(s.traceSpoolDir()+".tmp", "put-")
	}
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	f, err := os.Create(filepath.Join(tmp, "stream.ndjson"))
	if err != nil {
		return err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h)
	r := bufio.NewReader(io.LimitReader(content, maxTraceBytes+1))
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			line = redact(line)
			if _, werr := w.Write(line); werr != nil {
				f.Close()
				return werr
			}
			rec.Bytes += int64(len(line))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return fmt.Errorf("reading trace: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if rec.Bytes > maxTraceBytes {
		return ErrTraceTooLarge
	}
	rec.SHA256 = hex.EncodeToString(h.Sum(nil))

	s.mu.Lock()
	existing, ok := s.traces[rec.Key]
	s.mu.Unlock()
	if ok && existing.SHA256 == rec.SHA256 {
		return nil // archived already, unchanged
	}

	meta, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "meta.json"), meta, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(s.traceSpoolDir(), 0o755); err != nil {
		return err
	}
	entry := filepath.Join(s.traceSpoolDir(), fmt.Sprintf("%020d-%s-%s", time.Now().UnixNano(), rec.Sandbox, rec.Task))
	return os.Rename(tmp, entry)
}

// uploadTraces uploads the spooled traces in order, indexes each in the
// traces log once the archive has it, and removes it from the PVC. The
// caller holds s.flushMu.
func (s *Store) uploadTraces(ctx context.Context) error {
	entries, err := os.ReadDir(s.traceSpoolDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		dir := filepath.Join(s.traceSpoolDir(), name)
		meta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			return err
		}
		var rec TraceRecord
		if err := json.Unmarshal(meta, &rec); err != nil {
			return fmt.Errorf("spooled trace %s: %w", name, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "stream.ndjson"))
		if err != nil {
			return err
		}
		gz, err := gzipBytes(data)
		if err != nil {
			return err
		}
		rec.Object = traceObject(rec)
		if err := s.archive.Write(ctx, rec.Object, gz); err != nil {
			return fmt.Errorf("uploading %s: %w", rec.Object, err)
		}
		rec.ArchivedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.PutTrace(rec); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

// PutTrace upserts a trace's index record.
func (s *Store) PutTrace(rec TraceRecord) error {
	if rec.Key == "" {
		return fmt.Errorf("trace key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.tracesLog.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("appending trace: %w", err)
	}
	if err := s.tracesLog.Sync(); err != nil {
		return fmt.Errorf("syncing traces log: %w", err)
	}
	s.traces[rec.Key] = rec
	return nil
}

// ListTraces returns the matching trace records, newest archived first.
func (s *Store) ListTraces(f TraceFilter) []TraceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TraceRecord
	for _, rec := range s.traces {
		if (f.Repo != "" && rec.Repo != f.Repo) || (f.Issue != 0 && rec.Issue != f.Issue) ||
			(f.PR != 0 && rec.PR != f.PR) || (f.Sandbox != "" && rec.Sandbox != f.Sandbox) {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ArchivedAt != out[j].ArchivedAt {
			return out[i].ArchivedAt > out[j].ArchivedAt
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// dirSize is the total size of the regular files under dir, 0 when it
// does not exist.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// traceQueryInt parses an optional integer query value.
func traceQueryInt(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.Atoi(v)
}
