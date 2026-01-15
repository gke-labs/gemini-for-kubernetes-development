package sandbox

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DevSandboxOptions holds options for creating a DevSandbox.
type DevSandboxOptions struct {
	Name        string
	Namespace   string
	Labels      map[string]string
	Annotations map[string]string

	// Source
	CloneURL string
	HTMLURL  string

	// Destination
	Branch      string
	Origin      string
	PushEnabled bool
	UserLogin   string
	UserName    string
	UserEmail   string

	// User Config
	DotFilesRepo string

	// LLM
	LLMProvider         string
	LLMConfigdirRef     string
	LLMAPIKeySecretName string
	Prompt              string

	// Infra
	ServiceAccountName    string
	GithubSecretName      string
	DevcontainerConfigRef string
	Image                 string

	// Gateway
	HTTPEnabled bool

	// Scaling
	Replicas int64
}

// NewDevSandbox creates a new DevSandbox unstructured object.
func NewDevSandbox(opt DevSandboxOptions) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"source": map[string]interface{}{
			"cloneURL": opt.CloneURL,
			"htmlURL":  opt.HTMLURL,
		},
		"destination": map[string]interface{}{
			"branch": opt.Branch,
		},
	}

	if opt.Replicas > 0 {
		spec["replicas"] = opt.Replicas
	}

	// Destination details
	dest := spec["destination"].(map[string]interface{})
	if opt.Origin != "" {
		dest["origin"] = opt.Origin
	}
	if opt.PushEnabled {
		dest["pushEnabled"] = true
	}
	if opt.UserLogin != "" || opt.UserName != "" || opt.UserEmail != "" {
		userMap := map[string]interface{}{}
		if opt.UserLogin != "" {
			userMap["login"] = opt.UserLogin
		}
		if opt.UserName != "" {
			userMap["name"] = opt.UserName
		}
		if opt.UserEmail != "" {
			userMap["email"] = opt.UserEmail
		}
		dest["user"] = userMap
	}

	// User Config (Dotfiles)
	if opt.DotFilesRepo != "" {
		spec["user"] = map[string]interface{}{
			"dotFilesRepo": opt.DotFilesRepo,
		}
	}

	// LLM
	if opt.LLMProvider != "" {
		spec["llmBackend"] = map[string]interface{}{
			"name": opt.LLMProvider,
		}
	}
	if opt.LLMConfigdirRef != "" || opt.LLMAPIKeySecretName != "" || opt.Prompt != "" {
		llmMap := map[string]interface{}{}
		if opt.LLMConfigdirRef != "" {
			llmMap["configdirRef"] = opt.LLMConfigdirRef
		}
		if opt.LLMAPIKeySecretName != "" {
			llmMap["apiKeySecretName"] = opt.LLMAPIKeySecretName
		}
		if opt.Prompt != "" {
			llmMap["prompt"] = opt.Prompt
		}
		spec["llm"] = llmMap
	}

	// Infra
	if opt.ServiceAccountName != "" {
		spec["serviceAccountName"] = opt.ServiceAccountName
	}
	if opt.GithubSecretName != "" {
		spec["githubSecretName"] = opt.GithubSecretName
	}
	if opt.DevcontainerConfigRef != "" {
		spec["devcontainerConfigRef"] = opt.DevcontainerConfigRef
	}
	if opt.Image != "" {
		spec["image"] = opt.Image
	}

	// Gateway
	if opt.HTTPEnabled {
		spec["gateway"] = map[string]interface{}{
			"httpEnabled": true,
		}
	}

	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "custom.agents.x-k8s.io/v1alpha1",
			"kind":       "IssueSandbox",
			"metadata": map[string]interface{}{
				"name": opt.Name,
				"annotations": map[string]interface{}{
					"agentState":  "provisioning",
					"reviewState": "",
				},
			},
			"spec": spec,
		},
	}

	if opt.Namespace != "" {
		u.SetNamespace(opt.Namespace)
	}

	// Ensure we label this as a dev sandbox
	labels := opt.Labels
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["sandbox.gemini.google.com/type"] = "dev"
	u.SetLabels(labels)

	if len(opt.Annotations) > 0 {
		u.SetAnnotations(opt.Annotations)
	}

	return u
}
