package rules

import (
	"slices"
	"testing"
)

// The listings a set needs: factory's rules none beyond the pool and
// trigger listings; a rule on someone else's pull request its namespaced
// labels - never one outside the namespace, a gate or a hand-over label.
func TestListingLabels(t *testing.T) {
	base, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv // trigger overseer, prefixes [overseer]
	if got := base.ListingLabels(OnPR, env); len(got) != 0 {
		t.Errorf("factory's rules list %v", got)
	}
	if got := base.Unlisted(OnPR, env); len(got) != 0 {
		t.Errorf("factory's rules unlisted: %v", got)
	}
	repo, err := LoadFiles(map[string][]byte{
		// One label: listed by it.
		"adopt.yaml": []byte("name: adopt\non: pr\norder: 8\nwhen: {author: not-pool, labels: {any: [\"{{trigger}}/adopt\"]}}\nrun: {recipe: adopt, apply: open-pr}\n"),
		// The trigger label too: the trigger listing reaches it.
		"both.yaml": []byte("name: both\non: pr\norder: 9\nwhen: {author: not-pool, labels: {all: [\"{{trigger}}\", \"{{trigger}}/look\"]}}\nrun: {recipe: review, apply: submit-review}\n"),
		// */x, a gate and a hand-over label, an alternative's label.
		"star.yaml": []byte("name: star\non: pr\norder: 45\nwhen: {labels: {all: [\"*/review\", \"*/stop\"], none: [\"*/ready-for-human\"]}, any-of: [{labels: {any: [overseer/extra]}}, {author: pool}]}\nrun: {recipe: review, apply: submit-review}\n"),
		// Outside the namespace: nothing listed, so unlisted.
		"bug.yaml": []byte("name: bug\non: pr\norder: 46\nwhen: {author: not-pool, labels: {any: [bug]}}\nrun: {recipe: review, apply: submit-review}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := Overlay(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := set.ListingLabels(OnPR, env), []string{"overseer/adopt", "overseer/extra", "overseer/review"}; !slices.Equal(got, want) {
		t.Errorf("listing labels %v, want %v", got, want)
	}
	if got := set.Unlisted(OnPR, env); !slices.Equal(got, []string{"bug"}) {
		t.Errorf("unlisted %v, want [bug]", got)
	}
	// A deployment's own trigger: */x in each prefix.
	kcc := Env{Trigger: "kcc", Prefixes: []string{"overseer", "kcc"}}
	if got, want := set.ListingLabels(OnPR, kcc), []string{"kcc/adopt", "kcc/review", "overseer/extra", "overseer/review"}; !slices.Equal(got, want) {
		t.Errorf("kcc listing labels %v, want %v", got, want)
	}
}

// author: not-pool admits who author: pool does not, and a set admits an
// author when one of its rules does.
func TestAuthorNotPool(t *testing.T) {
	base, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	human := Item{Kind: OnPR, Author: "alice"}
	bot := Item{Kind: OnPR, Author: "ada-coder-bot"}
	if base.AdmitsAuthor(human, testEnv) || !base.AdmitsAuthor(bot, testEnv) {
		t.Error("factory's rules admit the wrong authors")
	}
	repo, err := LoadFiles(map[string][]byte{
		"adopt.yaml": []byte("name: adopt\non: pr\norder: 8\nwhen: {author: not-pool, labels: {any: [\"{{trigger}}/adopt\"]}}\nrun: {recipe: adopt, apply: open-pr}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := Overlay(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !set.AdmitsAuthor(human, testEnv) {
		t.Error("a not-pool rule does not admit an outside author")
	}
	only := Set{set.Named("adopt")}
	if only.AdmitsAuthor(bot, testEnv) {
		t.Error("author: not-pool admits a pool author")
	}
	mergeable := true
	pr := Item{Kind: OnPR, Number: 5, Author: "alice", Labels: []string{"overseer/adopt"}, Head: "abc", Mergeable: &mergeable}
	if got := names(set.Decide(pr, testEnv)); !slices.Equal(got, []string{"adopt"}) {
		t.Errorf("decided %v on an outside PR labelled for adoption", got)
	}
	pr.Author = "ada-coder-bot"
	if slices.Contains(names(set.Decide(pr, testEnv)), "adopt") {
		t.Error("adopt decided on a pool PR")
	}
}

func names(ds []Decision) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Rule)
	}
	return out
}
