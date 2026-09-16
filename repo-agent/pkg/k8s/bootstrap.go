package k8s

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	SystemNamespace  = "repo-agent-system"
	GithubSecretName = "github-pat"
	GeminiSecretName = "gemini-vscode-tokens"
	ClaudeSecretName = "anthropic-api-key"
)

// BootstrapNamespace bootstraps the target namespace with necessary secrets.
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

func ignoreAlreadyExists(err error) error {
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// BindUserIAMToNamespace creates a RoleBinding in the namespace for the user's GCP IAM email,
// mapping it to the 'overseer-cli-user' ClusterRole.
func BindUserIAMToNamespace(ctx context.Context, clientset kubernetes.Interface, namespace, email string) error {
	log := klog.FromContext(ctx)
	log.Info("Binding GCP IAM user to namespace", "email", email, "namespace", namespace)

	rb, err := clientset.RbacV1().RoleBindings(namespace).Get(ctx, "overseer-cli-user-binding", v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Create new
			newRb := &rbacv1.RoleBinding{
				ObjectMeta: v1.ObjectMeta{
					Name:      "overseer-cli-user-binding",
					Namespace: namespace,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:     "User",
						Name:     email,
						APIGroup: "rbac.authorization.k8s.io",
					},
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					Name:     "overseer-cli-user",
					APIGroup: "rbac.authorization.k8s.io",
				},
			}
			_, err = clientset.RbacV1().RoleBindings(namespace).Create(ctx, newRb, v1.CreateOptions{})
			return err
		}
		return err
	}

	// If already exists, check if email needs update
	needsUpdate := true
	for _, s := range rb.Subjects {
		if s.Kind == "User" && s.Name == email {
			needsUpdate = false
			break
		}
	}

	if needsUpdate {
		log.Info("Updating existing RoleBinding subject email", "email", email, "namespace", namespace)
		rb.Subjects = []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     email,
				APIGroup: "rbac.authorization.k8s.io",
			},
		}
		_, err = clientset.RbacV1().RoleBindings(namespace).Update(ctx, rb, v1.UpdateOptions{})
		return err
	}

	return nil
}
