package rules

import "testing"

// {{pool}} in a recipe input is the overseer's bots, comma-separated.
func TestPoolInput(t *testing.T) {
	set, err := LoadFiles(map[string][]byte{
		"implement.yaml": []byte("name: implement\non: issue\norder: 16\nwhen: {labels: {any: [\"{{trigger}}/implement\"]}}\nrun: {recipe: fix, apply: open-pr, inputs: {plan_authors: \"{{pool}}\"}}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	env := testEnv
	env.Pool = []string{"ada-coder-bot", "hopper-coder-bot"}
	ds := Set(set).Decide(Item{Kind: OnIssue, Number: 7, Labels: []string{"overseer/implement"}}, env)
	if len(ds) != 1 || ds[0].Inputs["plan_authors"] != "ada-coder-bot,hopper-coder-bot" {
		t.Errorf("decided %+v", ds)
	}
}
