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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// memArchive is an Archive in memory.
type memArchive struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemArchive() *memArchive { return &memArchive{objects: map[string][]byte{}} }

func (m *memArchive) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for name := range m.objects {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (m *memArchive) Read(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[name]
	if !ok {
		return nil, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return data, nil
}

func (m *memArchive) Write(_ context.Context, name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[name] = append([]byte(nil), data...)
	return nil
}

func (m *memArchive) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, name)
	return nil
}

func (m *memArchive) names() []string {
	names, _ := m.List(context.Background(), "")
	return names
}

func openArchived(t *testing.T, root string, a Archive) *Store {
	t.Helper()
	s, err := OpenStore(context.Background(), root, a)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func putRecord(t *testing.T, s *Store, key string, total int64) {
	t.Helper()
	if err := s.Put(UsageRecord{Key: key, Repo: "org/repo", Stats: statsFor("m", total, 0, total)}); err != nil {
		t.Fatalf("Put %s: %v", key, err)
	}
}

func totals(s *Store) map[string]int64 {
	out := map[string]int64{}
	for _, rec := range s.List(ListFilter{}) {
		out[rec.Key] = rec.Stats.Models["m"].Tokens.Total
	}
	return out
}

func sameTotals(t *testing.T, got, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("records = %v, want %v", got, want)
		}
	}
}

// A new PVC is rebuilt from the archive alone.
func TestFlushThenRestoreOnNewPVC(t *testing.T) {
	ctx := context.Background()
	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)
	putRecord(t, s, "a", 1)
	putRecord(t, s, "b", 2)
	if err := s.PutSubject(Subject{Key: "issue-7", Repo: "org/repo", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSummarized("issue-7"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	putRecord(t, s, "a", 10) // an upsert in a later segment
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// An empty flush seals nothing.
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	want := []string{
		"records/00000000000000000001.seg.jsonl.gz",
		"records/00000000000000000002.seg.jsonl.gz",
		"subjects/00000000000000000001.seg.jsonl.gz",
		"summarized.json",
	}
	if got := a.names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("archive = %v, want %v", got, want)
	}

	s2 := openArchived(t, t.TempDir(), a)
	sameTotals(t, totals(s2), map[string]int64{"a": 10, "b": 2})
	if s2.subjects["issue-7"].State != "open" {
		t.Errorf("subject not restored: %+v", s2.subjects)
	}
	if posted, _ := s2.MarkSummarized("issue-7"); !posted {
		t.Errorf("summarized set not restored")
	}
	// Numbering carries on after the archive's segments.
	putRecord(t, s2, "c", 3)
	if err := s2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(ctx, "records/00000000000000000003.seg.jsonl.gz"); err != nil {
		t.Errorf("expected segment 3: %v", err)
	}
}

// What was written since the last flush is on the PVC, and survives a
// restart on the same PVC; the PVC that was already there before the
// archive (the migration) goes up as the first segment.
func TestPVCTierAndMigration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	old, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	putRecord(t, old, "before", 1)
	_ = old.Close()

	a := newMemArchive()
	s := openArchived(t, root, a)
	putRecord(t, s, "unflushed", 2)
	_ = s.Close()

	s2 := openArchived(t, root, a)
	sameTotals(t, totals(s2), map[string]int64{"before": 1, "unflushed": 2})
	if err := s2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	sameTotals(t, totals(openArchived(t, t.TempDir(), a)), map[string]int64{"before": 1, "unflushed": 2})
	if _, err := os.Stat(s2.sealedPath(logRecords, 1)); !os.IsNotExist(err) {
		t.Errorf("uploaded segment still on the PVC: %v", err)
	}
}

// A segment sealed but not uploaded (the archive was down) stays on the
// PVC, is replayed from there, and goes up at the next flush.
func TestFailedUploadKeepsSegment(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := &failingArchive{memArchive: newMemArchive(), fail: true}
	s := openArchived(t, root, a)
	putRecord(t, s, "a", 1)
	if err := s.Flush(ctx); err == nil {
		t.Fatal("Flush: expected the upload to fail")
	}
	putRecord(t, s, "a", 5)
	_ = s.Close()

	s2 := openArchived(t, root, a)
	sameTotals(t, totals(s2), map[string]int64{"a": 5})
	a.fail = false
	if err := s2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	sameTotals(t, totals(openArchived(t, t.TempDir(), a)), map[string]int64{"a": 5})
}

