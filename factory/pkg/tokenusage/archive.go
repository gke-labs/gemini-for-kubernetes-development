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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The logs on the PVC are the store's write-ahead tier; an Archive (a GCS
// bucket) is where they end up. Flush seals the active logs into a
// numbered segment, uploads it and deletes it from the PVC. OpenStore
// replays the archive first, then whatever the PVC still holds. A lost PVC
// costs what was written since the last flush; a lost archive costs
// nothing the PVC still has.
//
// In the archive, under its prefix:
//
//	records/<seq>.seg.jsonl.gz    a sealed segment of the records log
//	records/<seq>.snap.jsonl.gz   every record as of segment <seq>
//	subjects/…                    the same for the subjects log
//	task-index/…                  the same for the index of archived tasks (tasks.go)
//	tasks/…                       the archived tasks themselves (tasks.go)
//	summarized.json               the workflows whose summary was posted

// Archive is an object store: flat names, whole-object reads and writes.
type Archive interface {
	// List returns the names starting with prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	// Read returns an object's content, or an error wrapping ErrNotFound.
	Read(ctx context.Context, name string) ([]byte, error)
	Write(ctx context.Context, name string, data []byte) error
	// Delete removes an object; a missing one is not an error.
	Delete(ctx context.Context, name string) error
}

// ErrNotFound is what Archive.Read wraps for a missing object.
var ErrNotFound = errors.New("not found")

const (
	logRecords  = "records"
	logSubjects = "subjects"

	archivedSummarized = "summarized.json"
)

// compactAfter is how many segments may pile up above a log's latest
// snapshot before a flush folds them into a new one, so that startup reads
// a few objects rather than every flush since the beginning.
var compactAfter = 100

var logKinds = []string{logRecords, logSubjects, logTasks}

func segmentName(kind string, seq int64) string {
	return fmt.Sprintf("%s/%020d.seg.jsonl.gz", kind, seq)
}

func snapshotName(kind string, seq int64) string {
	return fmt.Sprintf("%s/%020d.snap.jsonl.gz", kind, seq)
}

// archivedLog is what the archive holds of one log: its latest snapshot
// (-1 for none) and its segments, oldest first.
type archivedLog struct {
	snap  int64
	snaps []int64
	segs  []int64
}

func parseArchived(kind string, names []string) archivedLog {
	a := archivedLog{snap: -1}
	for _, name := range names {
		rest, ok := strings.CutPrefix(name, kind+"/")
		if !ok {
			continue
		}
		num, suffix, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		seq, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			continue
		}
		switch suffix {
		case "seg.jsonl.gz":
			a.segs = append(a.segs, seq)
		case "snap.jsonl.gz":
			a.snaps = append(a.snaps, seq)
			a.snap = max(a.snap, seq)
		}
	}
	sort.Slice(a.segs, func(i, j int) bool { return a.segs[i] < a.segs[j] })
	return a
}

func (s *Store) activePath(kind string) string { return filepath.Join(s.root, kind+".jsonl") }
func (s *Store) sealedDir(kind string) string  { return filepath.Join(s.root, "sealed", kind) }

func (s *Store) sealedPath(kind string, seq int64) string {
	return filepath.Join(s.sealedDir(kind), fmt.Sprintf("%020d.jsonl", seq))
}

