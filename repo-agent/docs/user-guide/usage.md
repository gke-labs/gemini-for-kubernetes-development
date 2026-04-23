## Usage

Once the `repo-agent` is installed, it will start monitoring the repositories configured in the `repowatch` CRs. 
The agent will automatically review new pull requests and provide feedback.

## Use the UI

Depending on the installation, you would need to expose the UI using port-forward or via a URL.
Using the UI, you can register for new repositories.
It would create a default repowatch for the given github repo URL.

## Adding your own `repowatch`

Another way is to create the `repowatch` CR manually and applying it to the cluster.
To add your own repowatch, start by cloning one of the existing `RepoWatch` examples from the `examples/` directory. These examples demonstrate how to configure the agent to watch a repository and handle different types of events.

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
  # Optional: Files to ignore during review (glob patterns)
  ignoreFiles:
  - "go.sum"
  - "vendor/**"
  - "*.generated.go"
  # Optional: Only review these files (glob patterns)
  # If set, ignoreFiles still takes precedence.
  includeFiles:
  - "pkg/**"
```

### Dynamic Overrides in Pull Requests

You can dynamically override some review settings by adding commands to your Pull Request description. This is useful for adjusting the review scope for a specific PR without changing the global configuration.

Supported commands:
- `/max-review-files <number>`: Override the maximum number of files to review (e.g., `/max-review-files: 50`).
- `/ignore-files <patterns>`: Add more glob patterns to ignore, comma-separated (e.g., `/ignore-files: vendor/**, *.md`).
- `/include-files <patterns>`: Add glob patterns to include, comma-separated (e.g., `/include-files: pkg/api/*.go`).

Commands can use space, colon (`:`), or equals (`=`) as separators.

> **Note on glob patterns:** 
> - Recursive matching using `**` is only supported when it's at the end of a pattern (e.g., `vendor/**`). 
> - Patterns **without** a separator (e.g., `*.go`, `go.sum`) match against the filename only, meaning they match in any directory.
> - Patterns **with** a separator (e.g., `pkg/*.go`, `internal/api/config.yaml`) match against the full path from the repository root. These do **not** match subdirectories recursively. For example, `pkg/*.go` matches `pkg/main.go` but not `pkg/utils/helper.go`.
> - Patterns like `pkg/**/*.go` are **not supported** and will not work as expected. Use `pkg/**` if you want to include or ignore everything under a directory.

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

## User Guides

- [Interactive Terminal](terminal.md): Learn how to use the web terminal to explore and debug sandboxes.
- [Co-authoring with Robot Accounts](co-authoring.md): Learn how to collaborate with the robot in the same sandbox, with proper git attribution.
