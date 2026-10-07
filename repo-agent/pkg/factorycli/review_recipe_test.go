package factorycli

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReviewPROf(t *testing.T) {
	sb := func(name, repo string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetName(name)
		if repo != "" {
			u.SetAnnotations(map[string]string{"repo": repo})
		}
		return u
	}
	for _, tc := range []struct {
		sb   *unstructured.Unstructured
		want int
	}{
		{sb(ReviewSandboxName("repo", 7), "repo"), 7},
		{sb("review-repo-7", ""), 7},
		{sb("review-repo-extra-7", "repo-extra"), 0},
		{sb("review-repo-7", "other"), 0},
		{sb("factory-pr-repo-7", "repo"), 0},
		{sb("recipe-repo-7", "repo"), 0},
	} {
		n, ok := ReviewPROf(tc.sb, "repo")
		if (tc.want > 0) != ok || n != tc.want {
			t.Errorf("ReviewPROf(%s) = %d, %v; want %d", tc.sb.GetName(), n, ok, tc.want)
		}
	}
}
