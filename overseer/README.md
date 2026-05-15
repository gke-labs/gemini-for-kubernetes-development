# Overseer

Overseer is an autonomous agent responsible for orchestrating other agents and managing the state of a repository in a Kubernetes-based agentic system.

## Components

- `cmd/overseer-cli`: A CLI tool used by the Overseer agent to manage sandboxes and tasks.
- `pkg/overseer`: Go package for reconciling Overseer sandboxes.
- `images/overseer`: Dockerfile and scripts for the Overseer agent image.

## Getting Started with kind

To try out Overseer locally, you can easily spin it up in a `kind` cluster.

### 1. Set Environment Variables

Before running the setup, you need to provide your Gemini API key and GitHub credentials for the agent's robot account. These are used to create the necessary Kubernetes secrets during setup:

```bash
export GEMINI_API_KEY="your-gemini-api-key"
export ROBOT1_GH_PAT="your-github-personal-access-token"
export ROBOT1_GH_USERID="your-github-username"
export ROBOT1_GH_NAME="Your Name"
export ROBOT1_GH_EMAIL="your-email@example.com"
```

### 2. Deploy

Simply run `make` in the `overseer` directory:

```bash
make
```

This will automatically check prerequisites, create a `kind` cluster named `overseer-agent`, install required CRDs, create the secrets from your environment variables, build the images, and deploy the Overseer controller.

### 3. Watch a Repository

To instruct Overseer to start watching a repository, create an `Overseer` Custom Resource (CR).

Here is an example that watches a repo, enables background chores, and disables automatic PR/issue handling. Create a file named `my-overseer.yaml`:

```yaml
apiVersion: overseer.gemini.google.com/v1alpha1
kind: Overseer
metadata:
  name: my-repo-agent
spec:
  repoURL: https://github.com/your-org/your-repo
  robotAccount: your-github-username # Must match ROBOT1_GH_USERID
  geminiAPIKeySecretName: gemini-vscode-tokens
  # Enable chores. This looks for .agents/<chore files>
  # and for each chore file we start a sandbox to run the agent in it.
  chores:
    mode: enabled
  # Disable Issue/PR handling
  # this is important if you dont want overseer to start sending PRs and reviews on a 
  # public repo
  repo:
    issueMode: disabled
    prMode: disabled
    reviewMode: disabled
```

Apply it to your cluster:

```bash
kubectl apply -f my-overseer.yaml
```

### 4. Using ConfigDir

You can optionally inject custom agent instructions or context files (such as `.agents/file.md`) directly into the repository sandbox by defining a `ConfigDir` resource and referencing it in the `Overseer` spec. This is particularly useful for adding chore definitions without committing them directly to the repository.

Here is an example of defining a `ConfigDir` and referencing it:

```yaml
apiVersion: configdir.gke.io/v1alpha1
kind: ConfigDir
metadata:
  name: my-agent-config
spec:
  files:
  - path: .agents/file.md
    source:
      inline: |
        # Custom Agent Instructions
        These are project-specific guidelines for the agent...
---
apiVersion: overseer.gemini.google.com/v1alpha1
kind: Overseer
metadata:
  name: my-repo-agent
spec:
  repoURL: https://github.com/your-org/your-repo
  robotAccount: your-github-username
  geminiAPIKeySecretName: gemini-vscode-tokens
  # Inject the ConfigDir defined above
  configdirRef: my-agent-config
  chores:
    mode: enabled
```

### 5. View Logs

**Overseer Controller Logs:**
The controller manages the lifecycle of the Overseer sandboxes.
```bash
kubectl logs -n overseer-system -l app=overseer-controller -f
```

**Overseer Agent Logs:**
When you apply an `Overseer` CR, the controller creates a dedicated namespace (e.g., `overseer-my-repo-agent`) and deploys the agent sandbox there. To watch the agent's autonomous loop and see what it is doing:
```bash
kubectl logs -n overseer-my-repo-agent -l sandbox=overseer-my-repo-agent -f
```

## `overseer-cli` Reference

`overseer-cli` is a command-line tool used by the Overseer agent and developers to manage sandboxes, RepoWatch resources, Kubernetes secrets, and agentic tasks. 

### Command Tree & Hierarchy

The CLI features a clean, flat, and highly consistent command hierarchy:

```
overseer-cli
├── issue [--number N] [--pr P] [--task T] [--prompt P]  # Create/ensure sandbox + task for an issue
├── pr --number N [--task T] [--submit] [--prompt P]     # Create/ensure sandbox + task for a PR
│
├── sandbox                                             # Manage development sandboxes
│   ├── list [--type review|issue|chore] [--prs]        # Lists active sandboxes (can filter) or handled PRs
│   ├── chat <target>                                   # Connects interactive LLM chat session in sandbox
│   ├── connect <target>                                # Connects via SSH + tmux into sandbox
│   ├── delete <target>                                 # Deletes a sandbox and associated LB services
│   ├── suspend <target>                                # Scales sandbox replicas to 0 (suspend environment)
│   └── resume <target>                                 # Scales sandbox replicas to 1 (resume environment)
│
├── task                                                # Manage tasks in sandboxes
│   ├── run <target> <command>                          # Runs a script task and tails logs
│   ├── list <target>                                   # Lists tasks and statuses in a sandbox
│   └── logs <task-name> [-f/--follow]                  # Streams or cats logs of a task
│
├── repo                                                # Manage RepoWatch CRDs
│   ├── init [--name N] [--github-secret S]             # Initializes a RepoWatch for the current repo
│   ├── list                                            # Lists all active RepoWatch resources
│   ├── get [name]                                      # Gets a RepoWatch resource as YAML
│   ├── delete [name]                                   # Deletes a RepoWatch resource
│   └── edit [name]                                     # Opens local editor to edit RepoWatch spec
│
├── secret                                              # Manage Kubernetes Secrets in the namespace
│   ├── set [github-pat|gemini] [token]                 # Sets standard credentials
│   └── clear [github-pat|gemini|all]                   # Clears configured credentials
│
└── admin                                               # Administrative tools
    ├── onboard [github-id]                             # Bootstraps a namespace for a new user
    └── chore                                           # Manage background chores
        ├── ensure --file F [--name N]                  # Schedules/ensures a chore task
        └── reconcile                                   # Syncs chores and cleans up stale sandboxes
```

