package commands

import (
	"context"
	"fmt"
	"time"
)

// proposeFanout runs the fanout recipe on a fan-out parent with no spec and
// applies its result: the spec comment, and the stop label holding it for a
// maintainer. Run again for the same parent, it follows the recipe's last run
// there if that one is running or not yet applied (runRecipe's --apply), so a
// restarted watcher does not start a second; once it is applied, as when the
// spec comment was deleted, it starts a new one. user is the account the
// task runs as, the watcher's choice for the issue; empty, the watcher's own.
func proposeFanout(ctx context.Context, issueURL, user string) error {
	it, err := parseRecipeTarget(issueURL)
	if err != nil {
		return err
	}
	runName := fmt.Sprintf("fanout-%d-%d", it.Number, time.Now().Unix())
	return runRecipe(ctx, "fanout", issueURL, runName, "", user, false, applyMode{on: true}, map[string]string{}, nil)
}
