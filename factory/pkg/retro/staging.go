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

// StagingConfig configures input staging for a retrospective.
type StagingConfig struct {
	CollectorURL string
	Owner        string
	Repo         string
	Since        time.Time
	DestDir      string
	HTTPClient   *http.Client
}

// StagingResult describes the staged outcome records, rollups, and task files.
type StagingResult struct {
	RollupsSkill    []tokenusage.OutcomeRollup `json:"rollupsSkill,omitempty"`
	RollupsWorkflow []tokenusage.OutcomeRollup `json:"rollupsWorkflow,omitempty"`
	Outcomes        []tokenusage.OutcomeRecord `json:"outcomes,omitempty"`
	CandidatePRs    []int                      `json:"candidatePRs,omitempty"`
	StagedFiles     map[string][]byte          `json:"-"`
}

// IsCandidatePR reports whether a PR outcome warrants retrospective investigation:
// closed unmerged, 3 or more care passes, human feedback, or human edits above threshold.
func IsCandidatePR(o *tokenusage.OutcomeRecord) bool {
	if o == nil {
		return false
	}
	if o.State == "closed" {
		return true
	}
	if o.CarePasses >= 3 {
		return true
	}
	if o.Feedback != nil && o.Feedback.Items > 0 {
		return true
	}
	if o.HumanEdit != nil && (o.HumanEdit.Added > 0 || o.HumanEdit.Deleted > 0 || o.HumanEdit.Files > 0) {
		return true
	}
	return false
}

// StageInputs fetches the week's rollups and outcome records from the collector,
// identifies candidate PRs, stages their traces, logs, and feedback.jsonl using
// GET /v1/tasks/{sandbox}/{task}/files/{name}, and writes them to DestDir.
func StageInputs(ctx context.Context, cfg StagingConfig) (*StagingResult, error) {
	if cfg.CollectorURL == "" {
		return nil, fmt.Errorf("collector URL is required")
	}
	base := strings.TrimRight(cfg.CollectorURL, "/")
	repoSlug := fmt.Sprintf("%s/%s", cfg.Owner, cfg.Repo)

	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	result := &StagingResult{
		StagedFiles: make(map[string][]byte),
	}

	// 1. Fetch skill rollups
	skillURL := fmt.Sprintf("%s/v1/outcomes/rollup?repo=%s&by=skill", base, url.QueryEscape(repoSlug))
	if !cfg.Since.IsZero() {
		skillURL += "&since=" + url.QueryEscape(cfg.Since.Format(time.RFC3339))
	}
	if data, err := httpGet(ctx, hc, skillURL); err == nil {
		var resp struct {
			Rollups []tokenusage.OutcomeRollup `json:"rollups"`
		}
		if json.Unmarshal(data, &resp) == nil {
			result.RollupsSkill = resp.Rollups
			result.StagedFiles["rollups-skill.json"] = data
		}
	}

	// 2. Fetch workflow rollups
	wfURL := fmt.Sprintf("%s/v1/outcomes/rollup?repo=%s&by=workflow", base, url.QueryEscape(repoSlug))
	if !cfg.Since.IsZero() {
		wfURL += "&since=" + url.QueryEscape(cfg.Since.Format(time.RFC3339))
	}
	if data, err := httpGet(ctx, hc, wfURL); err == nil {
		var resp struct {
			Rollups []tokenusage.OutcomeRollup `json:"rollups"`
		}
		if json.Unmarshal(data, &resp) == nil {
			result.RollupsWorkflow = resp.Rollups
			result.StagedFiles["rollups-workflow.json"] = data
		}
	}

	// 3. Fetch outcome records
	outcomesURL := fmt.Sprintf("%s/v1/outcomes?repo=%s", base, url.QueryEscape(repoSlug))
	if !cfg.Since.IsZero() {
		outcomesURL += "&since=" + url.QueryEscape(cfg.Since.Format(time.RFC3339))
	}
	outcomesData, err := httpGet(ctx, hc, outcomesURL)
	if err != nil {
		return nil, fmt.Errorf("fetching outcomes: %w", err)
	}
	var outcomesResp struct {
		Outcomes []tokenusage.OutcomeRecord `json:"outcomes"`
	}
	if err := json.Unmarshal(outcomesData, &outcomesResp); err != nil {
		return nil, fmt.Errorf("decoding outcomes: %w", err)
	}
	result.Outcomes = outcomesResp.Outcomes
	result.StagedFiles["outcomes.json"] = outcomesData

	// 4. Identify candidate PRs and stage their traces & feedback
	for _, o := range result.Outcomes {
		if !IsCandidatePR(&o) {
			continue
		}
		result.CandidatePRs = append(result.CandidatePRs, o.PR)
		prDir := fmt.Sprintf("prs/%d", o.PR)

		oData, _ := json.MarshalIndent(o, "", "  ")
		result.StagedFiles[fmt.Sprintf("%s/outcome.json", prDir)] = oData

		// Fetch feedback.jsonl from tasks or task endpoint
		feedbackStaged := false
		for _, taskRef := range o.Tasks {
			if taskRef.Sandbox == "" || taskRef.Task == "" {
				continue
			}

			// Stream transcript
			transcriptURL := fmt.Sprintf("%s/v1/tasks/%s/%s/files/session/stream.ndjson", base, url.PathEscape(taskRef.Sandbox), url.PathEscape(taskRef.Task))
			if streamData, err := httpGet(ctx, hc, transcriptURL); err == nil && len(streamData) > 0 {
				result.StagedFiles[fmt.Sprintf("%s/tasks/%s/session/stream.ndjson", prDir, taskRef.Task)] = streamData
			}

			// Stream execution log
			logURL := fmt.Sprintf("%s/v1/tasks/%s/%s/files/execution.log", base, url.PathEscape(taskRef.Sandbox), url.PathEscape(taskRef.Task))
			if logData, err := httpGet(ctx, hc, logURL); err == nil && len(logData) > 0 {
				result.StagedFiles[fmt.Sprintf("%s/tasks/%s/execution.log", prDir, taskRef.Task)] = logData
			}

			// Stream feedback.jsonl if not yet fetched
			if !feedbackStaged {
				fbURL := fmt.Sprintf("%s/v1/tasks/%s/%s/files/feedback.jsonl", base, url.PathEscape(taskRef.Sandbox), url.PathEscape(taskRef.Task))
				if fbData, err := httpGet(ctx, hc, fbURL); err == nil && len(fbData) > 0 {
					result.StagedFiles[fmt.Sprintf("%s/feedback.jsonl", prDir)] = fbData
					feedbackStaged = true
				}
			}
		}
	}

	// 5. Write to DestDir if provided
	if cfg.DestDir != "" {
		for relPath, content := range result.StagedFiles {
			fullPath := filepath.Join(cfg.DestDir, relPath)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
				return nil, fmt.Errorf("creating directory for %s: %w", relPath, err)
			}
			if err := os.WriteFile(fullPath, content, 0o644); err != nil {
				return nil, fmt.Errorf("writing staged file %s: %w", relPath, err)
			}
		}
	}

	return result, nil
}

func httpGet(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
