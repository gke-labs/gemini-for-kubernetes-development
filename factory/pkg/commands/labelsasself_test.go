package commands

import (
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/recipe"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/taskoutput"
)

// A Change opened as another account (an overseer's pool bot, --user) is
// labelled as the process (the watcher), with or without --pr-labels:
// adopt's labels input as much as fix's issue labels. Run as itself, or a
// result that opens no PR, it labels nothing differently.
func TestLabelsAsSelf(t *testing.T) {
	change := &recipe.Recipe{TaskOutput: &taskoutput.Decl{Kind: "Change"}}
	review := &recipe.Recipe{TaskOutput: &taskoutput.Decl{Kind: "Review"}}
	for _, tc := range []struct {
		name  string
		apply applyMode
		rec   *recipe.Recipe
		want  bool
	}{
		{"fix as a pool bot, --pr-labels", applyMode{asUser: true, prLabels: true}, change, true},
		{"adopt as a pool bot, a labels input", applyMode{asUser: true}, change, true},
		{"run as itself", applyMode{}, change, false},
		{"a review as a pool bot", applyMode{asUser: true}, review, false},
		{"no result", applyMode{asUser: true}, &recipe.Recipe{}, false},
	} {
		if got := labelsAsSelf(tc.apply, tc.rec); got != tc.want {
			t.Errorf("%s: %t, want %t", tc.name, got, tc.want)
		}
	}
}
