package github

import (
	"errors"
	"net/http"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestForRepoRecordsTheRepository(t *testing.T) {
	c := ForRepo(githubv39.NewClient(nil), testOwner, testRepo)

	if c.Owner() != testOwner {
		t.Errorf("Owner() = %q, want %q", c.Owner(), testOwner)
	}
	if c.Repo() != testRepo {
		t.Errorf("Repo() = %q, want %q", c.Repo(), testRepo)
	}
}

func TestIsNotFound(t *testing.T) {
	notFound := &githubv39.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}}
	forbidden := &githubv39.ErrorResponse{Response: &http.Response{StatusCode: http.StatusForbidden}}

	if !IsNotFound(notFound) {
		t.Error("IsNotFound(404) = false, want true")
	}
	// A wrapped error must still be recognised: callers add context before the
	// error reaches whoever decides what it means.
	if !IsNotFound(errors.Join(errors.New("listing .agents"), notFound)) {
		t.Error("IsNotFound(wrapped 404) = false, want true")
	}
	if IsNotFound(forbidden) {
		t.Error("IsNotFound(403) = true, want false")
	}
	if IsNotFound(errors.New("some other failure")) {
		t.Error("IsNotFound(plain error) = true, want false")
	}
	if IsNotFound(nil) {
		t.Error("IsNotFound(nil) = true, want false")
	}
}
