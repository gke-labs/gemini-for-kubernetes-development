package config

import "testing"

func TestWarmWorkspaceConfigValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg WarmWorkspaceConfig
		ok  bool
	}{
		"script":           {WarmWorkspaceConfig{Interval: "24h", Script: "/workspaces/warm-workspace.sh"}, true},
		"scriptURL":        {WarmWorkspaceConfig{Interval: "24h", ScriptURL: "https://example.com/warm.sh"}, true},
		"scriptPath":       {WarmWorkspaceConfig{Interval: "24h", ScriptPath: "dev/tasks/warm"}, true},
		"no interval":      {WarmWorkspaceConfig{Script: "/w.sh"}, false},
		"zero interval":    {WarmWorkspaceConfig{Interval: "0s", Script: "/w.sh"}, false},
		"no script":        {WarmWorkspaceConfig{Interval: "24h"}, false},
		"two scripts":      {WarmWorkspaceConfig{Interval: "24h", Script: "/w.sh", ScriptPath: "w.sh"}, false},
		"http URL":         {WarmWorkspaceConfig{Interval: "24h", ScriptURL: "http://example.com/warm.sh"}, false},
		"absolute path":    {WarmWorkspaceConfig{Interval: "24h", ScriptPath: "/etc/warm"}, false},
		"path out of repo": {WarmWorkspaceConfig{Interval: "24h", ScriptPath: "dev/../../warm"}, false},
		"negative keep":    {WarmWorkspaceConfig{Interval: "24h", Script: "/w.sh", Keep: -1}, false},
	} {
		if _, err := tc.cfg.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", name, err, tc.ok)
		}
	}
	if k := (&WarmWorkspaceConfig{}).KeepOrDefault(); k != DefaultWarmKeep {
		t.Errorf("KeepOrDefault() = %d", k)
	}
}
