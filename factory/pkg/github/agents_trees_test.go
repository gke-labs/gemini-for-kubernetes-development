package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

// .agents/'s folders by name, with their tree SHAs; files are not
// folders, and a repository without .agents/ has none.
func TestAgentsTrees(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/contents/.agents", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"type":"dir","name":"recipes","sha":"aaa"},{"type":"dir","name":"workflows","sha":"bbb"},{"type":"file","name":"deps.md","sha":"ccc"}]`)
	})
	mux.HandleFunc("/repos/o/none/contents/.agents", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(srv.URL + "/")

	trees, err := AgentsTrees(context.Background(), gh, "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 || trees["recipes"] != "aaa" || trees["workflows"] != "bbb" {
		t.Errorf("trees = %v", trees)
	}
	if trees, err := AgentsTrees(context.Background(), gh, "o", "none"); err != nil || len(trees) != 0 {
		t.Errorf("no .agents/: %v, %v", trees, err)
	}
}
