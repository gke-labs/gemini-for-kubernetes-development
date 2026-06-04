# AI Factory CLI (`factory`)

AI Factory CLI (`factory`) is a powerful, robust, and fully decoupled command-line tool for automating software engineering tasks in Kubernetes sandboxes.

It spins up isolated development environments (`agents.x-k8s.io`), establishes direct port-forwarding to the embedded `envd` Connect-RPC daemon, and executes LLM-powered coding workflows (fixing bugs, reviewing pull requests, and watching repositories) without local side-effects or host dependencies.

## Architecture & Design

```
+-------------------+         +------------------------------------------------+
|   Local Machine   |         |               Kubernetes Cluster               |
|                   |         |                                                |
|  +-------------+  | kubectl |  +------------------------------------------+  |
|  | factory CLI | <========> |  | Pod: factory-issue-917                   |  |
|  +-------------+  | port-   |  |                                          |  |
|         |         | forward |  |  +------------------------------------+  |  |
|         |         | (49983) |  |  | Container: sandbox                 |  |  |
|         |         |         |  |  |                                    |  |  |
|         +------------------------>| Daemon: envd (Connect-RPC)         |  |  |
|                   |         |  |  |                                    |  |  |
|                   |         |  |  |  +------------------------------+  |  |  |
|                   |         |  |  |  | /workspaces/tasks/fix-*/     |  |  |  |
|                   |         |  |  |  |   ├── agent-prompt.txt       |  |  |  |
|                   |         |  |  |  |   ├── pre-script.sh          |  |  |  |
|                   |         |  |  |  |   └── execution.log          |  |  |  |
|                   |         |  |  |  +------------------------------+  |  |  |
|                   |         |  |  +------------------------------------+  |  |
|                   |         |  +------------------------------------------+  |
+-------------------+         +------------------------------------------------+
```

### Key Principles
- **Zero Host Side-Effects**: No temporary file creation on your local machine. All scripts, prompts, and task logs are written directly to timestamped subdirectories inside the sandbox PVC (`/workspaces/tasks/fix-<timestamp>/`).
- **Full Environment Injection & Dynamic Tokens**: The CLI dynamically resolves your GitHub credentials and Gemini API keys from Kubernetes Secrets (or dynamically via a token script if `TOKENSCRIPT_DIR` is set) and injects them directly into the `envd` execution environment.
- **Background Daemon Execution**: Support for running blocking remote tasks in the background (`--background`). The CLI detaches, spawns as a background process, and redirects all stdout and stderr cleanly to a command-specific log file (storing logs under a custom `FACTORY_LOGS` path if set, or in the current directory by default).
- **Live Streaming & Logging**: Standard output and error are streamed live to your terminal while simultaneously being recorded into `execution.log` inside the sandbox.
- **Label-Based Discovery & Session Continuity**: Sandboxes are dynamically aliased to Pull Requests via Kubernetes labels (`factory.gemini.google.com/pr`), allowing `pr watch`, `investigate`, and `address-comments` to reuse existing sandboxes (`fix-...`) and maintain full Gemini chat session history across PR workflows.

### Command Tree
```
factory
 ├── up (Install CRDs & operator components, interactively onboard user)
 ├── fix (Fix a bug for a given GitHub issue URL in a sandbox)
 ├── pr
 │    ├── review (Review a GitHub pull request in a sandbox)
 │    ├── investigate (Investigate CI check failures for a PR in a sandbox)
 │    ├── address-comments (Address review feedback and comments for a PR)
 │    └── watch (Continuously monitor a PR for CI failures or new feedback)
 ├── agent
 │    └── create (Run a custom agent definition in a sandbox)
 ├── watch (Continuously monitor a GitHub repo for failures and assigned issues)
 ├── status (Diagnostic pre-flight checks to verify cluster and factory health)
 ├── user
 │    └── onboard (Onboard a new user by creating a namespace and secret)
 ├── cleanup (Delete sandboxes older than a specified duration)
 └── sandbox
      ├── list (List sandboxes in the current namespace)
      ├── delete (Delete a sandbox and its load-balancer service)
      ├── cp (Copy files into sandbox)
      ├── exec (Run interactive commands with env/cwd injection)
      ├── connect (Connect to a sandbox via interactive tmux session)
      ├── chat (Connect to a sandbox and resume a Gemini CLI chat session)
      ├── inspect (Inspect sandbox status, PVC usage, pod info)
      └── logs (Stream task execution logs or envd daemon logs)
```

