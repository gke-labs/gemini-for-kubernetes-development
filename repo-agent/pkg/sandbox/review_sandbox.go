package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ReviewSandboxOptions holds options for creating a ReviewSandbox.
type ReviewSandboxOptions struct {
	DevSandboxOptions

	PRNumber   int
	PRTitle    string
	PRHTMLURL  string
	PRDiffURL  string
	PRCloneURL string

	RepoName string

	MaxReviewFiles    int
	IgnoreFiles       []string
	SeverityThreshold string
	LLMExtensions     []reviewv1alpha1.Extension
	WorkspaceDiskSize string

	SkipDevcPrefix bool
}

// NewReviewSandbox creates a new Sandbox (unstructured) and Service object for PR reviews.
func NewReviewSandbox(opt ReviewSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	if opt.RepoSandboxImage == "" {
		opt.RepoSandboxImage = os.Getenv("REPO_SANDBOX_IMAGE")
	}
	if opt.ConfigDirImage == "" {
		opt.ConfigDirImage = os.Getenv("CONFIGDIR_CLI_IMAGE")
	}
	if opt.OverseerName == "" {
		opt.OverseerName = os.Getenv("OVERSEER_NAME")
	}

	name := opt.Name
	sandboxName := name

	labels := make(map[string]interface{})
	for k, v := range opt.Labels {
		labels[k] = v
	}
	// Default type to review if not set
	if _, ok := labels["sandbox-type"]; !ok {
		labels["sandbox-type"] = "review"
	}
	if _, ok := labels["sandbox.gemini.google.com/type"]; !ok {
		labels["sandbox.gemini.google.com/type"] = "review"
	}

	annotations := make(map[string]interface{})
	for k, v := range opt.Annotations {
		annotations[k] = v
	}
	annotations["agentState"] = "provisioning"
	annotations["reviewState"] = ""
	annotations["pr"] = fmt.Sprintf("%d", opt.PRNumber)
	annotations["title"] = opt.PRTitle
	annotations["repo"] = opt.RepoName
	annotations["htmlURL"] = opt.PRHTMLURL
	annotations["diffURL"] = opt.PRDiffURL
	annotations["cloneURL"] = opt.PRCloneURL

	image := opt.Image
	if image == "" {
		image = opt.RepoSandboxImage
	}
	command := []interface{}{}
	if opt.Image != "" {
		command = []interface{}{RepoSandboxBinary, "review-daemon"}
	}

	userName := opt.UserName
	if userName == "" {
		userName = opt.UserLogin
	}
	if userName == "" {
		userName = "Unknown User"
	}

	env := []interface{}{
		map[string]interface{}{"name": "NAMESPACE", "value": opt.Namespace},
		map[string]interface{}{"name": "NAME", "value": sandboxName},
		map[string]interface{}{"name": "REPO", "value": opt.RepoName},
		map[string]interface{}{"name": "PRID", "value": fmt.Sprintf("%d", opt.PRNumber)},
		map[string]interface{}{"name": "MAX_REVIEW_FILES", "value": strconv.Itoa(opt.MaxReviewFiles)},
		map[string]interface{}{"name": "IGNORE_FILES", "value": strings.Join(opt.IgnoreFiles, ",")},
		map[string]interface{}{"name": "SEVERITY_THRESHOLD", "value": opt.SeverityThreshold},
		map[string]interface{}{"name": "AGENT_NAME", "value": opt.LLMProvider},
		map[string]interface{}{"name": "GITHUB_USER_LOGIN", "value": opt.UserLogin},
		map[string]interface{}{"name": "GITHUB_USER_NAME", "value": userName},
		map[string]interface{}{"name": "GITHUB_USER_EMAIL", "value": opt.UserEmail},
		map[string]interface{}{"name": "GIT_AUTHOR_NAME", "value": userName},
		map[string]interface{}{"name": "GIT_AUTHOR_EMAIL", "value": opt.UserEmail},
		map[string]interface{}{"name": "GITHUB_BOT_LOGIN", "value": opt.BotLogin},
		map[string]interface{}{"name": "GITHUB_BOT_NAME", "value": opt.BotName},
		map[string]interface{}{"name": "GITHUB_BOT_EMAIL", "value": opt.BotEmail},
		map[string]interface{}{"name": "GEMINI_CLI_TRUST_WORKSPACE", "value": "true"},
	}

	if len(opt.LLMExtensions) > 0 {
		exts, _ := json.Marshal(opt.LLMExtensions)
		env = append(env, map[string]interface{}{
			"name":  "AGENT_LLM_EXTENSIONS",
			"value": string(exts),
		})
	} else {
		env = append(env, map[string]interface{}{
			"name":  "AGENT_LLM_EXTENSIONS",
			"value": "",
		})
	}

	env = append(env,
		map[string]interface{}{"name": "GIT_CLONE_URL", "value": opt.PRCloneURL},
		map[string]interface{}{"name": "ENVBUILDER_GIT_URL", "value": opt.PRCloneURL},
		map[string]interface{}{"name": "GIT_DIFF_URL", "value": opt.PRDiffURL},
		map[string]interface{}{"name": "GIT_HTML_URL", "value": opt.PRHTMLURL},
		map[string]interface{}{
			"name": "GITHUB_TOKEN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     opt.GithubSecretName,
					"key":      "pat",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "MANUAL_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     opt.GithubSecretName,
					"key":      "manual_pat",
					"optional": true,
				},
			},
		},
		map[string]interface{}{
			"name": "OAUTH_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name":     opt.GithubSecretName,
					"key":      "oauth_pat",
					"optional": true,
				},
			},
		},
	)

	for _, e := range opt.Env {
		env = append(env, map[string]interface{}{
			"name":  e.Name,
			"value": e.Value,
		})
	}
	env = append(env, buildLLMEnvVars(opt.DevSandboxOptions)...)

	env = append(env,
		map[string]interface{}{"name": "ENVBUILDER_CACHE_REPO", "value": "registry.repo-agent-system.svc.cluster.local:5000/envbuilder-cache"},
		map[string]interface{}{"name": "ENVBUILDER_DEVCONTAINER_DIR", "value": "/"},
		map[string]interface{}{"name": "ENVBUILDER_GIT_CLONE_SINGLE_BRANCH", "value": "true"},
		map[string]interface{}{"name": "ENVBUILDER_INIT_SCRIPT", "value": RepoSandboxBinary + " review-daemon"},
		map[string]interface{}{"name": "ENVBUILDER_IGNORE_PATHS", "value": "/var/run,/product_uuid,/product_name,/tokens,/repo-agent/,/etc/github-portal/ca"},
		map[string]interface{}{"name": "GOCACHE", "value": GoCachePath},
		map[string]interface{}{"name": "GOMODCACHE", "value": GoModCachePath},
		map[string]interface{}{"name": "TMPDIR", "value": TmpDirPath},
		map[string]interface{}{"name": "GOTMPDIR", "value": TmpDirPath},
		map[string]interface{}{"name": "SSL_CERT_FILE", "value": "/opt/repo-agent/ca/tls.crt"},
	)

	if opt.DisableGitHubProxy {
		env = append(env, map[string]interface{}{"name": "DISABLE_GITHUB_PROXY", "value": "true"})
	}

	workspaceDiskSize := opt.WorkspaceDiskSize
	if workspaceDiskSize == "" {
		workspaceDiskSize = "10Gi"
	}

	volumeMounts := []interface{}{
		map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
		map[string]interface{}{"name": "agent-bin", "mountPath": "/opt/repo-agent"},
		map[string]interface{}{"name": "ca-cert", "mountPath": "/etc/github-portal/ca", "readOnly": true},
	}
	if opt.LLMAPIKey == "" {
		volumeMounts = append(volumeMounts, map[string]interface{}{"name": "tokens-secret", "mountPath": "/tokens", "readOnly": true})
	}
	if opt.DevcontainerConfigRef != "" {
		volumeMounts = append(volumeMounts, map[string]interface{}{
			"name":      "devcontainer-config",
			"mountPath": "/devcontainer.json",
			"subPath":   "devcontainer.json",
		})
	}

	for _, secret := range opt.Secrets {
		volumeMounts = append(volumeMounts, map[string]interface{}{
			"name":      secret.Name + "-vol",
			"mountPath": secret.MountPath,
		})
	}

	volumes := []interface{}{
		map[string]interface{}{"name": "agent-bin", "emptyDir": map[string]interface{}{}},
		map[string]interface{}{
			"name": "ca-cert",
			"secret": map[string]interface{}{
				"secretName": "github-portal-ca",
				"optional":   true,
			},
		},
	}
	if opt.LLMAPIKey == "" {
		volumes = append(volumes, map[string]interface{}{
			"name": "tokens-secret",
			"projected": map[string]interface{}{
				"sources": buildLLMVolumeSources(opt.DevSandboxOptions),
			},
		})
	}
	if opt.DevcontainerConfigRef != "" {
		volumes = append(volumes, map[string]interface{}{
			"name": "devcontainer-config",
			"configMap": map[string]interface{}{
				"name": opt.DevcontainerConfigRef,
			},
		})
	}

	for _, secret := range opt.Secrets {
		volumes = append(volumes, map[string]interface{}{
			"name": secret.Name + "-vol",
			"secret": map[string]interface{}{
				"secretName": secret.Name,
			},
		})
	}

	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":        sandboxName,
				"namespace":   opt.Namespace,
				"labels":      labels,
				"annotations": annotations,
			},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"sandbox": sandboxName,
						},
					},
					"spec": map[string]interface{}{
						"serviceAccountName": opt.ServiceAccountName,
						"runtimeClassName": func() interface{} {
							if opt.DindSupport == DindSupportGvisor {
								return "gvisor"
							}
							return nil
						}(),
						"initContainers": func() []interface{} {
							containers := []interface{}{}
							if opt.LLMConfigdirRef != "" {
								containers = append(containers, map[string]interface{}{
									"name":  "gemini-configs",
									"image": opt.ConfigDirImage,
									"args":  []interface{}{"--directory", "/workspaces", "--namespace", opt.Namespace, "--name", opt.LLMConfigdirRef, "--ignore-not-found-error"},
									"volumeMounts": []interface{}{
										map[string]interface{}{
											"name":      "workspaces-pvc",
											"mountPath": "/workspaces",
										},
									},
								})
							}
							containers = append(containers, map[string]interface{}{
								"name":    "inject-agent",
								"image":   opt.RepoSandboxImage,
								"command": []interface{}{"/repo-agent/repo-sandbox", "inject", "--path", "/opt/repo-agent"},
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "agent-bin",
										"mountPath": "/opt/repo-agent",
									},
									map[string]interface{}{
										"name":      "ca-cert",
										"mountPath": "/etc/github-portal/ca",
										"readOnly":  true,
									},
								},
							})
							return containers
						}(),
						"containers": []interface{}{
							map[string]interface{}{
								"name":    "sandbox",
								"image":   image,
								"command": command,
								"resources": map[string]interface{}{
									"limits": map[string]interface{}{
										"ephemeral-storage": "6Gi",
									},
									"requests": map[string]interface{}{
										"ephemeral-storage": "6Gi",
									},
								},
								"env":          env,
								"volumeMounts": volumeMounts,
								"ports": []interface{}{
									map[string]interface{}{"containerPort": int64(13337)},
									map[string]interface{}{"containerPort": int64(13339)},
								},
							},
						},
						"volumes": volumes,
					},
				},
				"volumeClaimTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{"name": "workspaces-pvc"},
						"spec": map[string]interface{}{
							"accessModes": []interface{}{"ReadWriteOnce"},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"storage": workspaceDiskSize,
								},
							},
						},
					},
				},
			},
		},
	}

	serviceName := fmt.Sprintf("%s-lb", sandboxName)
	service := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":      serviceName,
				"namespace": opt.Namespace,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"sandbox": sandboxName,
				},
				"ports": []interface{}{
					map[string]interface{}{
						"name":        "code-server",
						"protocol":    "TCP",
						"port":        int64(13338),
						"targetPort":  int64(13337),
						"appProtocol": "kubernetes.io/ws",
					},
					map[string]interface{}{
						"name":       "agent-server",
						"protocol":   "TCP",
						"port":       int64(13339),
						"targetPort": int64(13339),
					},
				},
			},
		},
	}

	// We return corev1.Service matching dev/agent
	var svc corev1.Service
	b, err := service.MarshalJSON()
	if err == nil {
		_ = json.Unmarshal(b, &svc)
	}

	return sandbox, &svc
}
