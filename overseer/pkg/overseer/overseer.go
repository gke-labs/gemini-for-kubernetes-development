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
	"path"
	"strings"

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

// collectorURL is the token-usage collector endpoint injected into overseer
// sandboxes; the factory binary reports per-task gemini token usage there.
// Overridable on the controller deployment; empty disables reporting.
func collectorURL() string {
	if v, ok := os.LookupEnv("COLLECTOR_URL"); ok {
		return v
	}
	return "http://token-usage.overseer-system.svc.cluster.local:8080"
}

// ReconcileOverseer ensures the Overseer sandbox is running for the given Overseer.
func ReconcileOverseer(ctx context.Context, c client.Client, o *overseerv1alpha1.Overseer) error {
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
			newSandbox := newOverseerSandboxFromOverseer(o, overseerName, namespace, hasTokenScript)
			if err := controllerutil.SetControllerReference(o, newSandbox, c.Scheme()); err != nil {
				return err
			}
			if err := c.Create(ctx, newSandbox); err != nil {
				return err
			}
			o.Status.OverseerStatus = "Active"
			return nil
		}
		return err
	}

	// If exists, compare spec and update if changed
	desiredSandbox := newOverseerSandboxFromOverseer(o, overseerName, namespace, hasTokenScript)
	existingSpec := sandbox.Object["spec"]
	desiredSpec := desiredSandbox.Object["spec"]

	existingJSON, _ := json.Marshal(existingSpec)
	desiredJSON, _ := json.Marshal(desiredSpec)

	if string(existingJSON) != string(desiredJSON) {
		log.Info("Updating Overseer sandbox spec", "name", overseerName, "namespace", namespace)
		sandbox.Object["spec"] = desiredSpec
		if err := c.Update(ctx, sandbox); err != nil {
			return fmt.Errorf("updating sandbox spec: %w", err)
		}
		// TODO: At this point the sandbox Pod may be stale (using old PodSpec). Ideally this should
		// implement a safe update which allows the sandbox to gracefully restart (e.g. draining its task queue).
	}

	o.Status.OverseerStatus = overseerv1alpha1.OverseerStatusActive
	return nil
}

// factoryRepo is this project's sandbox image, built and tagged with each
// commit's SHA alongside the overseer's (.github/workflows/build-images.yaml).
const factoryRepo = "ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang"

// factoryImage is the image the overseer's task sandboxes run: spec.image,
// pinned to the overseer's own build (the tag of overseerImage, the
// controller's OVERSEER_IMAGE) when it names this project's factory image
// at :latest or untagged, or names none. The factory in the sandboxes then
// is the one the overseer was built with: on :latest it moved ahead of a
// pinned overseer, and a breaking change between them (#1938 dropped
// revise:) failed every task. Another image, or a pinned tag, is left as
// given, and so is everything when the overseer itself is unpinned.
func factoryImage(specImage, overseerImage string) string {
	tag := imageTag(overseerImage)
	if tag == "" || tag == "latest" {
		return specImage
	}
	if specImage == "" {
		return factoryRepo + ":" + tag
	}
	if strings.Contains(specImage, "@") {
		return specImage
	}
	repo, specTag := specImage, ""
	if i := strings.LastIndex(specImage, ":"); i > strings.LastIndex(specImage, "/") {
		repo, specTag = specImage[:i], specImage[i+1:]
	}
	if repo == factoryRepo && (specTag == "" || specTag == "latest") {
		return factoryRepo + ":" + tag
	}
	return specImage
}

