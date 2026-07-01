package commands

import (
	"testing"
	"time"
)

func TestMatchField(t *testing.T) {
	tests := []struct {
		field    string
		value    int
		minVal   int
		maxVal   int
		expected bool
	}{
		{"*", 5, 0, 59, true},
		{"5", 5, 0, 59, true},
		{"5", 6, 0, 59, false},
		{"1-5", 3, 0, 59, true},
		{"1-5", 6, 0, 59, false},
		{"*/30", 30, 0, 59, true},
		{"*/30", 0, 0, 59, true},
		{"*/30", 45, 0, 59, false},
		{"1,2,3", 2, 0, 59, true},
		{"1,2,3", 4, 0, 59, false},
		{"1-10/2", 5, 0, 59, true}, // 1, 3, 5, 7, 9
		{"1-10/2", 6, 0, 59, false},
	}

	for _, tc := range tests {
		got := matchField(tc.field, tc.value, tc.minVal, tc.maxVal)
		if got != tc.expected {
			t.Errorf("matchField(%q, %d, %d, %d) = %v; want %v", tc.field, tc.value, tc.minVal, tc.maxVal, got, tc.expected)
		}
	}
}

func TestMatchesCron(t *testing.T) {
	// Schedule: "*/30 * * * *" (every 30 minutes)
	fields := []string{"*/30", "*", "*", "*", "*"}

	t1 := time.Date(2026, 7, 1, 12, 30, 0, 0, time.UTC)
	if !matchesCron(fields, t1) {
		t.Errorf("expected matchesCron for 12:30 with schedule */30 * * * *")
	}

	t2 := time.Date(2026, 7, 1, 12, 15, 0, 0, time.UTC)
	if matchesCron(fields, t2) {
		t.Errorf("expected no matchesCron for 12:15 with schedule */30 * * * *")
	}

	// DOM and DOW intersection logic:
	// "0 0 1 * 0" -> Day 1 of month OR Sunday (0)
	fieldsDOMOrDOW := []string{"0", "0", "1", "*", "0"}

	// Match: Day 1 (not Sunday)
	t3 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) // July 1st, 2026 is Wednesday
	if !matchesCron(fieldsDOMOrDOW, t3) {
		t.Errorf("expected matchesCron for July 1st (Wednesday) on schedule 0 0 1 * 0")
	}

	// Match: Sunday (not Day 1)
	t4 := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC) // July 5th, 2026 is Sunday
	if !matchesCron(fieldsDOMOrDOW, t4) {
		t.Errorf("expected matchesCron for July 5th (Sunday) on schedule 0 0 1 * 0")
	}

	// No match: Day 2 (Thursday)
	t5 := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	if matchesCron(fieldsDOMOrDOW, t5) {
		t.Errorf("expected no matchesCron for July 2nd (Thursday) on schedule 0 0 1 * 0")
	}
}

func TestShouldRunChore(t *testing.T) {
	// A schedule of every 30 minutes
	schedule := "*/30 * * * *"

	// Case 1: Never run before
	if !shouldRunChore(schedule, time.Time{}) {
		t.Errorf("expected shouldRunChore to be true if lastRun is zero")
	}

	// Case 2: Run 35 minutes ago, cron triggers every 30 mins -> should run
	lastRun1 := time.Now().Add(-35 * time.Minute)
	if !shouldRunChore(schedule, lastRun1) {
		t.Errorf("expected shouldRunChore to be true when last run was 35m ago")
	}

	// Case 3: Run 5 minutes ago, cron triggers every 30 mins -> should not run
	lastRun2 := time.Now().Add(-5 * time.Minute)
	if shouldRunChore(schedule, lastRun2) {
		t.Errorf("expected shouldRunChore to be false when last run was 5m ago")
	}
}
