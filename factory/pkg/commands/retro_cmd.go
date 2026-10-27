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

package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/retro"
)

// NewRetroCommand groups commands for the weekly retro.
func NewRetroCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "retro",
		Short:  "Weekly retro tools: stage outcome records and traces",
		Hidden: true,
	}
	cmd.AddCommand(newRetroStageCommand(ctx))
	return cmd
}

func newRetroStageCommand(ctx context.Context) *cobra.Command {
	var collectorURL, repo, dir, sinceStr string

	cmd := &cobra.Command{
		Use:   "stage",
		Short: "Stage week's rollups, candidate PR outcomes, and archived traces/logs",
		RunE: func(c *cobra.Command, args []string) error {
			if collectorURL == "" {
				return fmt.Errorf("--collector is required")
			}
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			if dir == "" {
				return fmt.Errorf("--dir is required")
			}
			var since time.Time
			if sinceStr != "" {
				var err error
				since, err = time.Parse(time.RFC3339, sinceStr)
				if err != nil {
					d, dErr := time.ParseDuration(sinceStr)
					if dErr == nil {
						since = time.Now().UTC().Add(-d)
					} else {
						return fmt.Errorf("invalid --since timestamp: %w", err)
					}
				}
			}
			res, err := retro.StageWeeklyData(ctx, retro.StagingOptions{
				CollectorURL: collectorURL,
				Repo:         repo,
				Since:        since,
				TargetDir:    dir,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Staged %d outcome(s) and %d candidate PR(s) to %s\n",
				len(res.Outcomes), len(res.Candidates), dir)
			return nil
		},
	}

	cmd.Flags().StringVar(&collectorURL, "collector", "", "Collector endpoint URL")
	cmd.Flags().StringVar(&repo, "repo", "", "Target repository (owner/repo)")
	cmd.Flags().StringVar(&dir, "dir", "", "Target directory to stage files into")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Analyze outcomes since timestamp (RFC3339)")

	return cmd
}
