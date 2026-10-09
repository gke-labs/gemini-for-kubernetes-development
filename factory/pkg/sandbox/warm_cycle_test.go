package sandbox

import (
	"context"
	"testing"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEnsureWarmSandbox(t *testing.T) {
	ctx := context.Background()
	// A snapshot it must not restore from: the warm sandbox fills it.
	kc := warmTestClients(warmTestSnapshot("warm-substrate-1", "substrate", "img:1", time.Now().Add(-time.Hour), true, time.Now().Add(time.Hour)))
	sb, err := EnsureWarmSandbox(ctx, kc, "ns", "substrate", "https://github.com/o/substrate.git", "https://github.com/o/substrate", "img:1", "40Gi", "premium-rwo", "", nil, "coder")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace("ns").Get(ctx, "warm-substrate", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetLabels()[LabelWarmWorkspace] != "substrate" || got.GetAnnotations()[AnnotationWarmRun] == "" {
		t.Fatalf("labels %v annotations %v", got.GetLabels(), got.GetAnnotations())
	}
	if _, found, _ := unstructured.NestedMap(workspaceClaim(t, got), "spec", "dataSource"); found {
		t.Fatal("the warm sandbox was restored from a snapshot")
	}
	if vols, found, _ := unstructured.NestedSlice(got.Object, "spec", "podTemplate", "spec", "volumes"); found {
		t.Fatalf("the warm sandbox mounts %v", vols)
	}
	again, err := EnsureWarmSandbox(ctx, kc, "ns", "substrate", "", "", "img:1", "", "", "", nil, "coder")
	if err != nil || again.GetAnnotations()[AnnotationWarmRun] != sb.GetAnnotations()[AnnotationWarmRun] {
		t.Fatalf("second ensure: %v, run %q want %q", err, again.GetAnnotations()[AnnotationWarmRun], sb.GetAnnotations()[AnnotationWarmRun])
	}
}

func TestWarmDue(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	day := 24 * time.Hour
	for name, tc := range map[string]struct {
		snaps []*unstructured.Unstructured
		warm  bool
		due   bool
	}{
		"nothing":        {due: true},
		"fresh":          {snaps: []*unstructured.Unstructured{warmTestSnapshot("a", "substrate", "img:1", now.Add(-time.Hour), true, now.Add(day))}},
		"old":            {snaps: []*unstructured.Unstructured{warmTestSnapshot("a", "substrate", "img:1", now.Add(-2*day), true, now.Add(day))}, due: true},
		"fresh, unready": {snaps: []*unstructured.Unstructured{warmTestSnapshot("a", "substrate", "img:1", now.Add(-time.Hour), false, now.Add(day))}, due: true},
		"warm sandbox":   {snaps: []*unstructured.Unstructured{warmTestSnapshot("a", "substrate", "img:1", now.Add(-time.Hour), true, now.Add(day))}, warm: true, due: true},
	} {
		t.Run(name, func(t *testing.T) {
			kc := warmTestClients()
			for _, s := range tc.snaps {
				if _, err := kc.DynamicClient.Resource(VolumeSnapshotGVR).Namespace("ns").Create(ctx, s, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.warm {
				if _, err := EnsureWarmSandbox(ctx, kc, "ns", "substrate", "", "", "img:1", "", "", "", nil, ""); err != nil {
					t.Fatal(err)
				}
			}
			due, err := WarmDue(ctx, kc, "ns", "substrate", day, now)
			if err != nil || due != tc.due {
				t.Fatalf("WarmDue = %v, %v; want %v", due, err, tc.due)
			}
		})
	}
}

func TestSnapshotAndFinishWarm(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	day := 24 * time.Hour
	kc := warmTestClients(
		warmTestSnapshot("warm-substrate-old1", "substrate", "img:1", now.Add(-3*day), true, now.Add(day)),
		warmTestSnapshot("warm-substrate-old2", "substrate", "img:1", now.Add(-2*day), true, now.Add(day)),
		warmTestSnapshot("other", "other-repo", "img:1", now.Add(-5*day), true, now.Add(day)),
	)
	sb, err := EnsureWarmSandbox(ctx, kc, "ns", "substrate", "", "", "img:1", "", "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	name, err := SnapshotWarmSandbox(ctx, kc, "ns", sb, map[string]string{AnnotationWarmScript: "inline"}, 3*day, now)
	if err != nil {
		t.Fatal(err)
	}
	snaps := kc.DynamicClient.Resource(VolumeSnapshotGVR).Namespace("ns")
	snap, err := snaps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pvc, _, _ := unstructured.NestedString(snap.Object, "spec", "source", "persistentVolumeClaimName")
	class, _, _ := unstructured.NestedString(snap.Object, "spec", "volumeSnapshotClassName")
	a := snap.GetAnnotations()
	if pvc != "workspaces-pvc-warm-substrate" || class != WarmSnapshotClass || a[AnnotationWarmImage] != "img:1" || a[AnnotationWarmScript] != "inline" || a[AnnotationWarmExpires] == "" {
		t.Fatalf("snapshot %s: pvc %q class %q annotations %v", name, pvc, class, a)
	}
	sbNow, _ := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace("ns").Get(ctx, "warm-substrate", metav1.GetOptions{})
	if r, _, _ := unstructured.NestedInt64(sbNow.Object, "spec", "replicas"); r != 0 || sbNow.GetAnnotations()[AnnotationWarmSnapshot] != name {
		t.Fatalf("sandbox replicas %d annotations %v", r, sbNow.GetAnnotations())
	}

	// Not ready yet: nothing happens.
	if done, err := FinishWarm(ctx, kc, "ns", sbNow, 2); err != nil || done {
		t.Fatalf("FinishWarm before ready = %v, %v", done, err)
	}
	// The fake has no snapshot controller: make it ready, and give it
	// the creation time the fake leaves unset.
	_ = unstructured.SetNestedField(snap.Object, true, "status", "readyToUse")
	snap.SetCreationTimestamp(metav1.NewTime(now))
	if _, err := snaps.Update(ctx, snap, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if done, err := FinishWarm(ctx, kc, "ns", sbNow, 2); err != nil || !done {
		t.Fatalf("FinishWarm = %v, %v", done, err)
	}
	list, _ := snaps.List(ctx, metav1.ListOptions{})
	var left []string
	for _, s := range list.Items {
		left = append(left, s.GetName())
	}
	if len(left) != 3 || !contains(left, name) || !contains(left, "warm-substrate-old2") || !contains(left, "other") {
		t.Fatalf("snapshots left %v, want %s, warm-substrate-old2 and other", left, name)
	}
	if _, err := kc.DynamicClient.Resource(k8s.SandboxGVR).Namespace("ns").Get(ctx, "warm-substrate", metav1.GetOptions{}); err == nil {
		t.Fatal("the warm sandbox is still there")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
