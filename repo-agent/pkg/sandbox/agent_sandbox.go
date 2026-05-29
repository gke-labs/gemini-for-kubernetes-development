package sandbox

import (
	"encoding/json"
	"os"
	"strconv"

	reviewv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repowatch/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	DindSupportNone       = "none"
	DindSupportGvisor     = "gvisor"
	DindSupportPrivileged = "privileged"

	GoCachePath    = "/workspaces/.cache/go-build"
	GoModCachePath = "/workspaces/.cache/mod"
	TmpDirPath     = "/workspaces/.tmp"
)

// AgentSandboxOptions holds options for creating an AgentSandbox.
// It is a superset of DevSandboxOptions.
type AgentSandboxOptions struct {
	DevSandboxOptions

	// Issue details for labels/env
	IssueID    string
	IssueTitle string
	IssueRepo  string
	Handler    string

	Resources   corev1.ResourceRequirements
	DindSupport string

	LLMExtensions []reviewv1alpha1.Extension
}

// NewAgentSandbox creates a new Sandbox (unstructured) and Service object.
func NewAgentSandbox(opt AgentSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	if opt.RepoSandboxImage == "" {
		opt.RepoSandboxImage = os.Getenv("REPO_SANDBOX_IMAGE")
	}
	if opt.ConfigDirImage == "" {
		opt.ConfigDirImage = os.Getenv("CONFIGDIR_CLI_IMAGE")
	}

	name := opt.Name
	sandboxName := name

	// Default resources if not set
	resources := opt.Resources
	if resources.Requests == nil {
		resources.Requests = make(corev1.ResourceList)
	}
	if resources.Limits == nil {
		resources.Limits = make(corev1.ResourceList)
	}
	if resources.Requests.Memory().IsZero() {
		resources.Requests[corev1.ResourceMemory] = resource.MustParse("2Gi")
	}
	if resources.Limits.Memory().IsZero() {
		resources.Limits[corev1.ResourceMemory] = resource.MustParse("6Gi")
	}
	if resources.Requests.Cpu().IsZero() {
		resources.Requests[corev1.ResourceCPU] = resource.MustParse("2000m")
	}
	if resources.Limits.Cpu().IsZero() {
		resources.Limits[corev1.ResourceCPU] = resource.MustParse("4000m")
	}
	if _, ok := resources.Requests["ephemeral-storage"]; !ok {
		size := "6Gi"
		if opt.DindSupport == DindSupportPrivileged {
			size = "40Gi"
		}
		resources.Requests["ephemeral-storage"] = resource.MustParse(size)
	}
	if _, ok := resources.Limits["ephemeral-storage"]; !ok {
		size := "6Gi"
		if opt.DindSupport == DindSupportPrivileged {
			size = "40Gi"
		}
		resources.Limits["ephemeral-storage"] = resource.MustParse(size)
	}

	labels := make(map[string]string)
	for k, v := range opt.Labels {
		labels[k] = v
	}
	// Ensure sandbox label matches for service selector
	labels["sandbox"] = sandboxName
	// Default type to issue if not set
	if _, ok := labels["sandbox-type"]; !ok {
		labels["sandbox-type"] = "issue"
	}
	if _, ok := labels["sandbox.gemini.google.com/type"]; !ok {
		labels["sandbox.gemini.google.com/type"] = "issue"
	}

	if opt.IdeaID != "" {
		labels["repo-agent.gemini.google.com/idea-id"] = opt.IdeaID
	}
	if opt.Approach != "" {
		labels["repo-agent.gemini.google.com/approach"] = opt.Approach
	}
	if opt.ParentApproach != "" {
		labels["repo-agent.gemini.google.com/parent-approach"] = opt.ParentApproach
	}

	// Environment variables
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
		map[string]interface{}{"name": "REPO", "value": opt.IssueRepo},
		map[string]interface{}{"name": "HANDLER", "value": opt.Handler},
		map[string]interface{}{"name": "AGENT_NAME", "value": opt.LLMProvider},
		map[string]interface{}{"name": "AGENT_PROMPT", "value": opt.Prompt},
		map[string]interface{}{"name": "ISSUEID", "value": opt.IssueID},
		map[string]interface{}{"name": "ISSUE_BRANCH", "value": opt.Branch},
		map[string]interface{}{"name": "USER_DOTFILESREPO", "value": opt.DotFilesRepo},
		map[string]interface{}{"name": "DEV_BRANCH", "value": opt.Branch},
		map[string]interface{}{"name": "GIT_HTML_URL", "value": opt.HTMLURL},
		map[string]interface{}{"name": "ISSUE_URL", "value": opt.HTMLURL},
		map[string]interface{}{"name": "GITHUB_USER_ORIGIN", "value": opt.Origin},
		map[string]interface{}{"name": "GITHUB_USER_LOGIN", "value": opt.UserLogin},
		map[string]interface{}{"name": "GITHUB_USER_NAME", "value": userName},
		map[string]interface{}{"name": "GITHUB_USER_EMAIL", "value": opt.UserEmail},
		map[string]interface{}{"name": "GIT_AUTHOR_NAME", "value": userName},
		map[string]interface{}{"name": "GIT_AUTHOR_EMAIL", "value": opt.UserEmail},
		map[string]interface{}{"name": "GITHUB_BOT_LOGIN", "value": opt.BotLogin},
		map[string]interface{}{"name": "GITHUB_BOT_NAME", "value": opt.BotName},
		map[string]interface{}{"name": "GITHUB_BOT_EMAIL", "value": opt.BotEmail},
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
		map[string]interface{}{
			"name":  "GEMINI_CLI_TRUST_WORKSPACE",
			"value": "true",
		},
	}
	for _, e := range opt.Env {
		env = append(env, map[string]interface{}{
			"name":  e.Name,
			"value": e.Value,
		})
	}
	env = append(env, buildLLMEnvVars(opt.DevSandboxOptions)...)

	env = append(env,
		map[string]interface{}{"name": "GIT_PUSH_ENABLED", "value": strconv.FormatBool(opt.PushEnabled)},
		map[string]interface{}{"name": "GIT_CLONE_URL", "value": opt.CloneURL},
		map[string]interface{}{"name": "ENVBUILDER_GIT_URL", "value": opt.CloneURL},
		map[string]interface{}{"name": "ENVBUILDER_CACHE_REPO", "value": "registry.repo-agent-system.svc.cluster.local:5000/envbuilder-cache"},
		map[string]interface{}{"name": "ENVBUILDER_DEVCONTAINER_DIR", "value": "/"},
		map[string]interface{}{"name": "ENVBUILDER_INIT_SCRIPT", "value": RepoSandboxBinary + " dev-daemon"},
		map[string]interface{}{"name": "ENVBUILDER_IGNORE_PATHS", "value": "/var/run,/product_uuid,/product_name,/tokens,/repo-agent/,/etc/github-portal/ca"},
		map[string]interface{}{"name": "DIND_SUPPORT", "value": opt.DindSupport},
		map[string]interface{}{"name": "GOCACHE", "value": GoCachePath},
		map[string]interface{}{"name": "GOMODCACHE", "value": GoModCachePath},
		map[string]interface{}{"name": "TMPDIR", "value": TmpDirPath},
		map[string]interface{}{"name": "GOTMPDIR", "value": TmpDirPath},
		map[string]interface{}{"name": "SSL_CERT_FILE", "value": "/opt/repo-agent/ca/tls.crt"},
	)

	if opt.DisableGitHubProxy {
		env = append(env, map[string]interface{}{"name": "DISABLE_GITHUB_PROXY", "value": "true"})
	}

	if opt.OverseerName != "" {
		env = append(env, map[string]interface{}{"name": "OVERSEER_NAME", "value": opt.OverseerName})
	}
	if opt.RepoSandboxImage != "" {
		env = append(env, map[string]interface{}{"name": "REPO_SANDBOX_IMAGE", "value": opt.RepoSandboxImage})
	}
	if opt.ConfigDirImage != "" {
		env = append(env, map[string]interface{}{"name": "CONFIG_DIR_IMAGE", "value": opt.ConfigDirImage})
	}
	if len(opt.LLMExtensions) > 0 {
		exts, err := json.Marshal(opt.LLMExtensions)
		if err == nil {
			env = append(env, map[string]interface{}{
				"name":  "AGENT_LLM_EXTENSIONS",
				"value": string(exts),
			})
		}
	}

	image := opt.Image
	var cmd []string
	if image == "" {
		image = opt.RepoSandboxImage
		cmd = []string{}
	} else {
		cmd = []string{RepoSandboxBinary, "dev-daemon"}
	}

	cmdInterface := make([]interface{}, len(cmd))
	for i, v := range cmd {
		cmdInterface[i] = v
	}

	// Add metadata annotations for easier retrieval
	if opt.Annotations == nil {
		opt.Annotations = make(map[string]string)
	}
	opt.Annotations["sandbox.gemini.google.com/issue-id"] = opt.IssueID
	opt.Annotations["sandbox.gemini.google.com/issue-title"] = opt.IssueTitle
	opt.Annotations["sandbox.gemini.google.com/html-url"] = opt.HTMLURL
	opt.Annotations["sandbox.gemini.google.com/clone-url"] = opt.CloneURL
	opt.Annotations["sandbox.gemini.google.com/user-login"] = opt.UserLogin
	opt.Annotations["sandbox.gemini.google.com/bot-login"] = opt.BotLogin
	opt.Annotations["sandbox.gemini.google.com/origin"] = opt.Origin
	opt.Annotations["sandbox.gemini.google.com/branch"] = opt.Branch
	opt.Annotations["sandbox.gemini.google.com/push-enabled"] = strconv.FormatBool(opt.PushEnabled)

	ephemeralRequest := resources.Requests["ephemeral-storage"]
	ephemeralLimit := resources.Limits["ephemeral-storage"]

	labelsInterface := make(map[string]interface{}, len(labels))
	for k, v := range labels {
		labelsInterface[k] = v
	}

	annotationsInterface := make(map[string]interface{}, len(opt.Annotations))
	for k, v := range opt.Annotations {
		annotationsInterface[k] = v
	}

	// Construct unstructured Sandbox
	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":        sandboxName,
				"namespace":   opt.Namespace,
				"labels":      labelsInterface,
				"annotations": annotationsInterface,
			},
			"spec": map[string]interface{}{
				"replicas": opt.Replicas,
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": labelsInterface,
					},
					"spec": map[string]interface{}{
						"serviceAccountName": opt.ServiceAccountName,
						"runtimeClassName": func() interface{} {
							if opt.DindSupport == DindSupportGvisor {
								return "gvisor"
							}
							return nil
						}(),
						"dnsPolicy": func() interface{} {
							if opt.DindSupport != "" && opt.DindSupport != DindSupportNone {
								return "None"
							}
							return nil
						}(),
						"dnsConfig": func() interface{} {
							if opt.DindSupport != "" && opt.DindSupport != DindSupportNone {
								return map[string]interface{}{
									"nameservers": []interface{}{"8.8.8.8", "8.8.4.4"},
								}
							}
							return nil
						}(),
						"initContainers": func() []interface{} {
							containers := []interface{}{}
							if opt.LLMConfigdirRef != "" {
								containers = append(containers, map[string]interface{}{
									"name":  "gemini-configs",
									"image": opt.ConfigDirImage,
									"args":  []interface{}{"--directory", "/configdir", "--namespace", opt.Namespace, "--name", opt.LLMConfigdirRef, "--ignore-not-found-error"},
									"volumeMounts": []interface{}{
										map[string]interface{}{"name": "configdir-vol", "mountPath": "/configdir"},
									},
								})
							}
							containers = append(containers, map[string]interface{}{
								"name":    "inject-agent",
								"image":   opt.RepoSandboxImage,
								"command": []interface{}{"/repo-agent/repo-sandbox", "inject", "--path", "/opt/repo-agent"},
								"volumeMounts": []interface{}{
									map[string]interface{}{"name": "agent-bin", "mountPath": "/opt/repo-agent"},
									map[string]interface{}{"name": "ca-cert", "mountPath": "/etc/github-portal/ca", "readOnly": true},
								},
							})
							return containers
						}(),
						"containers": []interface{}{
							map[string]interface{}{
								"name":    "sandbox",
								"image":   image,
								"command": cmdInterface,
								"securityContext": func() map[string]interface{} {
									sc := map[string]interface{}{}
									if opt.DindSupport == DindSupportPrivileged {
										sc["privileged"] = true
									} else if opt.DindSupport == DindSupportGvisor {
										sc["capabilities"] = map[string]interface{}{
											"add": []interface{}{
												"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD", "NET_BIND_SERVICE", "NET_RAW", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT", "SYS_PTRACE", "NET_ADMIN", "SYS_ADMIN",
											},
										}
									}
									return sc
								}(),
								"resources": map[string]interface{}{
									"requests": map[string]interface{}{
										"cpu":               resources.Requests.Cpu().String(),
										"memory":            resources.Requests.Memory().String(),
										"ephemeral-storage": ephemeralRequest.String(),
									},
									"limits": map[string]interface{}{
										"cpu":               resources.Limits.Cpu().String(),
										"memory":            resources.Limits.Memory().String(),
										"ephemeral-storage": ephemeralLimit.String(),
									},
								},
								"env": env,
								"volumeMounts": func() []interface{} {
									vm := []interface{}{
										map[string]interface{}{"name": "configdir-vol", "mountPath": "/configdir"},
										map[string]interface{}{"name": "workspaces-pvc", "mountPath": "/workspaces"},
										map[string]interface{}{"name": "agent-bin", "mountPath": "/opt/repo-agent"},
									}
									if opt.LLMAPIKey == "" {
										vm = append(vm, map[string]interface{}{"name": "tokens-secret", "mountPath": "/tokens", "readOnly": true})
									}
									if opt.DevcontainerConfigRef != "" {
										vm = append(vm, map[string]interface{}{"name": "devcontainer-config", "mountPath": "/devcontainer.json", "subPath": "devcontainer.json"})
									}
									if opt.DindSupport != "" && opt.DindSupport != DindSupportNone {
										vm = append(vm, map[string]interface{}{"name": "docker", "mountPath": "/var/lib/docker"})
									}
									vm = append(vm, map[string]interface{}{"name": "ca-cert", "mountPath": "/etc/github-portal/ca", "readOnly": true})
									for _, secret := range opt.Secrets {
										vm = append(vm, map[string]interface{}{
											"name":      secret.Name + "-vol",
											"mountPath": secret.MountPath,
										})
									}
									return vm
								}(),
								"ports": []interface{}{
									map[string]interface{}{"containerPort": int64(13337)},
									map[string]interface{}{"containerPort": int64(13339)},
								},
							},
						},
						"volumes": func() []interface{} {
							v := []interface{}{
								map[string]interface{}{
									"name":     "configdir-vol",
									"emptyDir": map[string]interface{}{},
								},
								map[string]interface{}{
									"name":     "agent-bin",
									"emptyDir": map[string]interface{}{},
								},
							}
							if opt.LLMAPIKey == "" {
								v = append(v, map[string]interface{}{
									"name": "tokens-secret",
									"projected": map[string]interface{}{
										"sources": buildLLMVolumeSources(opt.DevSandboxOptions),
									},
								})
							}
							if opt.DevcontainerConfigRef != "" {
								v = append(v, map[string]interface{}{
									"name": "devcontainer-config",
									"configMap": map[string]interface{}{
										"name": opt.DevcontainerConfigRef,
									},
								})
							}
							if opt.DindSupport != "" && opt.DindSupport != DindSupportNone {
								v = append(v, map[string]interface{}{
									"name":     "docker",
									"emptyDir": map[string]interface{}{},
								})
							}
							v = append(v, map[string]interface{}{
								"name": "ca-cert",
								"secret": map[string]interface{}{
									"secretName": "github-portal-ca",
									"optional":   true,
								},
							})
							for _, secret := range opt.Secrets {
								v = append(v, map[string]interface{}{
									"name": secret.Name + "-vol",
									"secret": map[string]interface{}{
										"secretName": secret.Name,
									},
								})
							}
							return v
						}(),
					},
				},
				"volumeClaimTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "workspaces-pvc",
						},
						"spec": map[string]interface{}{
							"accessModes": []interface{}{"ReadWriteOnce"},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"storage": func() string {
										if opt.WorkspaceDiskSize != "" {
											return opt.WorkspaceDiskSize
										}
										return "10Gi"
									}(),
								},
							},
						},
					},
				},
			},
		},
	}

	// Service
	serviceName := sandboxName + "-lb"
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: opt.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"sandbox": sandboxName,
			},
			Ports: []corev1.ServicePort{
				{
					Name:        "code-server",
					Protocol:    corev1.ProtocolTCP,
					Port:        13338,
					TargetPort:  intstr.FromInt(13337),
					AppProtocol: stringPtr("kubernetes.io/ws"),
				},
				{
					Name:       "agent-server",
					Protocol:   corev1.ProtocolTCP,
					Port:       13339,
					TargetPort: intstr.FromInt(13339),
				},
			},
		},
	}
	service.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))

	return sandbox, service
}

func stringPtr(s string) *string {
	return &s
}
