package config

import (
	"reflect"
	"testing"
)

func TestTrustedLogins(t *testing.T) {
	var nilCfg *FactoryConfig
	if got := nilCfg.TrustedLogins(); got != nil {
		t.Errorf("nil config TrustedLogins() = %v, want nil", got)
	}

	cfg := &FactoryConfig{
		AllowlistedUsers: []string{"private-member"},
		AllowlistedBots:  []string{"reviewbot-robot"},
		Roles: map[string]RoleConfig{
			"reviewer": {Users: []string{"review-account"}},
			// Other role pools are the factory's own coder accounts, not
			// sources of feedback, and are not trusted by this list.
			"coder": {Users: []string{"coder-account"}},
		},
	}
	want := []string{"private-member", "reviewbot-robot", "review-account"}
	if got := cfg.TrustedLogins(); !reflect.DeepEqual(got, want) {
		t.Errorf("TrustedLogins() = %v, want %v", got, want)
	}
}
