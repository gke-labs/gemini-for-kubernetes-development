# Design Note: Co-Authoring with Robot Accounts

## Context
We want to enable human actors to co-author a PR with a bot.
Currently, when a sandbox is created with a `robotAccount`, the git credentials are set to that robot account.
If a human pushes changes from within the sandbox, the commits are attributed to the robot (both Author and Committer).
Humans may not have permission to push to the robot's fork using their own credentials, or setting up those permissions is cumbersome.
We need a design that allows human actors to cooperate with a robot account, ensuring proper attribution while solving permission constraints.

## Constraints
*   **Robot Identity**: The sandbox is initialized with the robot's git credentials (PAT) for authentication.
*   **Permission Barriers**: Humans may not have write access to the robot's fork.
*   **Attribution**: We want to correctly attribute changes to the human author.
*   **Automation**: Ideally, if a sandbox is created for a specific user (e.g., an assignee), it should be pre-configured for them.

## Options Exploration

### Option 1: Shared Fork with Git Co-Authors (Recommended)
The human uses the robot's credentials to push, but configures their own `user.name` and `user.email` in git.

*   **Mechanism**:
    *   **Authentication**: The sandbox uses the Robot's PAT for `gh` and `git push` operations. The remote `origin` points to the Robot's fork.
    *   **Authorship**: The `git config user.name` and `git config user.email` are set to the *Human's* identity.
    *   **Result**: Commits appear as "Authored by Human" but "Committed by Robot". GitHub UI renders this as "Human with Robot".
*   **Pros**:
    *   Solves the permission issue (uses Robot's write access).
    *   Solves the attribution issue (Human is the Author).
    *   No complex permission delegation required on GitHub.
*   **Cons**:
    *   Requires correct configuration of `git` inside the sandbox.

### Option 2: Permission Delegation (Robot Grants Access)
The robot grants the human collaborator access to its fork.

*   **Mechanism**:
    *   Robot creates fork.
    *   Robot calls GitHub API to add the human as a collaborator.
    *   Human must provide their own PAT to the sandbox.
    *   Human pushes using their own PAT.
*   **Pros**:
    *   Full separation of concerns.
    *   Audit logs show Human pushed.
*   **Cons**:
    *   Complex orchestration.
    *   Requires Human to supply a PAT with `repo` scope.
    *   Robot needs admin rights on the fork.

### Option 3: Human Fork
The robot pushes to the human's fork.

*   **Mechanism**:
    *   Human creates fork.
    *   Robot pushes to it.
*   **Cons**:
    *   Robot needs permission to push to Human's fork (Human must grant it).
    *   Hard to automate.

## Recommendation
**Option 1** is the best immediate solution. It is robust, requires no extra permissions, and correctly attributes authorship.

## Proposed Implementation (Option 1)

### 1. Automated Configuration for Assignees
When `repowatch-controller` creates a sandbox for an Issue or PR:
*   If the Issue/PR has a clear "primary assignee" (or the trigger user), the controller should capture their Name and Email.
*   Currently, `repowatch-controller` overwrites `userName` and `userEmail` with the Robot's details when `RobotAccount` is used.
*   **Change**: Modify `repowatch-controller` to:
    *   Always set `userLogin` (and `githubSecretName`) to the `RobotAccount` (for Auth and Fork location).
    *   Set `userName` and `userEmail` to the **Assignee's** details (if available/applicable), otherwise fallback to Robot.
*   The `IssueSandbox` spec will then carry:
    *   `destination.user.login` = Robot (used for `origin` URL).
    *   `destination.user.name` = Human (used for `git config`).
    *   `destination.user.email` = Human (used for `git config`).
*   The `dev_setup.sh` script already uses these fields correctly to configure `gh` (via ID/Token) and `git` (via Name/Email).

### 2. Manual Helper Script
For cases where the human enters a sandbox not originally assigned to them (or generic):
*   Add a script `gemini-git-setup` (or similar) to the sandbox image.
*   Usage: `gemini-git-setup --name "Jane Doe" --email "jane@example.com"`
*   This script simply runs:
    ```bash
    git config --global user.name "Jane Doe"
    git config --global user.email "jane@example.com"
    ```
    (It does *not* change `gh` auth or `origin` remote).

### 3. Future: Multiple Robots
If multiple robots cooperate, they can use Option 1 as well (Secondary Robot authors commits, Primary Robot pushes them). Or they can use Option 2 if they have distinct permissions.

## Summary
We will decouple **Authentication Identity** (Robot) from **Authorship Identity** (Human) in the `IssueSandbox` specification to enable seamless co-authoring.
