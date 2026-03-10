package overseer

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	pkg_github "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/github"
)

// Reconcile ensures the Overseer sandbox is running for the given RepoWatch.
func Reconcile(ctx context.Context, c client.Client, repoWatch *reviewv1alpha1.RepoWatch, user *pkg_github.User, repoSandboxImage, configDirImage string) error {
	log := log.FromContext(ctx)

	if repoWatch.Spec.Overseer == nil || !repoWatch.Spec.Overseer.Enabled {
		return nil
	}

	overseerName := fmt.Sprintf("overseer-%s", repoWatch.Name)

	// Define the sandbox object
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})

	err := c.Get(ctx, types.NamespacedName{Name: overseerName, Namespace: repoWatch.Namespace}, sandbox)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create
			log.Info("Creating Overseer sandbox", "name", overseerName)
			newSandbox := newOverseerSandbox(repoWatch, overseerName, user, repoSandboxImage, configDirImage)
			if err := controllerutil.SetControllerReference(repoWatch, newSandbox, c.Scheme()); err != nil {
				return err
			}
			return c.Create(ctx, newSandbox)
		}
		return err
	}

	repoWatch.Status.OverseerStatus = "Active"
	return nil
}

func newOverseerSandbox(repoWatch *reviewv1alpha1.RepoWatch, name string, user *pkg_github.User, repoSandboxImage, configDirImage string) *unstructured.Unstructured {
	// Construct the unstructured Sandbox

	image := repoWatch.Spec.Overseer.Image
	if image == "" {
		image = os.Getenv("OVERSEER_IMAGE")
	}

	apiKeySecretName := "gemini-api-key"
	if repoWatch.Spec.Review.LLM.APIKeySecretRef != "" {
		apiKeySecretName = repoWatch.Spec.Review.LLM.APIKeySecretRef
	} else if repoWatch.Spec.Issue != nil && repoWatch.Spec.Issue.LLM.APIKeySecretRef != "" {
		apiKeySecretName = repoWatch.Spec.Issue.LLM.APIKeySecretRef
	}

	githubSecretName := repoWatch.Spec.GithubSecretName

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
			"value": repoWatch.Spec.RepoURL,
		},
		map[string]interface{}{
			"name":  "REPOWATCH_NAME",
			"value": repoWatch.Name,
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
			"value": repoSandboxImage,
		},
		map[string]interface{}{
			"name":  "CONFIG_DIR_IMAGE",
			"value": configDirImage,
		},
	}

	if repoWatch.Spec.Overseer != nil && repoWatch.Spec.Overseer.Chores != nil && repoWatch.Spec.Overseer.Chores.Mode != "" {
		env = append(env, map[string]interface{}{
			"name":  "CHORES_MODE",
			"value": repoWatch.Spec.Overseer.Chores.Mode,
		})
	}

	if repoWatch.Spec.Overseer != nil && repoWatch.Spec.Overseer.Repo != nil && repoWatch.Spec.Overseer.Repo.Mode != "" {
		env = append(env, map[string]interface{}{
			"name":  "REPO_MODE",
			"value": repoWatch.Spec.Overseer.Repo.Mode,
		})
	}

	if user != nil {
		env = append(env, map[string]interface{}{
			"name":  "GITHUB_USER_ID",
			"value": user.UserID,
		})
		env = append(env, map[string]interface{}{
			"name":  "GITHUB_USER_NAME",
			"value": user.Name,
		})
		env = append(env, map[string]interface{}{
			"name":  "GITHUB_USER_EMAIL",
			"value": user.Email,
		})
	}

	botSecretName := repoWatch.Spec.Overseer.RobotAccount
	if botSecretName == "" && repoWatch.Spec.Review.RobotAccount != "" {
		botSecretName = repoWatch.Spec.Review.RobotAccount
	}
	if botSecretName == "" && repoWatch.Spec.Issue != nil && repoWatch.Spec.Issue.RobotAccount != "" {
		botSecretName = repoWatch.Spec.Issue.RobotAccount
	}

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
				"volumeMounts": []interface{}{
					// We might need to mount secrets if envFrom isn't enough or for other tools
					// For now, env vars should suffice for GITHUB_TOKEN and GEMINI_API_KEY
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
				"namespace": repoWatch.Namespace,
				"labels": map[string]interface{}{
					"sandbox-type":                       "agent",
					"review.gemini.google.com/repowatch": repoWatch.Name,
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