---

## Installation & Setup

### Prerequisites
- `kubectl` configured to communicate with your Kubernetes or Kind cluster.
- `gh` (GitHub CLI) installed and authenticated (`gh auth login`).
- `GEMINI_API_KEY` environment variable exported in your local terminal.

### 1. Build or Run the CLI
**Option A: Build Locally**
```bash
cd factory/
make build

# Set up a local alias
alias factory="./bin/factory"
```

**Option B: Run Directly via Go**
You can execute the CLI directly from the repository without cloning or building locally by setting up a shell alias:
```bash
alias factory="go run github.com/gke-labs/gemini-for-kubernetes-development/factory@main"

# Now you can run all commands natively
factory --help
```
*(Note: In the following examples, both aliases allow you to use `factory` directly).*

### 2. Cluster Bootstrap (`factory up`)
Spin up operator components and install required CRDs (`agents.x-k8s.io/v1alpha1`).

**Option A: Create a New Kind Cluster (Default)**
```bash
GEMINI_API_KEY=yourkey factory up
```

**Option B: Connect to an Existing Cluster**
If you already have an active Kubernetes cluster configured in your `KUBECONFIG`:
```bash
GEMINI_API_KEY=yourkey factory up --current-context
```

**User Onboarding**:
User onboarding is automatically performed during `factory up` if `gh` CLI is installed and authenticated (`gh auth login`). The CLI deduces your GitHub username, email, and token, displays them for confirmation, and provisions a dedicated namespace and `factory-user` Kubernetes Secret.

If automatic onboarding is skipped or fails, you can configure your identity and keys manually at any time. You can also onboard to a custom namespace by using the global `-n` or `--namespace` flag:
```bash
factory -n my-custom-namespace user onboard \
  --github-login yourlogin \
  --github-token yourpat \
  --github-email youremail \
  --gemini-key yourkey
```

### 3. Diagnostic Check (`factory status`)
Verify system health before running workflows:
```bash
factory status
```
Example Output:
```
CHECK            STATUS   MESSAGE
Kubernetes API   [OK]     Connected to cluster
Namespace        [OK]     barney-s
Agent CRDs       [OK]     agents.x-k8s.io/v1alpha1 installed
GitHub Login     [OK]     barney-s
GitHub Token     [OK]     Configured in secret 'factory-user'
Gemini Key       [OK]     Configured in secret 'factory-user'
```

---

## Usage & AI Workflows

### Fixing Issues (`factory fix`)
Automatically spin up a sandbox, clone the repository, checkout a dedicated issue branch, run Gemini to fix the bug, and open a Pull Request:
```bash
factory fix --url https://github.com/owner/repo/issues/1
```

**Advanced Customization**: Override the default instruction, provide an instruction file, execute repository-level tasks without an issue, push a branch without creating a PR, or automatically transition to watching the created PR:
```bash
factory fix \
  --url https://github.com/owner/repo/issues/1 \
  --instruction "Use Go 1.26 and ensure 100% test coverage" \
  --image kind.local/factory-golang:latest \
  --workspace-disk-size 20Gi

# Execute a custom task on a repository without an issue number (requires --name)
factory fix \
  --url https://github.com/owner/repo \
  --name refactor-auth \
  --instruction "Refactor the auth package"

# Read instruction from a file
factory fix \
  --url https://github.com/owner/repo \
  --name refactor-auth \
  --instruction-file ./prompt.txt

# Commit changes and push branch remotely, but do not create a pull request
factory fix \
  --url https://github.com/owner/repo/issues/1 \
  --name refactor-auth \
  --instruction "Refactor the auth package" \
  --no-pr

# Automatically alias the sandbox to the created PR, start watching it, and cleanup on completion
factory fix \
  --url https://github.com/owner/repo/issues/1 \
  --watch \
  --watch-timeout 1h \
  --cleanup
```

