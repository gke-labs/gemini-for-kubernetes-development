package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const (
	OAuthPATKey  = "oauth_pat"
	ManualPATKey = "manual_pat"
)

var (
	ConfigDirGVR = schema.GroupVersionResource{
		Group:    "configdir.gke.io",
		Version:  "v1alpha1",
		Resource: "configdirs",
	}
	SandboxGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	OverseerGVR = schema.GroupVersionResource{
		Group:    "overseer.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "overseers",
	}
)

type Manager struct {
	Client     dynamic.Interface
	Clientset  kubernetes.Interface
	KubeClient *clients.KubernetesClient
}

func NewManager(kube *clients.KubernetesClient) *Manager {
	return &Manager{Client: kube.DynamicClient, Clientset: kube.Clientset, KubeClient: kube}
}

func (m *Manager) ListOverseers(ctx context.Context) (*unstructured.UnstructuredList, error) {
	return m.Client.Resource(OverseerGVR).List(ctx, v1.ListOptions{})
}

func (m *Manager) GetOverseer(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return m.Client.Resource(OverseerGVR).Get(ctx, name, v1.GetOptions{})
}

func (m *Manager) ListSandboxes(ctx context.Context, namespace string, labelSelector string) (*unstructured.UnstructuredList, error) {
	return m.Client.Resource(SandboxGVR).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: labelSelector,
	})
}

func (m *Manager) GetConfigDir(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return m.Client.Resource(ConfigDirGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
}

func (m *Manager) UpdateConfigDirFile(ctx context.Context, namespace, name, filePath, content string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cd, err := m.GetConfigDir(ctx, namespace, name)
		if err != nil {
			return err
		}

		files, found, err := unstructured.NestedSlice(cd.Object, "spec", "files")
		if err != nil {
			return err
		}
		if !found {
			files = make([]interface{}, 0)
		}

		newFiles := make([]interface{}, 0, len(files))
		updated := false

		for _, f := range files {
			fileMap, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			path, _, _ := unstructured.NestedString(fileMap, "path")
			if path == filePath {
				if content == "" {
					// Delete file
					updated = true
					continue
				}
				// Update file
				if err := unstructured.SetNestedField(fileMap, content, "source", "inline"); err != nil {
					return err
				}
				newFiles = append(newFiles, fileMap)
				updated = true
				continue
			}
			newFiles = append(newFiles, fileMap)
		}

		if !updated && content != "" {
			// Add new file
			newFile := map[string]interface{}{
				"path": filePath,
				"source": map[string]interface{}{
					"inline": content,
				},
			}
			newFiles = append(newFiles, newFile)
		}

		if err := unstructured.SetNestedSlice(cd.Object, newFiles, "spec", "files"); err != nil {
			return err
		}

		_, err = m.Client.Resource(ConfigDirGVR).Namespace(namespace).Update(ctx, cd, v1.UpdateOptions{})
		return err
	})
}

func (m *Manager) UpdateSecret(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error {
	secret, err := m.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Annotations: annotations,
			},
			Data: data,
		}
		_, err = m.Clientset.CoreV1().Secrets(namespace).Create(ctx, secret, v1.CreateOptions{})
		return err
	} else if err != nil {
		return err
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	for k, v := range data {
		if v == nil {
			delete(secret.Data, k)
		} else {
			secret.Data[k] = v
		}
	}

	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	for k, v := range annotations {
		secret.Annotations[k] = v
	}

	_, err = m.Clientset.CoreV1().Secrets(namespace).Update(ctx, secret, v1.UpdateOptions{})
	return err
}

func (m *Manager) ScaledownSandbox(ctx context.Context, namespace, repo, prID string) error {
	log := klog.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)

	log.Info("Scaling down sandbox", "name", sandboxName)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown sandbox: %w", err)
	}
	return nil
}

func (m *Manager) UpdateSandboxUserDraft(ctx context.Context, namespace, sandboxName, userDraft string) error {
	sandbox, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}

	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations["userDraft"] = userDraft
	sandbox.SetAnnotations(annotations)

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Update(context.TODO(), sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update sandbox annotation: %w", err)
	}

	return nil
}

