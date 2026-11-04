package taskoutput

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// A rerun the apply could not make yet: GitHub reruns a workflow run's
// failed jobs only once the whole run has completed, and care-ci often
// judges the failed jobs (unrelated: rerun) while the rest still run - the
// gatekeeper job, say. Rather than drop the agent's assessment, the apply
// posts one comment on the PR carrying it (DeferredRerun.Marker), and the
// watch's PR scan reruns the run once it has completed and failed, at the
// head it was judged at, once (pkg/commands/watch/prs).

// Deferred rerun states (DeferredRerun.State).
const (
	DeferredPending = "pending"
	DeferredDone    = "done"
	DeferredClosed  = "closed"
)

// DeferredRerunFor is how long a deferred rerun waits for its run to
// complete before it is given up.
const DeferredRerunFor = 2 * time.Hour

// DeferredRerun is a rerun of run Run's failed jobs, judged at head Head,
// to be made once the run completes, until Until.
type DeferredRerun struct {
	Run   int64
	Head  string
	Until time.Time
	State string
}

var deferredRerunRE = regexp.MustCompile(`<!-- factory:deferred action=rerun run=(\d+) head=([0-9A-Za-z]+) until=(\S+) state=(\w+) -->`)

// Marker is the hidden marker a deferred rerun's comment carries.
func (d DeferredRerun) Marker() string {
	return fmt.Sprintf("<!-- factory:deferred action=rerun run=%d head=%s until=%s state=%s -->", d.Run, d.Head, d.Until.UTC().Format(time.RFC3339), d.State)
}

// ParseDeferredRerun reads a comment's deferred rerun marker.
func ParseDeferredRerun(body string) (DeferredRerun, bool) {
	m := deferredRerunRE.FindStringSubmatch(body)
	if m == nil {
		return DeferredRerun{}, false
	}
	run, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return DeferredRerun{}, false
	}
	until, err := time.Parse(time.RFC3339, m[3])
	if err != nil {
		return DeferredRerun{}, false
	}
	return DeferredRerun{Run: run, Head: m[2], Until: until, State: m[4]}, true
}

// DeferredRerunComment is the comment that defers a rerun of run (named
// name): what will happen, and its marker.
func DeferredRerunComment(d DeferredRerun, name string) string {
	return fmt.Sprintf("🤖 These checks failed through no fault of this change, so their failed jobs are to be re-run. Run %d (%s) is still running, and GitHub reruns a run only once it has finished: its failed jobs will be re-run when it does.\n\n%s", d.Run, name, d.Marker())
}

// SettleDeferredRerun is a deferred rerun's comment once settled: the
// first paragraph as it was, what became of it (note), and the marker in
// its new state.
func SettleDeferredRerun(body string, d DeferredRerun, state, note string) string {
	first := body
	if i := deferredRerunRE.FindStringIndex(body); i != nil {
		first = body[:i[0]]
	}
	d.State = state
	return fmt.Sprintf("%s%s\n\n%s", first, note, d.Marker())
}
