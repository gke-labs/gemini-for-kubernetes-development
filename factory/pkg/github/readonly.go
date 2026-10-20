package github

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	githubv39 "github.com/google/go-github/v39/github"
)

// NewReadOnlyClient is NewClient whose requests can only read: a GET, a
// HEAD, or a GraphQL query; any other request fails before it leaves. For
// a command that runs code which may write (a scanner, dry-run) and must
// not (factory watch rules explain).
func NewReadOnlyClient(ctx context.Context) (*githubv39.Client, error) {
	token, err := GetGithubToken(ctx)
	if err != nil {
		return nil, err
	}
	hc := cachedHTTPClient(ctx, token)
	hc.Transport = readOnly{next: hc.Transport}
	return githubv39.NewClient(hc), nil
}

// readOnly refuses every request that is not a read.
type readOnly struct{ next http.RoundTripper }

func (t readOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	switch {
	case req.Method == http.MethodGet || req.Method == http.MethodHead:
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/graphql") && isGraphQLQuery(req):
	default:
		return nil, fmt.Errorf("read-only client: refusing %s %s", req.Method, req.URL.Path)
	}
	return t.next.RoundTrip(req)
}

// isGraphQLQuery reports whether a GraphQL request is a query, not a
// mutation, leaving its body readable.
func isGraphQLQuery(req *http.Request) bool {
	if req.Body == nil {
		return true
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false
	}
	return !bytes.Contains(bytes.ToLower(body), []byte("mutation"))
}
