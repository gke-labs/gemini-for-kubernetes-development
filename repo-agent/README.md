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

2.  **Installing Application:**

    There are 2 installation modes:
    * Build locally and install in a kind cluster
    * Install in any kubernetes cluster from images built in GHCR

    Run the following command to build the project, create a KinD cluster, and deploy the application:

    ```bash
    make
    ```

    To install in an existing kubernetes cluster (say GKE), run this command:

    ```bash
    IMAGE_TAG=latest REGISTRY=ghcr.io/gke-labs/gemini-for-kubernetes-development/ make install
    ```

3.  **Apply Example Configuration:**

    Apply the example `repowatch` configuration:

    ```bash
    kubectl apply -f examples/agent-sandbox-repowatch.yaml
    ```

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

## Makefile Targets

The following table lists the most common `make` targets:

| Target          | Description                                                              |
| --------------- | ------------------------------------------------------------------------ |
| `all`           | Builds, creates a KinD cluster, and deploys the application.             |
| `build`         | Builds the Go binaries and container images.                             |
| `create-kind`   | Creates a KinD cluster.                                                  |
| `install-repo-agent` | Deploys the application to the KinD cluster.                      |
| `port-forward`  | Forwards the port to access the UI.                                      |