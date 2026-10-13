package tasks

// GetRecipeStepScript returns the script a recipe's uses steps run: lib.sh
// and a dispatcher that calls one of its functions (pkg/recipe).
func GetRecipeStepScript() ([]byte, error) {
	return getScriptWithDefaults("recipe_step.sh")
}
