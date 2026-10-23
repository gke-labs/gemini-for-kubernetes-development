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

Every sandbox setting can be overridden per command by the matching flag (shown in parentheses).

### Core Settings
* **`engine`** (string: `gemini` | `claude` | `antigravity`, default: `gemini`; `--engine`): The coding agent that runs tasks. `claude` needs `ANTHROPIC_API_KEY` in the user secret; `gemini` and `antigravity` use the Gemini key.
* **`image`** (string; `--image`): Default base image to use for spawned sandboxes (e.g., `ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest`).
* **`workspaceDiskSize`** (string, default: `10Gi`; `--workspace-disk-size`): Default size of the persistent volume claim (PVC) for the sandbox workspace (e.g., `20Gi`).
* **`workspaceStorageClass`** (string, optional; `--workspace-storage-class`): StorageClass for the sandbox workspace PVC (e.g., `premium-rwo`, `pd-ssd`). If omitted, uses the cluster default StorageClass.
* **`ephemeralStorage`** (string, default: `6Gi`; `--ephemeral-storage`): Default ephemeral storage request and limit for the sandbox pod (e.g., `10Gi`).
* **`sandboxCPURequest`**, **`sandboxCPULimit`** (string; `--cpu-request`, `--cpu-limit`): CPU request and limit for sandbox pods (e.g., `2`, `500m`).
* **`sandboxMemoryRequest`**, **`sandboxMemoryLimit`** (string; `--memory-request`, `--memory-limit`): Memory request and limit for sandbox pods (e.g., `4Gi`).
* **`minNumber`** (integer, default: 0): Minimum issue or PR number to process. Any issue or PR with a number less than `minNumber` will be ignored.

### Repository Watching & Triggering
* **`triggerLabel`** (string, default: `factory`): The GitHub label that triggers automatic issue fixing when detected by `factory watch`.
* **`additionalLabels`** (array of strings): Additional labels automatically applied to pull requests created by the AI Factory.
* **`allowlistedBots`** (array of strings): GitHub usernames of bots whose issues, PRs, or comments are allowed to trigger automatic workflows.
* **`allowlistedUsers`** (array of strings): GitHub logins whose comments and reviews are trusted even though GitHub does not report them as `OWNER`, `MEMBER` or `COLLABORATOR` (typically organization members with private membership). Feedback from any other account without write access is neither acted on by `factory watch` nor placed in an agent prompt. The `allowlistedBots` and the `reviewer` role accounts are trusted the same way.
* **`sessionURL`** (string): links what `factory` posts on a PR (the description `open-pr` writes or a care run rewrites, replies, reports) to the agent conversation that wrote it, with a small hint at the bottom. A URL with `{sandbox}` and `{task}` in it, e.g. `https://staging.repoagent.dev/#/overseers/kcc/task-session/{sandbox}/{task}`; `{task}` is the conversation's task (the session a care run continued, else the run's own). Unset, or with `--disclose=false`, nothing is linked.

### Chores Configuration
* **`chores`** (object): Configures the automated repository maintenance routines.
  * **`mode`** (string: `enabled` | `disabled`, default: `enabled`): Whether `factory watch` schedules chores. `disabled` here overrides `--chores-mode` and `CHORES_MODE`. For a dry run, use `factory watch --dryrun`.

### Secrets & Environment Variables Injection
* **`secrets`** (array of secret mounts): Custom Kubernetes secrets to mount in all sandboxes.
  * **`name`** (string): The name of the Kubernetes secret in the target namespace.
  * **`mountPath`** (string): The path inside the sandbox container where the secret keys should be mounted as files.
* **`env`** (array of environment variables): Custom environment variables to inject into all sandboxes.
  * **`name`** (string): The environment variable key.
  * **`value`** (string): The environment variable value.

### Multi-Identity & Bot Pools (`roles`)
* **`roles`** (map of role names to role specs): Pools of bot user accounts. Which pool runs a task is its recipe's (`as:`), not configured here. This prevents API rate limits and conflicts when running concurrent sandboxes.
  * **Role Spec**:
    * **`users`** (array of strings): The bot user accounts in this pool.
  * *Note*: The role names recipes say are:
    * `coder`: fix, plan, triage and the other issue recipes, a pool bot pinned to the issue (the bot owning its sandbox, then a pool bot assigned to it, else any).
    * `reviewer`: review.
    * `agent`: workflow steps and chores (falls back to `coder` when there is no `agent` pool).
    * A recipe that says `as: author` (the care recipes) runs as the PR's own bot: the pool bot assigned to it, else its author.

### Parsed but not used
This key is accepted for compatibility and has no effect:
* **`prInactivityTimeout`**: use `factory watch --pr-inactivity-timeout` or the `PR_INACTIVITY_TIMEOUT` environment variable.

---

## Example `.factory.cfg`

```yaml
engine: gemini

# Sandbox Resource Configuration
image: ghcr.io/gke-labs/gemini-for-kubernetes-development/factory-golang:latest
workspaceDiskSize: 20Gi
workspaceStorageClass: premium-rwo
ephemeralStorage: 10Gi
sandboxCPURequest: "2"
sandboxMemoryRequest: 4Gi
sandboxMemoryLimit: 8Gi

minNumber: 10400

# Watching Options
triggerLabel: overseer
additionalLabels:
  - ai-engineered
  - auto-generated
allowlistedBots:
  - reviewbot-robot
allowlistedUsers:
  - private-org-member

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
    users:
      - coder-bot-1
      - coder-bot-2
  reviewer:
    users:
      - reviewer-bot-1
      - reviewer-bot-2
```
