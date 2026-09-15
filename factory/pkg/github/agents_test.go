package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

const (
	testOwner = "test-owner"
	testRepo  = "test-repo"
)

// newTestClient returns a Client bound to test-owner/test-repo and backed by the
// given fake API handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	gh := githubv39.NewClient(nil)
	gh.BaseURL, _ = url.Parse(server.URL + "/")
	return ForRepo(gh, testOwner, testRepo)
}

// contentsHandler serves the .agents directory listing, answering 404 when
// entries is nil, and serves any other path as a single file.
func contentsHandler(t *testing.T, entries []*githubv39.RepositoryContent, files map[string]*githubv39.RepositoryContent) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/test-owner/test-repo/contents/.agents":
			if entries == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(entries)

		default:
			file, ok := files[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(file)
		}
	}
}

func dirEntry(name, entryType string) *githubv39.RepositoryContent {
	return &githubv39.RepositoryContent{Name: &name, Type: &entryType}
}

func TestListAgentFilesKeepsOnlyDefinitions(t *testing.T) {
	c := newTestClient(t, contentsHandler(t, []*githubv39.RepositoryContent{
		dirEntry("triage.md", "file"),
		dirEntry("release.yaml", "file"),
		dirEntry("README.txt", "file"),
		dirEntry("templates", "dir"),
	}, nil))

	paths, err := c.ListAgentFiles(t.Context())
	if err != nil {
		t.Fatalf("ListAgentFiles returned error: %v", err)
	}

	want := []string{".agents/triage.md", ".agents/release.yaml"}
	if len(paths) != len(want) {
		t.Fatalf("ListAgentFiles = %v, want %v", paths, want)
	}
	for i, path := range paths {
		if path != want[i] {
			t.Errorf("path %d = %q, want %q", i, path, want[i])
		}
	}
}

// TestListAgentFilesWithoutAnAgentsDirectory covers the ordinary state of a
// repository that declares no agents: no definitions, not an error.
func TestListAgentFilesWithoutAnAgentsDirectory(t *testing.T) {
	c := newTestClient(t, contentsHandler(t, nil, nil))

	paths, err := c.ListAgentFiles(t.Context())
	if err != nil {
		t.Fatalf("ListAgentFiles returned error for a missing %s directory: %v", AgentsDir, err)
	}
	if len(paths) != 0 {
		t.Errorf("ListAgentFiles = %v, want no paths", paths)
	}
}

func TestListAgentFilesReportsOtherFailures(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})

	if _, err := c.ListAgentFiles(t.Context()); err == nil {
		t.Error("expected a rate limited listing to be an error, got nil")
	}
}

func TestReadAgentFileDecodesContent(t *testing.T) {
	encoding := "base64"
	// "hello" in base64, which is how the contents API returns file bodies.
	content := "aGVsbG8="
	path := ".agents/triage.md"

	c := newTestClient(t, contentsHandler(t, nil, map[string]*githubv39.RepositoryContent{
		"/repos/test-owner/test-repo/contents/" + path: {
			Name:     &path,
			Encoding: &encoding,
			Content:  &content,
		},
	}))

	got, err := c.ReadAgentFile(t.Context(), path)
	if err != nil {
		t.Fatalf("ReadAgentFile returned error: %v", err)
	}
	if got != "hello" {
		t.Errorf("ReadAgentFile = %q, want %q", got, "hello")
	}
}

func TestReadAgentFileMissing(t *testing.T) {
	c := newTestClient(t, contentsHandler(t, nil, nil))

	if _, err := c.ReadAgentFile(t.Context(), ".agents/gone.md"); err == nil {
		t.Error("expected reading a missing definition to be an error, got nil")
	}
}

// TestAgentHelpersWithoutAClient covers a Client bound to a repository before
// authentication happened: the helpers must report it rather than panic on the
// nil embedded client.
func TestAgentHelpersWithoutAClient(t *testing.T) {
	c := ForRepo(nil, testOwner, testRepo)

	if _, err := c.ListAgentFiles(t.Context()); !errors.Is(err, errNoClient) {
		t.Errorf("ListAgentFiles error = %v, want %v", err, errNoClient)
	}
	if _, err := c.ReadAgentFile(t.Context(), ".agents/triage.md"); !errors.Is(err, errNoClient) {
		t.Errorf("ReadAgentFile error = %v, want %v", err, errNoClient)
	}
}
