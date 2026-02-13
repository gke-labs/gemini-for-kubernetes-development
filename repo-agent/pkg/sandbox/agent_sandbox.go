package sandbox

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
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

	// Bot info
	BotLogin string
	BotName  string
	BotEmail string
	
	// Resources
	Resources corev1.ResourceRequirements
	DockerEnabled bool
}

// NewAgentSandbox creates a new Sandbox (unstructured) and Service object.
func NewAgentSandbox(opt AgentSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	name := opt.Name
	sandboxName := "devc-" + name
	
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
	if _, ok := resources.Requests["ephemeral-storage"]; !ok {
		resources.Requests["ephemeral-storage"] = resource.MustParse("6Gi")
	}
	if _, ok := resources.Limits["ephemeral-storage"]; !ok {
		resources.Limits["ephemeral-storage"] = resource.MustParse("6Gi")
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

	// Environment variables
	env := []map[string]interface{}{
		{"name": "NAMESPACE", "value": opt.Namespace},
		{"name": "NAME", "value": name},
		{"name": "REPO", "value": opt.IssueRepo},
		{"name": "HANDLER", "value": opt.Handler},
		{"name": "AGENT_NAME", "value": opt.LLMProvider},
		{"name": "AGENT_PROMPT", "value": opt.Prompt},
		{"name": "ISSUEID", "value": opt.IssueID},
		{"name": "ISSUE_BRANCH", "value": opt.Branch},
		{"name": "USER_DOTFILESREPO", "value": opt.DotFilesRepo},
		{"name": "DEV_BRANCH", "value": opt.Branch},
		{"name": "GIT_HTML_URL", "value": opt.HTMLURL},
		{"name": "ISSUE_URL", "value": opt.HTMLURL},
		{"name": "GITHUB_USER_ORIGIN", "value": opt.Origin},
		{"name": "GITHUB_USER_LOGIN", "value": opt.UserLogin},
		{"name": "GITHUB_USER_NAME", "value": opt.UserName},
		{"name": "GITHUB_USER_EMAIL", "value": opt.UserEmail},
		{"name": "GIT_AUTHOR_NAME", "value": opt.UserName},
		{"name": "GIT_AUTHOR_EMAIL", "value": opt.UserEmail},
		{"name": "GITHUB_BOT_LOGIN", "value": opt.BotLogin},
		{"name": "GITHUB_BOT_NAME", "value": opt.BotName},
		{"name": "GITHUB_BOT_EMAIL", "value": opt.BotEmail},
		{
			"name": "GITHUB_TOKEN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": opt.GithubSecretName,
					"key":  "pat",
					"optional": true,
				},
			},
		},
		{
			"name": "MANUAL_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": opt.GithubSecretName,
					"key":  "manual_pat",
					"optional": true,
				},
			},
		},
		{
			"name": "OAUTH_PAT",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": opt.GithubSecretName,
					"key":  "oauth_pat",
					"optional": true,
				},
			},
		},
		{"name": "GIT_PUSH_ENABLED", "value": strconv.FormatBool(opt.PushEnabled)},
		{"name": "GIT_CLONE_URL", "value": opt.CloneURL},
		{"name": "ENVBUILDER_GIT_URL", "value": opt.CloneURL},
		{"name": "ENVBUILDER_CACHE_REPO", "value": "registry.repo-agent-system.svc.cluster.local:5000/envbuilder-cache"},
		{"name": "ENVBUILDER_DEVCONTAINER_DIR", "value": "/"},
		{"name": "ENVBUILDER_INIT_SCRIPT", "value": "/opt/repo-agent/repo-sandbox dev-daemon"},
		{"name": "ENVBUILDER_IGNORE_PATHS", "value": "/var/run,/product_uuid,/product_name,/tokens,/repo-agent/"},
	}

	image := opt.Image
	var cmd []string
	if image == "" {
		image = "ko://repo-agent/images/repo-sandbox"
		cmd = []string{} 
	} else {
		cmd = []string{"/opt/repo-agent/repo-sandbox", "dev-daemon"}
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
	opt.Annotations["sandbox.gemini.google.com/branch"] = opt.Branch
	opt.Annotations["sandbox.gemini.google.com/push-enabled"] = strconv.FormatBool(opt.PushEnabled)

	ephemeralRequest := resources.Requests["ephemeral-storage"]
	ephemeralLimit := resources.Limits["ephemeral-storage"]

	// Construct unstructured Sandbox
	sandbox := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":      sandboxName,
				"namespace": opt.Namespace,
				"labels":    labels,
				"annotations": opt.Annotations,
			},
			"spec": map[string]interface{}{
				"replicas": opt.Replicas,
				"podTemplate": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": labels,
					},
					"spec": map[string]interface{}{
						"serviceAccountName": opt.ServiceAccountName,
						"initContainers": []map[string]interface{}{
							{
								"name":  "gemini-configs",
								"image": "ko://repo-agent/configdir/cmd/configdir-cli",
								"args":  []string{"--directory", "/workspaces", "--namespace", opt.Namespace, "--name", opt.LLMConfigdirRef, "--ignore-not-found-error"},
								"volumeMounts": []map[string]interface{}{
									{"name": "workspaces-pvc", "mountPath": "/workspaces"},
								},
							},
							{
								"name":    "inject-agent",
								"image":   "ko://repo-agent/images/repo-sandbox",
								"command": []string{"/repo-agent/repo-sandbox", "inject", "--path", "/opt/repo-agent"},
								"volumeMounts": []map[string]interface{}{
									{"name": "agent-bin", "mountPath": "/opt/repo-agent"},
								},
							},
						},
						"containers": []map[string]interface{}{
							{
								"name":            "sandbox",
								"image":           image,
								"command":         cmd,
								"securityContext": map[string]interface{}{
									"privileged": opt.DockerEnabled,
								},
								"resources": map[string]interface{}{
									"requests": map[string]interface{}{
										"memory": resources.Requests.Memory().String(),
										"ephemeral-storage": ephemeralRequest.String(),
									},
									"limits": map[string]interface{}{
										"memory": resources.Limits.Memory().String(),
										"ephemeral-storage": ephemeralLimit.String(),
									},
								},
								"env":             env,
								"volumeMounts": []map[string]interface{}{
									{"name": "workspaces-pvc", "mountPath": "/workspaces"},
									{"name": "tokens-secret", "mountPath": "/tokens", "readOnly": true},
									{"name": "devcontainer-config", "mountPath": "/devcontainer.json", "subPath": "devcontainer.json"},
									{"name": "agent-bin", "mountPath": "/opt/repo-agent"},
								},
								"ports": []map[string]interface{}{
									{"containerPort": 13337},
									{"containerPort": 13339},
								},
							},
						},
						"volumes": []map[string]interface{}{
							{
								"name": "agent-bin",
								"emptyDir": map[string]interface{}{},
							},
							{
								"name": "devcontainer-config",
								"configMap": map[string]interface{}{
									"name": opt.DevcontainerConfigRef,
								},
							},
							{
								"name": "tokens-secret",
								"secret": map[string]interface{}{
									"secretName": opt.LLMAPIKeySecretName,
								},
							},
						},
					},
				},
				"volumeClaimTemplates": []map[string]interface{}{
					{
						"metadata": map[string]interface{}{
							"name": "workspaces-pvc",
						},
						"spec": map[string]interface{}{
							"accessModes": []string{"ReadWriteOnce"},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"storage": "5Gi",
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
					Name:       "code-server",
					Protocol:   corev1.ProtocolTCP,
					Port:       13338,
					TargetPort: intstr.FromInt(13337),
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

func boolPtr(b bool) *bool {
	return &b
}

func stringPtr(s string) *string {
	return &s
}
