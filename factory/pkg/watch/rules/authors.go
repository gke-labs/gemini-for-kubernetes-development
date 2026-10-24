package rules

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The kinds of author a rule's author predicate admits. Each author is
// exactly one, the first that applies: the bot pool's; then by what GitHub
// says of the author's standing in the repository (author_association) -
// the owner, an organization member, a collaborator; anyone else is
// external. A login the overseer trusts (Env.Members: its allowlisted
// users and bots, its reviewers) is a member whatever GitHub says: GitHub
// reports a private organization member as a contributor to a token that
// cannot see the membership.
const (
	AuthorPool         = "pool"
	AuthorOwner        = "owner"
	AuthorMember       = "member"
	AuthorCollaborator = "collaborator"
	AuthorExternal     = "external"
)

// Authors are the kinds of author a rule admits: one or a list.
type Authors []string

// UnmarshalYAML reads one kind or a list.
func (a *Authors) UnmarshalYAML(n *yaml.Node) error {
	var s Since
	if err := s.UnmarshalYAML(n); err != nil {
		return err
	}
	*a = Authors(s)
	return nil
}

// MarshalYAML writes one kind as itself, a list as a list.
func (a Authors) MarshalYAML() (any, error) {
	if len(a) == 1 {
		return a[0], nil
	}
	return []string(a), nil
}

// Is is whether a admits exactly the one kind.
func (a Authors) Is(kind string) bool { return len(a) == 1 && a[0] == kind }

// AuthorKind is the kind of author that opened it, as env sees it.
func AuthorKind(it Item, env Env) string {
	switch {
	case anyIn([]string{it.Author}, env.Pool):
		return AuthorPool
	case strings.EqualFold(it.AuthorAssociation, "OWNER"):
		return AuthorOwner
	case strings.EqualFold(it.AuthorAssociation, "MEMBER"), anyIn([]string{it.Author}, env.Members):
		return AuthorMember
	case strings.EqualFold(it.AuthorAssociation, "COLLABORATOR"):
		return AuthorCollaborator
	}
	return AuthorExternal
}

// admitsAuthor is w's author predicate alone.
func (w When) admitsAuthor(it Item, env Env) bool {
	return len(w.Author) == 0 || slices.Contains(w.Author, AuthorKind(it, env))
}
