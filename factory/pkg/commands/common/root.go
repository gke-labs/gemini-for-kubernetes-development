package common

import (
	"time"

	factorysandbox "github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/sandbox"
)

type RootFlags struct {
	Namespace        string
	Engine           string
	Image            string
	DiskSize         string
	SecretName       string
	User             string
	Timeout          time.Duration
	Background       bool
	Cleanup          bool
	EphemeralStorage string
	CPURequest       string
	CPULimit         string
	MemoryRequest    string
	MemoryLimit      string
	Secrets          []string
	ResolvedSecrets  []factorysandbox.SecretMount
	Envs             []string
	ResolvedEnvs     []factorysandbox.EnvVar
	Detached         bool
	AbortOnCancel    bool
	// Disclose makes the agent say, in what it posts, that an agent wrote
	// it: the PR description, and the footer on its comments and reports.
	Disclose bool
}
