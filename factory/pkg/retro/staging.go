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

package retro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

// StagingOptions specifies what to stage and where.
type StagingOptions struct {
	CollectorURL string
	Repo         string
	Since        time.Time
	TargetDir    string
	HTTPClient   *http.Client
}

// StagedCandidate records what was staged for one candidate PR.
type StagedCandidate struct {
	PR      int                      `json:"pr"`
	Outcome tokenusage.OutcomeRecord `json:"outcome"`
	Tasks   []string                 `json:"tasks"`
	Files   []string                 `json:"files"`
}

// StagingResult records the outcome of a staging run.
type StagingResult struct {
	Since          time.Time                  `json:"since"`
	Repo           string                     `json:"repo"`
	Outcomes       []tokenusage.OutcomeRecord `json:"outcomes"`
	Candidates     []StagedCandidate          `json:"candidates"`
	RollupSkill    []tokenusage.OutcomeRollup `json:"rollupSkill"`
	RollupWorkflow []tokenusage.OutcomeRollup `json:"rollupWorkflow"`
}

// IsCandidatePR reports whether a PR outcome warrants deeper retrospective analysis:
// closed unmerged, 3 or more care passes, human feedback, or human edits above a threshold.
func IsCandidatePR(rec tokenusage.OutcomeRecord) bool {
	if rec.State == "closed" {
		return true
	}
	if rec.CarePasses >= 3 {
		return true
	}
	if rec.Feedback != nil && (rec.Feedback.Items > 0 || rec.Feedback.Humans > 0) {
		return true
	}
	if rec.HumanEdit != nil && (rec.HumanEdit.Files > 0 || rec.HumanEdit.Added+rec.HumanEdit.Deleted > 0) {
		return true
	}
	return false
}

// StageWeeklyData fetches week's rollups from GET /v1/outcomes/rollup,
// identifies candidate PRs, and stages their traces, logs, and feedback.jsonl
// using the collector's uncompressed read endpoint GET /v1/tasks/{sandbox}/{task}/files/{name}.
func StageWeeklyData(ctx context.Context, opts StagingOptions) (*StagingResult, error) {
	if opts.CollectorURL == "" {
		return nil, fmt.Errorf("collector URL is required")
	}
	if opts.Repo == "" {
		return nil, fmt.Errorf("repository is required")
	}
	if opts.TargetDir == "" {
		return nil, fmt.Errorf("target directory is required")
	}
	if err := os.MkdirAll(opts.TargetDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating target directory: %w", err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	since := opts.Since
	if since.IsZero() {
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	}
	sinceStr := since.Format(time.RFC3339)
	baseURL := strings.TrimRight(opts.CollectorURL, "/")

	// 1. Fetch skill rollup
	skillURL := fmt.Sprintf("%s/v1/outcomes/rollup?repo=%s&since=%s&by=skill",
		baseURL, url.QueryEscape(opts.Repo), url.QueryEscape(sinceStr))
	skillRollup, err := fetchRollups(ctx, client, skillURL)
	if err != nil {
		return nil, fmt.Errorf("fetching skill rollups: %w", err)
	}
	if err := writeJSON(filepath.Join(opts.TargetDir, "rollup-skill.json"), skillRollup); err != nil {
		return nil, err
	}

	// 2. Fetch workflow rollup
	wfURL := fmt.Sprintf("%s/v1/outcomes/rollup?repo=%s&since=%s&by=workflow",
		baseURL, url.QueryEscape(opts.Repo), url.QueryEscape(sinceStr))
	wfRollup, err := fetchRollups(ctx, client, wfURL)
	if err != nil {
		return nil, fmt.Errorf("fetching workflow rollups: %w", err)
	}
	if err := writeJSON(filepath.Join(opts.TargetDir, "rollup-workflow.json"), wfRollup); err != nil {
		return nil, err
	}

	// 3. Fetch outcome records
	outcomesURL := fmt.Sprintf("%s/v1/outcomes?repo=%s&since=%s",
		baseURL, url.QueryEscape(opts.Repo), url.QueryEscape(sinceStr))
	outcomes, err := fetchOutcomes(ctx, client, outcomesURL)
	if err != nil {
		return nil, fmt.Errorf("fetching outcomes: %w", err)
	}
	if err := writeJSON(filepath.Join(opts.TargetDir, "outcomes.json"), outcomes); err != nil {
		return nil, err
	}

	// 4. Identify candidate PRs and stage their files
	var candidates []StagedCandidate
	targetFiles := []string{
		"session/stream.ndjson",
		"execution.log",
		"feedback.jsonl",
	}

	for _, rec := range outcomes {
		if !IsCandidatePR(rec) {
			continue
		}

		prDir := filepath.Join(opts.TargetDir, "candidates", fmt.Sprintf("pr-%d", rec.PR))
		if err := os.MkdirAll(prDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating candidate dir for PR %d: %w", rec.PR, err)
		}
		_ = writeJSON(filepath.Join(prDir, "outcome.json"), rec)

		cand := StagedCandidate{
			PR:      rec.PR,
			Outcome: rec,
		}

		for _, taskRef := range rec.Tasks {
			taskID := fmt.Sprintf("%s-%s", taskRef.Sandbox, taskRef.Task)
			cand.Tasks = append(cand.Tasks, taskID)
			taskDir := filepath.Join(prDir, taskID)

			for _, name := range targetFiles {
				fileURL := fmt.Sprintf("%s/v1/tasks/%s/%s/files/%s",
					baseURL, url.PathEscape(taskRef.Sandbox), url.PathEscape(taskRef.Task), name)
				content, err := downloadFile(ctx, client, fileURL)
				if err != nil {
					continue
				}
				destPath := filepath.Join(taskDir, name)
				if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
					continue
				}
				if err := os.WriteFile(destPath, content, 0o644); err != nil {
					continue
				}
				cand.Files = append(cand.Files, filepath.Join(taskID, name))
				if name == "feedback.jsonl" {
					_ = os.WriteFile(filepath.Join(prDir, "feedback.jsonl"), content, 0o644)
				}
			}
		}

		candidates = append(candidates, cand)
	}

	_ = writeJSON(filepath.Join(opts.TargetDir, "candidates.json"), candidates)

	return &StagingResult{
		Since:          since,
		Repo:           opts.Repo,
		Outcomes:       outcomes,
		Candidates:     candidates,
		RollupSkill:    skillRollup,
		RollupWorkflow: wfRollup,
	}, nil
}

func fetchRollups(ctx context.Context, client *http.Client, reqURL string) ([]tokenusage.OutcomeRollup, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d from %s", resp.StatusCode, reqURL)
	}
	var data struct {
		Rollups []tokenusage.OutcomeRollup `json:"rollups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Rollups, nil
}

func fetchOutcomes(ctx context.Context, client *http.Client, reqURL string) ([]tokenusage.OutcomeRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d from %s", resp.StatusCode, reqURL)
	}
	var data struct {
		Outcomes []tokenusage.OutcomeRecord `json:"outcomes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Outcomes, nil
}

func downloadFile(ctx context.Context, client *http.Client, reqURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func writeJSON(path string, val any) error {
	data, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
