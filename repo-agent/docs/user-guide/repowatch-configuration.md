# RepoWatch Configuration

The `RepoWatch` Custom Resource (CR) is the core configuration object for the Repo Agent. It defines which repositories to monitor, how to handle events (Pull Requests and Issues), and what environment to use for executing tasks.

## Basic Structure

A `RepoWatch` resource consists of the following main sections:

*   **`repoURL`**: The URL of the GitHub repository to watch (e.g., `https://github.com/kubernetes/kubernetes`).
*   **`githubSecretName`**: The name of the Kubernetes Secret containing the GitHub Personal Access Token (PAT) or App credentials.
*   **`review`**: Configuration for reviewing Pull Requests.
*   **`issue`**: Configuration for handling GitHub Issues.

## Review Configuration

The `review` section controls how the agent reviews Pull Requests.

```yaml
review:
  # Image to use for the sandbox environment
  image: ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest

  # Reference to the DevContainer configuration for the sandbox environment (Alternative to image)
  # devcontainerConfigRef: go-devcontainer-json
  
  # How long to keep the sandbox running after the review is complete
  reviewShutdownAfterMinutes: 60
  
  # Maximum number of concurrent review sandboxes
  maxActiveSandboxes: 3
  
  # Maximum total number of sandboxes (active + inactive)
  maxSandboxes: 5
  
  # Disk size for the workspace volume (default: 10Gi)
  workspaceDiskSize: 20Gi
  
  # Configuration for the LLM (Large Language Model)
  llm:
    provider: gemini-cli
    apiKeySecretRef: gemini-vscode-tokens
    prompt: |
      You are an expert code reviewer...
      
  # Optional: List of specific PR numbers to review (useful for testing)
  # pullRequests:
  # - 12345
```

### Key Fields:

*   **`image`**: The container image to use for the sandbox environment.
*   **`devcontainerConfigRef`**: (Optional) The name of a `ConfigMap` containing a `devcontainer.json` file. This defines the environment where the agent runs (e.g., installed tools, extensions). Use this as an alternative to `image`.
*   **`reviewShutdownAfterMinutes`**: How long to keep the sandbox running after the review is complete (in minutes).
*   **`maxActiveSandboxes`**: The maximum number of concurrent sandboxes to run for reviews. This helps manage resource usage.
*   **`maxSandboxes`**: The maximum total number of sandboxes (active + inactive) to keep.
*   **`workspaceDiskSize`**: (Optional) The disk size for the workspace PVC (e.g., `10Gi`, `20Gi`). Defaults to `10Gi`.
*   **`llm`**:
    *   **`provider`**: The LLM provider to use (e.g., `gemini-cli`, `vertex-ai`).
    *   **`apiKeySecretRef`**: The name of the Secret containing the API key for the LLM.
    *   **`prompt`**: The system instruction or prompt given to the LLM for reviewing code.

## Issue Configuration

The `issue` section allows you to configure how the agent handles GitHub Issues and define multiple handlers.

```yaml
issue:
  # Image to use for the sandbox environment
  image: ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest
  
  # Maximum number of concurrent issue sandboxes
  maxActiveSandboxes: 6
  
  # Maximum total number of sandboxes (active + inactive)
  maxSandboxes: 6
  
  # Optional: Robot account name. If not set, the PR is created in the user's name.
  robotAccount: codebot-robot
  
  # Optional: How long the sandbox remains active after an issue is processed.
  issueShutdownAfterMinutes: 300
  
  # Disk size for the workspace volume (default: 10Gi)
  workspaceDiskSize: 10Gi
  
  # Configuration for the LLM (Large Language Model)
  llm:
    provider: gemini-cli
    apiKeySecretRef: gemini-vscode-tokens
    prompt: |
      You are a helpful assistant that fixes GitHub issues...

  handlers:
  - name: fix-bug
    labels:
      - "repo-agent"
    taskType: fix-issue
    # ...
```

### Key Fields:

*   **`image`**: The container image to use for the sandbox environment.
*   **`maxActiveSandboxes`**: The maximum number of concurrent sandboxes to run for issues.
*   **`maxSandboxes`**: The maximum total number of sandboxes (active + inactive) to keep.
*   **`robotAccount`**: (Optional) Name of the GitHub user account used by the bot. If not set, the PR is created in the user's name.
*   **`issueShutdownAfterMinutes`**: (Optional) How long to keep the sandbox active after processing (in minutes).
*   **`workspaceDiskSize`**: (Optional) The disk size for the workspace PVC (e.g., `10Gi`, `20Gi`). Defaults to `10Gi`.
*   **`handlers`**: A list of handler configurations.
    *   **`name`**: A unique name for the handler.
    *   **`labels`**: A list of GitHub labels. The handler will only process issues that have at least one of these labels.
    *   **`taskType`**: The type of task to perform (e.g., `fix-issue`).

## Example

Here is a complete example of a `RepoWatch` configuration:

```yaml
apiVersion: review.gemini.google.com/v1alpha1
kind: RepoWatch
metadata:
  name: my-repo-watch
spec:
  repoURL: https://github.com/my-org/my-repo
  githubSecretName: github-pat
  
  # Pull Request Review Configuration
  review:
    image: ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest
    reviewShutdownAfterMinutes: 60
    maxActiveSandboxes: 3
    maxSandboxes: 5
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
      prompt: |
        You are an expert code reviewer. Please review this PR for:
        - Logic errors
        - Security vulnerabilities
        - Code style adherence

  # Issue Handling Configuration
  issue:
    image: ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest
    maxActiveSandboxes: 6
    maxSandboxes: 6
    robotAccount: codebot-robot
    issueShutdownAfterMinutes: 300
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
      prompt: |
        You are an expert software engineer. 
        Analyze the issue, reproduce the bug, and implement a fix.
        Commit your changes.
    handlers:
    - name: fix-bug
      labels:
        - "repo-agent"
      taskType: fix-issue
```
