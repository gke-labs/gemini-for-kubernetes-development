package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type SecretMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type ChoresConfig struct {
	Mode string `yaml:"mode"`
}

type RoleConfig struct {
	Tasks []string `yaml:"tasks"`
	Users []string `yaml:"users"`
}

type FactoryConfig struct {
	Engine                string       `yaml:"engine"`
	Chores                ChoresConfig `yaml:"chores"`
	EphemeralStorage      string       `yaml:"ephemeralStorage"`
	Image                 string       `yaml:"image"`
	WorkspaceDiskSize     string       `yaml:"workspaceDiskSize"`
	WorkspaceStorageClass string       `yaml:"workspaceStorageClass"`
	SandboxCPURequest     string       `yaml:"sandboxCPURequest"`
	SandboxCPULimit       string       `yaml:"sandboxCPULimit"`
	SandboxMemoryRequest  string       `yaml:"sandboxMemoryRequest"`
	SandboxMemoryLimit    string       `yaml:"sandboxMemoryLimit"`
	AdditionalLabels      []string     `yaml:"additionalLabels"`
	TriggerLabel          string       `yaml:"triggerLabel"`
	AllowlistedBots       []string     `yaml:"allowlistedBots"`
	// AllowlistedUsers are logins whose comments reach agent prompts even
	// though GitHub does not report them as OWNER, MEMBER or COLLABORATOR -
	// typically organisation members with private membership.
	AllowlistedUsers    []string              `yaml:"allowlistedUsers"`
	Secrets             []SecretMount         `yaml:"secrets"`
	Env                 []EnvVar              `yaml:"env"`
	MinNumber           int                   `yaml:"minNumber"`
	PRInactivityTimeout string                `yaml:"prInactivityTimeout"`
	Roles               map[string]RoleConfig `yaml:"roles"`
}

func LoadConfig() (*FactoryConfig, error) {
	configPath := ""

	// 1. Check local repo root (.factory.cfg in current directory)
	if _, err := os.Stat(".factory.cfg"); err == nil {
		configPath = ".factory.cfg"
	} else if envVal := os.Getenv("FACTORY_CONFIG"); envVal != "" {
		// 2. Check FACTORY_CONFIG env
		fi, err := os.Stat(envVal)
		if err == nil {
			if fi.IsDir() {
				configPath = filepath.Join(envVal, ".factory.cfg")
			} else {
				configPath = envVal
			}
		}
	}

	cfg := &FactoryConfig{}
	if configPath == "" {
		return cfg, nil // Return empty/default config
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config file %s: %w", configPath, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", configPath, err)
	}

	return cfg, nil
}

// TrustedLogins are the logins an operator has explicitly configured as
// sources of feedback, and so are trusted regardless of the
// author_association GitHub reports for them: the allowlistedUsers, the
// allowlistedBots and the accounts in the reviewer role.
//
// The watcher deciding which feedback to queue work for and the agent
// deciding which feedback to read must agree on this set; otherwise the
// watcher queues tasks for comments the agent then never sees.
func (c *FactoryConfig) TrustedLogins() []string {
	if c == nil {
		return nil
	}
	var out []string
	out = append(out, c.AllowlistedUsers...)
	out = append(out, c.AllowlistedBots...)
	out = append(out, c.Roles["reviewer"].Users...)
	return out
}
