package watch

import (
	"context"
	"errors"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/watch/rules"
)

// The watch decides with the repository's rules when RepoRules lays them
// over factory's, and with factory's alone when it reads none or fails.
func TestNewRulesControlTakesTheRepositorysRules(t *testing.T) {
	off := func(ctx context.Context, base rules.Set) (rules.Set, string, error) {
		repo, _ := rules.LoadFiles(map[string][]byte{"review.yaml": []byte("name: review\nenabled: false\n")})
		set, err := rules.Overlay(base, repo)
		return set, ".agents/watch@abc (replace, 1 rules)", err
	}
	if c := (&Watcher{RepoRules: off}).newRulesControl(); c == nil || c.Set().Named("review").IsEnabled() {
		t.Error("the repository's toggle did not turn review off")
	}
	for name, hook := range map[string]func(context.Context, rules.Set) (rules.Set, string, error){
		"none":  func(_ context.Context, base rules.Set) (rules.Set, string, error) { return base, "", nil },
		"fails": func(context.Context, rules.Set) (rules.Set, string, error) { return nil, "", errors.New("no") },
	} {
		if c := (&Watcher{RepoRules: hook}).newRulesControl(); c == nil || !c.Set().Named("review").IsEnabled() {
			t.Errorf("%s: factory's rules were not kept", name)
		}
	}
}
