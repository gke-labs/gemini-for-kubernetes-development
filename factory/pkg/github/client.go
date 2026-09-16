package github

import (
	"errors"
	"net/http"

	githubv39 "github.com/google/go-github/v39/github"
)

// errNoClient reports a Client that was bound to a repository before its
// underlying GitHub client existed. Components are often constructed before
// authentication has happened, so the helpers report this rather than panicking
// on a nil pointer.
var errNoClient = errors.New("github client is not configured")

// Client is a GitHub client bound to a single repository.
//
// It carries the owner and repo that every call needs, so callers stop threading
// that pair through each helper, and it is where repository-scoped operations
// live as methods rather than as free functions over a raw client.
//
// The underlying go-github client is deliberately unexported rather than
// embedded. This type is a boundary, not a convenience alias: everything a
// caller may do to the repository is a method defined here, reviewable in one
// place and fakeable in tests through the narrow interfaces consumers declare.
// Embedding would promote the whole go-github surface through it, and a wrapper
// that also exposes the thing it wraps constrains nothing. The cost is that
// adopting it is incremental - a call site that needs an operation this type
// does not offer yet has to grow one first.
type Client struct {
	gh    *githubv39.Client
	owner string
	repo  string
}

// ForRepo binds an authenticated GitHub client to a repository. Callers that
// need to authenticate first get their client from NewClient.
//
// gh may be nil: the helpers then fail with errNoClient instead of panicking,
// which keeps a component that was wired up before authentication from taking
// the process down on its first call.
func ForRepo(gh *githubv39.Client, owner, repo string) *Client {
	return &Client{gh: gh, owner: owner, repo: repo}
}

// Owner returns the organization or user owning the bound repository.
func (c *Client) Owner() string { return c.owner }

// Repo returns the name of the bound repository.
func (c *Client) Repo() string { return c.repo }

// Ready reports whether the client can issue requests.
//
// Components are often constructed before authentication has happened, so this
// lets a caller skip work that could only fail rather than make the call and
// discard errNoClient.
func (c *Client) Ready() bool { return c != nil && c.gh != nil }

// IsNotFound reports whether a GitHub API call failed because the target does
// not exist. It answers on the response status rather than on the text of the
// error, which is what makes it safe to act on.
func IsNotFound(err error) bool {
	var ghErr *githubv39.ErrorResponse
	return errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusNotFound
}
