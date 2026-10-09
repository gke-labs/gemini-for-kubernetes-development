package github

import (
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// trustedAssociations are the author_association values of accounts that
// hold write access to a repository, or own it. Text from anyone else on
// GitHub - CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR, FIRST_TIMER, NONE - is
// untrusted and is not placed in an agent prompt.
var trustedAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
}

// IsTrustedAuthor reports whether text written by user, whose relationship
// to the repository is association, may be placed in an agent prompt.
//
// Trusted are:
//   - accounts GitHub reports as OWNER, MEMBER or COLLABORATOR;
//   - GitHub App bots (type "Bot"), which can only act on a repository
//     where an admin installed them;
//   - the logins in trustedLogins, compared case-insensitively. This is the
//     escape hatch for the agent's own account and for organisation members
//     whose private membership makes GitHub report them as CONTRIBUTOR or
//     NONE to a token outside the organisation.
func IsTrustedAuthor(user *githubv39.User, association string, trustedLogins []string) bool {
	if user == nil {
		return false
	}
	if trustedAssociations[strings.ToUpper(association)] {
		return true
	}
	if strings.EqualFold(user.GetType(), "Bot") {
		return true
	}
	login := user.GetLogin()
	if login == "" {
		return false
	}
	for _, t := range trustedLogins {
		if strings.EqualFold(login, t) {
			return true
		}
	}
	return false
}
