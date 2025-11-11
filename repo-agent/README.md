# Gemini Code Repo Agent

This project provides a Kubernetes-based framework for running Gemini agents that can review code, create, and review issues in Github. It is designed to be deployed on a Kubernetes cluster and uses custom controllers to manage its operations.

The key components of the framework include:
*   **Repo Watch Controller**: Monitors GitHub repositories for new pull requests and other events.
*   **Review Sandbox**: Spins up isolated environments to perform automated code reviews using a Gemini agent.
*   **Issue Sandbox**: Provides a similar sandboxed environment for creating and managing GitHub issues.
*   **Review UI and API**: A web-based interface and backend service to visualize and interact with the review process.
*   **Configdir API**: API analogous to configmap used to preserve directory structure and load multiple files to be projected in a volume using a sidecar.

## Prerequisites

Before you begin, ensure you have the following tools installed:

| Tool                                                              | Description                                       |
| ----------------------------------------------------------------- | ------------------------------------------------- |
| [Gemini API Key](https://aistudio.google.com)                     | Required to authenticate with the Gemini API.     |
| [GitHub Personal Access Token](https://github.com/settings/tokens) | Required to interact with the GitHub API.         |
| [KinD](https://kind.sigs.k8s.io/)                                 | A tool for running local Kubernetes clusters.     |
| [kubectl](https://kubernetes.io/docs/tasks/tools/)                | The Kubernetes command-line tool.                 |
| [Helm](https://helm.sh/docs/intro/install/)                       | The package manager for Kubernetes.               |

## Quick Start

1.  **Set Environment Variables:**

    Export your Gemini API key and GitHub Personal Access Token as environment variables:

    ```bash
    export GEMINI_API_KEY="..."
    export GITHUB_PAT="..."
    ```

    1. `GEMINI_API_KEY` is used with `gemini-cli` to generate reviews or bug fixes
    2. `GITHUB_PAT` is used to make API calls to poll for Pull Requests, Issues etc. It is also used to create Draft reviews and code branches.

2.  **Installing Repo-Agent:**

    Install from the release manifests:

    ```bash
    kind create cluster  # optional you can use an existing cluster
    export VERSION=v0.1.0-rc.2
    curl -L  https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/${VERSION}/installer.sh | bash
    ```

    Run port-forwarding to access the UI.
    Once you run the following command, the UI is accesible at `http://localhost:13380`.

3.  **Apply Example Configurations:**

    ```bash
    export VERSION=v0.1.0-rc.2
    export URL_PREFIX=https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/refs/tags/${VERSION}/repo-agent/examples
    ```

    Kubernetes repo review example:

    ```bash
    curl ${URL_PREFIX}/k8s-configdir.yaml | kubectl apply -f -
    curl ${URL_PREFIX}/k8s-repowatch.yaml | kubectl apply -f -
    ```

    GKE Labs repo example:

    ```bash
    curl ${URL_PREFIX}/gkelabs-geminifork8s-repowatch.yaml | kubectl apply -f -
    ```

    KCC repo example:

    ```bash
    curl ${URL_PREFIX}/kcc-configdir.yaml | kubectl apply -f -
    curl ${URL_PREFIX}/kcc-repowatch.yaml | kubectl apply -f -
    ```

    Agent Sandbox repo example:

    ```bash
    curl ${URL_PREFIX}/agent-sandbox-repowatch.yaml | kubectl apply -f -
    ```

4.  **Access the UI:**

    ```bash
    # Setup port forwarding to access the UI
    while true; do \
	  ENVOY_SERVICE=$$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=repo-agent-system,gateway.envoyproxy.io/owning-gateway-name=repo-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/$${ENVOY_SERVICE} 13380:13380;\
	  done
    ```

## Developer Installation from source

1.  **Set Environment Variables:**

    Export your Gemini API key and GitHub Personal Access Token as environment variables:

    ```bash
    export GEMINI_API_KEY="..."
    export GITHUB_PAT="..."
    ```

2.  **Installing Repo-Agent:**

    Run the following command to build the project, create a KinD cluster, and deploy the application:

    ```bash
    make
    ```

3.  **Apply Example Configurations:**

    The examples have push changes to branch turned off by default.
    Enable this in those `repowatches` for which you want to automatically push PR fixes to a branch.

    ```
    # enable this to automatically push the changes to the feature branch
    #pushEnabled: true
    ```

    ```bash
    make apply-examples
    ```

    This applies SA and RoleBindings for the sandboxes before creating the repowatches.


4.  **Access the UI:**

    Forward the port to access the UI:

    ```bash
    make port-forward
    ```

    The UI can be accessed at `http://localhost:13380`.

## Deployment

The application is deployed on a KinD cluster. The `make` command handles the following:

*   **Builds the project:** Compiles the Go binaries and builds the container images.
*   **Creates a KinD cluster:** Sets up a local Kubernetes cluster using KinD.
*   **Deploys the application:** Deploys all the necessary Kubernetes resources, including deployments, services, and custom resource definitions.

## Usage

Once the application is deployed, it will start monitoring the repositories configured in the `repowatch.yaml` file. The agent will automatically review new pull requests and provide feedback.

## Cleanup

To delete the KinD cluster and all the deployed resources, run the following command:

```bash
kind delete cluster --name repo-agent
```

## Adding your own examples

To add your own examples, it's easiest to start by cloning one of the existing `RepoWatch` examples from the `examples/` directory. These examples demonstrate how to configure the agent to watch a repository and handle different types of events.

A `RepoWatch` custom resource has two main sections: `review` and `issueHandlers`.

### The `review` section

The `review` section configures the agent to review pull requests. You can specify a Gemini prompt to guide the review process. For example, you can ask the agent to check for specific coding standards, look for potential bugs, or verify that the changes are well-tested.

Here is an example of a `review` section:
```yaml
review:
  prompt: |
    You are an expert code reviewer. You are reviewing a pull request.
    Please review the following code and provide feedback.
    - Does the code follow the project's coding standards?
    - Are there any potential bugs or security vulnerabilities?
    - Is the code well-tested?
```

### The `issueHandlers` section

The `issueHandlers` section configures the agent to handle GitHub issues. You can define multiple handlers, each with its own set of rules and actions. For example, you can have a handler that automatically triages new issues, another that attempts to fix bugs, and a third that responds to feature requests.

Each handler can be configured with a `name`, `labels` to filter issues, and a Gemini `prompt` to guide the agent's response.

Here is an example of an `issueHandlers` section:
```yaml
issueHandlers:
- name: "bug-fixer"
  labels:
  - "bug"
  prompt: |
    You are an expert bug fixer. You are assigned a bug to fix.
    Please analyze the issue, identify the root cause, and provide a fix.
    - Explain the root cause of the bug.
    - Provide a code snippet with the fix.
    - Explain how the fix addresses the issue.
- name: "feature-request-handler"
  labels:
  - "feature"
  prompt: |
    You are a senior software engineer. You are assigned a feature request.
    Please analyze the request and provide a high-level implementation plan.
    - Break down the feature into smaller tasks.
    - Provide a rough estimate for each task.
    - Identify any potential risks or dependencies.
```

### Using `ConfigDir` and `devcontainer`

The examples also demonstrate how to use `ConfigDir` and `devcontainer` to create a consistent and reproducible environment for the agent.

*   **`ConfigDir`**: The `ConfigDir` API is used to mount configuration files, such as a `.gemini/` folder, into the agent's sandbox. This is similar to a `ConfigMap`, but it preserves the directory structure.
*   **`devcontainer`**: The `devcontainer.json` file defines the development environment for the agent. You can specify the base image, install additional tools, and configure the editor. This ensures that the agent has all the necessary dependencies to build, test, and analyze the code. See `go-configmap-devcontainer.yaml` for an example.

By customizing the `RepoWatch` resource and the `devcontainer` configuration, you can create a powerful and flexible Gemini agent that is tailored to your specific needs.

#### Creating a configdir from your .gemini folder

Build the `configdir-cli` binary
```bash
make build # build all the binaries
```

Create a `configdir` entry from your `.gemini` folder:
```bash
% bin/configdir-cli --include-folder-name --directory ~/workspace/src/acp/oss-tool-sync/gemini-configs/kubernetes/.gemini --sync-to-cluster --name k8s-gemini-configdir
2025/11/06 17:33:28 found files. count: 7, totalSize: 40003
2025/11/06 17:33:28 total size is less than 1MB, using inline files
2025/11/06 17:33:28 created configdir k8s-gemini-configdir
2025/11/06 17:33:28 successfully synced to cluster


% kubectl get configdir 
NAME                       AGE
k8s-gemini-configdir       10s
kcc-review-gemini-config   10h

% kubectl get configdir  k8s-gemini-configdir -o yaml | less
apiVersion: configdir.gke.io/v1alpha1
kind: ConfigDir
metadata:
  creationTimestamp: "2025-11-06T17:55:01Z"
  generation: 1
  name: k8s-gemini-configdir
  namespace: default
  resourceVersion: "70225"
  uid: 6e95992f-49d6-45d6-a5a1-30eab4ea5450
spec:
  files:
  - path: .gemini/commands/document/package.toml
    source:
      inline: |-
        # In: ~/.gemini/commands/document/package.toml
        # This command will be invoked via: /document:package /path/to/package

        description = "Asks the model to write documentation for a golang package."
...
```

## Makefile Targets

The following table lists the most common `make` targets:

| Target          | Description                                                              |
| --------------- | ------------------------------------------------------------------------ |
| `all`           | Builds, creates a KinD cluster, and deploys the application.             |
| `build`         | Builds the Go binaries and container images.                             |
| `create-kind`   | Creates a KinD cluster.                                                  |
| `install-repo-agent` | Deploys the application to the KinD cluster.                      |
| `port-forward`  | Forwards the port to access the UI.                                      |
