package taskoutput

import (
	"strings"
	"testing"
	"time"
)

func TestDeferredRerunMarker(t *testing.T) {
	d := DeferredRerun{Run: 41899978055, Head: "b67a6e47abc", Until: time.Date(2026, 11, 4, 22, 0, 0, 0, time.UTC), State: DeferredPending}
	body := DeferredRerunComment(d, "ci-presubmit")
	got, ok := ParseDeferredRerun(body)
	if !ok || got != d {
		t.Fatalf("round trip %+v, want %+v\n%s", got, d, body)
	}
	settled := SettleDeferredRerun(body, d, DeferredDone, "Re-ran the failed jobs of run 41899978055 at 20:19Z.")
	if s, _ := ParseDeferredRerun(settled); s.State != DeferredDone {
		t.Errorf("settled state %q", s.State)
	}
	if !strings.HasPrefix(settled, "🤖 These checks failed") || !strings.Contains(settled, "Re-ran the failed jobs") || strings.Count(settled, "factory:deferred") != 1 {
		t.Errorf("settled comment:\n%s", settled)
	}
	if _, ok := ParseDeferredRerun("a comment"); ok {
		t.Error("parsed a marker out of nothing")
	}
}
