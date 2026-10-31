package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// A sandbox that is not there is "does not exist"; a cluster that could
// not be asked is that error, not "does not exist": a lookup that failed
// for want of credentials sent `factory sandbox task output` users off to
// recreate a sandbox that was there, suspended (2026-10-30).
func TestSandboxPodLookupErrors(t *testing.T) {
	newClient := func() (*clients.KubernetesClient, *dynamicfake.FakeDynamicClient) {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{SandboxGVR: "SandboxList"})
		return &clients.KubernetesClient{DynamicClient: dyn, Clientset: k8sfake.NewSimpleClientset()}, dyn
	}

	kube, _ := newClient()
	if _, err := sandboxPod(context.Background(), kube, "ns", "gone"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing sandbox: err = %v", err)
	}

	kube, dyn := newClient()
	dyn.PrependReactor("get", "sandboxes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("getting credentials: dial tcp: lookup oauth2.googleapis.com: no such host")
	})
	_, err := sandboxPod(context.Background(), kube, "ns", "there")
	if err == nil || strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "no such host") {
		t.Errorf("unreachable cluster: err = %v", err)
	}
}