### Developer Machine Setup (GKE Autopilot)

For enterprise environments running GKE Autopilot with Google Cloud IAM, the admin can onboard developers and bind their GCP IAM email directly to their scoped developer namespace.

#### 1. Admin Onboarding
The cluster administrator runs the `admin onboard` command specifying the developer's GitHub ID and their corresponding Google Cloud IAM identity (email):
```bash
overseer-cli admin onboard <github-id> --email alex.developer@yourcompany.com
```
This automatically:
* Bootstraps their isolated namespace (naming it `<github-id>`).
* Configures sandbox ServiceAccounts.
* Generates a local Kubernetes `RoleBinding` mapping the developer's GCP identity directly to the scoped `overseer-cli-user` ClusterRole in their namespace.

#### 2. Local Developer Configuration
Once onboarded, the developer can configure their local shell to access the GKE Autopilot cluster:
1. **Authenticate locally with Google Cloud:**
   ```bash
   gcloud auth login
   ```
2. **Generate local scoped `kubeconfig`:**
   ```bash
   gcloud container clusters get-credentials <cluster-name> \
       --region <region> \
       --project <project-id>
   ```
3. **Set their context default namespace to their own namespace:**
   ```bash
   kubectl config set-context --current --namespace=<github-id>
   ```

The developer's local `overseer-cli` will now automatically run scoped against their own private workspace! Any attempt to interact with other namespaces or cluster-wide resources will be blocked by Kubernetes RBAC.

### Command and Parameter Map

The table below details the commands, arguments, flags, and target resolution rules:

| Command PATH | Positionals | Key Flags | Description | Target Resolution (Shortcuts)? |
| :--- | :--- | :--- | :--- | :--- |
| **Global / Root** | N/A | `--namespace`, `--repo` | Namespace & target repository context. | Auto-deduces via local Git remotes |
| `issue` | None | `--number`, `--pr`, `--task`, `--prompt`, `--image`, `--workspace-disk-size` | Ensures sandbox and creates task for an issue | N/A (Creates new) |
| `pr` | None | `--number`, `--task`, `--submit`, `--prompt`, `--image`, `--workspace-disk-size` | Ensures sandbox and creates task for a PR | N/A (Creates new) |
| `task create` | `<target> <command>` | None | Creates a script task inside a sandbox and streams logs | **Yes** (resolves PR/issue number or name) |
| `task list` | `<target>` | None | Lists all tasks in a sandbox | **Yes** (resolves PR/issue number or name) |
| `task logs` | `<task-name>` | `-f`, `--follow` | Displays or tails task execution logs | N/A (target is exact task name) |
| `sandbox list` | None | `--type`, `--prs` | Lists sandboxes (optionally filters by type) or handled PRs | N/A |
| `sandbox chat` | `<target>` | None | Opens interactive Gemini session in sandbox | **Yes** (resolves PR/issue number or name) |
| `sandbox connect` | `<target>` | None | Connects via SSH + tmux to the sandbox | **Yes** (resolves PR/issue number or name) |
| `sandbox delete` | `<target>` | None | Deletes a sandbox | **Yes** (resolves PR/issue number or name) |
| `sandbox suspend` | `<target>` | None | Scales sandbox replicas to `0` (suspends pod) | **Yes** (resolves PR/issue number or name) |
| `sandbox resume` | `<target>` | None | Scales sandbox replicas to `1` (resumes pod) | **Yes** (resolves PR/issue number or name) |
| `repo init` | None | `--name`, `--github-secret` | Initializes a `RepoWatch` resource for the repo | N/A |
| `repo list` | None | None | Lists all active `RepoWatch` resources | N/A |
| `repo get` | `[name]` | None | Gets a `RepoWatch` resource as YAML | Yes (falls back to deduced repo name) |
| `repo delete` | `[name]` | None | Deletes a `RepoWatch` resource | Yes (falls back to deduced repo name) |
| `repo edit` | `[name]` | None | Opens local editor via `kubectl edit` for a RepoWatch | Yes (falls back to deduced repo name) |
| `secret set` | `[type] [token]` | `--github-email`, `--github-name` | Configures standard credentials | N/A |
| `secret clear` | `[type]` | None | Clears configured credentials | N/A |
| `admin onboard` | `[github-id]` | None | Bootstraps a namespace for a new user | N/A |
| `admin chore ensure` | None | `--name`, `--file` | Schedules/ensures a chore task | N/A |
| `admin chore reconcile` | None | None | Syncs chores and cleans up stale sandboxes | N/A |

> [!TIP]
> **Target Shortcuts:** For all commands where target resolution is supported (indicated by **Yes** in the table above), you can use short integers representing the pull request or issue number (e.g., `123`) instead of the long resource name (e.g., `repo-agent-pr-123`). The CLI will automatically resolve it.

For more details on the architecture and design, see [docs/design-overseer.md](docs/design-overseer.md).

