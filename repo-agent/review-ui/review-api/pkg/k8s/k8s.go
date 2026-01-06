package k8s

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"

	redis "github.com/go-redis/redis/v8"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
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
)

type Manager struct {
	Client    dynamic.Interface
	Clientset *kubernetes.Clientset
	Redis     *redis.Client
}

func NewManager(client dynamic.Interface, clientset *kubernetes.Clientset, rdb *redis.Client) *Manager {
	return &Manager{Client: client, Clientset: clientset, Redis: rdb}
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
	prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
	sandboxName, err := m.Redis.HGet(ctx, prKey, "sandbox").Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("failed to get sandbox name from Redis: %w", err)
	}

	if sandboxName == "" {
		sandboxName = fmt.Sprintf("%s-pr-%s", repo, prID)
	}

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	log.Printf("Scaling down sandbox %s", sandboxName)

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err = m.Client.Resource(gvr).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown sandbox: %w", err)
	}
	return nil
}

func (m *Manager) UpdateReviewSandboxUserDraft(ctx context.Context, namespace, sandboxName, userDraft string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}

	sandbox, err := m.Client.Resource(gvr).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get reviewsandbox %s: %w", sandboxName, err)
	}

	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations["userDraft"] = userDraft
	sandbox.SetAnnotations(annotations)

	_, err = m.Client.Resource(gvr).Namespace(namespace).Update(context.TODO(), sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update reviewsandbox annotation: %w", err)
	}

	return nil
}

func (m *Manager) UpdateReviewSandboxAnnotation(ctx context.Context, namespace, sandboxName, key, value string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}

	sandbox, err := m.Client.Resource(gvr).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get reviewsandbox %s: %w", sandboxName, err)
	}

	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations[key] = value
	sandbox.SetAnnotations(annotations)

	_, err = m.Client.Resource(gvr).Namespace(namespace).Update(ctx, sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update reviewsandbox annotation: %w", err)
	}

	return nil
}

func (m *Manager) UpdateDevSandboxAnnotation(ctx context.Context, namespace, sandboxName, key, value string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "devsandboxes",
	}

	sandbox, err := m.Client.Resource(gvr).Namespace(namespace).Get(ctx, sandboxName, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get devsandbox %s: %w", sandboxName, err)
	}

	if sandbox.GetAnnotations() == nil {
		sandbox.SetAnnotations(make(map[string]string))
	}
	annotations := sandbox.GetAnnotations()
	annotations[key] = value
	sandbox.SetAnnotations(annotations)

	_, err = m.Client.Resource(gvr).Namespace(namespace).Update(ctx, sandbox, v1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update devsandbox annotation: %w", err)
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
	} else if val, ok := secretData["pat"]; ok && val != "" {
		tokenBase64 = val
	} else {
		return "", fmt.Errorf("no GitHub token found in secret %s (checked %s, %s, and pat)", secretName, ManualPATKey, OAuthPATKey)
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
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	log.Printf("Scaling down issue sandbox %s", sandboxName)

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err := m.Client.Resource(gvr).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown issue sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaledownDevSandboxHelper(ctx context.Context, namespace, name string) error {
	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "devsandboxes",
	}
	log.Printf("Scaling down dev sandbox %s", name)

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "DevSandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	_, err := m.Client.Resource(gvr).Namespace(namespace).Apply(ctx, name,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scaledown dev sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaleupSandbox(ctx context.Context, namespace, repo, prID string) error {
	prKey := fmt.Sprintf("pr:ns:%s:repo:%s:pr:%s", namespace, repo, prID)
	sandboxName, err := m.Redis.HGet(ctx, prKey, "sandbox").Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("failed to get sandbox name from Redis: %w", err)
	}
	if sandboxName == "" {
		sandboxName = fmt.Sprintf("%s-pr-%s", repo, prID)
	}

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "reviewsandboxes",
	}
	log.Printf("Scaling up sandbox %s", sandboxName)

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "ReviewSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err = m.Client.Resource(gvr).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scale up sandbox: %w", err)
	}
	return nil
}

func (m *Manager) ScaleupIssueSandbox(ctx context.Context, namespace, repo, issueID, handler string) error {
	sandboxName := fmt.Sprintf("%s-issue-%s-%s", repo, issueID, handler)

	gvr := schema.GroupVersionResource{
		Group:    "custom.agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "issuesandboxes",
	}
	log.Printf("Scaling up issue sandbox %s", sandboxName)

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
			},
		},
	}

	_, err := m.Client.Resource(gvr).Namespace(namespace).Apply(ctx, sandboxName,
		sandbox, v1.ApplyOptions{FieldManager: "review-ui", Force: true})
	if err != nil {
		return fmt.Errorf("failed to scale up issue sandbox: %w", err)
	}
	return nil
}
