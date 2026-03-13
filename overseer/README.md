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
    mode: disabled
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

For more details on the architecture and design, see [docs/design-overseer.md](docs/design-overseer.md).
