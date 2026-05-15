# Overseer CLI (`overseer-cli`)

`overseer-cli` is a command-line tool designed to orchestrate and manage development sandboxes and tasks for PRs, Issues, and Chores within the Gemini for Kubernetes Development ecosystem.

It enables both cluster administrators (to onboard users) and developers (to configure secrets, initialize repositories, and spawn/manage interactive sandboxes).

---

## Building the CLI

To build the binary, run `make build` from the `overseer` directory:

```bash
cd overseer
make build
```

This compiles the binary to `./bin/overseer-cli`.

---

## Global Flags

These flags are persistent and can be used with any subcommand:

*   `--namespace <name>`: The target Kubernetes namespace. Defaults to the `$NAMESPACE` environment variable. If not set, it automatically deduces the namespace from your local git origin remote (matches the owner's username).
*   `--repo <url_or_name>`: The target repository URL (or the `RepoWatch` resource name). Defaults to the `$REPO` environment variable. If not set, it automatically deduces the URL from your local git upstream remote.

---

## 1. Administrator Flow (Onboarding)

Administrators can onboard new users by bootstrapping their dedicated Kubernetes namespace with base configurations (such as the portal CA certificate, devcontainer configuration, and dedicated service accounts).

### Onboard a User
```bash
overseer-cli admin onboard <github-id>
```
*   **What it does:** Creates a namespace named `<github-id>` and runs a simplified bootstrapping process that copies only essential non-legacy secrets and configmaps, and sets up the `review-sandbox` and `issue-sandbox` service accounts with appropriate RBAC.

---

## 2. Developer User Flow

Once onboarded, developers follow this standard flow to configure their environment, initialize repositories, and start orchestrating sandboxes.

### Step 2.1: Configure Secrets

Before initializing a repository, you must configure your personal GitHub PAT and Gemini API tokens in your namespace.

#### Set GitHub PAT
```bash
overseer-cli secret set github-pat <token> [flags]
```
*   **Flags (Optional):**
    *   `--github-name "<name>"`: Your GitHub display name.
    *   `--github-email "<email>"`: Your GitHub email.
*   **Git Config Fallback:** If name or email are not provided, the CLI automatically deduces them from your local git configuration (`user.name` and `user.email`).
*   **Keys Populated:** Automatically populates both `manual_pat` and `pat` keys in the secret data for compatibility across all components.

#### Set Gemini API Token
```bash
overseer-cli secret set gemini <token>
```
*   **What it does:** Creates the `gemini-vscode-tokens` secret in your namespace with the `gemini` key.

#### Clear Secrets
If you need to update or remove secrets:
```bash
overseer-cli secret clear github-pat      # Deletes github-pat secret
overseer-cli secret clear gemini          # Deletes gemini-vscode-tokens secret
overseer-cli secret clear all             # Deletes both secrets
```

---

### Step 2.2: Initialize a Repository

Initialize a repository to tell the system how to handle PRs and Issues for that repo.

```bash
overseer-cli repo init [flags]
```
*   **Flags (Optional):**
    *   `--name <custom-name>`: Customize the `RepoWatch` resource name. Defaults to the repository name from the URL (truncated to 30 characters).
*   **Security Check:** The command automatically verifies that both `github-pat` and `gemini-vscode-tokens` secrets exist in your namespace before creating the resource. If missing, it aborts and prints clear setup instructions.
*   **Safety Lock:** Creates the `RepoWatch` resource with all sandbox limits set to `0` for safety. You must edit the resource (via `overseer-cli repo edit`) to enable sandbox allocations.

#### Manage Repo Configuration
```bash
overseer-cli repo list              # List all RepoWatches
overseer-cli repo get [name]        # Get a structured, human-readable summary of config and status
overseer-cli repo edit [name]       # Open the config in kubectl edit
overseer-cli repo delete [name]     # Delete the RepoWatch config
```
*   **Name Resolution:** For `get`, `edit`, and `delete`, you can omit the `[name]` argument. The CLI will automatically query the cluster to match your current repository URL and resolve the name.

---

### Step 2.3: Orchestrate Sandboxes & Tasks

Once the repo is initialized and limits are increased, you can create sandboxes for issues and PRs.

#### Create Sandbox for a PR
```bash
overseer-cli pr --number <pr-number> [flags]
```
*   **What it does:** Automatically resolves the `RepoWatch` config by URL, fetches the PR details from GitHub, checks counts limits, creates a Review Sandbox (running your generic image and utilizing `review-sandbox` SA), and schedules the `review` task.

#### Create Sandbox for an Issue
```bash
overseer-cli issue --number <issue-number> [flags]
```
*   **What it does:** Automatically resolves the `RepoWatch` config, fetches the Issue details, checks counts limits, creates an Issue Sandbox (utilizing `issue-sandbox` SA), and schedules the `fix-issue` task.

---

### Step 2.4: Interactive Sandbox Management

Manage your running sandboxes directly from the CLI.

#### List Sandboxes
```bash
overseer-cli sandbox list sandboxes
```

#### Connect to a Sandbox (Tmux + SSH)
```bash
overseer-cli sandbox connect <sandbox-name-or-number>
```
*   **What it does:** Resolves the sandbox name, updates your local `~/.ssh/config` with the pod's connection details, and launches a `tmux` session tunneled via SSH directly into the sandbox container.
*   **Shortcut:** You can pass just the issue or PR number (e.g. `overseer-cli sandbox connect 688`) and it will automatically resolve to the correct sandbox.

#### Continue Chat Session
```bash
overseer-cli sandbox chat <sandbox-name-or-number>
```
*   **What it does:** Connects you to the active Gemini agent session inside the sandbox container to continue debugging or pairing.

#### Delete a Sandbox
```bash
overseer-cli sandbox delete sandbox <name>
```
*   **What it does:** Deletes the Sandbox CR and automatically cleans up any leftover LoadBalancer service (`-lb`) to conserve cluster IPs.

---

## Error Handling & Usability

*   **Dynamic Help/Usage:** The CLI dynamically silences the help output on errors. If a command fails due to syntax or missing flags, the full usage help text is shown. If it fails due to runtime issues (e.g. connection failure, limit reached, resource not found), only the clean error message is printed.
*   **Double Error Prevention:** All error printing is unified through Cobra, preventing duplicate error logs.