### Reviewing Pull Requests (`factory pr review`)
Spin up a review sandbox to analyze diffs and provide constructive feedback. You can pass multiple instructions as either paths to files containing guidelines (located locally or in the repo) or as inline raw instruction strings:
```bash
# Review a PR with standard options
factory pr review --pr-url https://github.com/owner/repo/pull/1

# Review a PR with file-based instructions
factory pr review --pr-url https://github.com/owner/repo/pull/1 --instruction docs/guidelines.md

# Review a PR with inline/raw instruction strings
factory pr review --pr-url https://github.com/owner/repo/pull/1 \
  --instruction "focus on the memory allocations" \
  --instruction "ignore changes in test files"

# Review a PR in the background and post as a draft review comment on GitHub
factory pr review --pr-url https://github.com/owner/repo/pull/1 \
  --instruction docs/guidelines.md \
  --publish draft \
  --background
```

### Investigating Check Failures (`factory pr investigate`)
Spin up a review sandbox to analyze failed CI check logs, review previous investigation comments, and attempt to fix the failure or report root causes. Use `--continue-session` to preserve LLM conversation history across multiple PR operations in the same sandbox:
```bash
factory pr investigate --pr-url https://github.com/owner/repo/pull/1 --continue-session
```

### Addressing Review Comments (`factory pr address-comments`)
Spin up a review sandbox to parse new review feedback and PR comments, execute code fixes, and push updated commits. Use `--continue-session` to maintain full Gemini chat context from previous fixes or investigations:
```bash
factory pr address-comments --pr-url https://github.com/owner/repo/pull/1 --continue-session
```

### Watching Pull Requests (`factory pr watch`)
Continuously monitor a specific PR in the foreground, automatically triggering `investigate` on CI failures or `address-comments` on new review feedback. Use `--continue-session` to ensure all dispatched tasks inherit the ongoing chat session. The watch loop logs explicit sleep intervals and cleanly terminates when the PR is merged, closed, or timeout expires:
```bash
factory pr watch --pr-url https://github.com/owner/repo/pull/1 --watch-timeout 1h --cleanup
```

### Running Custom Agents (`factory agent create`)
Automatically spin up a sandbox, clone the repository (or PR branch), retrieve a custom agent definition file, execute its prompt instructions inside the sandbox, and automatically commit/push/create/update a Pull Request depending on your configuration and whether it was triggered on a PR or a repository:
```bash
# Run an agent defined in the remote .agents/my-agent.yaml on a repository
factory agent create --url https://github.com/owner/repo --agent my-agent.yaml
```

**Advanced Customization**:
```bash
# Run an agent defined locally in a sandbox for a PR
factory agent create \
  --url https://github.com/owner/repo/pull/123 \
  --agent ./my-agent.yaml \
  --local

# Simulate agent execution (dry-run) without invoking the LLM inside the sandbox
factory agent create \
  --url https://github.com/owner/repo \
  --agent my-agent.yaml \
  --dry-run
```

### Watching Repositories (`factory watch`)
Continuously monitor a GitHub repository in the foreground for test failures, assigned issues, or open pull requests, automatically dispatching `fix`, `investigate`, or `address-comments` tasks.

