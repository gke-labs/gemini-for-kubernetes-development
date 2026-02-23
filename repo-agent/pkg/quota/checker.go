package quota

import (
	"context"
	"fmt"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/klog/v2"
)

type Usage struct {
	Model          string            `json:"model"`
	Total          int64             `json:"total"`
	Limit          int64             `json:"limit,omitempty"`
	MetricLabels   map[string]string `json:"metric_labels,omitempty"`
	ResourceLabels map[string]string `json:"resource_labels,omitempty"`
}

type Checker struct {
	ProjectID string
}

func NewChecker(projectID string) *Checker {
	return &Checker{
		ProjectID: projectID,
	}
}

// MidnightInTimezone truncates a time.Time to the beginning of the day in its current location.
func MidnightInTimezone(t time.Time) time.Time {
	// Use t.Location() to preserve the timezone information for the new date components.
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func (c *Checker) GetUsage(ctx context.Context) ([]Usage, error) {
	if c.ProjectID == "" {
		return nil, fmt.Errorf("project ID is not set")
	}

	client, err := monitoring.NewMetricClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating client for GCP Monitoring API: %w", err)
	}
	defer client.Close()

	now := time.Now()

	// Rate limits are applied per project, not per API key.
	// Requests per day (RPD) quotas reset at midnight Pacific time.
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		klog.Errorf("failed to load location: %v", err)
		// Fallback to FixedZone if loading location fails
		location = time.FixedZone("PST", -8*60*60)
	}

	nowInPacificTime := now.In(location)
	startOfDay := MidnightInTimezone(nowInPacificTime)

	startTime := startOfDay
	endTime := nowInPacificTime

	klog.Info("Querying quota usage", "startTime", startTime, "endTime", endTime)

	req := &monitoringpb.ListTimeSeriesRequest{
		Name: "projects/" + c.ProjectID,
		// Filter: `metric.type = "generativelanguage.googleapis.com/quota/generate_content_paid_tier_input_token_count/usage"`,
		Filter: `metric.type = "generativelanguage.googleapis.com/quota/generate_requests_per_model/usage"`,
		Interval: &monitoringpb.TimeInterval{
			StartTime: timestamppb.New(startTime),
			EndTime:   timestamppb.New(endTime),
		},
	}

	var usages []Usage
	it := client.ListTimeSeries(ctx, req)
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list time series: %w", err)
		}

		model := resp.Resource.Labels["model"]
		total := int64(0)
		for _, point := range resp.Points {
			t := point.Interval.StartTime.AsTime()
			if t.Before(startTime) || t.After(endTime) {
				continue
			}
			total += point.Value.GetInt64Value()
		}

		usages = append(usages, Usage{
			Model:          model,
			Total:          total,
			MetricLabels:   resp.Metric.Labels,
			ResourceLabels: resp.Resource.Labels,
		})
	}

	return usages, nil
}
