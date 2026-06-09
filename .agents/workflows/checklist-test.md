---
name: checklist-test
description: A test workflow that verifies formatting and linting sequentially to test the reconciliation loop.
schedule: never
mode: workflow
---
You are the checklist-test workflow agent. Your job is to run a multi-step verification checklist on this repository.

To support persistent multi-step execution across reconciliation runs, we track state in a journal file at `.agents/workflows/checklist-test/session-${SESSION_ID}.md`.

Here is the checklist we must execute:
1. [ ] Step 1: Run `go mod tidy` in all Go module directories and verify no files are modified.
2. [ ] Step 2: Run `go vet ./...` in the `factory/` directory to check for common Go programming mistakes.

Please perform the following steps:

1. **Check or Initialize Journal**:
   - Check if the journal file `.agents/workflows/checklist-test/session-${SESSION_ID}.md` exists.
   - If it does not exist, initialize it by creating the directory and the file. Write down the two steps with unchecked boxes (`[ ]`).
   - If it exists, read it to understand the current progress.

2. **Reconcile Step 1**:
   - If Step 1 is not checked yet:
     - Run `go mod tidy` in the Go module directories (`factory/`, `repo-agent/`, `overseer/`).
     - Check `git status` or `git diff`.
     - If there are changes:
       - Commit the changes, push them to a new branch, and create a Pull Request.
       - Update the journal: Mark Step 1 as `[~] Step 1: PR created at <PR_URL>` (use the actual PR URL).
       - Print a message indicating you are waiting for the PR to be merged, and exit.
     - If there are no changes, mark Step 1 as completed: `[x] Step 1: Run go mod tidy... (No changes)`.
     - Proceed directly to Step 2 in the same run if you marked it completed.
   - If Step 1 is marked as `[~] Step 1: PR created at <PR_URL>`:
     - Use the `gh` CLI to check the merge status of that Pull Request: `gh pr view <PR_URL> --json state --jq .state`.
     - If the state is not `MERGED`, log that you are still waiting for the PR to be merged, and exit.
     - If the state is `MERGED`, update the journal to mark Step 1 as completed: `[x] Step 1: Run go mod tidy... (Merged <PR_URL>)`.
     - Proceed to Step 2.

3. **Reconcile Step 2**:
   - If Step 1 is completed and Step 2 is not checked yet:
     - Change directory to `factory/` and run `go vet ./...`.
     - If `go vet` fails, report the errors in the journal and exit (do not check the box).
     - If `go vet` succeeds, mark Step 2 as completed: `[x] Step 2: Run go vet ./... (Succeeded)`.

4. **Update Parent Issue & Complete**:
   - Update the description of the parent issue (PR/Issue number: `${SESSION_ID}` or extracted from the environment) using `gh issue edit ${SESSION_ID#issue-} --body <updated_body>` to show the updated checklist state.
   - If all checklist items in the journal are marked as completed (`[x]`), close the parent issue: `gh issue close ${SESSION_ID#issue-}`.
