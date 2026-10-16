package prs

import (
	"slices"
	"testing"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

// A rebase, an investigation and a review are decided at a head; a
// comments task holds across a new commit; a task without a head records
// nothing to check.
func TestExpectFor(t *testing.T) {
	for _, tc := range []struct {
		t    api.TaskType
		sha  string
		want []string
	}{
		{api.TypePRIterate, "abc", []string{api.ExpectHead}},
		{api.TypePRInvestigate, "abc", []string{api.ExpectHead}},
		{api.TypePRReview, "abc", []string{api.ExpectHead}},
		{api.TypePRComments, "abc", nil},
		{api.TypePRInvestigate, "", nil},
	} {
		if got := expectFor(tc.t, tc.sha); !slices.Equal(got, tc.want) {
			t.Errorf("expectFor(%s, %q) = %v, want %v", tc.t, tc.sha, got, tc.want)
		}
	}
}
