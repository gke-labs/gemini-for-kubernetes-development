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
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

// CandidateReasons returns the reasons an outcome qualifies for retro investigation.
func CandidateReasons(rec *tokenusage.OutcomeRecord) []string {
	if rec == nil {
		return nil
	}
	var reasons []string
	if rec.State == "closed" {
		reasons = append(reasons, "unmerged")
	}
	if rec.CarePasses >= 3 {
		reasons = append(reasons, fmt.Sprintf("%d care passes", rec.CarePasses))
	}
	if rec.Feedback != nil && (rec.Feedback.Humans > 0 || rec.Feedback.Items > 0) {
		reasons = append(reasons, "human feedback")
	}
	if rec.HumanEdit != nil && (rec.HumanEdit.Files > 0 || rec.HumanEdit.Added > 0 || rec.HumanEdit.Deleted > 0) {
		reasons = append(reasons, fmt.Sprintf("human edit (%d files, +%d/-%d)", rec.HumanEdit.Files, rec.HumanEdit.Added, rec.HumanEdit.Deleted))
	}
	return reasons
}

// IsCandidatePR reports whether a PR outcome record qualifies for retro analysis:
// unmerged (closed without merge), 3 or more care passes, human feedback,
// or significant human edits.
func IsCandidatePR(rec *tokenusage.OutcomeRecord) bool {
	return len(CandidateReasons(rec)) > 0
}

// StageConfig configures the staging helper.
type StageConfig struct {
	CollectorURL string
	Repo         string // "owner/repo"
	Since        time.Time
	TargetDir    string
	Client       *http.Client
}

// CandidatePRInfo records summary information for a staged candidate PR.
type CandidatePRInfo struct {
	PR          int      `json:"pr"`
	Reasons     []string `json:"reasons"`
	Tasks       int      `json:"tasks"`
	HasFeedback bool     `json:"hasFeedback"`
}

// StageResult summarizes the outcome of the staging pass.
type StageResult struct {
	Candidates []CandidatePRInfo `json:"candidates"`
	TargetDir  string            `json:"targetDir"`
}

// taskFilesToStage lists the files to retrieve from archived tasks.
var taskFilesToStage = []string{
	"session/stream.ndjson",
	"session/session.json",
	"execution.log",
	"task.json",
	"status.json",
	"inputs.json",
	"push.json",
	"change.yaml",
	"task-output.yaml",
}

