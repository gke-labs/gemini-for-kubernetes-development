package api

import (
	"fmt"
	"regexp"
	"strconv"
)

// Before every rule's task was a rule task (task-<kind>-<n>-rule-<rule>,
// TypePRRule, TypeIssueRule), factory's own had a type and a file name of
// their own. A watch upgraded from one finds such tasks queued, running
// and processed, and run history kept under their names: MigrateTask and
// MigratedTaskID bring them over, so they run, and count toward cooldowns
// and give-ups, as the rule tasks they are now.

// legacyPRSuffixes are the rule of each legacy PR task file's suffix.
var legacyPRSuffixes = map[string]string{"comments": "comments", "iterate": "conflict", "investigate": "ci", "review": "review"}

// legacyTypes are the legacy types, with their runs' prefix and their
// type now.
var legacyTypes = map[TaskType]struct {
	runPrefix string
	now       TaskType
}{
	"pr-review":      {"review", TypePRRule},
	"pr-investigate": {"investigate", TypePRRule},
	"pr-comments":    {"comments", TypePRRule},
	"pr-iterate":     {"iterate", TypePRRule},
	"issue-fix":      {"fix", TypeIssueRule},
}

var (
	legacyPRTaskID    = regexp.MustCompile(`^task-pr-(\d+)-(comments|iterate|investigate|review)$`)
	legacyIssueTaskID = regexp.MustCompile(`^task-issue-(\d+)$`)
)

// MigratedTaskID is the task id (a task file's name without .yaml) a
// legacy one is now - task-pr-7-investigate is task-pr-7-rule-ci,
// task-issue-7 task-issue-7-rule-fix - and any other id itself; the rule
// is "" for an id that is not legacy.
func MigratedTaskID(id string) (string, string) {
	if m := legacyPRTaskID.FindStringSubmatch(id); m != nil {
		n, _ := strconv.Atoi(m[1])
		rule := legacyPRSuffixes[m[2]]
		return trimYAML(RuleTaskFile(true, n, rule)), rule
	}
	if m := legacyIssueTaskID.FindStringSubmatch(id); m != nil {
		n, _ := strconv.Atoi(m[1])
		return trimYAML(RuleTaskFile(false, n, "fix")), "fix"
	}
	return id, ""
}

// MigrateTask brings a task of a legacy type over, queued under the legacy
// id: the run name its type gave it stamped (a run in flight is followed,
// not started again), its rule from the id when it carries none, and its
// type now. It reports whether the task was legacy.
func MigrateTask(t *QueueTask, id string) bool {
	legacy, ok := legacyTypes[t.Type]
	if !ok {
		return false
	}
	if t.RunName == "" {
		sha := t.CommitSHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		if sha == "" {
			sha = "head"
		}
		at := t.EnqueuedAt
		if at.IsZero() {
			at = t.CreatedAt
		}
		t.RunName = fmt.Sprintf("%s-%d-%s-%d", legacy.runPrefix, t.Number, sha, at.Unix())
	}
	if _, rule := MigratedTaskID(id); t.Rule == "" {
		t.Rule = rule
	}
	if t.Type == "pr-comments" && t.Acknowledge == "" {
		// What the comments rule declares, read from it at start for a
		// queued task; a processed one is read back as it is.
		t.Acknowledge = "feedback"
	}
	t.Type = legacy.now
	return true
}

func trimYAML(file string) string { return file[:len(file)-len(".yaml")] }
