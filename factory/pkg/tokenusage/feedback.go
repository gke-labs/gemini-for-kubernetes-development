// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FeedbackMeta captures the repository and pull request identity of spooled feedback.
type FeedbackMeta struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
}

func (s *Store) feedbackSpoolDir() string {
	return filepath.Join(s.root, "feedback")
}

// PutFeedback redacts credentials from the feedback stream, spools it on the
// PVC, and queues it for upload to the archive under prs/<owner>/<repo>/<n>/feedback.jsonl.gz.
func (s *Store) PutFeedback(owner, repo string, n int, body io.Reader) error {
	if s.archive == nil {
		return ErrNoArchive
	}
	if !pathPart.MatchString(owner) || !pathPart.MatchString(repo) || n <= 0 {
		return fmt.Errorf("invalid owner %q, repo %q, or pr %d", owner, repo, n)
	}

	spooled, err := dirSize(s.feedbackSpoolDir())
	if err != nil {
		return err
	}
	taskSpooled, err := dirSize(s.taskSpoolDir())
	if err != nil {
		return err
	}
	if spooled+taskSpooled >= maxSpoolBytes {
		return ErrSpoolFull
	}

	tmpDir := s.feedbackSpoolDir() + ".tmp"
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(tmpDir, "feedback-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	f, err := os.Create(filepath.Join(tmp, "feedback.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()

	var total int64
	br := bufio.NewReader(io.LimitReader(body, maxTaskBytes+1))
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			line = redact(line)
			if _, werr := f.Write(line); werr != nil {
				return werr
			}
			total += int64(len(line))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("reading feedback body: %w", rerr)
		}
	}
	if total > maxTaskBytes {
		return ErrTaskTooLarge
	}
	if err := f.Close(); err != nil {
		return err
	}

	meta := FeedbackMeta{Owner: owner, Repo: repo, PR: n}
	metaData, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "meta.json"), metaData, 0o644); err != nil {
		return err
	}

	if err := os.MkdirAll(s.feedbackSpoolDir(), 0o755); err != nil {
		return err
	}
	entry := filepath.Join(s.feedbackSpoolDir(), fmt.Sprintf("%020d-%s-%s-%d", time.Now().UnixNano(), owner, repo, n))
	return os.Rename(tmp, entry)
}

// uploadFeedback uploads spooled feedback files to the archive.
// The caller holds s.flushMu.
func (s *Store) uploadFeedback(ctx context.Context) error {
	entries, err := os.ReadDir(s.feedbackSpoolDir())
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
		dir := filepath.Join(s.feedbackSpoolDir(), name)
		if err := s.uploadFeedbackEntry(ctx, dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) uploadFeedbackEntry(ctx context.Context, dir string) error {
	metaData, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return err
	}
	var meta FeedbackMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("unmarshaling feedback meta for %s: %w", filepath.Base(dir), err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "feedback.jsonl"))
	if err != nil {
		return err
	}

	gz, err := gzipBytes(data)
	if err != nil {
		return err
	}

	objectPath := fmt.Sprintf("prs/%s/%s/%d/feedback.jsonl.gz", meta.Owner, meta.Repo, meta.PR)
	if err := s.archive.Write(ctx, objectPath, gz); err != nil {
		return fmt.Errorf("uploading %s: %w", objectPath, err)
	}

	return os.RemoveAll(dir)
}
