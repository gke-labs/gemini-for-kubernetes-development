package github

import (
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestIsTrustedAuthor(t *testing.T) {
	user := func(login, typ string) *githubv39.User {
		return &githubv39.User{Login: githubv39.String(login), Type: githubv39.String(typ)}
	}
	cases := []struct {
		name        string
		user        *githubv39.User
		association string
		trusted     []string
		want        bool
	}{
		{"owner", user("alice", "User"), "OWNER", nil, true},
		{"member", user("alice", "User"), "MEMBER", nil, true},
		{"collaborator", user("alice", "User"), "COLLABORATOR", nil, true},
		{"association is case-insensitive", user("alice", "User"), "collaborator", nil, true},
		{"contributor", user("mallory", "User"), "CONTRIBUTOR", nil, false},
		{"first-time contributor", user("mallory", "User"), "FIRST_TIME_CONTRIBUTOR", nil, false},
		{"none", user("mallory", "User"), "NONE", nil, false},
		{"empty association", user("mallory", "User"), "", nil, false},
		{"app bot", user("gemini-code-assist[bot]", "Bot"), "NONE", nil, true},
		{"bot-looking login is not a bot", user("lookalike[bot]", "User"), "NONE", nil, false},
		{"trusted login", user("Codebot-Robot", "User"), "NONE", []string{"codebot-robot"}, true},
		{"empty trusted login matches nothing", user("", "User"), "NONE", []string{""}, false},
		{"nil user", nil, "OWNER", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTrustedAuthor(tc.user, tc.association, tc.trusted); got != tc.want {
				t.Errorf("IsTrustedAuthor() = %v, want %v", got, tc.want)
			}
		})
	}
}
