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

package overseer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	overseerv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/overseer/pkg/api/v1alpha1"
)

// ReconcileOverseer ensures the Overseer sandbox is running for the given Overseer.
func ReconcileOverseer(ctx context.Context, c client.Client, o *overseerv1alpha1.Overseer, repoSandboxImage, configDirImage string) error {
	log := log.FromContext(ctx)

	overseerName := fmt.Sprintf("overseer-%s", o.Name)
	if len(overseerName) > 63 {
		overseerName = overseerName[:63]
	}
	namespace := fmt.Sprintf("overseer-%s", o.Name)
	if len(namespace) > 63 {
		namespace = namespace[:63]
	}

	// Define the sandbox object
	sandbox := &unstructured.Unstructured{}
	sandbox.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agents.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "Sandbox",
	})

	hasTokenScript := false
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: "tokenscript", Namespace: namespace}, secret); err == nil {
		hasTokenScript = true
	} else if !errors.IsNotFound(err) {
		return err
	}

	err := c.Get(ctx, types.NamespacedName{Name: overseerName, Namespace: namespace}, sandbox)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create
			log.Info("Creating Overseer sandbox", "name", overseerName, "namespace", namespace)
			newSandbox := newOverseerSandboxFromOverseer(o, overseerName, namespace, repoSandboxImage, configDirImage, hasTokenScript)
			if err := controllerutil.SetControllerReference(o, newSandbox, c.Scheme()); err != nil {
				return err
			}
			return c.Create(ctx, newSandbox)
		}
		return err
	}

	o.Status.OverseerStatus = "Active"
	return nil
}