// imageTag is ref's tag, "" for none or a digest reference.
func imageTag(ref string) string {
	if strings.Contains(ref, "@") {
		return ""
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return ""
}

func newOverseerSandboxFromOverseer(o *overseerv1alpha1.Overseer, name, namespace string, hasTokenScript bool) *unstructured.Unstructured {
	image := os.Getenv("OVERSEER_IMAGE")

	secretName := "factory-user"
	if roleSpec, ok := o.Spec.Roles["watcher"]; ok && len(roleSpec.Users) > 0 && roleSpec.Users[0] != "" {
		secretName = fmt.Sprintf("user-%s", roleSpec.Users[0])
	}

	env := []interface{}{
		map[string]interface{}{
			"name": "GITHUB_TOKEN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     secretName,
					"key":      "GITHUB_TOKEN",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "GITHUB_LOGIN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     secretName,
					"key":      "GITHUB_LOGIN",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "GITHUB_EMAIL",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     secretName,
					"key":      "GITHUB_EMAIL",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "GEMINI_API_KEY",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     secretName,
					"key":      "GEMINI_API_KEY",
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
			"name":  "HOME",
			"value": "/workspaces/.home",
		},
		// factory's GitHub response cache, on the volume so that it
		// outlives run.sh's restart of factory watch every poll interval
		// and is shared by the factory processes it starts. Not under the
		// repository's clone: run.sh pushes the queue directory to the
		// repository, and the cache holds response bodies.
		map[string]interface{}{
			"name":  "FACTORY_GITHUB_CACHE_DIR",
			"value": "/workspaces/.cache/factory-github",
		},
		// The shared scan's snapshot, beside the cache for the same reason:
		// it only needs to outlive the restarts, which the volume does, and
		// in the queue directory it would be pushed to the repository's
		// state branch on every cycle (a megabyte on KCC).
		map[string]interface{}{
			"name":  "WATCH_SCAN_STATE",
			"value": "/workspaces/.cache/factory-watch/scan_state.json",
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
			"name":  "SANDBOX_CPU_REQUEST",
			"value": o.Spec.SandboxCPURequest,
		},
		map[string]interface{}{
			"name":  "SANDBOX_CPU_LIMIT",
			"value": o.Spec.SandboxCPULimit,
		},
		map[string]interface{}{
			"name":  "SANDBOX_MEMORY_REQUEST",
			"value": o.Spec.SandboxMemoryRequest,
		},
		map[string]interface{}{
			"name":  "SANDBOX_MEMORY_LIMIT",
			"value": o.Spec.SandboxMemoryLimit,
		},
		map[string]interface{}{
			"name":  "ALLOW_GEMINI_ORCHESTRATION",
			"value": fmt.Sprintf("%t", o.Spec.EnableGeminiOrchestrator),
		},
		map[string]interface{}{
			"name":  "COLLECTOR_URL",
			"value": collectorURL(),
		},
	}

	rolesJSON, _ := json.Marshal(o.Spec.Roles)
	env = append(env, map[string]interface{}{
		"name":  "FACTORY_ROLES",
		"value": string(rolesJSON),
	})

	if len(o.Spec.AllowlistedUsers) > 0 {
		// GitHub logins cannot contain commas, so a comma-separated list is
		// unambiguous; run.sh splits it into the factory config.
		env = append(env, map[string]interface{}{
			"name":  "ALLOWLISTED_USERS",
			"value": strings.Join(o.Spec.AllowlistedUsers, ","),
		})
	}

	if o.Spec.SessionURL != "" {
		env = append(env, map[string]interface{}{
			"name":  "SESSION_URL",
			"value": o.Spec.SessionURL,
		})
	}

	if o.Spec.Chores != nil && o.Spec.Chores.Mode != "" {
		env = append(env, map[string]interface{}{
			"name":  "CHORES_MODE",
			"value": o.Spec.Chores.Mode,
		})
	}

	if o.Spec.Repo != nil {
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

	if o.Spec.SandboxEvictionAge != "" {
		env = append(env, map[string]interface{}{
			"name":  "SANDBOX_EVICTION_AGE",
			"value": o.Spec.SandboxEvictionAge,
		})
	}
	if o.Spec.SandboxIdleTimeout != "" {
		env = append(env, map[string]interface{}{
			"name":  "SANDBOX_IDLE_TIMEOUT",
			"value": o.Spec.SandboxIdleTimeout,
		})
	}
	if o.Spec.PRInactivityTimeout != nil {
		env = append(env, map[string]interface{}{
			"name":  "PR_INACTIVITY_TIMEOUT",
			"value": o.Spec.PRInactivityTimeout.Duration.String(),
		})
	}
	if o.Spec.TaskTimeout != nil {
		env = append(env, map[string]interface{}{
			"name":  "TASK_TIMEOUT",
			"value": o.Spec.TaskTimeout.Duration.String(),
		})
	}
	if o.Spec.SharedScan {
		env = append(env, map[string]interface{}{
			"name":  "WATCH_SHARED_SCAN",
			"value": "true",
		})
	}
	env = append(env, rulesEnv(o.Spec.Rules)...)
	env = append(env, repoRecipeEnv(o.Spec.RepoRecipe, o.Spec.RepoURL)...)
	if o.Spec.RulesRecord {
		// Beside the scan's snapshot, on the volume: it outlives restarts
		// and stays out of the queue directory run.sh pushes.
		env = append(env, map[string]interface{}{
			"name":  "WATCH_RULES_RECORD",
			"value": "/workspaces/.cache/factory-watch/decisions.jsonl",
		})
	}
	if o.Spec.Graphs {
		env = append(env, map[string]interface{}{
			"name":  "WATCH_GRAPHS",
			"value": "true",
		})
	}
	if o.Spec.Retro.Schedule != "" {
		env = append(env, map[string]interface{}{
			"name":  "WATCH_RETRO_SCHEDULE",
			"value": o.Spec.Retro.Schedule,
		})
	}
	if o.Spec.GraphQLPRDetails {
		env = append(env, map[string]interface{}{
			"name":  "WATCH_GRAPHQL_PR_DETAILS",
			"value": "true",
		})
	}
	if o.Spec.MinNumber != nil {
		env = append(env, map[string]interface{}{
			"name":  "MIN_NUMBER",
			"value": fmt.Sprintf("%d", *o.Spec.MinNumber),
		})
	}
	if o.Spec.WorkspaceDiskSize != "" {
		env = append(env, map[string]interface{}{
			"name":  "WORKSPACE_DISK_SIZE",
			"value": o.Spec.WorkspaceDiskSize,
		})
	}
	if o.Spec.WorkspaceStorageClassName != "" {
		env = append(env, map[string]interface{}{
			"name":  "WORKSPACE_STORAGE_CLASS",
			"value": o.Spec.WorkspaceStorageClassName,
		})
	}
	if img := factoryImage(o.Spec.Image, image); img != "" {
		env = append(env, map[string]interface{}{
			"name":  "FACTORY_IMAGE",
			"value": img,
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

	if w := o.Spec.WarmWorkspace; w != nil {
		env = append(env, map[string]interface{}{
			"name":  "WARM_WORKSPACE_INTERVAL",
			"value": w.Interval.Duration.String(),
		})
		if w.Keep != nil {
			env = append(env, map[string]interface{}{
				"name":  "WARM_WORKSPACE_KEEP",
				"value": fmt.Sprintf("%d", *w.Keep),
			})
		}
		for _, e := range [][2]string{
			{"WARM_WORKSPACE_SCRIPT", w.Script},
			{"WARM_WORKSPACE_SCRIPT_URL", w.ScriptURL},
			{"WARM_WORKSPACE_SCRIPT_PATH", w.ScriptPath},
		} {
			if e[1] != "" {
				env = append(env, map[string]interface{}{"name": e[0], "value": e[1]})
			}
		}
	}

	ephemeralStorage := o.Spec.EphemeralStorage
	if ephemeralStorage == "" {
		ephemeralStorage = "10Gi"
	}

	diskSize := o.Spec.WorkspaceDiskSize
	if diskSize == "" {
		diskSize = "10Gi"
	}

	podSpec := map[string]interface{}{
		"serviceAccountName": "overseer",
		// run.sh's stop handler stops factory watch and pushes the queue
		// state before the pod goes (stopOverseer, STOP_WAIT 60s): the
		// default 30 seconds is too short for both.
		"terminationGracePeriodSeconds": int64(120),
		"containers": []interface{}{
			map[string]interface{}{
				"name":    "overseer",
				"image":   image,
				"command": []interface{}{"/app/bootstrap.sh"},
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
				"volumeMounts": []interface{}{
					map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
				},
			},
		},
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

	pvcSpec := map[string]interface{}{
		"accessModes": []interface{}{"ReadWriteOnce"},
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{
				"storage": diskSize,
			},
		},
	}
	if o.Spec.WorkspaceStorageClassName != "" {
		pvcSpec["storageClassName"] = o.Spec.WorkspaceStorageClassName
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
				"volumeClaimTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "workspaces-pvc",
						},
						"spec": pvcSpec,
					},
				},
			},
		},
	}

	return u
}