// Uploaded but not yet deleted from the PVC (a crash in between): the
// copy on the PVC is skipped, and numbering does not reuse it.
func TestUploadedNotDeleted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a := newMemArchive()
	s := openArchived(t, root, a)
	putRecord(t, s, "a", 1)
	s.mu.Lock()
	if err := s.sealLocked(); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	data, _ := os.ReadFile(s.sealedPath(logRecords, 1))
	gz, _ := gzipBytes(data)
	_ = a.Write(ctx, segmentName(logRecords, 1), gz)
	_ = s.Close()

	s2 := openArchived(t, root, a)
	sameTotals(t, totals(s2), map[string]int64{"a": 1})
	if s2.nextSeq != 2 {
		t.Errorf("nextSeq = %d, want 2", s2.nextSeq)
	}
}

func TestCompaction(t *testing.T) {
	ctx := context.Background()
	defer func(n int) { compactAfter = n }(compactAfter)
	compactAfter = 3
	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)
	for i := range 4 {
		putRecord(t, s, fmt.Sprintf("r%d", i%2), int64(i))
		if err := s.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Three segments compacted into a snapshot at 3; the fourth is above it.
	want := []string{
		"records/00000000000000000003.snap.jsonl.gz",
		"records/00000000000000000004.seg.jsonl.gz",
	}
	if got := a.names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("archive = %v, want %v", got, want)
	}
	sameTotals(t, totals(openArchived(t, t.TempDir(), a)), map[string]int64{"r0": 2, "r1": 3})
}

type failingArchive struct {
	*memArchive
	fail bool
}

func (f *failingArchive) Write(ctx context.Context, name string, data []byte) error {
	if f.fail {
		return fmt.Errorf("archive down")
	}
	return f.memArchive.Write(ctx, name, data)
}

// The GCS archive speaks the JSON API: a fake bucket behind httptest.
func TestGCSArchive(t *testing.T) {
	ctx := context.Background()
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodPost && path == "/upload/storage/v1/b/bkt/o":
			if r.URL.Query().Get("uploadType") != "media" {
				http.Error(w, "uploadType", http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(r.Body)
			objects[r.URL.Query().Get("name")] = data
			fmt.Fprint(w, "{}")
		case r.Method == http.MethodGet && path == "/storage/v1/b/bkt/o":
			prefix := r.URL.Query().Get("prefix")
			var items []string
			for name := range objects {
				if strings.HasPrefix(name, prefix) {
					items = append(items, fmt.Sprintf("{%q:%q}", "name", name))
				}
			}
			fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
		case strings.HasPrefix(path, "/storage/v1/b/bkt/o/"):
			name := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/bkt/o/")
			if !strings.Contains(path, "%2F") {
				http.Error(w, "object name not escaped", http.StatusBadRequest)
				return
			}
			data, ok := objects[name]
			if !ok {
				http.Error(w, "no such object", http.StatusNotFound)
				return
			}
			if r.Method == http.MethodDelete {
				delete(objects, name)
				return
			}
			_, _ = w.Write(data)
		default:
			http.Error(w, r.Method+" "+path, http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	g := newGCSArchive(srv.Client(), srv.URL, "bkt", "token-usage")
	if err := g.Write(ctx, "records/1.seg.jsonl.gz", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, ok := objects["token-usage/records/1.seg.jsonl.gz"]; !ok {
		t.Fatalf("object not under the prefix: %v", objects)
	}
	names, err := g.List(ctx, "records/")
	if err != nil || len(names) != 1 || names[0] != "records/1.seg.jsonl.gz" {
		t.Fatalf("List = %v, %v", names, err)
	}
	if data, err := g.Read(ctx, "records/1.seg.jsonl.gz"); err != nil || string(data) != "x" {
		t.Fatalf("Read = %q, %v", data, err)
	}
	if _, err := g.Read(ctx, "missing"); err == nil || !strings.Contains(err.Error(), ErrNotFound.Error()) {
		t.Fatalf("Read missing: %v", err)
	}
	if err := g.Delete(ctx, "records/1.seg.jsonl.gz"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := g.Delete(ctx, "records/1.seg.jsonl.gz"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestFlushOutcomesToArchive(t *testing.T) {
	ctx := context.Background()
	a := newMemArchive()
	s := openArchived(t, t.TempDir(), a)

	rec := OutcomeRecord{
		Repo:        "org/repo",
		PR:          10,
		State:       "merged",
		CarePasses:  2,
		OpenedByBot: true,
	}
	if err := s.PutOutcome(rec); err != nil {
		t.Fatal(err)
	}

	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Verify sealed segment uploaded
	if _, err := a.Read(ctx, "outcomes/00000000000000000001.seg.jsonl.gz"); err != nil {
		t.Fatalf("expected outcomes segment 1: %v", err)
	}

	// Restore on a fresh PVC
	s2 := openArchived(t, t.TempDir(), a)
	outcomes := s2.ListOutcomes(OutcomeFilter{})
	if len(outcomes) != 1 || outcomes[0].PR != 10 || outcomes[0].State != "merged" {
		t.Fatalf("restored outcomes unexpected: %+v", outcomes)
	}
}
