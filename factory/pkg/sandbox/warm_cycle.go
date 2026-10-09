package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// The warm sandbox's annotations are the warm cycle's state: a watch
// process lives minutes, a warm can take an hour.
const (
	// AnnotationWarmRun is the run name of the warm recipe's run in the
	// sandbox, chosen when it is created, so a later watch follows that
	// run rather than starting another.
	AnnotationWarmRun = "factory.gemini.google.com/warm-run"
	// AnnotationWarmSnapshot is the snapshot made of the sandbox's disk
	// once its run succeeded: the cycle waits for it to be ready.
	AnnotationWarmSnapshot = "factory.gemini.google.com/warm-snapshot"
	// AnnotationWarmFailed is when the sandbox's run failed (RFC 3339).
	// The sandbox stays, suspended, for its logs, until the next try.
	AnnotationWarmFailed = "factory.gemini.google.com/warm-failed"
	// AnnotationWarmScript is where a snapshot's script came from:
	// "inline", "url <URL> sha256:<digest>" or "path <path>@<commit>".
	AnnotationWarmScript = "factory.gemini.google.com/warm-script"
	// AnnotationWarmHead is the default branch's commit a snapshot has
	// checked out.
	AnnotationWarmHead = "factory.gemini.google.com/warm-head"
	// AnnotationWarmGoVersion is the Go a snapshot's caches were built by.
	AnnotationWarmGoVersion = "factory.gemini.google.com/warm-go-version"

	// WarmSnapshotClass is the VolumeSnapshotClass warm snapshots are
	// made with, created once per cluster (design/warm-workspace.md).
	WarmSnapshotClass = "warm-workspace"
)

// WarmSandboxName is the name of repo's warm sandbox.
func WarmSandboxName(repo string) string {
	return "warm-" + repo
}

// GetWarmSandbox is repo's warm sandbox, or nil if it has none.
func GetWarmSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repo string) (*unstructured.Unstructured, error) {
	sb, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, WarmSandboxName(repo), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting sandbox %s: %w", WarmSandboxName(repo), err)
	}
	return sb, nil
}

// EnsureWarmSandbox creates (or returns) repo's warm sandbox, the one the
// warm recipe runs in: its disk becomes every new sandbox's, so it mounts
// no secret and is never itself restored from a snapshot. It is created
// with its run name (AnnotationWarmRun).
func EnsureWarmSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repoName, cloneURL, htmlURL, image, diskSize, storageClass, ephemeralStorage string, envs []EnvVar, user string) (*unstructured.Unstructured, error) {
	name := WarmSandboxName(repoName)
	sb, err := GetWarmSandbox(ctx, kubeClient, namespace, repoName)
	if err != nil {
		return nil, err
	}
	if sb != nil && !terminating(sb) {
		return sb, nil
	}
	if sb != nil {
		if err := awaitSandboxGone(ctx, kubeClient, namespace, name); err != nil {
			return nil, err
		}
	}
	opt := AgentSandboxOptions{
		DevSandboxOptions: DevSandboxOptions{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/type":    "warm",
				"factory.gemini.google.com/managed": "true",
				"factory.gemini.google.com/user":    user,
				LabelRepo:                           labelValue(repoName),
				LabelWarmWorkspace:                  labelValue(repoName),
			},
			Annotations: map[string]string{
				"repo":            repoName,
				"cloneURL":        cloneURL,
				"htmlURL":         htmlURL,
				AnnotationWarmRun: fmt.Sprintf("warm-%d", time.Now().Unix()),
			},
			Image:                 image,
			Replicas:              1,
			WorkspaceDiskSize:     diskSize,
			WorkspaceStorageClass: storageClass,
			EphemeralStorage:      ephemeralStorage,
			Env:                   envs,
		},
	}
	fillEnvResources(&opt.DevSandboxOptions)
	sbObj, svc := NewAgentSandbox(opt)
	if err := createSandbox(ctx, kubeClient, namespace, sbObj, svc); err != nil {
		return nil, err
	}
	return sbObj, nil
}

// AnnotateSandbox sets annotations on sandbox name.
func AnnotateSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string, annotations map[string]string) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		return err
	}
	if _, err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("annotating sandbox %s: %w", name, err)
	}
	return nil
}

// WarmDue reports whether repo's warm cycle has anything to do: a warm
// sandbox exists (a warm running, waiting on its snapshot, or failed), or
// no ready snapshot is younger than interval.
func WarmDue(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repo string, interval time.Duration, now time.Time) (bool, error) {
	sb, err := GetWarmSandbox(ctx, kubeClient, namespace, repo)
	if err != nil || sb != nil {
		return sb != nil, err
	}
	snaps, err := warmSnapshots(ctx, kubeClient, namespace, repo)
	if err != nil {
		return false, err
	}
	for _, s := range snaps {
		if ready, _, _ := unstructured.NestedBool(s.Object, "status", "readyToUse"); ready && now.Sub(s.GetCreationTimestamp().Time) < interval {
			return false, nil
		}
	}
	return true, nil
}

// warmSnapshots are repo's warm snapshots, newest first.
func warmSnapshots(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, repo string) ([]unstructured.Unstructured, error) {
	sel := labels.Set{LabelWarmWorkspace: labelValue(repo)}.String()
	list, err := kubeClient.DynamicClient.Resource(VolumeSnapshotGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("listing warm snapshots: %w", err)
	}
	items := list.Items
	sort.SliceStable(items, func(i, j int) bool {
		return items[j].GetCreationTimestamp().Time.Before(items[i].GetCreationTimestamp().Time)
	})
	return items, nil
}

