## Quick Start


1.  **Set Environment Variables:**

    Follow [these instructions](env-variables.md) to set the required `env` variables.

2.  **Installing Repo-Agent:**

    Install from the release manifests:

    ```bash
    kind create cluster  # optional you can use an existing cluster
    export VERSION=v0.1.0-rc.3
    curl -L  https://github.com/gke-labs/gemini-for-kubernetes-development/releases/download/${VERSION}/installer.sh | bash
    ```

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
    export VERSION=v0.1.0-rc.3
    export URL_PREFIX=https://raw.githubusercontent.com/gke-labs/gemini-for-kubernetes-development/refs/tags/${VERSION}/repo-agent/examples
    curl ${URL_PREFIX}/repoboard.yaml | kubectl apply -f -
    ```

