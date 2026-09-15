package chores

import (
	"testing"
	"time"
)

func TestShouldRunAt(t *testing.T) {
	// Wednesday, July 1st 2026 at 9:55 AM UTC.
	wednesday := time.Date(2026, 7, 1, 9, 55, 0, 0, time.UTC)
	// Monday, July 6th 2026 at 9:55 AM UTC, for the day-of-week schedules.
	monday := time.Date(2026, 7, 6, 9, 55, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule string
		lastRun  time.Time
		now      time.Time
		expected bool
	}{
		{
			name:     "Never run before (zero lastRun)",
			schedule: "*/30 * * * *",
			lastRun:  time.Time{},
			now:      wednesday,
			expected: true,
		},
		{
			name:     "Interval triggers - run at 9:15 AM (40m ago, next was 9:30 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			now:      wednesday,
			expected: true,
		},
		{
			name:     "Interval skips - run at 9:40 AM (15m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "*/30 * * * *",
			lastRun:  time.Date(2026, 7, 1, 9, 40, 0, 0, time.UTC),
			now:      wednesday,
			expected: false,
		},
		{
			name:     "Macro descriptor @hourly - run at 8:45 AM (70m ago, next was 9:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 8, 45, 0, 0, time.UTC),
			now:      wednesday,
			expected: true,
		},
		{
			name:     "Macro descriptor @hourly - run at 9:15 AM (40m ago, next is 10:00 AM), now is 9:55 AM",
			schedule: "@hourly",
			lastRun:  time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC),
			now:      wednesday,
			expected: false,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Saturday 9 AM (2 days ago), should trigger",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC),
			now:      monday,
			expected: true,
		},
		{
			name:     "Complex schedule (9 AM on Monday) - run on Monday 9:15 AM (40m ago, next is next Monday), now is Monday 9:55 AM",
			schedule: "0 9 * * 1",
			lastRun:  time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC),
			now:      monday,
			expected: false,
		},
		{
			name:     "Never schedule",
			schedule: "never",
			lastRun:  time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			now:      wednesday,
			expected: false,
		},
		{
			name:     "Paused schedule",
			schedule: "paused",
			lastRun:  time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			now:      wednesday,
			expected: false,
		},
		{
			name:     "Invalid cron fallback >= 24h triggers",
			schedule: "invalid-cron",
			lastRun:  wednesday.Add(-25 * time.Hour),
			now:      wednesday,
			expected: true,
		},
		{
			name:     "Invalid cron fallback < 24h skips",
			schedule: "invalid-cron",
			lastRun:  wednesday.Add(-23 * time.Hour),
			now:      wednesday,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRunAt(tc.schedule, tc.lastRun, tc.now)
			if got != tc.expected {
				t.Errorf("shouldRunAt(%q, %v, %v) = %v; want %v", tc.schedule, tc.lastRun, tc.now, got, tc.expected)
			}
		})
	}
}

// TestShouldRunAtIgnoresPauseKeywordCasing covers definitions written as
// "Never" or "PAUSED", which read as valid to an author but would otherwise
// fall through to the unparsable-schedule path and run daily.
func TestShouldRunAtIgnoresPauseKeywordCasing(t *testing.T) {
	now := time.Date(2026, 7, 1, 9, 55, 0, 0, time.UTC)
	for _, schedule := range []string{"Never", "PAUSED", " never "} {
		if shouldRunAt(schedule, time.Time{}, now) {
			t.Errorf("shouldRunAt(%q, zero, now) = true; want false", schedule)
		}
	}
}

func TestDueAt(t *testing.T) {
	now := time.Date(2026, 7, 1, 9, 55, 0, 0, time.UTC)

	t.Run("never run before is due now", func(t *testing.T) {
		if got := dueAt("@hourly", time.Time{}, now); !got.Equal(now) {
			t.Errorf("dueAt = %v, want %v", got, now)
		}
	})

	t.Run("due at the first scheduled point after the last run", func(t *testing.T) {
		lastRun := time.Date(2026, 7, 1, 8, 45, 0, 0, time.UTC)
		want := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
		if got := dueAt("@hourly", lastRun, now); !got.Equal(want) {
			t.Errorf("dueAt = %v, want %v", got, want)
		}
	})

	t.Run("unparsable schedule is due now", func(t *testing.T) {
		lastRun := time.Date(2026, 7, 1, 8, 45, 0, 0, time.UTC)
		if got := dueAt("invalid-cron", lastRun, now); !got.Equal(now) {
			t.Errorf("dueAt = %v, want %v", got, now)
		}
	})
}
