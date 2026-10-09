package sandbox

import (
	"context"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// A warm workspace (design/warm-workspace.md) is a VolumeSnapshot of a
// workspace disk with the repository checked out and its caches filled.
// A sandbox for the repository gets its disk restored from the newest one.
const (
	// LabelWarmWorkspace is on the warm sandbox and on its snapshots: the
	// repository (as labelValue makes it) they warm.
	LabelWarmWorkspace = "factory.gemini.google.com/warm-workspace"
	// AnnotationWarmImage is the image the warm sandbox ran. A snapshot
	// restores only into a sandbox of the same image: another image's
	// toolchain would miss the snapshot's caches.
	AnnotationWarmImage = "factory.gemini.google.com/warm-image"
	// AnnotationWarmExpires is when a snapshot is too old to restore
	// (RFC 3339), however many newer ones failed to appear.
	AnnotationWarmExpires = "factory.gemini.google.com/warm-expires"
	// AnnotationWarmRestored is on a sandbox restored from a snapshot: its
	// name.
	AnnotationWarmRestored = "factory.gemini.google.com/warm-restored-from"
)

// VolumeSnapshotGVR is the CSI snapshot API's VolumeSnapshot.
var VolumeSnapshotGVR = schema.GroupVersionResource{
	Group:    "snapshot.storage.k8s.io",
	Version:  "v1",
	Resource: "volumesnapshots",
}

// warmSnapshot is the snapshot a new sandbox of repo running image
// restores from: the newest ready, unexpired one labelled for repo and
// made with image. ok is false when there is none.
func warmSnapshot(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repo, image string, now time.Time) (name string, restoreSize resource.Quantity, ok bool, err error) {
	sel := labels.Set{LabelWarmWorkspace: labelValue(repo)}.String()
	list, err := kubeClient.DynamicClient.Resource(VolumeSnapshotGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return "", restoreSize, false, err
	}
	var newest *unstructured.Unstructured
	for i := range list.Items {
		s := &list.Items[i]
		if s.GetDeletionTimestamp() != nil || s.GetAnnotations()[AnnotationWarmImage] != image {
			continue
		}
		if ready, _, _ := unstructured.NestedBool(s.Object, "status", "readyToUse"); !ready {
			continue
		}
		if exp, err := time.Parse(time.RFC3339, s.GetAnnotations()[AnnotationWarmExpires]); err != nil || !now.Before(exp) {
			continue
		}
		if newest == nil || newest.GetCreationTimestamp().Time.Before(s.GetCreationTimestamp().Time) {
			newest = s
		}
	}
	if newest == nil {
		return "", restoreSize, false, nil
	}
	if size, found, _ := unstructured.NestedString(newest.Object, "status", "restoreSize"); found {
		restoreSize, _ = resource.ParseQuantity(size)
	}
	return newest.GetName(), restoreSize, true, nil
}

// restoreWorkspace points sb's workspace claim at the snapshot, growing
// the claim to the snapshot's size if that is larger: a restored disk is
// at least as large as its snapshot.
func restoreWorkspace(sb *unstructured.Unstructured, snapshot string, restoreSize resource.Quantity) {
	claims, _, _ := unstructured.NestedSlice(sb.Object, "spec", "volumeClaimTemplates")
	for _, c := range claims {
		claim, _ := c.(map[string]interface{})
		if name, _, _ := unstructured.NestedString(claim, "metadata", "name"); name != "workspaces-pvc" {
			continue
		}
		_ = unstructured.SetNestedMap(claim, map[string]interface{}{
			"apiGroup": VolumeSnapshotGVR.Group,
			"kind":     "VolumeSnapshot",
			"name":     snapshot,
		}, "spec", "dataSource")
		if size, _, _ := unstructured.NestedString(claim, "spec", "resources", "requests", "storage"); !restoreSize.IsZero() {
			if q, err := resource.ParseQuantity(size); err != nil || q.Cmp(restoreSize) < 0 {
				_ = unstructured.SetNestedField(claim, restoreSize.String(), "spec", "resources", "requests", "storage")
			}
		}
	}
	_ = unstructured.SetNestedSlice(sb.Object, claims, "spec", "volumeClaimTemplates")
	a := sb.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[AnnotationWarmRestored] = snapshot
	sb.SetAnnotations(a)
}

// restoreFromWarmSnapshot restores a new sandbox's workspace from the
// repository's warm snapshot, if it has one. The warm sandbox itself never
// is. Without a snapshot, or when looking for one fails (no snapshot API,
// no permission), the disk is empty, as it was before warm workspaces.
func restoreFromWarmSnapshot(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, sb *unstructured.Unstructured) {
	repo := sb.GetAnnotations()["repo"]
	if repo == "" || sb.GetLabels()[LabelWarmWorkspace] != "" {
		return
	}
	containers, _, _ := unstructured.NestedSlice(sb.Object, "spec", "podTemplate", "spec", "containers")
	if len(containers) == 0 {
		return
	}
	image, _, _ := unstructured.NestedString(containers[0].(map[string]interface{}), "image")
	name, size, ok, err := warmSnapshot(ctx, kubeClient, namespace, repo, image, time.Now())
	if err != nil {
		klog.V(1).Infof("sandbox %s: no warm workspace (%v); starting on an empty disk", sb.GetName(), err)
		return
	}
	if !ok {
		return
	}
	klog.Infof("sandbox %s: restoring its workspace from snapshot %s", sb.GetName(), name)
	restoreWorkspace(sb, name, size)
}
