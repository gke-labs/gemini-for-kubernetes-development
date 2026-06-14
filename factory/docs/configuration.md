# AI Factory Configuration Guide (`.factory.cfg`)

The `factory` CLI and watch daemon can be configured using a YAML configuration file named `.factory.cfg`.

---

## Configuration File Locations

The `factory` CLI looks for the configuration file in the following order:
1. **Current Directory**: A `.factory.cfg` file in the current working directory.
2. **Environment Variable**: The file path specified in the `FACTORY_CONFIG` environment variable. If `FACTORY_CONFIG` points to a directory, the CLI will look for `.factory.cfg` inside that directory.

---

## Configuration Schema & Fields

Here are the available fields in `.factory.cfg`:

### Core Settings
* **`maxActiveReviews`** (integer, default: unlimited): Maximum number of concurrent PR review sandboxes allowed in the namespace.
* **`maxActiveIssues`** (integer, default: unlimited): Maximum number of concurrent issue fix sandboxes allowed in the namespace.
* **`image`** (string): Default base image to use for spawned sandboxes (e.g., `ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest`).
* **`workspaceDiskSize`** (string, default: `10Gi`): Default size of the persistent volume claim (PVC) for the sandbox workspace (e.g., `20Gi`).
* **`ephemeralStorage`** (string, default: `6Gi`): Default ephemeral storage request and limit for the sandbox pod (e.g., `10Gi`).

### Repository Watching & Triggering
* **`triggerLabel`** (string, default: `factory`): The GitHub label that triggers automatic issue fixing when detected by `factory watch`.
* **`additionalLabels`** (array of strings): Additional labels automatically applied to pull requests created by the AI Factory.
* **`allowlistedBots`** (array of strings): GitHub usernames of bots whose issues, PRs, or comments are allowed to trigger automatic workflows.

### Chores Configuration
* **`chores`** (object): Configures the automated repository maintenance routines.
  * **`mode`** (string: `enabled` | `disabled` | `dryrun`, default: `enabled`): Whether background repo chores are executed.

### Secrets & Environment Variables Injection
* **`secrets`** (array of secret mounts): Custom Kubernetes secrets to mount in all sandboxes.
  * **`name`** (string): The name of the Kubernetes secret in the target namespace.
  * **`mountPath`** (string): The path inside the sandbox container where the secret keys should be mounted as files.
* **`env`** (array of environment variables): Custom environment variables to inject into all sandboxes.
  * **`name`** (string): The environment variable key.
  * **`value`** (string): The environment variable value.

### Multi-Identity & Bot Pools (`roles`)
* **`roles`** (map of role names to role specs): Groups task types and defines a pool of bot user accounts that are randomly selected to run them. This prevents API rate limits and conflicts when running concurrent sandboxes.
  * **Role Spec**:
    * **`tasks`** (array of strings): The list of task types mapped to this role (e.g., `issue-fix`, `pr-investigate`, `pr-comments`, `pr-iterate`, `pr-review`, `agent-chore`).
    * **`users`** (array of strings): The list of bot user accounts belonging to this pool. The `factory` CLI will select one randomly.
  * *Note*: Standard role names are `coder` (maps to coding tasks) and `reviewer` (maps to PR review tasks).

---

## Example `.factory.cfg`

```yaml
# Sandbox Resource Configuration
image: ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest
workspaceDiskSize: 20Gi
ephemeralStorage: 10Gi

# Watch Limits
maxActiveReviews: 5
maxActiveIssues: 3

# Watching Options
triggerLabel: overseer
additionalLabels:
  - ai-engineered
  - auto-generated
allowlistedBots:
  - reviewbot-robot

chores:
  mode: enabled

# Custom Secret Mounts
secrets:
  - name: internal-tls-certs
    mountPath: /etc/ssl/certs/internal

# Custom Env Variables
env:
  - name: GOPROXY
    value: "https://proxy.golang.org,direct"

# Bot Pool & Roles
roles:
  coder:
    tasks:
      - issue-fix
      - pr-investigate
      - pr-comments
      - pr-iterate
    users:
      - coder-bot-1
      - coder-bot-2
  reviewer:
    tasks:
      - pr-review
    users:
      - reviewer-bot-1
      - reviewer-bot-2
```
