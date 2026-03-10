/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/api/v1alpha1"
)

// OverseerReconciler reconciles a Overseer object
type OverseerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// NameHash generates an FNV-1a hash from a string and returns
// it as a fixed-length hexadecimal string.
func NameHash(objectName string) string {
	h := fnv.New32a()
	h.Write([]byte(objectName))
	hashValue := h.Sum32()

	// Convert the uint32 to a hexadecimal string.
	// This results in an 8-character string (e.g., "a5b3c2d1").
	return fmt.Sprintf("%08x", hashValue)
}

func parseRepoURL(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo url: %s", repoURL)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

//+kubebuilder:rbac:groups=overseer.gemini.google.com,resources=overseers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=overseer.gemini.google.com,resources=overseers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=overseer.gemini.google.com,resources=overseers/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete

func (r *OverseerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	overseer := &overseerv1alpha1.Overseer{}
	if err := r.Get(ctx, req.NamespacedName, overseer); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	owner, repo, err := parseRepoURL(overseer.Spec.RepoURL)
	if err != nil {
		log.Error(err, "Failed to parse repo URL")
		return ctrl.Result{}, err
	}

	repoName := fmt.Sprintf("%s-%s", owner, repo)
	repoHash := NameHash(repoName)
	targetNamespace := fmt.Sprintf("overseer-repo-%s", repoHash)

	// Ensure namespace exists
	ns := &corev1.Namespace{}
	err = r.Get(ctx, types.NamespacedName{Name: targetNamespace}, ns)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating namespace for repo", "namespace", targetNamespace, "repo", repoName)
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: targetNamespace,
					Labels: map[string]string{
						"overseer.gemini.google.com/repo": repoName,
					},
				},
			}
			if err := r.Create(ctx, ns); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			return ctrl.Result{}, err
		}
	}

	// Ensure ServiceAccount exists in target namespace
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overseer",
			Namespace: targetNamespace,
		},
	}
	err = r.Get(ctx, types.NamespacedName{Name: sa.Name, Namespace: sa.Namespace}, &corev1.ServiceAccount{})
	if err != nil {
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, sa); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			return ctrl.Result{}, err
		}
	}

	// Ensure RoleBinding exists in target namespace
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overseer",
			Namespace: targetNamespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "overseer",
				Namespace: targetNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "overseer",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	err = r.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, &rbacv1.RoleBinding{})
	if err != nil {
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, rb); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			return ctrl.Result{}, err
		}
	}

	// Ensure secret is copied if it's in a different namespace
	// Since Overseer is cluster-scoped, we need to know which namespace the secret is in.
	// Actually, the Overseer CRD doesn't specify the namespace of the secret.
	// Let's assume it's in the same namespace as the controller or some predefined namespace?
	// The issue says "The overseer needs to be run in its own namespace (overseer-system)".
	// Maybe secrets are expected to be in overseer-system too.

	// For now, let's assume the secret is in 'overseer-system'.
	sourceSecretNamespace := "overseer-system"
	
	// Copy GitHub secret
	if err := r.copySecret(ctx, overseer.Spec.GithubSecretName, sourceSecretNamespace, targetNamespace); err != nil {
		log.Error(err, "Failed to copy GitHub secret")
		return ctrl.Result{}, err
	}

	// Copy Gemini API key secret if it exists
	// We might need to make this configurable in the future.
	apiKeySecretName := "gemini-api-key"
	if err := r.copySecret(ctx, apiKeySecretName, sourceSecretNamespace, targetNamespace); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to copy Gemini API key secret")
			// Not returning error here as it might be optional
		}
	}

	// Ensure sandbox is running
	if err := r.ensureSandbox(ctx, overseer, targetNamespace); err != nil {
		log.Error(err, "Failed to ensure sandbox")
		return ctrl.Result{}, err
	}

	// Update status
	overseer.Status.State = "Active"
	overseer.Status.Namespace = targetNamespace
	if err := r.Status().Update(ctx, overseer); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *OverseerReconciler) copySecret(ctx context.Context, name, srcNamespace, destNamespace string) error {
	if name == "" {
		return nil
	}
	srcSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: srcNamespace}, srcSecret)
	if err != nil {
		return err
	}

	destSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: name, Namespace: destNamespace}, destSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			destSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: destNamespace,
				},
				Data: srcSecret.Data,
				Type: srcSecret.Type,
			}
			return r.Create(ctx, destSecret)
		}
		return err
	}

	// Update if data is different?
	// For simplicity, let's just update for now.
	destSecret.Data = srcSecret.Data
	destSecret.Type = srcSecret.Type
	return r.Update(ctx, destSecret)
}

