package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
		name             string
		pods             []corev1.Pod
		want, fail, dead string
	}{
		{"ready", []corev1.Pod{pod("a", corev1.PodRunning, true, false, "")}, "a", "", ""},
		{"not ready yet", []corev1.Pod{pod("a", corev1.PodRunning, false, false, "")}, "", "", ""},
		{"a restart in progress", []corev1.Pod{pod("old", corev1.PodRunning, true, true, ""), pod("new", corev1.PodRunning, true, false, "")}, "", "", ""},
		{"evicted, nothing else", []corev1.Pod{pod("a", corev1.PodFailed, false, false, "Evicted")}, "", "sandbox pod a is in Failed state (reason: Evicted)", "a"},
		{"refused by its node", []corev1.Pod{pod("a", corev1.PodFailed, false, false, "OutOfcpu")}, "", "sandbox pod a is in Failed state (reason: OutOfcpu)", "a"},
		{"exited", []corev1.Pod{pod("a", corev1.PodSucceeded, false, false, "")}, "", "sandbox pod a is in Succeeded state", ""},
		{"failed, a new one pending", []corev1.Pod{pod("a", corev1.PodFailed, false, false, ""), pod("b", corev1.PodPending, false, false, "")}, "", "", ""},
		{"none", nil, "", "", ""},
	} {
		name, failed, dead := readyPod(tc.pods)
		if name != tc.want || !strings.HasPrefix(failed, tc.fail) || (tc.fail == "") != (failed == "") || dead != tc.dead {
			t.Errorf("%s: readyPod = %q, %q, %q; want %q, %q, %q", tc.name, name, failed, dead, tc.want, tc.fail, tc.dead)
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

// A sandbox whose pod its node refused (OutOfcpu) gets a new pod: the
// failed one is deleted once and the sandbox controller's replacement is
// the one handed back. A replacement that fails too is the error (KCC
// #15222, 2026-11-07: three tasks failed at once against the same dead
// pod).
func TestSandboxPodReplacesAFailedPod(t *testing.T) {
	defer func(poll time.Duration) { sandboxPodPoll = poll }(sandboxPodPoll)
	sandboxPodPoll = time.Millisecond

	for _, tc := range []struct {
		name        string
		replacement corev1.Pod
		wantPod     string
		wantErr     string
	}{
		{"replacement ready", pod("sb-2", corev1.PodRunning, true, false, ""), "sb-2", ""},
		{"replacement refused too", pod("sb-2", corev1.PodFailed, false, false, "OutOfcpu"), "", "sandbox pod sb-2 is in Failed state (reason: OutOfcpu): cannot connect to terminated pod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": SandboxGVR.Group + "/" + SandboxGVR.Version, "kind": "Sandbox",
				"metadata": map[string]interface{}{"name": "sb", "namespace": "ns"},
			}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{SandboxGVR: "SandboxList"})
			if _, err := dyn.Resource(SandboxGVR).Namespace("ns").Create(context.Background(), sb, v1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			dead := pod("sb-1", corev1.PodFailed, false, false, "OutOfcpu")
			dead.Namespace, dead.Labels = "ns", map[string]string{"sandbox": "sb"}
			cs := k8sfake.NewSimpleClientset(&dead)
			// The sandbox controller: a deleted pod is replaced.
			var deleted []string
			cs.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
				deleted = append(deleted, a.(k8stesting.DeleteAction).GetName())
				next := tc.replacement
				next.Namespace, next.Labels = "ns", map[string]string{"sandbox": "sb"}
				if len(deleted) == 1 {
					_ = cs.Tracker().Add(&next)
				}
				return false, nil, nil
			})
			kube := &clients.KubernetesClient{DynamicClient: dyn, Clientset: cs}

			got, err := sandboxPod(context.Background(), kube, "ns", "sb")
			if got != tc.wantPod || (err == nil) != (tc.wantErr == "") || (err != nil && err.Error() != tc.wantErr) {
				t.Errorf("sandboxPod = %q, %v; want %q, %q", got, err, tc.wantPod, tc.wantErr)
			}
			if len(deleted) != 1 || deleted[0] != "sb-1" {
				t.Errorf("deleted %v, want only the failed pod sb-1", deleted)
			}
		})
	}
}
