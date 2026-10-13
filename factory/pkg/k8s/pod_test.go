package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(name string, phase corev1.PodPhase, ready, terminating bool, reason string) corev1.Pod {
	p := corev1.Pod{ObjectMeta: v1.ObjectMeta{Name: name}, Status: corev1.PodStatus{Phase: phase, Reason: reason}}
	if ready {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	if terminating {
		now := v1.Now()
		p.DeletionTimestamp = &now
	}
	return p
}

// The ready pod is the one to use, unless another is still terminating
// (its replacement is coming); a failed pod is an error only when nothing
// live or pending stands beside it, the message the watch looks for.
func TestReadyPod(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pods       []corev1.Pod
		want, fail string
	}{
		{"ready", []corev1.Pod{pod("a", corev1.PodRunning, true, false, "")}, "a", ""},
		{"not ready yet", []corev1.Pod{pod("a", corev1.PodRunning, false, false, "")}, "", ""},
		{"a restart in progress", []corev1.Pod{pod("old", corev1.PodRunning, true, true, ""), pod("new", corev1.PodRunning, true, false, "")}, "", ""},
		{"evicted, nothing else", []corev1.Pod{pod("a", corev1.PodFailed, false, false, "Evicted")}, "", "sandbox pod a is in Failed state (reason: Evicted)"},
		{"failed, a new one pending", []corev1.Pod{pod("a", corev1.PodFailed, false, false, ""), pod("b", corev1.PodPending, false, false, "")}, "", ""},
		{"none", nil, "", ""},
	} {
		name, failed := readyPod(tc.pods)
		if name != tc.want || !strings.HasPrefix(failed, tc.fail) || (tc.fail == "") != (failed == "") {
			t.Errorf("%s: readyPod = %q, %q; want %q, %q", tc.name, name, failed, tc.want, tc.fail)
		}
	}
}
