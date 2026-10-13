package tasks

// GetRecipeStepScript returns the script a recipe's uses steps run: lib.sh,
// the runbook recipe's functions (run.sh), and a dispatcher that calls one
// of them (pkg/recipe).
func GetRecipeStepScript() ([]byte, error) {
	lib, err := getScriptWithDefaults("run.sh")
	if err != nil {
		return nil, err
	}
	step, err := scriptsFS.ReadFile("recipe_step.sh")
	if err != nil {
		return nil, err
	}
	return append(append(lib, '\n'), step...), nil
}
