# Multi-Tenant Architecture and Isolation

The `repo-agent` employs a multi-tenant architecture designed to isolate user resources and configuration.

## 1. High-Level Architecture

The system uses a **Namespace-per-Tenant** model. Each authenticated user is assigned a dedicated Kubernetes Namespace where their resources (RepoWatches, Sandboxes, Secrets) reside. This ensures strong isolation of resources and simplifies clean-up and quota management.

### Key Components

*   **Review UI/API (`review-ui`)**: The entry point for users. It handles authentication, session management, and requests to the Kubernetes API on behalf of the user.
*   **Repo Watch Controller (`repowatch-controller`)**: A cluster-wide controller that reconciles `RepoWatch` custom resources. It operates within the context of the namespace where the `RepoWatch` is created.
*   **Sandboxes**: Isolated environments (Pods/Deployments) created for code reviews (`ReviewSandbox`), issue triage (`IssueSandbox`), or development (`DevSandbox`). These run within the user's namespace.

## 2. User Identity and Sessions

### Session Management
The application uses [Gorilla Sessions](https://github.com/gorilla/sessions) to manage user sessions.
*   **Store**: `cookie.NewStore` is used to store session data in encrypted cookies.
*   **Session Name**: `repo-agent-session`.
*   **Key**: The authenticated user's GitHub username is stored in the session under the key `ghUser`.
*   **Encryption**: The session is encrypted using a `SESSION_SECRET` environment variable (or a randomly generated one if not set).

### Authentication Flow
1.  **OAuth Login**: Users authenticate via GitHub OAuth (`/api/auth/login`).
2.  **Callback**: Upon successful authentication, the GitHub username is retrieved and normalized (lowercase).
3.  **Bootstrap**: The system calls `bootstrapNamespace(username)` to ensure the user's environment exists.
4.  **Session Creation**: The username is stored in the session cookie.

## 3. Isolation Mechanisms

### Namespacing
*   **Naming Convention**: The user's namespace is named exactly as their GitHub username (lowercase).
*   **Labels**: All namespaces created by the system are labeled with `review.gemini.google.com/tenant: <username>`.
*   **Resource scoping**: All interactions with the Kubernetes API for a specific user are scoped to their namespace.

### Service Accounts and RBAC
When a user's namespace is bootstrapped, the following Service Accounts are created within it:
*   `review-sandbox`
*   `issue-sandbox`
*   `dev-sandbox`

These Service Accounts are bound to corresponding **ClusterRoles** (e.g., `review-sandbox`, `configdir-controller`), granting the sandboxes specific permissions needed to operate (like reading secrets or syncing config directories) *only within their namespace*.

## 4. Secret Management

Secrets are a critical part of the multi-tenancy model, handling both global defaults and per-user overrides.

### Global Defaults (System Namespace)
The `repo-agent-system` namespace holds the default configuration and secrets:
*   `github-pat`: Contains a default GitHub Personal Access Token (PAT), name, and email.
*   `gemini-vscode-tokens`: Contains the default Gemini API key.
*   `anthropic-api-key`: Contains the default Anthropic API key.
*   `devcontainer-json`: A default ConfigMap for devcontainer configurations.

### Secret Copying & Bootstrapping
When a user logs in (or the namespace is bootstrapped), the system:
1.  Checks if the user's namespace exists. If not, it creates it.
2.  **Copies** the default secrets and ConfigMaps from `repo-agent-system` to the user's namespace.
    *   This ensures every user starts with a working configuration without needing manual setup.
    *   Copied secrets retain the same names (`github-pat`, `gemini-vscode-tokens`, etc.).

### Per-User Secrets & Overrides
Users can provide their own credentials via the `/settings` page.
*   **Mechanism**: The `updateSettings` API updates the `github-pat` or `gemini-vscode-tokens` secrets *in the user's namespace*.
*   **OAuth Token**: When a user logs in via OAuth, their access token is automatically stored in their namespace's `github-pat` secret (key: `pat`). This effectively overrides the default PAT with the user's own credentials.
*   **Isolation**: Since controllers look for secrets in the `RepoWatch`'s namespace, they will use the user-specific secrets if present (or the copied defaults).

## 5. Logic and UI Flows

### 1. Login & Provisioning
*   **User Action**: Clicks "Login with GitHub".
*   **Backend**: 
    *   Completes OAuth flow.
    *   Creates namespace `<username>`.
    *   Copies defaults from `repo-agent-system`.
    *   Updates `github-pat` in `<username>` namespace with the OAuth token.
    *   Sets `ghUser` session cookie.

### 2. Repo Watching
*   **User Action**: Adds a repository to watch via the UI.
*   **Backend**:
    *   Reads `ghUser` from session.
    *   Creates a `RepoWatch` CR in the `<username>` namespace.
*   **Controller**:
    *   Detects new `RepoWatch` in `<username>` namespace.
    *   Reads `github-pat` from `<username>` namespace.
    *   Polles GitHub using the user's credentials.

### 3. Sandbox Creation (Review/Issue/Dev)
*   **Trigger**: New PR detected or User requests a Dev Sandbox.
*   **Controller**:
    *   Creates a `ReviewSandbox`/`DevSandbox` CR in `<username>` namespace.
    *   Sets `OwnerReference` to the `RepoWatch`.
*   **Pod Execution**:
    *   The Sandbox Pod starts in `<username>` namespace.
    *   It uses the `review-sandbox` (or similar) ServiceAccount.
    *   It mounts secrets (e.g., API keys) from `<username>` namespace.
    *   `configdir-cli` sidecar syncs configurations from `ConfigDir` resources in `<username>` namespace.

## 6. GitHub Permissions

The system interacts with GitHub both as an OAuth App and using Personal Access Tokens (PATs).

### OAuth Scopes
The application requests the following scopes:
*   **Default**: `read:user`, `user:email` (for identity).
*   **Read-Write** (if requested): `repo`, `read:user`, `user:email`.

### PAT / Token Capabilities
The capabilities depend on the token used (Global default vs. User OAuth token).

*   **Read-Only Operations**:
    *   Polling for PRs and Issues.
    *   Reading file content for reviews.
    *   *Required Scope*: Public repos (no scope) or Private repos (`repo` scope).

*   **Write Operations**:
    *   Posting PR reviews/comments.
    *   Creating/Commenting on Issues.
    *   Pushing to branches (for automated fixes or Dev Sandboxes).
    *   *Required Scope*: `repo` (for private repos or write access), `public_repo` (for public repos).

**Note**: If a user is in "Read-Only" mode (using a token with limited scopes), actions like "Submit Review" or "Create Branch" will fail at the GitHub API level, which is logged by the controller.

## 7. ConfigDir and Filesystem Sync
*   **Concept**: `ConfigDir` resources represent a directory structure (e.g., `.gemini/`).
*   **Mechanism**: A `configdir-cli` sidecar runs in every Sandbox pod.
*   **Flow**:
    1.  Sidecar watches `ConfigDir` resources in the Pod's namespace.
    2.  It fetches content from `Inline`, `Secret`, `ConfigMap`, or `URL` sources defined in the CR.
    3.  It writes files to a shared volume mounted at the target path.
*   **Isolation**: The sidecar only sees `ConfigDir`s and referenced secrets within its own namespace, preserving tenant isolation.
