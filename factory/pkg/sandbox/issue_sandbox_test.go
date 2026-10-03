package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
)

func fakeKube(t *testing.T, ns string, objs ...*unstructured.Unstructured) *clients.KubernetesClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{k8s.SandboxGVR: "SandboxList"})
	for _, o := range objs {
		if _, err := dyn.Resource(k8s.SandboxGVR).Namespace(ns).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding %s: %v", o.GetName(), err)
		}
	}
	return &clients.KubernetesClient{DynamicClient: dyn, Clientset: k8sfake.NewSimpleClientset()}
}

func TestIssueLabels(t *testing.T) {
	got := IssueLabels("open-rl", "7")
	if got[LabelRepo] != "open-rl" || got[LabelIssue] != "7" {
		t.Errorf("IssueLabels = %v", got)
	}
	if _, ok := IssueLabels("open-rl", "my-task")[LabelIssue]; ok {
		t.Error("a named task's sandbox got an issue label")
	}
	long := "_" + strings.Repeat("a", 61) + "._b"
	if v := labelValue(long); len(v) > 63 || strings.HasPrefix(v, "_") || strings.HasSuffix(v, ".") {
		t.Errorf("labelValue(%q) = %q, not a valid label value", long, v)
	}
}

// Triage and plan find the issue's sandbox by its labels, so a sandbox
// made before they existed gains them when it is reused.
func TestFixSandboxIsLabelledOnCreateAndReuse(t *testing.T) {
	ns := "u"
	old := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata":   map[string]interface{}{"name": "fix-open-rl-7", "namespace": ns},
		"spec":       map[string]interface{}{"replicas": int64(1)},
	}}
	kc := fakeKube(t, ns, old)
	for _, issue := range []string{"7", "8"} { // reused, created
		name, err := EnsureFixSandbox(context.Background(), kc, ns, "open-rl", issue, "https://github.com/o/open-rl.git", "", "", "", "", "", "", nil, nil, "")
		if err != nil {
			t.Fatalf("EnsureFixSandbox(%s): %v", issue, err)
		}
		got, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if l := got.GetLabels(); l[LabelRepo] != "open-rl" || l[LabelIssue] != issue {
			t.Errorf("%s labels = %v", name, l)
		}
		if issue == "8" && !IsIssueSandbox(got, "o", "open-rl", 8) {
			t.Errorf("IsIssueSandbox(%s) = false", name)
		}
	}
}

func TestIsIssueSandboxChecksTheRepo(t *testing.T) {
	sb := &unstructured.Unstructured{}
	sb.SetLabels(map[string]string{LabelIssue: "7"})
	sb.SetAnnotations(map[string]string{"repo": "open-rl", "cloneURL": "https://github.com/o/open-rl.git"})
	for _, c := range []struct {
		owner, repo string
		n           int
		want        bool
	}{
		{"o", "open-rl", 7, true},
		{"O", "open-rl", 7, true},
		{"other", "open-rl", 7, false},
		{"o", "open", 7, false},
		{"o", "open-rl", 8, false},
	} {
		if got := IsIssueSandbox(sb, c.owner, c.repo, c.n); got != c.want {
			t.Errorf("IsIssueSandbox(%s/%s#%d) = %v, want %v", c.owner, c.repo, c.n, got, c.want)
		}
	}
}

// A side task records its state under its own name and leaves the fix's
// last-task-* alone; the idle suspender still sees it running.
func TestSideTaskLeavesLastTaskAlone(t *testing.T) {
	ns := "u"
	sb := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agents.x-k8s.io/v1alpha1",
		"kind":       "Sandbox",
		"metadata": map[string]interface{}{"name": "fix-open-rl-7", "namespace": ns,
			"creationTimestamp": time.Now().Add(-5 * time.Hour).Format(time.RFC3339),
			"annotations": map[string]interface{}{
				"sandbox.gemini.google.com/last-task-type":  "fix",
				"sandbox.gemini.google.com/last-task-state": "Completed",
			}},
		"spec": map[string]interface{}{"replicas": int64(1)},
	}}
	kc := fakeKube(t, ns, sb)
	ctx := context.Background()
	if err := MarkSandboxSideTaskRunning(ctx, kc, ns, "fix-open-rl-7", "triage", "gemini"); err != nil {
		t.Fatal(err)
	}
	got, _ := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "fix-open-rl-7", metav1.GetOptions{})
	a := got.GetAnnotations()
	if a["sandbox.gemini.google.com/last-task-type"] != "fix" || a["sandbox.gemini.google.com/last-task-state"] != "Completed" {
		t.Errorf("last-task-* changed: %v", a)
	}
	if a[SideTaskStateAnnotation("triage")] != "Running" {
		t.Errorf("%s = %q, want Running", SideTaskStateAnnotation("triage"), a[SideTaskStateAnnotation("triage")])
	}
	// Old timestamps, but a side task is running: not idle.
	a["sandbox.gemini.google.com/last-task-time"] = time.Now().Add(-4 * time.Hour).Format(time.RFC3339)
	got.SetAnnotations(a)
	if _, idle := idleSince(got, time.Hour, time.Now().Add(3*time.Hour)); idle {
		t.Error("idle while a side task runs")
	}

	if err := UpdateSandboxSideTaskAnnotation(ctx, kc, ns, "fix-open-rl-7", "triage", "Completed"); err != nil {
		t.Fatal(err)
	}
	got, _ = kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(ctx, "fix-open-rl-7", metav1.GetOptions{})
	if s := got.GetAnnotations()[SideTaskStateAnnotation("triage")]; s != "Completed" {
		t.Errorf("triage state = %q, want Completed", s)
	}
	if _, idle := idleSince(got, time.Hour, time.Now().Add(3*time.Hour)); !idle {
		t.Error("not idle after the side task finished")
	}
}

// repo-agent's board ignores sandboxes other launchers made, so when it
// reuses an issue's sandbox a hand-run triage made, it takes it over; a
// hand run never takes one from repo-agent.
func TestReusedFixSandboxLauncher(t *testing.T) {
	defer func(l string) { Launcher = l }(Launcher)
	ns := "u"
	sb := func(launcher string) *unstructured.Unstructured {
		o := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata":   map[string]interface{}{"name": "fix-open-rl-7", "namespace": ns},
			"spec":       map[string]interface{}{"replicas": int64(1)},
		}}
		o.SetLabels(map[string]string{LabelLauncher: launcher})
		return o
	}
	for _, c := range []struct{ had, launcher, want string }{
		{"factory", "repo-agent", "repo-agent"},
		{"repo-agent", "factory", "repo-agent"},
	} {
		kc := fakeKube(t, ns, sb(c.had))
		Launcher = c.launcher
		if _, err := EnsureFixSandbox(context.Background(), kc, ns, "open-rl", "7", "", "", "", "", "", "", "", nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		got, _ := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace(ns).Get(context.Background(), "fix-open-rl-7", metav1.GetOptions{})
		if l := got.GetLabels()[LabelLauncher]; l != c.want {
			t.Errorf("made by %s, reused by %s: launcher = %s, want %s", c.had, c.launcher, l, c.want)
		}
	}
}