func (m *Manager) UpdateSandboxAnnotation(ctx context.Context, namespace, sandboxName, key, value string) error {
	sandbox, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}

	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations[key] = value
	sandbox.SetAnnotations(annotations)

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Update(ctx, sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update sandbox annotation: %w", err)
	}

	return nil
}

func (m *Manager) GetGitHubToken(ctx context.Context, repoWatch *unstructured.Unstructured) (string, error) {
	secretName, found, err := unstructured.NestedString(repoWatch.Object, "spec", "githubSecretName")
	if err != nil || !found {
		return "", fmt.Errorf("githubSecretName not found in repowatch %s", repoWatch.GetName())
	}

	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secretUnstructured, err := m.Client.Resource(secretGVR).Namespace(repoWatch.GetNamespace()).Get(ctx, secretName, v1.GetOptions{})
	if err != nil {
		return "", err
	}

	secretData, found, err := unstructured.NestedStringMap(secretUnstructured.Object, "data")
	if err != nil || !found {
		return "", fmt.Errorf("data field not found in secret %s", secretName)
	}

	// Prefer manual_pat, then oauth_pat, then fallback to pat
	var tokenBase64 string
	if val, ok := secretData[ManualPATKey]; ok && val != "" {
		tokenBase64 = val
	} else if val, ok := secretData[OAuthPATKey]; ok && val != "" {
		tokenBase64 = val
	} else {
		return "", fmt.Errorf("no GitHub token found in secret %s (checked %s and %s)", secretName, ManualPATKey, OAuthPATKey)
	}

	tokenBytes, err := base64.StdEncoding.DecodeString(tokenBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode token in secret %s: %w", secretName, err)
	}

	return string(tokenBytes), nil
}

func (m *Manager) GetRepoWatch(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    "review.gemini.google.com",
		Version:  "v1alpha1",
		Resource: "repowatches",
	}
	repoWatch, err := m.Client.Resource(gvr).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return repoWatch, nil
}

