package k8s

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"

	redis "github.com/go-redis/redis/v8"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const (
	SystemNamespace  = "repo-agent-system"
	GithubSecretName = "github-pat"
	GeminiSecretName = "gemini-vscode-tokens"
	ClaudeSecretName = "anthropic-api-key"
	DevContainerCM   = "devcontainer-json"
)

type Manager struct {
	Client    dynamic.Interface
	Clientset *kubernetes.Clientset
	Redis     *redis.Client
}

func NewManager(client dynamic.Interface, clientset *kubernetes.Clientset, rdb *redis.Client) *Manager {
	return &Manager{Client: client, Clientset: clientset, Redis: rdb}
}

func (m *Manager) BootstrapNamespace(ctx context.Context, targetNS string) error {
	_, err := m.Clientset.CoreV1().Namespaces().Get(ctx, targetNS, v1.GetOptions{})
	if errors.IsNotFound(err) {
		log.Printf("Creating namespace %s", targetNS)
		ns := &corev1.Namespace{
			ObjectMeta: v1.ObjectMeta{
				Name:   targetNS,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "repo-agent", "review.gemini.google.com/tenant": targetNS},
			},
		}
		if _, err := m.Clientset.CoreV1().Namespaces().Create(ctx, ns, v1.CreateOptions{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Copy default secrets/configs from system namespace if they don't exist in user namespace
	if err := m.CopySecret(ctx, SystemNamespace, GithubSecretName, targetNS, GithubSecretName); err != nil {
		log.Printf("Warning: failed to copy default github secret: %v", err)
	}
	if err := m.CopySecret(ctx, SystemNamespace, GeminiSecretName, targetNS, GeminiSecretName); err != nil {
		log.Printf("Warning: failed to copy default gemini secret: %v", err)
	}
	if err := m.CopySecret(ctx, SystemNamespace, ClaudeSecretName, targetNS, ClaudeSecretName); err != nil {
		log.Printf("Warning: failed to copy default claude secret: %v", err)
	}
	if err := m.CopyConfigMap(ctx, SystemNamespace, DevContainerCM, targetNS, DevContainerCM); err != nil {
		log.Printf("Debug: failed to copy %s: %v", DevContainerCM, err)
	}

	if err := m.SetupServiceAccounts(ctx, targetNS); err != nil {
		log.Printf("Warning: failed to setup service accounts: %v", err)
	}

	return nil
}

func (m *Manager) CopySecret(ctx context.Context, srcNS, srcName, dstNS, dstName string) error {
	src, err := m.Clientset.CoreV1().Secrets(srcNS).Get(ctx, srcName, v1.GetOptions{})
	if err != nil {
		log.Printf("Error reading secret %s/%s: %v", srcNS, srcName, err)
		return err
	}
	dst := &corev1.Secret{ObjectMeta: v1.ObjectMeta{Name: dstName, Namespace: dstNS}, Data: src.Data, Type: src.Type}
	_, err = m.Clientset.CoreV1().Secrets(dstNS).Create(ctx, dst, v1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

func (m *Manager) CopyConfigMap(ctx context.Context, srcNS, srcName, dstNS, dstName string) error {
	src, err := m.Clientset.CoreV1().ConfigMaps(srcNS).Get(ctx, srcName, v1.GetOptions{})
	if err != nil {
		return err
	}
	dst := &corev1.ConfigMap{ObjectMeta: v1.ObjectMeta{Name: dstName, Namespace: dstNS}, Data: src.Data, BinaryData: src.BinaryData}
	_, err = m.Clientset.CoreV1().ConfigMaps(dstNS).Create(ctx, dst, v1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

func (m *Manager) SetupServiceAccounts(ctx context.Context, ns string) error {
	// --- Review Sandbox ---
	saReview := &corev1.ServiceAccount{ObjectMeta: v1.ObjectMeta{Name: "review-sandbox", Namespace: ns}}
	_, err := m.Clientset.CoreV1().ServiceAccounts(ns).Create(ctx, saReview, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Bind to review-sandbox cluster role (base permissions)
	rbReview := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "review-sandbox-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "review-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "review-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = m.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rbReview, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Add to review-sandbox cluster role binding (to match make apply-common-for-examples)
	if err := m.ensureClusterRoleBindingSubject(ctx, "review-sandbox", rbacv1.Subject{Kind: "ServiceAccount", Name: "review-sandbox", Namespace: ns}); err != nil {
		log.Printf("Warning: failed to update review-sandbox cluster role binding: %v", err)
	}

	// Bind to configdir-controller cluster role (needed for init container)
	rbReviewConfigDir := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "review-sandbox-configdir-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "review-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "configdir-controller", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = m.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rbReviewConfigDir, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// --- Dev Sandbox ---
	saDev := &corev1.ServiceAccount{ObjectMeta: v1.ObjectMeta{Name: "dev-sandbox", Namespace: ns}}
	_, err = m.Clientset.CoreV1().ServiceAccounts(ns).Create(ctx, saDev, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Bind to dev-sandbox cluster role (base permissions)
	rbDev := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "dev-sandbox-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "dev-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "dev-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = m.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rbDev, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Add to dev-sandbox cluster role binding (to match make apply-common-for-examples)
	if err := m.ensureClusterRoleBindingSubject(ctx, "dev-sandbox", rbacv1.Subject{Kind: "ServiceAccount", Name: "dev-sandbox", Namespace: ns}); err != nil {
		log.Printf("Warning: failed to update dev-sandbox cluster role binding: %v", err)
	}

	// Bind to configdir-controller cluster role (needed for init container)
	rbDevConfigDir := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "dev-sandbox-configdir-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "dev-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "configdir-controller", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = m.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rbDevConfigDir, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// --- Issue Sandbox ---
	saIssue := &corev1.ServiceAccount{ObjectMeta: v1.ObjectMeta{Name: "issue-sandbox", Namespace: ns}}
	_, err = m.Clientset.CoreV1().ServiceAccounts(ns).Create(ctx, saIssue, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Bind to issue-sandbox cluster role (base permissions)
	rbIssue := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "issue-sandbox-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "issue-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "issue-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = m.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rbIssue, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Add to issue-sandbox cluster role binding (to match make apply-common-for-examples)
	if err := m.ensureClusterRoleBindingSubject(ctx, "issue-sandbox", rbacv1.Subject{Kind: "ServiceAccount", Name: "issue-sandbox", Namespace: ns}); err != nil {
		log.Printf("Warning: failed to update issue-sandbox cluster role binding: %v", err)
	}

	// Bind to configdir-controller cluster role (needed for init container)
	rbIssueConfigDir := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "issue-sandbox-configdir-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "issue-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "configdir-controller", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = m.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rbIssueConfigDir, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

func (m *Manager) ensureClusterRoleBindingSubject(ctx context.Context, bindingName string, subject rbacv1.Subject) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := m.Clientset.RbacV1().ClusterRoleBindings().Get(ctx, bindingName, v1.GetOptions{})
		if err != nil {
			return err
		}
		for _, s := range binding.Subjects {
			if s.Kind == subject.Kind && s.Name == subject.Name && s.Namespace == subject.Namespace {
				return nil // Already exists
			}
		}
		binding.Subjects = append(binding.Subjects, subject)
		_, err = m.Clientset.RbacV1().ClusterRoleBindings().Update(ctx, binding, v1.UpdateOptions{})
		return err
	})
}

func ignoreAlreadyExists(err error) error {
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (m *Manager) UpdateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	secret, err := m.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
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
		secret.Data[k] = v
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

func (m *Manager) GetGitHubToken(ctx context.Context, repoWatch *unstructured.Unstructured) (string, error) {
	secretName, found, err := unstructured.NestedString(repoWatch.Object, "spec", "githubSecretName")
	if err != nil || !found {
		return "", fmt.Errorf("githubSecretName not found in repowatch %s", repoWatch.GetName())
	}
	secretKey := "pat"

	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secretUnstructured, err := m.Client.Resource(secretGVR).Namespace(repoWatch.GetNamespace()).Get(ctx, secretName, v1.GetOptions{})
	if err != nil {
		return "", err
	}

	secretData, found, err := unstructured.NestedStringMap(secretUnstructured.Object, "data")
	if err != nil || !found {
		return "", fmt.Errorf("data field not found in secret %s", secretName)
	}

	tokenBase64, ok := secretData[secretKey]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret %s", secretKey, secretName)
	}

	tokenBytes, err := base64.StdEncoding.DecodeString(tokenBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode token for key %s in secret %s: %w", secretKey, secretName, err)
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
