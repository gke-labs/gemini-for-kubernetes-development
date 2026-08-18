package watch

import (
	"testing"
	"time"
)

func TestShouldRunChoreAt(t *testing.T) {
	// Base mock "now" time: Wednesday, July 1st, 2026 at 9:55 AM UTC
	now := time.Date(2026, 7, 1, 9, 55, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule string
		lastRun  time.Time
		expected bool
	}{
		{
			name:     "Never run before (zero lastRun)",
			schedule: "*/30 * * * *",
			lastRun:  time.Time{},
			expected: true,
		},
		{
			name:     "Interval triggers - run at 9:15 AM (40m ago, next was 9:30 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Interval skips - run at 9:40 AM (15m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 40, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Macro descriptor @hourly - run at 8:45 AM (70m ago, next was 9:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 8, 45, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Macro descriptor @hourly - run at 9:15 AM (40m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Never schedule",
			schedule: "never",
			lastRun:  time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Paused schedule",
			schedule: "paused",
			lastRun:  time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Invalid cron fallback >= 24h triggers",
			schedule: "invalid-cron",
			lastRun:  now.Add(-25 * time.Hour),
			expected: true,
		},
		{
			name:     "Invalid cron fallback < 24h skips",
			schedule: "invalid-cron",
			lastRun:  now.Add(-23 * time.Hour),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currentNow := now
			// For the Monday test cases, override mock now to Monday, July 6th, 2026, 9:55 AM UTC
			if tc.name == "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger" ||
				tc.name == "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM" {
				currentNow = time.Date(2026, 7, 6, 9, 55, 0, 0, time.UTC)
			}

			got := shouldRunChoreAt(tc.schedule, tc.lastRun, currentNow)
			if got != tc.expected {
				t.Errorf("shouldRunChoreAt(%q, %v, %v) = %v; want %v", tc.schedule, tc.lastRun, currentNow, got, tc.expected)
			}
		})
	}
}

func TestShouldRunChore(t *testing.T) {
	// With empty lastRun, shouldRunChore should return true
	if !shouldRunChore("@daily", time.Time{}) {
		t.Errorf("expected shouldRunChore with zero lastRun to be true")
	}
	// With never, should return false
	if shouldRunChore("never", time.Time{}) {
		t.Errorf("expected shouldRunChore with 'never' to be false")
	}
}
