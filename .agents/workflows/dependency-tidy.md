---
name: dependency-tidy
description: A workflow that runs go mod tidy on all Go modules in the repository and opens a PR if there are modifications.
schedule: "@weekly"
mode: workflow
---
You are the dependency-tidy workflow agent. Your job is to ensure that all Go modules in this repository are tidy.

We track the workflow state in `.gemini/workflows/dependency-tidy/session-${SESSION_ID}.md`.

Here is the checklist:
1. [ ] Step 1: Run `go mod tidy` on all Go modules and commit changes if any.
2. [ ] Step 2: Verify the PR is merged.

Please execute the following steps:

1. **Check or Initialize Journal**:
   - Check if the journal file `.gemini/workflows/dependency-tidy/session-${SESSION_ID}.md` exists.
   - If not, create the directory and the journal file. Write the two checklist items with unchecked boxes (`[ ]`).
   - If yes, read it to understand the current progress.

2. **Reconcile Step 1**:
   - If Step 1 is not checked yet:
     - Find all directories containing a `go.mod` file (e.g. `factory/`, `repo-agent/`, `overseer/`, `agentsandboxes/`).
     - For each module directory:
       - Run `go mod tidy`.
     - Check `git status --porcelain`.
     - If there are changes in `go.mod` or `go.sum` files:
       - Create a new branch named `chore/dependency-tidy-${SESSION_ID}`.
       - Commit the changes with the message `chore: tidy go modules for session ${SESSION_ID}`.
       - Push the branch and create a Pull Request on GitHub.
       - Update the journal: Mark Step 1 as `[~] Step 1: PR created at <PR_URL>`.
       - Update the parent issue description with the checklist progress and PR link.
       - Exit.
     - If there are no changes:
       - Update the journal: Mark Step 1 as `[x] Step 1: All modules already tidy`.
       - Proceed to Step 2.
   - If Step 1 is marked as `[~] Step 1: PR created at <PR_URL>`:
     - Use the `gh` CLI to check the merge status of that PR: `gh pr view <PR_URL> --json state --jq .state`.
     - If the state is `MERGED`, update the journal: Mark Step 1 as `[x] Step 1: PR merged`.
     - Proceed to Step 2.
     - If the state is not `MERGED`, log that you are still waiting for the PR to be merged, and exit.

3. **Reconcile Step 2**:
   - If Step 1 is completed:
     - Mark Step 2 as completed: `[x] Step 2: Dependencies verified tidy`.
     - Update the parent issue description to show all steps are completed.
     - Close the parent issue using the `gh` CLI: `gh issue close ${SESSION_ID#issue-}`.
