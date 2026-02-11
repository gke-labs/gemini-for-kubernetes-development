# RepoWatch Configuration

The `RepoWatch` Custom Resource (CR) is the core configuration object for the Repo Agent. It defines which repositories to monitor, how to handle events (Pull Requests and Issues), and what environment to use for executing tasks.

## Basic Structure

A `RepoWatch` resource consists of the following main sections:

*   **`repoURL`**: The URL of the GitHub repository to watch (e.g., `https://github.com/kubernetes/kubernetes`).
*   **`githubSecretName`**: The name of the Kubernetes Secret containing the GitHub Personal Access Token (PAT) or App credentials.
*   **`review`**: Configuration for reviewing Pull Requests.
*   **`issueHandlers`**: Configuration for handling GitHub Issues.

## Review Configuration

The `review` section controls how the agent reviews Pull Requests.

```yaml
review:
  # Reference to the DevContainer configuration for the sandbox environment
  devcontainerConfigRef: go-devcontainer-json
  
  # Maximum number of concurrent review sandboxes
  maxActiveSandboxes: 3
  
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

*   **`devcontainerConfigRef`**: (Optional) The name of a `ConfigMap` containing a `devcontainer.json` file. This defines the environment where the agent runs (e.g., installed tools, extensions).
*   **`maxActiveSandboxes`**: The maximum number of concurrent sandboxes to run for reviews. This helps manage resource usage.
*   **`llm`**:
    *   **`provider`**: The LLM provider to use (e.g., `gemini-cli`, `vertex-ai`).
    *   **`apiKeySecretRef`**: The name of the Secret containing the API key for the LLM.
    *   **`prompt`**: The system instruction or prompt given to the LLM for reviewing code.

## Issue Handlers

The `issueHandlers` section allows you to define multiple handlers for GitHub Issues. Each handler can filter issues by labels and perform specific tasks.

```yaml
issueHandlers:
- name: triage
  # Filter issues by labels
  labels:
    - "needs-triage"
    
  # Handler-specific sandbox configuration
  maxActiveSandboxes: 2
  devcontainerConfigRef: go-devcontainer-json
  
  llm:
    provider: gemini-cli
    apiKeySecretRef: gemini-vscode-tokens
    prompt: |
      You are a helpful assistant that triages GitHub issues...
      
- name: fix-bug
  labels:
    - "bug"
  pushEnabled: true # Allow the agent to push code changes
  # ...
```

### Key Fields:

*   **`name`**: A unique name for the handler.
*   **`labels`**: A list of GitHub labels. The handler will only process issues that have at least one of these labels.
*   **`pushEnabled`**: (Boolean) If set to `true`, the agent is allowed to push commits to the repository (e.g., to fix a bug).
*   **`devcontainerConfigRef`**, **`maxActiveSandboxes`**, **`llm`**: Similar to the `review` section, these configure the environment and LLM for this specific handler.

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
    devcontainerConfigRef: go-devcontainer-json
    maxActiveSandboxes: 3
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
      prompt: |
        You are an expert code reviewer. Please review this PR for:
        - Logic errors
        - Security vulnerabilities
        - Code style adherence

  # Issue Handling Configuration
  issueHandlers:
  - name: bug-fixer
    labels:
      - "bug"
    pushEnabled: true
    devcontainerConfigRef: go-devcontainer-json
    maxActiveSandboxes: 2
    llm:
      provider: gemini-cli
      apiKeySecretRef: gemini-api-key
      prompt: |
        You are an expert software engineer. 
        Analyze the issue, reproduce the bug, and implement a fix.
        Commit your changes.
```
