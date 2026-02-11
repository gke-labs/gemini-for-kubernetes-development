# Co-authoring with Robot Accounts

The Repo Agent supports a seamless "co-authoring" workflow where human developers can collaborate directly with the robot account inside the same sandbox environment. This allows you to fix bugs, improve code, or guide the agent without needing to check out the branch locally or manage complex git permissions.

## Overview

When a sandbox is created for an issue or pull request that is **assigned to you**, the Repo Agent automatically configures the git environment to match your identity.

*   **Attribution**: The git configuration (`user.name` and `user.email`) is set to **Your** GitHub profile details. Any commits you make will be authored by you, even though they are pushed by the robot.

## How to Use

### 1. Enter the Sandbox
You can access the sandbox environment in two ways:
*   **Web Terminal**: Click the terminal icon (`>_`) in the Review UI.
*   **VS Code**: Click on the "Sandbox" icon to open VS Code.

### 2. Make Changes
Once inside, you are in a fully functional development environment. You can edit files, run builds, and test changes.

### 3. Commit and Push
When you are ready, use standard git commands:

```bash
# Stage your changes
git add .

# Commit (Your identity is already configured!)
git commit -m "Fix: handled edge case in validation logic"

# Push (Uses the Robot's credentials)
git push
# Or force push if necessary
git push origin --force
```

The commit will show up on GitHub as **Authored by You**.

## Attribution Details

*   **Your Commits**: When you run `git commit`, the `Author` and `Committer` fields are set to your GitHub Name and Email.
*   **Robot Commits**: When the agent runs (e.g., `gemini-cli`), it explicitly overrides the authorship to use the Robot's identity.

This ensures a clear history of who did what, even within the same branch.

## Troubleshooting

### Wrong Attribution?
If your commits are showing up as the Robot or a generic user:
1.  Check your current config:
    ```bash
    git config user.name
    git config user.email
    ```
2.  If these are incorrect, you can manually fix them for the session:
    ```bash
    git config user.name "Your Name"
    git config user.email "your.email@example.com"
    ```
    *(Note: This manual change will only last for the current container lifecycle).*

### Push Failed?
If `git push` fails with a permission error:
*   Ensure the Robot account has write access to the repository (or fork).
*   Verify that the remote `origin` is using the correct URL (it should be the Robot's fork).
    ```bash
    git remote -v
    ```
