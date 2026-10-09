package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// WarmWorkspace keeps a snapshot of a warmed workspace disk for the
	// repository, which new sandboxes start from (design/warm-workspace.md).
	// Unset, no warming.
	WarmWorkspace *WarmWorkspaceConfig `yaml:"warmWorkspace"`
}

// WarmWorkspaceConfig is how often the workspace is warmed, how many
// snapshots are kept, and the script that warms it: exactly one of
// Script, ScriptURL and ScriptPath.
type WarmWorkspaceConfig struct {
	// Interval is how old the newest snapshot may get before a new one is
	// made, e.g. "24h".
	Interval string `yaml:"interval"`
	// Keep is how many snapshots are kept; zero keeps DefaultWarmKeep.
	Keep int `yaml:"keep"`
	// Script is a file, where factory runs, holding the script.
	Script string `yaml:"script"`
	// ScriptURL is an https URL factory downloads the script from at
	// each warm.
	ScriptURL string `yaml:"scriptURL"`
	// ScriptPath is a path in the repository, run from its default branch.
	ScriptPath string `yaml:"scriptPath"`
}

// DefaultWarmKeep is how many warm snapshots are kept when Keep is unset.
const DefaultWarmKeep = 2

// Validate checks the warm workspace settings: a positive interval and
// exactly one script source.
func (c *WarmWorkspaceConfig) Validate() (time.Duration, error) {
	interval, err := time.ParseDuration(c.Interval)
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("warmWorkspace.interval %q is not a positive duration", c.Interval)
	}
	if c.Keep < 0 {
		return 0, fmt.Errorf("warmWorkspace.keep %d is negative", c.Keep)
	}
	n := 0
	for _, s := range []string{c.Script, c.ScriptURL, c.ScriptPath} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return 0, fmt.Errorf("warmWorkspace needs exactly one of script, scriptURL and scriptPath; it has %d", n)
	}
	if c.ScriptURL != "" && !strings.HasPrefix(c.ScriptURL, "https://") {
		return 0, fmt.Errorf("warmWorkspace.scriptURL %q is not https", c.ScriptURL)
	}
	if p := c.ScriptPath; p != "" && (strings.HasPrefix(p, "/") || strings.Contains(p, "..")) {
		return 0, fmt.Errorf("warmWorkspace.scriptPath %q must be relative, without ..", p)
	}
	return interval, nil
}

// KeepOrDefault is Keep, or DefaultWarmKeep when it is unset.
func (c *WarmWorkspaceConfig) KeepOrDefault() int {
	if c.Keep > 0 {
		return c.Keep
	}
	return DefaultWarmKeep
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
