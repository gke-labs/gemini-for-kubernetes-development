package commands

import (
	"fmt"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/config"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/github"
	githubv39 "github.com/google/go-github/v39/github"
)

// trustedLogins is the list of extra logins whose text may reach an agent
// prompt: the configured trusted logins (see config.FactoryConfig.TrustedLogins)
// plus the given identities (the agent's own account, a PR's author). Empty
// entries are dropped.
func trustedLogins(cfg *config.FactoryConfig, extra ...string) []string {
	out := cfg.TrustedLogins()
	for _, l := range extra {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// trustedIssueComments drops the comments whose authors are not trusted (see
// github.IsTrustedAuthor), so that only feedback from trusted authors is
// given to the agent. Each dropped comment is logged so an operator can tell
// why the agent did not see it.
func trustedIssueComments(comments []*githubv39.IssueComment, trusted []string) []*githubv39.IssueComment {
	var out []*githubv39.IssueComment
	for _, c := range comments {
		if !github.IsTrustedAuthor(c.GetUser(), c.GetAuthorAssociation(), trusted) {
			fmt.Printf("Leaving comment %d by @%s (%s) out of the prompt: author is not trusted\n", c.GetID(), c.GetUser().GetLogin(), c.GetAuthorAssociation())
			continue
		}
		out = append(out, c)
	}
	return out
}
