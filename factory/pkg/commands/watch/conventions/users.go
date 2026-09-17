package conventions

import (
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// AssignedBotUser returns the first bot account from the pool assigned to an
// issue or pull request, or an empty string when none is.
//
// Assignment is how a task is claimed, so this is what tells the scanners that
// an entity is already somebody's - and which account's.
func AssignedBotUser(issue *githubv39.Issue, botUsers []string) string {
	for _, u := range issue.Assignees {
		for _, bot := range botUsers {
			if strings.EqualFold(u.GetLogin(), bot) {
				return u.GetLogin()
			}
		}
	}
	return ""
}

// IsReviewerBot reports whether a user is one of the review bots, whose
// comments are treated as review feedback to act on rather than as noise to
// ignore.
//
// The configured reviewer accounts are authoritative; the 'reviewbot' name
// match is a fallback for deployments that never configured the role.
func IsReviewerBot(user *githubv39.User, reviewerLogins []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	for _, u := range reviewerLogins {
		if strings.EqualFold(login, u) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(login), "reviewbot")
}

// IsBotReply reports whether a comment came from the watcher itself, from an
// allowlisted bot, or from any other account that is ignored by default.
//
// It is the "has something already answered this?" question, which is why the
// allowlist counts as a bot here while ShouldIgnoreUser deliberately excludes it.
func IsBotReply(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true
	}
	for _, b := range allowlistedBots {
		if strings.EqualFold(login, b) {
			return true
		}
	}
	return ShouldIgnoreUser(user, githubLogin, nil)
}

// ShouldIgnoreUser reports whether a comment author's feedback must not be
// acted on: the watcher's own account, and automated accounts that were not
// allowlisted.
//
// The allowlist is what makes a bot's feedback count - a CI bot reporting a
// failure is worth acting on, an unrelated one spamming the thread is not.
func ShouldIgnoreUser(user *githubv39.User, githubLogin string, allowlistedBots []string) bool {
	if user == nil {
		return false
	}
	login := user.GetLogin()
	if strings.EqualFold(login, githubLogin) {
		return true // always ignore our own bot
	}

	loginLower := strings.ToLower(login)
	isBotUser := strings.EqualFold(user.GetType(), "Bot") ||
		strings.HasSuffix(loginLower, "[bot]") ||
		strings.HasSuffix(loginLower, "-bot") ||
		strings.HasSuffix(loginLower, "-robot") ||
		strings.Contains(loginLower, "prow")

	if isBotUser {
		// Check if it's in the allowlist
		for _, b := range allowlistedBots {
			if strings.EqualFold(login, b) {
				return false // DO NOT ignore (it is allowlisted)
			}
		}
		return true // ignore since it is not allowlisted
	}

	return false
}