func (r *OverseerReconciler) ensureSandbox(ctx context.Context, overseer *overseerv1alpha1.Overseer, namespace string) error {
	log := log.FromContext(ctx)

	overseerName := fmt.Sprintf("overseer-%s", overseer.Name)

	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})

	err := r.Get(ctx, types.NamespacedName{Name: overseerName, Namespace: namespace}, sandbox)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating Overseer sandbox", "name", overseerName, "namespace", namespace)
			newSandbox := r.newOverseerSandbox(overseer, overseerName, namespace)
			return r.Create(ctx, newSandbox)
		}
		return err
	}

	return nil
}

func (r *OverseerReconciler) newOverseerSandbox(overseer *overseerv1alpha1.Overseer, name, namespace string) *unstructured.Unstructured {
	image := overseer.Spec.Image
	if image == "" {
		image = os.Getenv("OVERSEER_IMAGE")
	}

	apiKeySecretName := "gemini-api-key"
	githubSecretName := overseer.Spec.GithubSecretName

	env := []interface{}{
		map[string]interface{}{
			"name": "MANUAL_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     githubSecretName,
					"key":      "manual_pat",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "OAUTH_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     githubSecretName,
					"key":      "oauth_pat",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "GITHUB_USER_TOKEN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     githubSecretName,
					"key":      "pat",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "GEMINI_API_KEY",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     apiKeySecretName,
					"key":      "gemini",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name":  "REPO_URL",
			"value": overseer.Spec.RepoURL,
		},
		map[string]interface{}{
			"name":  "OVERSEER_NAME",
			"value": overseer.Name,
		},
		map[string]interface{}{
			"name": "NAMESPACE",
			"valueFrom": map[string]interface{}{
				"fieldRef": map[string]interface{}{
					"fieldPath": "metadata.namespace",
				},
			},
		},
		map[string]interface{}{
			"name":  "REPO_SANDBOX_IMAGE",
			"value": os.Getenv("REPO_SANDBOX_IMAGE"),
		},
		map[string]interface{}{
			"name":  "CONFIG_DIR_IMAGE",
			"value": os.Getenv("CONFIG_DIR_IMAGE"),
		},
	}

	if overseer.Spec.Chores != nil && overseer.Spec.Chores.Mode != "" {
		env = append(env, map[string]interface{}{
			"name":  "CHORES_MODE",
			"value": overseer.Spec.Chores.Mode,
		})
	}

	if overseer.Spec.Repo != nil && overseer.Spec.Repo.Mode != "" {
		env = append(env, map[string]interface{}{
			"name":  "REPO_MODE",
			"value": overseer.Spec.Repo.Mode,
		})
	}

	botSecretName := overseer.Spec.RobotAccount
	if botSecretName != "" {
		env = append(env, map[string]interface{}{
			"name": "GITHUB_BOT_LOGIN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     botSecretName,
					"key":      "userid",
					"optional": true,
				},
			},
		})
		env = append(env, map[string]interface{}{
			"name": "GITHUB_BOT_NAME",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     botSecretName,
					"key":      "name",
					"optional": true,
				},
			},
		})
		env = append(env, map[string]interface{}{
			"name": "GITHUB_BOT_EMAIL",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     botSecretName,
					"key":      "email",
					"optional": true,
				},
			},
		})
		env = append(env, map[string]interface{}{
			"name": "GITHUB_BOT_TOKEN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     botSecretName,
					"key":      "pat",
					"optional": true,
				},
			},
		})
		env = append(env, map[string]interface{}{
			"name": "GITHUB_BOT_OAUTH_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     botSecretName,
					"key":      "oauth_pat",
					"optional": true,
				},
			},
		})
		env = append(env, map[string]interface{}{
			"name": "GITHUB_BOT_MANUAL_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     botSecretName,
					"key":      "manual_pat",
					"optional": true,
				},
			},
		})
	}

	// Pod Template Spec
	podSpec := map[string]interface{}{
		"serviceAccountName": "overseer",
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "overseer",
				"image":   image,
				"command": []string{"/workspaces/run.sh"},
				"env":     env,
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"cpu":    "1000m",
						"memory": "1Gi",
					},
					"limits": map[string]interface{}{
						"cpu":    "2000m",
						"memory": "2Gi",
					},
				},
			},
		},
	}

	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"sandbox-type":                   "agent",
					"overseer.gemini.google.com/overseer": overseer.Name,
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"sandbox":      name,
							"sandbox-type": "agent",
						},
					},
					"spec": podSpec,
				},
			},
		},
	}

	return u
}

// SetupWithManager sets up the controller with the Manager.
func (r *OverseerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&overseerv1alpha1.Overseer{}).
		Complete(r)
}
