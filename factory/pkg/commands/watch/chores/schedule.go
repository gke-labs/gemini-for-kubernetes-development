package chores

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/klog/v2"
)

// cronParser accepts the standard five field cron syntax as well as descriptors
// such as "@daily", which is what agent definitions in the wild use.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// fallbackInterval is how often a chore whose schedule cannot be parsed runs.
// Refusing to run it would silently disable the chore on a typo; running it
// daily keeps it working while the warning points at the broken expression.
const fallbackInterval = 24 * time.Hour

// shouldRunAt reports whether a chore last run at lastRun is due at now.
//
// A chore that has never run is always due, which is what bootstraps a newly
// added agent without waiting for its first scheduled point.
func shouldRunAt(schedule string, lastRun time.Time, now time.Time) bool {
	schedule = strings.TrimSpace(schedule)
	if strings.EqualFold(schedule, "never") || strings.EqualFold(schedule, "paused") {
		return false
	}

	if lastRun.IsZero() {
		return true
	}

	sched, err := cronParser.Parse(schedule)
	if err != nil {
		klog.Warningf("Failed to parse cron expression %q: %v, falling back to %s", schedule, err, fallbackInterval)
		return now.Sub(lastRun) >= fallbackInterval
	}

	return !sched.Next(lastRun).After(now)
}

// dueAt returns the time a chore became due, which is recorded on its task as
// the trigger event time.
//
// A chore that has never run is due now. Otherwise it came due at the first
// scheduled point after its last run, which is earlier than now by however long
// the evaluation cycle took to notice - reporting that point rather than now is
// what makes a late chore visibly late.
func dueAt(schedule string, lastRun, now time.Time) time.Time {
	if lastRun.IsZero() {
		return now
	}
	sched, err := cronParser.Parse(strings.TrimSpace(schedule))
	if err != nil {
		return now
	}
	return sched.Next(lastRun)
}