**Key Features**:
- **Assignee & Label Filtering**: By default, it monitors issues and PRs assigned to the onboarded user (resolved from the `factory-user` secret) OR labelled `overseer`. You can override the assignee with `--assignee` (e.g. `--assignee ""` to watch for unassigned issues/PRs).
- **PR Check Failure & Comment Watching**: For watched PRs, it automatically dispatches `investigate` on CI test failures and `address-comments` on new review comments since the last commit.
- **Smart Issue Link Detection**: To prevent redundant work, it will automatically skip triggering a fix task for an issue if it finds an open PR referencing that issue (checked via branch names, PR titles, PR bodies, or the GitHub Timeline API).
- **Dry-run Execution**: Fully respects the `--dryrun` flag, printing actions without creating sandboxes or starting tasks.

```bash
# Watch for issues/PRs assigned to the onboarded user (from secret) or labelled "overseer"
factory watch --repo owner/repo

# Watch for unassigned issues/PRs with specific labels
factory watch --repo owner/repo --assignee "" --labels "bug,help wanted"

# Simulate watch loop actions without executing any tasks or creating sandboxes
factory watch --repo owner/repo --dryrun
```

---

## Sandbox Management & Debugging

### Resuming Chat Sessions (`factory sandbox chat`)
Connect to an active sandbox container and resume a Gemini CLI chat session with automatic repository detection, session backup/restore, and `GEMINI_API_KEY` environment injection:
```bash
# Resume the latest chat session in the sandbox
factory sandbox chat factory-issue-917

# List all available saved sessions for the project
factory sandbox chat factory-issue-917 -l

# Resume a specific session by index or ID
factory sandbox chat factory-issue-917 -r 2
```

### Executing Commands (`factory sandbox exec`)
Run interactive commands in an active sandbox with environment variable injection and custom working directories:
```bash
factory sandbox exec factory-issue-917 -e FOO=bar -w /workspaces/my-repo -- make test
```

### Inspecting Sandboxes (`factory sandbox inspect`)
View metadata, PVC status, and active pod resolution:
```bash
factory sandbox inspect factory-issue-917
```

### Streaming Logs (`factory sandbox logs`)
Stream either the active task execution transcript or the underlying `envd` daemon logs:
```bash
# Stream task execution log (execution.log)
factory sandbox logs factory-issue-917

# Stream envd daemon logs
factory sandbox logs factory-issue-917 --daemon
```

### Copying Files (`factory sandbox cp`)
Copy files directly into a specific path in the sandbox container:
```bash
factory sandbox cp factory-issue-917 ./local-script.sh /workspaces/script.sh
```

### Listing & Deleting Sandboxes
```bash
# List all active sandboxes in your namespace with active tasks and PR/issue URLs
factory sandbox list

# Delete a sandbox and its load-balancer service
factory sandbox delete factory-issue-917
```

### Cleaning up Sandboxes (`factory cleanup`)
Delete sandboxes older than a specified duration (default 24h).
```bash
# Delete sandboxes older than 1 day
factory cleanup

# Delete sandboxes older than 6 hours
factory cleanup --older-than 6h
```

---

## Design Footnote: Directory-Based Queue Design for `factory watch`

To support decoupled operation and integration with external schedulers (e.g. Overseer prompt or custom scripts), `factory watch` supports a directory-based queueing model. 

```
                  ┌──────────────────────┐
                  │  External Systems /  │
                  │   Overseer Prompt    │
                  └──────────┬───────────┘
                             │ (writes task file)
                             ▼
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│factory watch │     │   incoming/  │     │factory watch │
│  --mode=scan ├────>│ task-123.yaml├────>│  --mode=run  │
└──────────────┘     └──────────────┘     └──────┬───────┘
                                                 │ (runs sandbox)
                                                 ▼
                                          ┌──────────────┐
                                          │  processing/ │
                                          └──────┬───────┘
                                                 │ (completed)
                                                 ▼
                                          ┌──────────────┐
                                          │  processed/  │
                                          └──────────────┘
```

By splitting the command execution into `--mode=scan` (discovers issues/PRs/chores, unassigns from the bot, and writes deterministic YAML files to `incoming/`) and `--mode=run` (reads from queue directories, respects concurrency and execution limits, and runs tasks), multiple daemons can safely interact asynchronously via POSIX-atomic file moves without file lock conflicts.

