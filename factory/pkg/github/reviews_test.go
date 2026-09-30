package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	githubv39 "github.com/google/go-github/v39/github"
)

// graphQLRequest is the body of a GraphQL request as the server sees it.
type graphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

// newGraphQLTestClient serves every request with handler and returns a client
// whose REST base URL points at it, which is where GraphQL is resolved from.
func newGraphQLTestClient(t *testing.T, handler func(w http.ResponseWriter, req graphQLRequest)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			t.Errorf("unexpected request %s %s, want POST /graphql", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding graphql request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, req)
	}))
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	return ForRepo(gh, "owner", "repo")
}

func TestReviewReactions(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, req graphQLRequest) {
		if got := req.Variables["id"]; got != "PRR_node" {
			t.Errorf("id variable = %v, want PRR_node", got)
		}
		_, _ = w.Write([]byte(`{"data":{"node":{"reactions":{"nodes":[
			{"content":"EYES","user":{"login":"factory-bot"}},
			{"content":"THUMBS_UP","user":{"login":"human-dev"}},
			{"content":"SOMETHING_NEW","user":{"login":"human-dev"}},
			{"content":"ROCKET","user":null}
		]}}}}`))
	})

	got, err := c.ReviewReactions(context.Background(), "PRR_node")
	if err != nil {
		t.Fatalf("ReviewReactions returned error: %v", err)
	}

	want := []struct{ content, login string }{
		{"eyes", "factory-bot"},
		{"+1", "human-dev"},
		{"rocket", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d reactions, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].GetContent() != w.content || got[i].GetUser().GetLogin() != w.login {
			t.Errorf("reaction %d = (%q, %q), want (%q, %q)", i, got[i].GetContent(), got[i].GetUser().GetLogin(), w.content, w.login)
		}
	}
}

func TestReviewReactions_NotFound(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, _ graphQLRequest) {
		_, _ = w.Write([]byte(`{"data":{"node":null}}`))
	})
	if _, err := c.ReviewReactions(context.Background(), "PRR_missing"); err == nil {
		t.Error("ReviewReactions returned nil error for a missing review")
	}
}

func TestReviewReactions_PageSize(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, req graphQLRequest) {
		// JSON numbers decode as float64.
		if got := req.Variables["first"]; got != float64(reactionPageSize) {
			t.Errorf("first variable = %v, want %d", got, reactionPageSize)
		}
		_, _ = w.Write([]byte(`{"data":{"node":{"reactions":{"nodes":[]}}}}`))
	})
	if _, err := c.ReviewReactions(context.Background(), "PRR_node"); err != nil {
		t.Fatalf("ReviewReactions returned error: %v", err)
	}
}

func TestReviewReactions_HTTPError(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, _ graphQLRequest) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"bad gateway"}`))
	})
	if _, err := c.ReviewReactions(context.Background(), "PRR_node"); err == nil {
		t.Error("ReviewReactions returned nil error for a non-2xx response")
	}
}

// TestGraphQLDoesNotSpendRESTRateLimit pins that GraphQL's rate-limit headers
// stay out of the REST client's bookkeeping. go-github files every non-search
// response under the core limit and refuses calls locally once it believes
// that limit is spent, so an exhausted GraphQL limit must not stop REST.
func TestGraphQLDoesNotSpendRESTRateLimit(t *testing.T) {
	var restCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/graphql" {
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			_, _ = w.Write([]byte(`{"data":{"node":{"reactions":{"nodes":[]}}}}`))
			return
		}
		restCalls++
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	c := ForRepo(gh, "owner", "repo")

	if _, err := c.ReviewReactions(context.Background(), "PRR_node"); err != nil {
		t.Fatalf("ReviewReactions returned error: %v", err)
	}
	if _, err := c.IssueCommentReactions(context.Background(), 1); err != nil {
		t.Fatalf("REST call after an exhausted GraphQL limit returned error: %v", err)
	}
	if restCalls != 1 {
		t.Errorf("server received %d REST requests, want 1", restCalls)
	}
}

func TestAddReviewReaction(t *testing.T) {
	var calls int
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, req graphQLRequest) {
		calls++
		if got := req.Variables["subjectId"]; got != "PRR_node" {
			t.Errorf("subjectId variable = %v, want PRR_node", got)
		}
		if got := req.Variables["content"]; got != "CONFUSED" {
			t.Errorf("content variable = %v, want CONFUSED", got)
		}
		_, _ = w.Write([]byte(`{"data":{"addReaction":{"reaction":{"content":"CONFUSED"}}}}`))
	})

	if err := c.AddReviewReaction(context.Background(), "PRR_node", "confused"); err != nil {
		t.Fatalf("AddReviewReaction returned error: %v", err)
	}
	if calls != 1 {
		t.Errorf("server received %d requests, want 1", calls)
	}
}

func TestAddReviewReaction_GraphQLError(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, _ graphQLRequest) {
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"Could not resolve to a node"}]}`))
	})
	if err := c.AddReviewReaction(context.Background(), "PRR_node", "+1"); err == nil {
		t.Error("AddReviewReaction returned nil error for a GraphQL error response")
	}
}

func TestAddReviewReaction_UnsupportedContent(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, _ graphQLRequest) {
		t.Error("no request should be sent for an unsupported reaction")
	})
	if err := c.AddReviewReaction(context.Background(), "PRR_node", "not-an-emoji"); err == nil {
		t.Error("AddReviewReaction returned nil error for an unsupported reaction")
	}
}

// TestGraphQLPathResolution pins where GraphQL is found relative to the REST
// base URL on both github.com and GitHub Enterprise.
func TestGraphQLPathResolution(t *testing.T) {
	tests := []struct{ base, want string }{
		{"https://api.github.com/", "https://api.github.com/graphql"},
		{"https://ghe.example.com/api/v3/", "https://ghe.example.com/api/graphql"},
	}
	for _, tt := range tests {
		base, _ := url.Parse(tt.base)
		got, err := base.Parse(graphQLPath)
		if err != nil {
			t.Fatalf("parsing %q against %q: %v", graphQLPath, tt.base, err)
		}
		if got.String() != tt.want {
			t.Errorf("GraphQL URL for base %q = %q, want %q", tt.base, got, tt.want)
		}
	}
}
