## Developer Installation from source

### Prerequisites

Ensure you have the following installed:
- **Go 1.25+**
- **Docker**
- **KinD**
- **kubectl**
- **Helm**
- **Node.js 18+ & npm** (Required for LLM CLIs like `gemini` and `claude`)

1.  **Set Environment Variables:**

    Follow [these instructions](env-variables.md) to set the required `env` variables.

2.  **Installing Repo-Agent:**

    Run the following command to build the project, create a KinD cluster, and deploy the application:

    ```bash
    cd repo-agent
    make
    ```

3. **Create secrets:**

   Run `make create-secrets` to create k8s secrets from the token exported in env variables.


4.  **Access the UI:**

    Forward the port to access the UI:

    ```bash
    make port-forward
    ```

    The UI can be accessed at `http://localhost:13380` (if running on Cloudtop, use your remote device name instead of `localhost`, e.g., `http://{CLOUDTOP_DEVICE_NAME}:13380`).

    **Note:** After opening the UI with your Cloudtop URL, you will need to log in via GitHub. To enable this, update your OAuth App configuration in your GitHub Developer settings:
    1. Copy the URL link from the UI and paste it into the **Homepage URL** field.
    2. For the **Authorization callback URL**, paste the same link and append `api/auth/callback` at the end.

5.  **Apply Example Configurations:**

    Optionally if you want to explore the example `repowatches`, apply them from the example folder.

    ```bash
    kubectl apply -f examples/<repowatch...>
    ```

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