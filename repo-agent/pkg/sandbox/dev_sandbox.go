package sandbox

import (
	corev1 "k8s.io/api/core/v1"
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

	// Bot info
	BotLogin string
	BotName  string
	BotEmail string

	// User Config
	DotFilesRepo string

	// LLM
	LLMProvider         string
	LLMConfigdirRef     string
	LLMAPIKeySecretName string
	LLMAPIKey           string
	Prompt              string

	// Infra
	ServiceAccountName    string
	GithubSecretName      string
	DevcontainerConfigRef string
	Image                 string
	OverseerName          string

	// System Images
	RepoSandboxImage string
	ConfigDirImage   string

	// Gateway
	HTTPEnabled bool

	// Scaling
	Replicas int64

	DindSupport string

	// WorkspaceDiskSize specifies the disk size for the workspace PVC.
	WorkspaceDiskSize string

	// Idea Exploration
	IdeaID         string
	Approach       string
	ParentApproach string
}

// NewDevSandbox creates a new DevSandbox.
func NewDevSandbox(opt DevSandboxOptions) (*unstructured.Unstructured, *corev1.Service) {
	agentOpt := AgentSandboxOptions{
		DevSandboxOptions: opt,
		DindSupport:       opt.DindSupport,
	}
	return NewAgentSandbox(agentOpt)
}
