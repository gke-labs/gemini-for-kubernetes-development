## Quick Start


1.  **Set Environment Variables:**

    Follow [these instructions](env-variables.md) to set the required `env` variables.

2.  **Installing Repo-Agent:**

    Install the images published from `main` (every merge pushes `:latest`
    and `:<commit sha>` to `ghcr.io/gke-labs/gemini-for-kubernetes-development`):

    ```bash
    kind create cluster  # optional you can use an existing cluster
    git clone https://github.com/gke-labs/gemini-for-kubernetes-development.git
    cd gemini-for-kubernetes-development/repo-agent
    make install-repo-agent-latest                 # or IMAGE_TAG=<commit sha> to pin
    ```

    This installs the dependencies (agent-sandbox, and Envoy Gateway on kind),
    creates the secrets from your environment variables, and applies the
    manifests. The last tagged release, `v0.1.0-rc.3`, predates boards, so
    don't use its `installer.sh` with this guide.

3.  **Access the UI:**

    Run port-forwarding to access the UI.
    Once you run the following command, the UI is accesible at `http://localhost:13380`.

    ```bash
    # Setup port forwarding to access the UI
    while true; do \
	  ENVOY_SERVICE=$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=repo-agent-system,gateway.envoyproxy.io/owning-gateway-name=repo-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/${ENVOY_SERVICE} 13380:13380;\
	  done
    ```

4.  **Create a board:**

    Boards are usually created from the UI (paste a repo URL on the Work
    page), or apply the example RepoBoard:

    ```bash
    kubectl apply -f examples/repoboard.yaml
    ```

