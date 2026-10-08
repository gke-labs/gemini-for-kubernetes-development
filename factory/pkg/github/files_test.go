package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestReadFile(t *testing.T) {
	var ref string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/test-owner/test-repo/commits":
			if r.URL.Query().Get("path") == "kinds.json" {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"sha": "abc123"}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/repos/test-owner/test-repo/contents/kinds.json":
			ref = r.URL.Query().Get("ref")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(`["a"]`)),
			})
		default:
			http.NotFound(w, r)
		}
	})

	data, sha, err := c.ReadFile(context.Background(), "kinds.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `["a"]` || sha != "abc123" || ref != "abc123" {
		t.Errorf("ReadFile() = %q at %q, read at ref %q", data, sha, ref)
	}

	// A path no commit touched is not on the default branch.
	if _, _, err := c.ReadFile(context.Background(), "missing.json"); !errors.Is(err, ErrUnreadableFile) {
		t.Errorf("ReadFile(missing) error = %v, want ErrUnreadableFile", err)
	}
}
