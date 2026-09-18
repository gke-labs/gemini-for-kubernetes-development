package sandbox

import (
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReviewSandboxName(t *testing.T) {
	cases := []struct {
		repo string
		pr   int
		want string
	}{
		{"agent-sandbox", 1438, "factory-pr-agent-sandbox-1438"},
		{"Repo_With.Caps", 7, "factory-pr-repo-with-caps-7"},
		{strings.Repeat("verylongreponame", 5), 123456, ""},
	}
	for _, c := range cases {
		got := ReviewSandboxName(c.repo, c.pr)
		if c.want != "" && got != c.want {
			t.Errorf("ReviewSandboxName(%q, %d) = %q, want %q", c.repo, c.pr, got, c.want)
		}
		// The companion "<name>-lb" Service must fit a 63-char DNS label.
		if len(got)+len("-lb") > 63 {
			t.Errorf("ReviewSandboxName(%q, %d) = %q: too long for the -lb Service", c.repo, c.pr, got)
		}
		if !strings.HasPrefix(got, "factory-pr-") || !strings.HasSuffix(got, "-"+strconv.Itoa(c.pr)) {
			t.Errorf("ReviewSandboxName(%q, %d) = %q: malformed", c.repo, c.pr, got)
		}
	}
}

func sbWith(annotations map[string]string) *unstructured.Unstructured {
	sb := &unstructured.Unstructured{Object: map[string]interface{}{}}
	sb.SetAnnotations(annotations)
	return sb
}

// PR numbers repeat across repos; adoption must be repo-scoped.
func TestSandboxBelongsToRepo(t *testing.T) {
	url := "https://github.com/org/repo-a/pull/5"
	if !sandboxBelongsToRepo(sbWith(map[string]string{"repo": "repo-a"}), "repo-a", url) {
		t.Error("repo annotation match rejected")
	}
	if sandboxBelongsToRepo(sbWith(map[string]string{"repo": "repo-b"}), "repo-a", url) {
		t.Error("adopted a sandbox from another repo")
	}
	if !sandboxBelongsToRepo(sbWith(map[string]string{"htmlURL": url}), "repo-a", url) {
		t.Error("htmlURL fallback match rejected")
	}
	if sandboxBelongsToRepo(sbWith(map[string]string{}), "repo-a", url) {
		t.Error("adopted a sandbox with no provenance")
	}
}