// rulesEnv is spec.rules for the watcher: WATCH_RULES_DISABLED, when set.
func rulesEnv(r *overseerv1alpha1.OverseerRules) []interface{} {
	if r == nil || len(r.Disabled) == 0 {
		return nil
	}
	return []interface{}{map[string]interface{}{"name": "WATCH_RULES_DISABLED", "value": strings.Join(r.Disabled, ",")}}
}

// repoRecipeEnv is spec.repoRecipe for the watcher and the factory
// commands it runs: FACTORY_REPO_RECIPE_MODE (augment or replace),
// FACTORY_REPO_RECIPE_COMMIT (a SHA, or latest) and FACTORY_REPO_RECIPE_DIR
// (the pod's clone of the repository, /workspaces/<repo>, which run.sh
// keeps, so the recipes are read from its git objects), when the mode is
// not off.
func repoRecipeEnv(r *overseerv1alpha1.OverseerRepoRecipe, repoURL string) []interface{} {
	if r == nil || r.Mode == "" || r.Mode == "off" {
		return nil
	}
	commit := r.GitCommit
	if commit == "" {
		commit = "latest"
	}
	return []interface{}{
		map[string]interface{}{"name": "FACTORY_REPO_RECIPE_MODE", "value": r.Mode},
		map[string]interface{}{"name": "FACTORY_REPO_RECIPE_COMMIT", "value": commit},
		map[string]interface{}{"name": "FACTORY_REPO_RECIPE_DIR", "value": "/workspaces/" + path.Base(strings.TrimSuffix(strings.TrimSuffix(repoURL, "/"), ".git"))},
	}
}
