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

type FactoryConfig struct {
	MaxActiveReviews  int           `yaml:"maxActiveReviews"`
	MaxActiveIssues   int           `yaml:"maxActiveIssues"`
	Chores            ChoresConfig  `yaml:"chores"`
	EphemeralStorage  string        `yaml:"ephemeralStorage"`
	Image             string        `yaml:"image"`
	WorkspaceDiskSize string        `yaml:"workspaceDiskSize"`
	Secrets           []SecretMount `yaml:"secrets"`
	Env               []EnvVar      `yaml:"env"`
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
