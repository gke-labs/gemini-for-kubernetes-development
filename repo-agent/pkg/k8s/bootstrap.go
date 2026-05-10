package k8s

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const (
	SystemNamespace  = "repo-agent-system"
	GithubSecretName = "github-pat"
	GeminiSecretName = "gemini-vscode-tokens"
	ClaudeSecretName = "anthropic-api-key"
	DevContainerCM   = "devcontainer-json"
)

// BootstrapNamespace bootstraps the target namespace with necessary secrets and service accounts.
// Multi-Tenancy Model: Each user gets a dedicated Kubernetes namespace.
// Upon login (which triggers this bootstrap), we copy essential system-level secrets (GitHub tokens, LLM API keys)
// from the 'repo-agent-system' namespace to the user's private namespace to enable isolated sandboxes.
func BootstrapNamespace(ctx context.Context, clientset kubernetes.Interface, targetNS string) error {
	log := klog.FromContext(ctx)
	_, err := clientset.CoreV1().Namespaces().Get(ctx, targetNS, v1.GetOptions{})
	if errors.IsNotFound(err) {
		log.Info("Creating namespace", "name", targetNS)
		ns := &corev1.Namespace{
			ObjectMeta: v1.ObjectMeta{
				Name:   targetNS,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "repo-agent", "review.gemini.google.com/tenant": targetNS},
			},
		}
		if _, err := clientset.CoreV1().Namespaces().Create(ctx, ns, v1.CreateOptions{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Copy default secrets/configs from system namespace if they don't exist in user namespace
	if err := CopySecret(ctx, clientset, SystemNamespace, GithubSecretName, targetNS, GithubSecretName); err != nil {
		log.Info("Warning: failed to copy default github secret", "err", err)
	}
	if err := CopySecret(ctx, clientset, "overseer-system", "github-portal-ca", targetNS, "github-portal-ca"); err != nil {
		log.Info("Warning: failed to copy github-portal-ca secret", "err", err)
	}
	if err := CopySecret(ctx, clientset, SystemNamespace, GeminiSecretName, targetNS, GeminiSecretName); err != nil {
		log.Info("Warning: failed to copy default gemini secret", "err", err)
	}
	if err := CopySecret(ctx, clientset, SystemNamespace, ClaudeSecretName, targetNS, ClaudeSecretName); err != nil {
		log.Info("Warning: failed to copy default claude secret", "err", err)
	}
	if err := CopyConfigMap(ctx, clientset, SystemNamespace, DevContainerCM, targetNS, DevContainerCM); err != nil {
		log.Info("Debug: failed to copy configmap", "name", DevContainerCM, "err", err)
	}

	if err := SetupServiceAccounts(ctx, clientset, targetNS); err != nil {
		log.Info("Warning: failed to setup service accounts", "err", err)
	}

	return nil
}

// CopySecret copies a secret from source namespace to destination namespace.
func CopySecret(ctx context.Context, clientset kubernetes.Interface, srcNS, srcName, dstNS, dstName string) error {
	log := klog.FromContext(ctx)
	src, err := clientset.CoreV1().Secrets(srcNS).Get(ctx, srcName, v1.GetOptions{})
	if err != nil {
		log.Info("Error reading secret", "namespace", srcNS, "name", srcName, "err", err)
		return err
	}
	dst := &corev1.Secret{ObjectMeta: v1.ObjectMeta{Name: dstName, Namespace: dstNS}, Data: src.Data, Type: src.Type}
	_, err = clientset.CoreV1().Secrets(dstNS).Create(ctx, dst, v1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// CopyConfigMap copies a configmap from source namespace to destination namespace.
func CopyConfigMap(ctx context.Context, clientset kubernetes.Interface, srcNS, srcName, dstNS, dstName string) error {
	src, err := clientset.CoreV1().ConfigMaps(srcNS).Get(ctx, srcName, v1.GetOptions{})
	if err != nil {
		return err
	}
	dst := &corev1.ConfigMap{ObjectMeta: v1.ObjectMeta{Name: dstName, Namespace: dstNS}, Data: src.Data, BinaryData: src.BinaryData}
	_, err = clientset.CoreV1().ConfigMaps(dstNS).Create(ctx, dst, v1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// SetupServiceAccounts sets up the necessary service accounts and role bindings in the namespace.
func SetupServiceAccounts(ctx context.Context, clientset kubernetes.Interface, ns string) error {
	log := klog.FromContext(ctx)
	// --- Review Sandbox ---
	saReview := &corev1.ServiceAccount{ObjectMeta: v1.ObjectMeta{Name: "review-sandbox", Namespace: ns}}
	_, err := clientset.CoreV1().ServiceAccounts(ns).Create(ctx, saReview, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Bind to review-sandbox cluster role (base permissions)
	rbReview := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "review-sandbox-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "review-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "review-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = clientset.RbacV1().RoleBindings(ns).Create(ctx, rbReview, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Add to review-sandbox cluster role binding (to match make apply-common-for-examples)
	if err := ensureClusterRoleBindingSubject(ctx, clientset, "review-sandbox", rbacv1.Subject{Kind: "ServiceAccount", Name: "review-sandbox", Namespace: ns}); err != nil {
		log.Info("Warning: failed to update review-sandbox cluster role binding", "err", err)
	}

	// Bind to configdir-controller cluster role (needed for init container)
	rbReviewConfigDir := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "review-sandbox-configdir-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "review-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "configdir-controller", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = clientset.RbacV1().RoleBindings(ns).Create(ctx, rbReviewConfigDir, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// --- Issue Sandbox ---
	saIssue := &corev1.ServiceAccount{ObjectMeta: v1.ObjectMeta{Name: "issue-sandbox", Namespace: ns}}
	_, err = clientset.CoreV1().ServiceAccounts(ns).Create(ctx, saIssue, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Bind to issue-sandbox cluster role (base permissions)
	rbIssue := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "issue-sandbox-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "issue-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "issue-sandbox", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = clientset.RbacV1().RoleBindings(ns).Create(ctx, rbIssue, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	// Add to issue-sandbox cluster role binding (to match make apply-common-for-examples)
	if err := ensureClusterRoleBindingSubject(ctx, clientset, "issue-sandbox", rbacv1.Subject{Kind: "ServiceAccount", Name: "issue-sandbox", Namespace: ns}); err != nil {
		log.Info("Warning: failed to update issue-sandbox cluster role binding", "err", err)
	}

	// Bind to configdir-controller cluster role (needed for init container)
	rbIssueConfigDir := &rbacv1.RoleBinding{
		ObjectMeta: v1.ObjectMeta{Name: "issue-sandbox-configdir-binding", Namespace: ns},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "issue-sandbox", Namespace: ns}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "configdir-controller", APIGroup: "rbac.authorization.k8s.io"},
	}
	_, err = clientset.RbacV1().RoleBindings(ns).Create(ctx, rbIssueConfigDir, v1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

func ensureClusterRoleBindingSubject(ctx context.Context, clientset kubernetes.Interface, bindingName string, subject rbacv1.Subject) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, bindingName, v1.GetOptions{})
		if err != nil {
			return err
		}
		for _, s := range binding.Subjects {
			if s.Kind == subject.Kind && s.Name == subject.Name && s.Namespace == subject.Namespace {
				return nil // Already exists
			}
		}
		binding.Subjects = append(binding.Subjects, subject)
		_, err = clientset.RbacV1().ClusterRoleBindings().Update(ctx, binding, v1.UpdateOptions{})
		return err
	})
}

func ignoreAlreadyExists(err error) error {
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
