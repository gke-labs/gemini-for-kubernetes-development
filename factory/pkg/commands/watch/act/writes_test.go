package act

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

type fakeGH struct {
	added, removed, assigned, unassigned []string
	comments                             []string
	failRemove                           bool
}

func (f *fakeGH) AddLabels(_ context.Context, _ int, l []string) error {
	f.added = append(f.added, l...)
	return nil
}
func (f *fakeGH) RemoveLabel(_ context.Context, _ int, l string) error {
	if f.failRemove {
		return errors.New("boom")
	}
	f.removed = append(f.removed, l)
	return nil
}
func (f *fakeGH) AddAssignees(_ context.Context, _ int, a []string) error {
	f.assigned = append(f.assigned, a...)
	return nil
}
func (f *fakeGH) RemoveAssignees(_ context.Context, _ int, a []string) error {
	f.unassigned = append(f.unassigned, a...)
	return nil
}
func (f *fakeGH) AddComment(_ context.Context, _ int, b string) error {
	f.comments = append(f.comments, b)
	return nil
}

// ready's writes go on as they are; a stop adds the stop label and a
// comment naming it (what PausedAt reads); a failed write is not reported
// as done; dry-run writes nothing.
func TestApplyWrites(t *testing.T) {
	ctx := context.Background()
	f := &fakeGH{}
	done := ApplyWrites(ctx, f, 7, rules.Writes{Label: []string{"overseer/ready-for-human"}, Unlabel: []string{"overseer/review"},
		Assign: []string{"alice"}, Unassign: []string{"ada-coder-bot"}}, "overseer", false)
	if strings.Join(f.added, ",") != "overseer/ready-for-human" || strings.Join(f.removed, ",") != "overseer/review" ||
		strings.Join(f.assigned, ",") != "alice" || strings.Join(f.unassigned, ",") != "ada-coder-bot" || len(f.comments) != 0 {
		t.Errorf("ready wrote %+v", f)
	}
	if strings.Join(done.Labeled, ",") != "overseer/ready-for-human" || strings.Join(done.Unlabeled, ",") != "overseer/review" {
		t.Errorf("done = %+v", done)
	}

	f = &fakeGH{}
	ApplyWrites(ctx, f, 7, rules.Writes{Stop: true, Comment: "Pausing this PR after a period without activity."}, "overseer", false)
	if strings.Join(f.added, ",") != "overseer/stop" || len(f.comments) != 1 ||
		!strings.HasPrefix(f.comments[0], "🤖 Pausing this PR after a period without activity.") || !strings.Contains(f.comments[0], "`overseer/stop`") {
		t.Errorf("stop wrote %+v", f)
	}

	f = &fakeGH{failRemove: true}
	if done := ApplyWrites(ctx, f, 7, rules.Writes{Unlabel: []string{"overseer/review"}}, "overseer", false); len(done.Unlabeled) != 0 {
		t.Errorf("a failed unlabel reported done: %+v", done)
	}

	f = &fakeGH{}
	ApplyWrites(ctx, f, 7, rules.Writes{Stop: true, Label: []string{"x"}}, "overseer", true)
	if len(f.added)+len(f.comments) != 0 {
		t.Errorf("dry-run wrote %+v", f)
	}
}
