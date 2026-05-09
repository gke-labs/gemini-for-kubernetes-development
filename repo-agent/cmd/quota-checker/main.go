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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/quota"
)

func main() {
	ctx := context.Background()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	projectID := ""

	flag.StringVar(&projectID, "project", projectID, "Google Cloud Project ID")
	flag.Parse()

	if projectID == "" {
		return fmt.Errorf("--project is required")
	}
	fmt.Printf("Fetching quota usage for project: %s\n", projectID)

	checker := quota.NewChecker(projectID)
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