// Stage fetches the week's rollups from GET /v1/outcomes/rollup, identifies
// candidate PRs (unmerged, >=3 care passes, human feedback, or significant
// human edits), and stages their traces, logs, and feedback under cfg.TargetDir.
func Stage(ctx context.Context, cfg StageConfig) (*StageResult, error) {
	if cfg.CollectorURL == "" {
		cfg.CollectorURL = "http://localhost:8080"
	}
	cfg.CollectorURL = strings.TrimRight(cfg.CollectorURL, "/")
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Since.IsZero() {
		cfg.Since = time.Now().Add(-7 * 24 * time.Hour)
	}
	if cfg.TargetDir == "" {
		return nil, fmt.Errorf("targetDir is required")
	}

	parts := strings.Split(cfg.Repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo %q: want owner/repo", cfg.Repo)
	}
	owner, repoName := parts[0], parts[1]

	rollupsDir := filepath.Join(cfg.TargetDir, "rollups")
	if err := os.MkdirAll(rollupsDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating rollups directory: %w", err)
	}

	// 1. Fetch skill rollup
	sinceRFC := cfg.Since.Format(time.RFC3339)
	skillURL := fmt.Sprintf("%s/v1/outcomes/rollup?repo=%s&since=%s&by=skill",
		cfg.CollectorURL, url.QueryEscape(cfg.Repo), url.QueryEscape(sinceRFC))
	if err := fetchAndSave(ctx, cfg.Client, skillURL, filepath.Join(rollupsDir, "skill.json")); err != nil {
		return nil, fmt.Errorf("fetching skill rollup: %w", err)
	}

	// 2. Fetch workflow rollup
	wfURL := fmt.Sprintf("%s/v1/outcomes/rollup?repo=%s&since=%s&by=workflow",
		cfg.CollectorURL, url.QueryEscape(cfg.Repo), url.QueryEscape(sinceRFC))
	if err := fetchAndSave(ctx, cfg.Client, wfURL, filepath.Join(rollupsDir, "workflow.json")); err != nil {
		return nil, fmt.Errorf("fetching workflow rollup: %w", err)
	}

	// 3. Fetch outcomes list
	outcomesURL := fmt.Sprintf("%s/v1/outcomes?repo=%s&since=%s",
		cfg.CollectorURL, url.QueryEscape(cfg.Repo), url.QueryEscape(sinceRFC))
	outcomesResp, err := fetchBytes(ctx, cfg.Client, outcomesURL)
	if err != nil {
		return nil, fmt.Errorf("fetching outcomes: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rollupsDir, "outcomes.json"), outcomesResp, 0o644); err != nil {
		return nil, fmt.Errorf("saving outcomes.json: %w", err)
	}

	var outcomesPayload struct {
		Outcomes []tokenusage.OutcomeRecord `json:"outcomes"`
	}
	if err := json.Unmarshal(outcomesResp, &outcomesPayload); err != nil {
		return nil, fmt.Errorf("unmarshaling outcomes: %w", err)
	}

	var result StageResult
	result.TargetDir = cfg.TargetDir

	// 4. Identify candidates and stage their files
	for _, outcome := range outcomesPayload.Outcomes {
		if !IsCandidatePR(&outcome) {
			continue
		}

		reasons := CandidateReasons(&outcome)
		info := CandidatePRInfo{
			PR:      outcome.PR,
			Reasons: reasons,
			Tasks:   len(outcome.Tasks),
		}

		prDir := filepath.Join(cfg.TargetDir, "prs", strconv.Itoa(outcome.PR))
		if err := os.MkdirAll(prDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating PR dir for #%d: %w", outcome.PR, err)
		}

		// Stage outcome.json
		outcomeBytes, err := json.MarshalIndent(outcome, "", "  ")
		if err == nil {
			_ = os.WriteFile(filepath.Join(prDir, "outcome.json"), outcomeBytes, 0o644)
		}

		// Stage feedback
		feedbackURL := fmt.Sprintf("%s/v1/prs/%s/%s/%d/feedback",
			cfg.CollectorURL, url.PathEscape(owner), url.PathEscape(repoName), outcome.PR)
		feedbackPath := filepath.Join(prDir, "feedback.jsonl")
		if err := fetchAndSaveOptional(ctx, cfg.Client, feedbackURL, feedbackPath); err == nil {
			if fi, err := os.Stat(feedbackPath); err == nil && fi.Size() > 0 {
				info.HasFeedback = true
			}
		}

		// Stage task files (traces and logs)
		for _, taskRef := range outcome.Tasks {
			if taskRef.Sandbox == "" || taskRef.Task == "" {
				continue
			}
			taskDir := filepath.Join(prDir, "tasks", taskRef.Task)
			for _, file := range taskFilesToStage {
				fileURL := fmt.Sprintf("%s/v1/tasks/%s/%s/files/%s",
					cfg.CollectorURL, url.PathEscape(taskRef.Sandbox), url.PathEscape(taskRef.Task), file)
				destPath := filepath.Join(taskDir, file)
				_ = fetchAndSaveOptional(ctx, cfg.Client, fileURL, destPath)
			}
		}

		result.Candidates = append(result.Candidates, info)
	}

	// Write candidate summary
	candidatesData, err := json.MarshalIndent(result.Candidates, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(cfg.TargetDir, "candidates.json"), candidatesData, 0o644)
	}

	return &result, nil
}

func fetchBytes(ctx context.Context, client *http.Client, targetURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %d", targetURL, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func fetchAndSave(ctx context.Context, client *http.Client, targetURL, destPath string) error {
	data, err := fetchBytes(ctx, client, targetURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o644)
}

func fetchAndSaveOptional(ctx context.Context, client *http.Client, targetURL, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
