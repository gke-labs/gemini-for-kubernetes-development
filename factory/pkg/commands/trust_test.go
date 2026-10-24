package commands

import (
	"strings"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
)

// The pool is every role's bots, once each, sorted.
func TestPoolLogins(t *testing.T) {
	cfg := &config.FactoryConfig{Roles: map[string]config.RoleConfig{
		"coder":    {Users: []string{"hopper-coder-bot", "ada-coder-bot"}},
		"reviewer": {Users: []string{"reviewbot", "Ada-coder-bot"}},
	}}
	if got := strings.Join(poolLogins(cfg), ","); got != "ada-coder-bot,hopper-coder-bot,reviewbot" {
		t.Errorf("poolLogins = %s", got)
	}
	if poolLogins(nil) != nil {
		t.Error("no config: want no pool")
	}
}