// SnapshotWarmSandbox suspends the warm sandbox sb, waits for its pod to
// go so that its disk is detached and quiet, and snapshots the disk,
// recording the snapshot on sb (AnnotationWarmSnapshot). annotations go on
// the snapshot, beside the image and its expiry, now + ttl.
func SnapshotWarmSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, sb *unstructured.Unstructured, annotations map[string]string, ttl time.Duration, now time.Time) (string, error) {
	name := sb.GetName()
	repo := sb.GetAnnotations()["repo"]
	if err := SuspendSandbox(ctx, kubeClient, namespace, name); err != nil {
		return "", err
	}
	if err := awaitPodsGone(ctx, kubeClient, namespace, name, 5*time.Minute); err != nil {
		return "", err
	}
	containers, _, _ := unstructured.NestedSlice(sb.Object, "spec", "podTemplate", "spec", "containers")
	image := ""
	if len(containers) > 0 {
		image, _, _ = unstructured.NestedString(containers[0].(map[string]interface{}), "image")
	}
	snapAnnotations := map[string]interface{}{
		AnnotationWarmImage:   image,
		AnnotationWarmExpires: now.Add(ttl).UTC().Format(time.RFC3339),
	}
	for k, v := range annotations {
		snapAnnotations[k] = v
	}
	snapName := fmt.Sprintf("%s-%s", name, now.UTC().Format("20060102-1504"))
	snap := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": VolumeSnapshotGVR.GroupVersion().String(),
		"kind":       "VolumeSnapshot",
		"metadata": map[string]interface{}{
			"name":        snapName,
			"namespace":   namespace,
			"labels":      map[string]interface{}{LabelWarmWorkspace: labelValue(repo), "factory.gemini.google.com/managed": "true"},
			"annotations": snapAnnotations,
		},
		"spec": map[string]interface{}{
			"volumeSnapshotClassName": WarmSnapshotClass,
			"source":                  map[string]interface{}{"persistentVolumeClaimName": "workspaces-pvc-" + name},
		},
	}}
	if _, err := kubeClient.DynamicClient.Resource(VolumeSnapshotGVR).Namespace(namespace).Create(ctx, snap, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating snapshot %s: %w", snapName, err)
	}
	if err := AnnotateSandbox(ctx, kubeClient, namespace, name, map[string]string{AnnotationWarmSnapshot: snapName}); err != nil {
		return "", err
	}
	klog.Infof("Warm workspace: snapshot %s of sandbox %s created", snapName, name)
	return snapName, nil
}

// awaitPodsGone waits for sandbox name's pods to go.
func awaitPodsGone(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pods, err := kubeClient.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"sandbox": name}.String()})
		if err != nil {
			return fmt.Errorf("listing sandbox %s's pods: %w", name, err)
		}
		if len(pods.Items) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox %s's pod is still there %s after suspending it", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// FinishWarm ends a warm whose snapshot was made: once the snapshot is
// ready it deletes all but repo's newest keep snapshots, then the warm
// sandbox sb. It reports whether it finished; false, the snapshot is
// not ready yet. A snapshot that failed is an error, and is deleted with
// the sandbox, so the next cycle warms again.
func FinishWarm(ctx context.Context, kubeClient *clients.KubernetesClient, namespace string, sb *unstructured.Unstructured, keep int) (bool, error) {
	repo := sb.GetAnnotations()["repo"]
	snapName := sb.GetAnnotations()[AnnotationWarmSnapshot]
	snaps := kubeClient.DynamicClient.Resource(VolumeSnapshotGVR).Namespace(namespace)
	snap, err := snaps.Get(ctx, snapName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		err = fmt.Errorf("snapshot %s is gone", snapName)
	case err != nil:
		return false, fmt.Errorf("getting snapshot %s: %w", snapName, err)
	default:
		if msg, found, _ := unstructured.NestedString(snap.Object, "status", "error", "message"); found {
			_ = snaps.Delete(ctx, snapName, metav1.DeleteOptions{})
			err = fmt.Errorf("snapshot %s failed: %s", snapName, msg)
		} else if ready, _, _ := unstructured.NestedBool(snap.Object, "status", "readyToUse"); !ready {
			return false, nil
		}
	}
	if err != nil {
		if derr := deleteWarmSandbox(ctx, kubeClient, namespace, sb.GetName()); derr != nil {
			klog.Errorf("Warm workspace: %v", derr)
		}
		return false, err
	}
	all, err := warmSnapshots(ctx, kubeClient, namespace, repo)
	if err != nil {
		return false, err
	}
	kept := 0
	for _, s := range all {
		ready, _, _ := unstructured.NestedBool(s.Object, "status", "readyToUse")
		if ready && kept < keep {
			kept++
			continue
		}
		if s.GetName() == snapName {
			continue
		}
		klog.Infof("Warm workspace: deleting snapshot %s", s.GetName())
		if err := snaps.Delete(ctx, s.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			klog.Errorf("Warm workspace: deleting snapshot %s: %v", s.GetName(), err)
		}
	}
	if err := deleteWarmSandbox(ctx, kubeClient, namespace, sb.GetName()); err != nil {
		return false, err
	}
	klog.Infof("Warm workspace: snapshot %s is ready", snapName)
	return true, nil
}

// DeleteWarmSandbox deletes the warm sandbox name and waits for it to go.
func DeleteWarmSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	if err := deleteWarmSandbox(ctx, kubeClient, namespace, name); err != nil {
		return err
	}
	return awaitSandboxGone(ctx, kubeClient, namespace, name)
}

func deleteWarmSandbox(ctx context.Context, kubeClient *clients.KubernetesClient, namespace, name string) error {
	err := kubeClient.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting sandbox %s: %w", name, err)
	}
	return nil
}
