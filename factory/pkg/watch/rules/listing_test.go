package rules

import (
	"slices"
	"strings"
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
		"adopt.yaml": []byte("name: adopt\non: pr\norder: 8\nwhen: {author: [owner, member, collaborator, external], labels: {any: [\"{{trigger}}/adopt\"]}}\nrun: {recipe: adopt, apply: open-pr}\n"),
		// The trigger label too: the trigger listing reaches it.
		"both.yaml": []byte("name: both\non: pr\norder: 9\nwhen: {author: [owner, member, collaborator, external], labels: {all: [\"{{trigger}}\", \"{{trigger}}/look\"]}}\nrun: {recipe: review, apply: submit-review}\n"),
		// */x, a gate and a hand-over label, an alternative's label.
		"star.yaml": []byte("name: star\non: pr\norder: 45\nwhen: {labels: {all: [\"*/review\", \"*/stop\"], none: [\"*/ready-for-human\"]}, any-of: [{labels: {any: [overseer/extra]}}, {author: pool}]}\nrun: {recipe: review, apply: submit-review}\n"),
		// Outside the namespace: nothing listed, so unlisted.
		"bug.yaml": []byte("name: bug\non: pr\norder: 46\nwhen: {author: [owner, member, collaborator, external], labels: {any: [bug]}}\nrun: {recipe: review, apply: submit-review}\n"),
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

// Every kind of author but the pool's admits who author: pool does not,
// and a set admits an author when one of its rules does.
func TestAuthorAnyButPool(t *testing.T) {
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
		"adopt.yaml": []byte("name: adopt\non: pr\norder: 8\nwhen: {author: [owner, member, collaborator, external], labels: {any: [\"{{trigger}}/adopt\"]}}\nrun: {recipe: adopt, apply: open-pr}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := Overlay(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !set.AdmitsAuthor(human, testEnv) {
		t.Error("a rule for every author but the pool's does not admit an outside author")
	}
	only := Set{set.Named("adopt")}
	if only.AdmitsAuthor(bot, testEnv) {
		t.Error("a rule for every author but the pool's admits a pool author")
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

// Each author is one kind, the first that applies: the pool's, then by
// GitHub's association, a login the overseer trusts a member whatever
// GitHub says, anyone else external.
func TestAuthorKind(t *testing.T) {
	env := testEnv
	env.Members = []string{"Carol"}
	for _, tc := range []struct {
		author, association, want string
	}{
		{"ada-coder-bot", "MEMBER", AuthorPool},
		{"alice", "OWNER", AuthorOwner},
		{"bob", "MEMBER", AuthorMember},
		{"carol", "CONTRIBUTOR", AuthorMember},
		{"carol", "OWNER", AuthorOwner},
		{"dan", "COLLABORATOR", AuthorCollaborator},
		{"erin", "CONTRIBUTOR", AuthorExternal},
		{"frank", "FIRST_TIME_CONTRIBUTOR", AuthorExternal},
		{"grace", "NONE", AuthorExternal},
		{"heidi", "", AuthorExternal},
	} {
		if got := AuthorKind(Item{Author: tc.author, AuthorAssociation: tc.association}, env); got != tc.want {
			t.Errorf("%s (%s): %s, want %s", tc.author, tc.association, got, tc.want)
		}
	}

	set, err := LoadFiles(map[string][]byte{
		"members.yaml": []byte("name: members\non: pr\norder: 8\nwhen: {author: [owner, member], labels: {any: [\"{{trigger}}/look\"]}}\nrun: {recipe: review, apply: submit-review}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		author, association string
		admits              bool
	}{
		{"bob", "MEMBER", true},
		{"carol", "CONTRIBUTOR", true},
		{"dan", "COLLABORATOR", false},
		{"erin", "NONE", false},
		{"ada-coder-bot", "MEMBER", false},
	} {
		if got := Set(set).AdmitsAuthor(Item{Kind: OnPR, Author: tc.author, AuthorAssociation: tc.association}, env); got != tc.admits {
			t.Errorf("author: [owner, member] admits %s (%s): %t", tc.author, tc.association, got)
		}
	}
}

// author is one kind or a list of them, each once.
func TestAuthorValidation(t *testing.T) {
	for _, tc := range []struct {
		when, err string
	}{
		{"{author: pool}", ""},
		{"{author: [owner, external]}", ""},
		{"{author: not-pool}", `author: "not-pool" is not one of`},
		{"{author: [member, member]}", "member is given twice"},
	} {
		r, err := Parse([]byte("name: x\non: pr\norder: 8\nwhen: " + tc.when + "\nrun: {recipe: review, apply: submit-review}\n"))
		if err == nil {
			err = r.Validate()
		}
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.when, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: error %v, want %q", tc.when, err, tc.err)
		}
	}
}

// An open PR closing an issue holds back every prebaked rule on issues
// that would act on it - fix, workflow, workflow-missing - by their own
// linked-pr: none, the scanner's gate before.
func TestLinkedPRHoldsPrebakedIssueRules(t *testing.T) {
	set, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{
		{Kind: OnIssue, Number: 7, Labels: []string{"overseer"}},
		{Kind: OnIssue, Number: 8, Labels: []string{"overseer"}, Workflow: "kcc-x", WorkflowMissing: true},
		{Kind: OnIssue, Number: 9, Labels: []string{"overseer"}, Workflow: "kcc-x"},
	} {
		if got := names(set.Decide(it, testEnv)); len(got) == 0 {
			t.Fatalf("#%d: nothing decided without a linked PR", it.Number)
		}
		it.LinkedPR = "open"
		if got := names(set.Decide(it, testEnv)); len(got) > 0 {
			t.Errorf("#%d with an open linked PR: decided %v", it.Number, got)
		}
	}
}
