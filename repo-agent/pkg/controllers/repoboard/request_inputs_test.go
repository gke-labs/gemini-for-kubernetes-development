package repoboard

import (
	"context"
	"testing"

	"github.com/onsi/gomega"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
)

// A Review, Plan or Triage click's inputs — its instructions — reach its
// run, as any recipe Request's do: a review with instructions is a second
// click with one.
func TestRecipeClickInputsReachTheirRun(t *testing.T) {
	g := gomega.NewWithT(t)
	for _, recipe := range []string{"review", "plan", "triage"} {
		req := launch(recipe, 42)
		req.Spec.Inputs = map[string]string{"instructions": "focus on the error paths"}
		fake := newFakeLauncher()
		board := testBoard(nil)
		board.Spec.Auto.Fix = "off"
		r := newTestReconciler(fake, testGithubClient(`[]`), board, githubSecret(), req)
		_, err := r.Reconcile(context.Background(), boardRequest())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		launches := fake.launches()
		g.Expect(launches).To(gomega.HaveLen(1), recipe)
		var opts *factorycli.RecipeOptions
		switch recipe {
		case "review":
			opts = launches[0].ReviewOpts
		case "plan":
			opts = launches[0].PlanOpts
		case "triage":
			opts = launches[0].TriageOpts
		}
		g.Expect(opts).NotTo(gomega.BeNil(), recipe)
		g.Expect(opts.Inputs).To(gomega.HaveKeyWithValue("instructions", "focus on the error paths"), recipe)
	}
}
