package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

func main() {
	ctx := context.Background()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// MidnightInTimezone truncates a time.Time to the beginning of the day in its current location.
func MidnightInTimezone(t time.Time) time.Time {
	// Use t.Location() to preserve the timezone information for the new date components.
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func run(ctx context.Context) error {
	log := klog.FromContext(ctx)

	projectID := ""

	flag.StringVar(&projectID, "project", projectID, "Google Cloud Project ID")
	flag.Parse()

	if projectID == "" {
		return fmt.Errorf("--project is required")
	}
	fmt.Printf("Fetching quota usage for project: %s\n", projectID)

	client, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		return fmt.Errorf("creating client for GCP Monitoring API: %w", err)
	}
	defer client.Close()

	now := time.Now()

	// Rate limits are applied per project, not per API key.
	// Requests per day (RPD) quotas reset at midnight Pacific time.

	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return fmt.Errorf("failed to load location: %w", err)
		// Could also do this, but ... daylight savings time?
		// location = time.FixedZone("PST", -8*60*60)
	}

	nowInPacificTime := now.In(location)
	startOfDay := MidnightInTimezone(nowInPacificTime)

	startTime := startOfDay
	endTime := nowInPacificTime

	log.Info("Querying quota usage", "startTime", startTime, "endTime", endTime)

	req := &monitoringpb.ListTimeSeriesRequest{
		Name: "projects/" + projectID,
		// Filter: `metric.type = "generativelanguage.googleapis.com/quota/generate_content_paid_tier_input_token_count/usage"`,
		Filter: `metric.type = "generativelanguage.googleapis.com/quota/generate_requests_per_model/usage"`,
		Interval: &monitoringpb.TimeInterval{
			StartTime: timestamppb.New(startTime),
			EndTime:   timestamppb.New(endTime),
		},
	}

	it := client.ListTimeSeries(ctx, req)
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to list time series: %w", err)
		}

		fmt.Println("---")
		fmt.Printf("Metric: %v\n", resp.Metric.Labels)
		// if resp.Metric.Labels["model"] != "gemini-3.1-pro" {
		// 	fmt.Printf("skipping model %v\n", resp.Resource.Labels["model"])
		// 	continue
		// }
		fmt.Printf("Resource: %v\n", resp.Resource.Labels)
		total := int64(0)
		for _, point := range resp.Points {
			t := point.Interval.StartTime.AsTime()
			if t.Before(startTime) || t.After(endTime) {
				continue
			}
			total += point.Value.GetInt64Value()
		}
		fmt.Printf("  Total: %d\n", total)
	}

	return nil
}