// sealedSegments returns the numbers of the log's segments sealed on the
// PVC and not yet uploaded, oldest first.
func (s *Store) sealedSegments(kind string) ([]int64, error) {
	entries, err := os.ReadDir(s.sealedDir(kind))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var seqs []int64
	for _, e := range entries {
		num, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok {
			continue
		}
		if seq, err := strconv.ParseInt(num, 10, 64); err == nil {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

// loadLog replays one log, oldest first so that later lines win: the
// archive's latest snapshot and the segments after it, then the segments
// still sealed on the PVC, then the active file.
func (s *Store) loadLog(ctx context.Context, kind string, apply func(io.Reader) error) error {
	after := int64(-1)
	if s.archive != nil {
		names, err := s.archive.List(ctx, kind+"/")
		if err != nil {
			return fmt.Errorf("listing archived %s: %w", kind, err)
		}
		a := parseArchived(kind, names)
		if a.snap >= 0 {
			if err := s.applyArchived(ctx, snapshotName(kind, a.snap), apply); err != nil {
				return err
			}
			after = a.snap
		}
		for _, seq := range a.segs {
			if seq <= a.snap {
				continue // folded into the snapshot; its delete didn't finish
			}
			if err := s.applyArchived(ctx, segmentName(kind, seq), apply); err != nil {
				return err
			}
			after = seq
			s.segsSinceSnap[kind]++
		}
		s.nextSeq = max(s.nextSeq, after+1)
	}
	sealed, err := s.sealedSegments(kind)
	if err != nil {
		return err
	}
	for _, seq := range sealed {
		s.nextSeq = max(s.nextSeq, seq+1)
		if seq <= after {
			continue // uploaded, not yet deleted from the PVC
		}
		if err := applyFile(s.sealedPath(kind, seq), apply); err != nil {
			return err
		}
	}
	return applyFile(s.activePath(kind), apply)
}

func (s *Store) applyArchived(ctx context.Context, name string, apply func(io.Reader) error) error {
	data, err := s.archive.Read(ctx, name)
	if err != nil {
		return fmt.Errorf("reading archived %s: %w", name, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reading archived %s: %w", name, err)
	}
	defer zr.Close()
	return apply(zr)
}

func applyFile(path string, apply func(io.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	return apply(f)
}

// loadArchivedSummarized fills in the summarized set from the archive when
// the PVC has none (a new PVC); the PVC's copy is the newer one otherwise.
func (s *Store) loadArchivedSummarized(ctx context.Context) error {
	if s.archive == nil {
		return nil
	}
	if _, err := os.Stat(s.summarizedPath()); err == nil {
		return nil
	}
	data, err := s.archive.Read(ctx, archivedSummarized)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading archived %s: %w", archivedSummarized, err)
	}
	return json.Unmarshal(data, &s.summarized)
}

// sealLocked moves the non-empty active logs aside as segment nextSeq,
// for Flush to upload, and starts new ones. The caller holds s.mu.
func (s *Store) sealLocked() error {
	sealed := false
	for _, kind := range logKinds {
		f := s.logFile(kind)
		info, err := (*f).Stat()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			continue
		}
		if err := os.MkdirAll(s.sealedDir(kind), 0o755); err != nil {
			return err
		}
		_ = (*f).Close()
		renameErr := os.Rename(s.activePath(kind), s.sealedPath(kind, s.nextSeq))
		// Reopen whatever happened: the store must keep a log to append to.
		reopened, err := os.OpenFile(s.activePath(kind), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("reopening %s log: %w", kind, err)
		}
		*f = reopened
		if renameErr != nil {
			return fmt.Errorf("sealing %s log: %w", kind, renameErr)
		}
		sealed = true
	}
	if sealed {
		s.nextSeq++
	}
	return nil
}

func (s *Store) logFile(kind string) **os.File {
	switch kind {
	case logSubjects:
		return &s.subjectsLog
	case logTasks:
		return &s.tasksLog
	}
	return &s.log
}

// Flush seals the active logs, uploads every sealed segment to the archive
// and removes it from the PVC, uploads the summarized set if it changed,
// and folds a log's segments into a snapshot once enough have piled up.
// Without an archive it does nothing.
func (s *Store) Flush(ctx context.Context) error {
	if s.archive == nil {
		return nil
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	// Tasks first: indexing one appends to the task index, which this
	// flush then seals and uploads with the rest.
	if err := s.uploadTasks(ctx); err != nil {
		return err
	}
	if err := s.uploadFeedback(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	err := s.sealLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.uploadSealed(ctx); err != nil {
		return err
	}
	if err := s.uploadSummarized(ctx); err != nil {
		return err
	}
	for _, kind := range logKinds {
		if s.segsSinceSnap[kind] >= compactAfter {
			if err := s.compact(ctx, kind); err != nil {
				return fmt.Errorf("compacting %s: %w", kind, err)
			}
		}
	}
	return nil
}

// uploadSealed uploads the sealed segments in order and deletes each from
// the PVC once the archive has it. The caller holds s.flushMu.
func (s *Store) uploadSealed(ctx context.Context) error {
	for _, kind := range logKinds {
		seqs, err := s.sealedSegments(kind)
		if err != nil {
			return err
		}
		for _, seq := range seqs {
			path := s.sealedPath(kind, seq)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			gz, err := gzipBytes(data)
			if err != nil {
				return err
			}
			if err := s.archive.Write(ctx, segmentName(kind, seq), gz); err != nil {
				return fmt.Errorf("uploading %s: %w", segmentName(kind, seq), err)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			s.segsSinceSnap[kind]++
		}
	}
	return nil
}

func (s *Store) uploadSummarized(ctx context.Context) error {
	s.mu.Lock()
	if !s.summarizedDirty {
		s.mu.Unlock()
		return nil
	}
	data, err := json.MarshalIndent(s.summarized, "", "  ")
	s.summarizedDirty = false
	s.mu.Unlock()
	if err == nil {
		err = s.archive.Write(ctx, archivedSummarized, data)
	}
	if err != nil {
		s.mu.Lock()
		s.summarizedDirty = true
		s.mu.Unlock()
		return fmt.Errorf("uploading %s: %w", archivedSummarized, err)
	}
	return nil
}

// compact writes the log's whole index as a snapshot and deletes the
// objects it replaces. The index is marshalled under the lock right after
// sealing, so the snapshot holds exactly the segments up to its number:
// anything written later lands in a later segment. The caller holds
// s.flushMu.
func (s *Store) compact(ctx context.Context, kind string) error {
	s.mu.Lock()
	if err := s.sealLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	at := s.nextSeq - 1
	data, err := s.snapshotLocked(kind)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	// Every segment the snapshot replaces is archived before any is deleted.
	if err := s.uploadSealed(ctx); err != nil {
		return err
	}
	gz, err := gzipBytes(data)
	if err != nil {
		return err
	}
	if err := s.archive.Write(ctx, snapshotName(kind, at), gz); err != nil {
		return err
	}
	names, err := s.archive.List(ctx, kind+"/")
	if err != nil {
		return err
	}
	a := parseArchived(kind, names)
	for _, seq := range a.segs {
		if seq <= at {
			if err := s.archive.Delete(ctx, segmentName(kind, seq)); err != nil {
				return err
			}
		}
	}
	for _, seq := range a.snaps {
		if seq < at {
			if err := s.archive.Delete(ctx, snapshotName(kind, seq)); err != nil {
				return err
			}
		}
	}
	s.segsSinceSnap[kind] = 0
	return nil
}

// snapshotLocked marshals the log's index, one line per key in key order.
// The caller holds s.mu.
func (s *Store) snapshotLocked(kind string) ([]byte, error) {
	var buf bytes.Buffer
	write := func(v any) error {
		line, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
		return nil
	}
	switch kind {
	case logSubjects:
		for _, key := range sortedKeys(s.subjects) {
			if err := write(s.subjects[key]); err != nil {
				return nil, err
			}
		}
	case logTasks:
		for _, key := range sortedKeys(s.tasks) {
			if err := write(s.tasks[key]); err != nil {
				return nil, err
			}
		}
	default:
		for _, key := range sortedKeys(s.records) {
			if err := write(s.records[key]); err != nil {
				return nil, err
			}
		}
	}
	return buf.Bytes(), nil
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RunFlusher flushes every interval until ctx is done. A failed flush is
// retried at the next tick: sealed segments stay on the PVC until they are
// uploaded.
func (s *Store) RunFlusher(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Flush(ctx); err != nil {
				log.Printf("token-usage: flushing to the archive: %v", err)
			}
		}
	}
}
