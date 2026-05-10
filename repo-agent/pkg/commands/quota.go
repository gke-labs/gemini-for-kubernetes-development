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
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/quota"
	"github.com/spf13/cobra"
)

// QuotaOptions holds options for the Run function.
type QuotaOptions struct {
	ProjectID string
}

// InitDefaults initializes default values for QuotaOptions.
func (o *QuotaOptions) InitDefaults() {
	if o.ProjectID == "" {
		o.ProjectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
}

// BuildQuotaCommand creates a new cobra command for checking quota usage.
func BuildQuotaCommand() *cobra.Command {
	o := &QuotaOptions{}

	cmd := &cobra.Command{
		Use:   "quota-checker",
		Short: "Check Gemini API quota usage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.InitDefaults()
			return o.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&o.ProjectID, "project", "", "Google Cloud Project ID")

	return cmd
}

// Run executes the quota check.
func (o *QuotaOptions) Run(ctx context.Context) error {
	if o.ProjectID == "" {
		return fmt.Errorf("--project is required or GOOGLE_CLOUD_PROJECT environment variable must be set")
	}
	fmt.Printf("Fetching quota usage for project: %s\n", o.ProjectID)

	checker := quota.NewChecker(o.ProjectID)
	usages, err := checker.GetUsage(ctx)
	if err != nil {
		return err
	}

	for _, usage := range usages {
		fmt.Println("---")
		fmt.Printf("Model: %s\n", usage.Model)
		fmt.Printf("Metric: %v\n", usage.MetricLabels)
		fmt.Printf("Resource: %v\n", usage.ResourceLabels)
		fmt.Printf("  Total: %d\n", usage.Total)
	}

	return nil
}
