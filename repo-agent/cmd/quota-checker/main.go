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