func newOverseerSandboxFromOverseer(o *overseerv1alpha1.Overseer, name, namespace, repoSandboxImage, configDirImage string, hasTokenScript bool) *unstructured.Unstructured {
	image := os.Getenv("OVERSEER_IMAGE")

	apiKeySecretName := o.Spec.GeminiAPIKeySecretName
	if apiKeySecretName == "" {
		apiKeySecretName = "gemini-api-key"
	}

	githubSecretName := o.Spec.RobotAccount

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
			"name":  "GITHUB_API_URL",
			"value": "http://github-portal.overseer-system.svc.cluster.local/",
		},
		map[string]interface{}{
			"name":  "GEMINI_CLI_TRUST_WORKSPACE",
			"value": "true",
		},
		map[string]interface{}{
			"name":  "REPO_URL",
			"value": o.Spec.RepoURL,
		},
		map[string]interface{}{
			"name":  "OVERSEER_NAME",
			"value": o.Name,
		},
		map[string]interface{}{
			"name":  "NAMESPACE",
			"value": namespace,
		},
		map[string]interface{}{
			"name":  "REPO_SANDBOX_IMAGE",
			"value": repoSandboxImage,
		},
		map[string]interface{}{
			"name":  "CONFIG_DIR_IMAGE",
			"value": configDirImage,
		},
		map[string]interface{}{
			"name":  "POLL_INTERVAL",
			"value": o.Spec.PollInterval,
		},
		map[string]interface{}{
			"name":  "EPHEMERAL_STORAGE",
			"value": o.Spec.EphemeralStorage,
		},
		map[string]interface{}{
			"name":  "ALLOW_GEMINI_ORCHESTRATION",
			"value": fmt.Sprintf("%t", o.Spec.EnableGeminiOrchestrator),
		},
	}

	if o.Spec.Chores != nil && o.Spec.Chores.Mode != "" {
		env = append(env, map[string]interface{}{
			"name":  "CHORES_MODE",
			"value": o.Spec.Chores.Mode,
		})
	}

	if o.Spec.Repo != nil {
		if o.Spec.Repo.ReviewMode != "" {
			env = append(env, map[string]interface{}{
				"name":  "REVIEW_MODE",
				"value": o.Spec.Repo.ReviewMode,
			})
		}
		if o.Spec.Repo.PRMode != "" {
			env = append(env, map[string]interface{}{
				"name":  "PR_MODE",
				"value": o.Spec.Repo.PRMode,
			})
		}
		if o.Spec.Repo.IssueMode != "" {
			env = append(env, map[string]interface{}{
				"name":  "ISSUE_MODE",
				"value": o.Spec.Repo.IssueMode,
			})
		}
	}

	if o.Spec.MaxActiveReviews != nil {
		env = append(env, map[string]interface{}{
			"name":  "MAX_ACTIVE_REVIEWS",
			"value": fmt.Sprintf("%d", *o.Spec.MaxActiveReviews),
		})
	}
	if o.Spec.MaxActiveIssues != nil {
		env = append(env, map[string]interface{}{
			"name":  "MAX_ACTIVE_ISSUES",
			"value": fmt.Sprintf("%d", *o.Spec.MaxActiveIssues),
		})
	}
	if o.Spec.WorkspaceDiskSize != "" {
		env = append(env, map[string]interface{}{
			"name":  "WORKSPACE_DISK_SIZE",
			"value": o.Spec.WorkspaceDiskSize,
		})
	}
	if o.Spec.Image != "" {
		env = append(env, map[string]interface{}{
			"name":  "FACTORY_IMAGE",
			"value": o.Spec.Image,
		})
	}
	if len(o.Spec.Secrets) > 0 {
		secretsJSON, err := json.Marshal(o.Spec.Secrets)
		if err == nil {
			env = append(env, map[string]interface{}{
				"name":  "FACTORY_SECRETS",
				"value": string(secretsJSON),
			})
		}
	}
	if len(o.Spec.Env) > 0 {
		envJSON, err := json.Marshal(o.Spec.Env)
		if err == nil {
			env = append(env, map[string]interface{}{
				"name":  "FACTORY_ENV",
				"value": string(envJSON),
			})
		}
	}

	botSecretName := o.Spec.RobotAccount
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

	ephemeralStorage := o.Spec.EphemeralStorage
	if ephemeralStorage == "" {
		ephemeralStorage = "10Gi"
	}

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
						"cpu":               "1000m",
						"memory":            "1Gi",
						"ephemeral-storage": ephemeralStorage,
					},
					"limits": map[string]interface{}{
						"cpu":               "2000m",
						"memory":            "2Gi",
						"ephemeral-storage": ephemeralStorage,
					},
				},
			},
		},
	}

	if o.Spec.ConfigdirRef != "" {
		podSpec["initContainers"] = []interface{}{
			map[string]interface{}{
				"name":  "gemini-configs",
				"image": configDirImage,
				"args":  []interface{}{"--directory", "/configdir", "--namespace", namespace, "--name", o.Spec.ConfigdirRef, "--ignore-not-found-error"},
				"volumeMounts": []interface{}{
					map[string]interface{}{"name": "configdir-vol", "mountPath": "/configdir"},
				},
			},
		}

		podSpec["volumes"] = []interface{}{
			map[string]interface{}{
				"name":     "configdir-vol",
				"emptyDir": map[string]interface{}{},
			},
		}

		// Also mount configdir-vol to the main container
		containers := podSpec["containers"].([]interface{})
		mainContainer := containers[0].(map[string]interface{})
		mainContainer["volumeMounts"] = []interface{}{
			map[string]interface{}{"name": "configdir-vol", "mountPath": "/configdir"},
		}
	}

	if hasTokenScript {
		// Define the volume
		volume := map[string]interface{}{
			"name": "tokenscript-vol",
			"secret": map[string]interface{}{
				"secretName":  "tokenscript",
				"defaultMode": int32(0755),
			},
		}

		var volumes []interface{}
		if v, ok := podSpec["volumes"]; ok {
			volumes = v.([]interface{})
		}
		volumes = append(volumes, volume)
		podSpec["volumes"] = volumes

		// Define the volumeMount
		volumeMount := map[string]interface{}{
			"name":      "tokenscript-vol",
			"mountPath": "/etc/tokenscript",
		}

		containers := podSpec["containers"].([]interface{})
		mainContainer := containers[0].(map[string]interface{})
		var volumeMounts []interface{}
		if vm, ok := mainContainer["volumeMounts"]; ok {
			volumeMounts = vm.([]interface{})
		}
		volumeMounts = append(volumeMounts, volumeMount)
		mainContainer["volumeMounts"] = volumeMounts

		// Add an environment variable so overseer-cli knows where it is
		envList := mainContainer["env"].([]interface{})
		envList = append(envList, map[string]interface{}{
			"name":  "TOKENSCRIPT_DIR",
			"value": "/etc/tokenscript",
		})
		mainContainer["env"] = envList
	}

	// Inject CA cert volume
	{
		volume := map[string]interface{}{
			"name": "ca-cert",
			"secret": map[string]interface{}{
				"secretName": "github-portal-ca",
				"optional":   true,
			},
		}

		var volumes []interface{}
		if v, ok := podSpec["volumes"]; ok {
			volumes = v.([]interface{})
		}
		volumes = append(volumes, volume)
		podSpec["volumes"] = volumes

		volumeMount := map[string]interface{}{
			"name":      "ca-cert",
			"mountPath": "/etc/github-portal/ca",
			"readOnly":  true,
		}

		containers := podSpec["containers"].([]interface{})
		mainContainer := containers[0].(map[string]interface{})
		var volumeMounts []interface{}
		if vm, ok := mainContainer["volumeMounts"]; ok {
			volumeMounts = vm.([]interface{})
		}
		volumeMounts = append(volumeMounts, volumeMount)
		mainContainer["volumeMounts"] = volumeMounts
	}

	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"sandbox-type":                        "agent",
					"overseer.gemini.google.com/overseer": o.Name,
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
