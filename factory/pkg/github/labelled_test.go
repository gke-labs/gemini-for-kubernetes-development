package github

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// OpenPRsLabelled asks for the labels as one OR filter, follows the pages,
// and returns each pull request as the REST issue listing would.
func TestOpenPRsLabelled(t *testing.T) {
	pages := 0
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, req graphQLRequest) {
		pages++
		labels, _ := req.Variables["labels"].([]interface{})
		if len(labels) != 2 || labels[0] != "overseer/adopt" || labels[1] != "overseer/review" {
			t.Errorf("labels variable = %v", req.Variables["labels"])
		}
		if pages == 1 {
			if req.Variables["after"] != nil {
				t.Errorf("first page asked after %v", req.Variables["after"])
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"items":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
				{"number":7,"title":"Add a thing","createdAt":"2026-10-20T10:00:00Z","updatedAt":"2026-10-23T10:00:00Z","url":"https://github.com/owner/repo/pull/7",
				 "author":{"__typename":"User","login":"alice"},"labels":{"nodes":[{"name":"overseer"},{"name":"overseer/adopt"}]},"assignees":{"nodes":[]}}]}}}}`))
			return
		}
		if req.Variables["after"] != "c1" {
			t.Errorf("second page after %v, want c1", req.Variables["after"])
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
			{"number":9,"title":"Bump","createdAt":"2026-10-21T10:00:00Z","updatedAt":"2026-10-23T11:00:00Z","url":"https://github.com/owner/repo/pull/9",
			 "author":{"__typename":"Bot","login":"dependabot"},"labels":{"nodes":[{"name":"overseer/review"}]},"assignees":{"nodes":[{"login":"bob"}]}}]}}}}`))
	})
	got, err := c.OpenPRsLabelled(context.Background(), []string{"overseer/adopt", "overseer/review"})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(got) != 2 {
		t.Fatalf("%d pages, %d pull requests", pages, len(got))
	}
	a, b := got[0], got[1]
	if a.GetNumber() != 7 || a.GetState() != "open" || !a.IsPullRequest() || a.GetUser().GetLogin() != "alice" || a.GetUpdatedAt().IsZero() {
		t.Errorf("first = %+v", a)
	}
	var names []string
	for _, l := range a.Labels {
		names = append(names, l.GetName())
	}
	if !slices.Equal(names, []string{"overseer", "overseer/adopt"}) {
		t.Errorf("labels %v", names)
	}
	if b.GetUser().GetLogin() != "dependabot[bot]" || len(b.Assignees) != 1 || b.Assignees[0].GetLogin() != "bob" {
		t.Errorf("second = %+v", b)
	}
	if none, err := c.OpenPRsLabelled(context.Background(), nil); none != nil || err != nil || pages != 2 {
		t.Errorf("no labels: %v, %v, %d pages", none, err, pages)
	}
}

// OpenIssuesLabelled asks the issues connection, and returns issues: no
// pull request link.
func TestOpenIssuesLabelled(t *testing.T) {
	c := newGraphQLTestClient(t, func(w http.ResponseWriter, req graphQLRequest) {
		if !strings.Contains(req.Query, "items: issues(") {
			t.Errorf("query %q does not list issues", req.Query)
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
			{"number":12,"title":"It breaks","createdAt":"2026-10-20T10:00:00Z","updatedAt":"2026-10-23T10:00:00Z","url":"https://github.com/owner/repo/issues/12",
			 "author":{"__typename":"User","login":"erin"},"authorAssociation":"CONTRIBUTOR","labels":{"nodes":[{"name":"overseer/triage"}]},"assignees":{"nodes":[]}}]}}}}`))
	})
	got, err := c.OpenIssuesLabelled(context.Background(), []string{"overseer/triage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetNumber() != 12 || got[0].IsPullRequest() || got[0].GetAuthorAssociation() != "CONTRIBUTOR" {
		t.Errorf("got %+v", got)
	}
}
