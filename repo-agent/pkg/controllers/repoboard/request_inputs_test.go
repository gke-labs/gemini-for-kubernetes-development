package repoboard

import (
	"testing"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// A Review, Plan or Triage click's inputs — its instructions — reach the
// pass that runs it, as a Fix click's do: a review with instructions is a
// second click with one.
func TestRecipeClickInputsReachTheirPass(t *testing.T) {
	instructions := map[string]string{"instructions": "focus on the error paths"}
	click := func(recipe, item string, n int) *boardv1alpha1.Request {
		return &boardv1alpha1.Request{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(recipe)},
			Spec:       boardv1alpha1.RequestSpec{Verb: boardv1alpha1.VerbRecipe, Recipe: recipe, Item: item, Number: n, Member: "ada", Inputs: instructions},
		}
	}
	work := &workState{requests: []*boardv1alpha1.Request{click("review", "pr", 1), click("plan", "issue", 2), click("triage", "issue", 3)}}
	box := (&Reconciler{}).requestMailbox(work)
	if len(box.reviews) != 1 || box.reviews[0].inputs["instructions"] != instructions["instructions"] {
		t.Errorf("reviews = %+v, want the click's instructions", box.reviews)
	}
	if len(box.plans) != 1 || box.plans[0].inputs["instructions"] != instructions["instructions"] {
		t.Errorf("plans = %+v, want the click's instructions", box.plans)
	}
	if len(box.triages) != 1 || box.triages[0].inputs["instructions"] != instructions["instructions"] {
		t.Errorf("triages = %+v, want the click's instructions", box.triages)
	}
}