func (m *Manager) ScaledownIssueSandbox(ctx context.Context, namespace, repo, issueID, handler string) error {
	log := klog.FromContext(ctx)
	var sandboxName string
	if handler != "" {
		sandboxName = fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)
	} else {
		sandboxName = fmt.Sprintf("%s-issue-%s", repo, issueID)
	}

	log.Info("Scaling down issue sandbox", "name", sandboxName)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get issue sandbox %s: %w", sandboxName, err)
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown issue sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaledownDevSandboxHelper(ctx context.Context, namespace, name string) error {
	log := klog.FromContext(ctx)
	log.Info("Scaling down dev sandbox", "name", name)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get dev sandbox %s: %w", name, err)
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, name,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown dev sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaleupSandbox(ctx context.Context, namespace, repo, prID, annotationValue string) error {
	log := klog.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-pr-%s", repo, prID)

	log.Info("Scaling up sandbox", "name", sandboxName)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}

	metadata := map[string]interface{}{
		"name":      sandboxName,
		"namespace": namespace,
	}

	if annotationValue != "" {
		metadata["annotations"] = map[string]interface{}{
			"sandbox.gemini.google.com/prevent-auto-shutdown": annotationValue,
		}
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata":   metadata,
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scale up sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaleupIssueSandbox(ctx context.Context, namespace, repo, issueID, handler, annotationValue string) error {
	log := klog.FromContext(ctx)
	sandboxName := fmt.Sprintf("%s-issue-%s", repo, issueID)

	log.Info("Scaling up issue sandbox", "name", sandboxName, "handler", handler, "annotationValue", annotationValue)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get issue sandbox %s: %w", sandboxName, err)
	}

	metadata := map[string]interface{}{
		"name":      sandboxName,
		"namespace": namespace,
	}

	if annotationValue != "" {
		metadata["annotations"] = map[string]interface{}{
			"sandbox.gemini.google.com/prevent-auto-shutdown": annotationValue,
		}
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata":   metadata,
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scale up issue sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaleupDevSandboxHelper(ctx context.Context, namespace, name string) error {
	log := klog.FromContext(ctx)
	log.Info("Scaling up dev sandbox", "name", name)

	_, err := m.Client.Resource(SandboxGVR).Namespace(namespace).Get(ctx, name, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get dev sandbox %s: %w", name, err)
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err = m.Client.Resource(SandboxGVR).Namespace(namespace).Apply(ctx, name,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scale up dev sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ListSandboxTasks(ctx context.Context, namespace, sandboxName string) (*sandboxtaskv1alpha1.SandboxTaskList, error) {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}

	labelSelector := ""
	if sandboxName != "" {
		labelSelector = fmt.Sprintf("sandbox.gemini.google.com/sandbox-name=%s", sandboxName)
	}

	unstructuredList, err := m.Client.Resource(gvr).Namespace(namespace).List(ctx, v1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list sandboxtasks: %w", err)
	}

	taskList := &sandboxtaskv1alpha1.SandboxTaskList{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredList.UnstructuredContent(), taskList)
	if err != nil {
		return nil, fmt.Errorf("failed to convert unstructured list to SandboxTaskList: %w", err)
	}

	return taskList, nil
}

func (m *Manager) UpdateSandboxTaskUserDraft(ctx context.Context, namespace, taskName, userDraft string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}

	task, err := m.Client.Resource(gvr).Namespace(namespace).Get(ctx, taskName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get sandboxtask %s: %w", taskName, err)
	}

	if task.GetAnnotations() == nil {
		task.SetAnnotations(make(map[string]string))
	}
	annotations := task.GetAnnotations()
	annotations["userDraft"] = userDraft
	task.SetAnnotations(annotations)

	_, err = m.Client.Resource(gvr).Namespace(namespace).Update(context.TODO(), task, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update sandboxtask annotation: %w", err)
	}

	return nil
}

func (m *Manager) CreateSandboxTask(ctx context.Context, namespace, sandboxName, sandboxKind, taskType string, params map[string]string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}

	// Determine the GVR for the sandbox owner
	var ownerGVR schema.GroupVersionResource
	switch sandboxKind {
	case "Sandbox":
		ownerGVR = SandboxGVR
	default:
		return fmt.Errorf("unknown sandbox kind: %s", sandboxKind)
	}

	// Fetch the sandbox to get its UID
	sandbox, err := m.Client.Resource(ownerGVR).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get sandbox %s: %w", sandboxName, err)
	}

	// Generate a name
	name := fmt.Sprintf("%s-%d-%s", sandboxName, time.Now().Unix(), taskType)

	task := &sandboxtaskv1alpha1.SandboxTask{
		TypeMeta: v1.TypeMeta{
			APIVersion: "custom.agents.x-k8s.io/v1alpha1",
			Kind:       "SandboxTask",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"sandbox.gemini.google.com/sandbox-name": sandboxName,
			},
			OwnerReferences: []v1.OwnerReference{
				*v1.NewControllerRef(sandbox, schema.GroupVersionKind{
					Group:   ownerGVR.Group,
					Version: ownerGVR.Version,
					Kind:    sandboxKind,
				}),
			},
		},
		Spec: sandboxtaskv1alpha1.SandboxTaskSpec{
			SandboxName: sandboxName,
			Type:        taskType,
			Params:      params,
		},
	}

	unstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(task)
	if err != nil {
		return fmt.Errorf("failed to convert SandboxTask to unstructured: %w", err)
	}

	_, err = m.Client.Resource(gvr).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: unstructuredMap}, v1.CreateOptions{})
	return err
}

func (m *Manager) UpdateSandboxTaskStatus(ctx context.Context, namespace, taskName, state, result string, stats *sandboxtaskv1alpha1.Stats) error {
	klog.Infof("Updating task %s status to %s", taskName, state)

	now := v1.Now()
	timestamp := now.UTC().Format(time.RFC3339)

	statusMap := map[string]interface{}{
		"taskState": state,
		"result":    result,
	}

	if state == "Running" {
		statusMap["startTime"] = timestamp
	} else if state == "Completed" || state == "Failed" {
		statusMap["completionTime"] = timestamp
	}

	if stats != nil {
		usageMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(stats)
		if err != nil {
			return fmt.Errorf("failed to convert stats to unstructured: %w", err)
		}
		statusMap["stats"] = usageMap
	}

	patch := map[string]interface{}{
		"status": statusMap,
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxtasks",
	}

	_, err = m.Client.Resource(gvr).Namespace(namespace).Patch(ctx, taskName, types.MergePatchType, patchBytes, v1.PatchOptions{}, "status")
	return err
}
