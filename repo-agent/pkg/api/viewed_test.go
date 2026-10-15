package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// Reading a board's feed stamps it viewed, when the stamp is missing or
// older than two minutes, and leaves a fresh one alone; a failed stamp
// does not fail the read.
func TestFeedReadStampsTheBoardViewed(t *testing.T) {
	ctx := context.Background()
	server, r, dyn := boardTestServer(t, nil, boardCR())
	viewedAt := func() string {
		b, err := dyn.Resource(repoBoardGVR).Namespace("alice").Get(ctx, "myboard", v1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return b.GetAnnotations()[AnnotationViewedAt]
	}
	read := func() int {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/board/myboard/work", nil)
		r.ServeHTTP(w, req)
		return w.Code
	}
	code := read()
	first := viewedAt()
	if _, err := time.Parse(time.RFC3339, first); err != nil {
		t.Fatalf("viewed-at after a read = %q, want a time", first)
	}

	patches := func() int {
		n := 0
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "patch" {
				n++
			}
		}
		return n
	}
	before := patches()
	read()
	if patches() != before {
		t.Error("a fresh stamp was patched again")
	}

	board, _ := dyn.Resource(repoBoardGVR).Namespace("alice").Get(ctx, "myboard", v1.GetOptions{})
	board.SetAnnotations(map[string]string{AnnotationViewedAt: time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)})
	server.stampViewed(ctx, board, time.Now())
	if viewedAt() == board.GetAnnotations()[AnnotationViewedAt] {
		t.Error("a stale stamp was not renewed")
	}

	dyn.PrependReactor("patch", "repoboards", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("patch refused")
	})
	board.SetAnnotations(nil)
	server.stampViewed(ctx, board, time.Now()) // logs, does not panic
	board.SetAnnotations(map[string]string{AnnotationViewedAt: "2000-01-01T00:00:00Z"})
	if _, err := dyn.Resource(repoBoardGVR).Namespace("alice").Update(ctx, board, v1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != code {
		t.Errorf("a read whose stamp failed = %d, want %d as without", got, code)
	}
}
