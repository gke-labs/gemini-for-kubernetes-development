# User Instructions Injector

This CLI tool automates the process of injecting generated user instructions (from `gen-user-instructions`) into the Kubernetes `ConfigDir` resources used by the Gemini Agent.

## Overview

The tool acts as a bridge between the offline instruction generation process and the live Kubernetes cluster. It:
1.  Reads generated instruction files (JSON) from a local directory (e.g., `user-instructions/`).
2.  Matches each instruction to a running `RepoWatch` in the cluster by comparing repository URLs.
3.  Identifies the `ConfigDir` resource linked to that `RepoWatch`.
4.  Injects the instructions into the `ConfigDir` as a file (inline content).

## Prerequisites

*   **Kubernetes Access**: You must have a valid `kubeconfig` pointing to the target cluster where `RepoWatch` and `ConfigDir` resources reside.
*   **Generated Instructions**: You need a directory containing the output from the `gen-user-instructions` tool (structure: `<namespace>/<owner>-<repo>.json`).

## Usage

### 1. Blind Update (Direct)

Directly overwrites the live instructions file (`.gemini/user-instructions.json`) in the cluster. The agent will pick up these changes immediately (or on the next poll).

```bash
go run syncer/cmd/inject-user-instructions/main.go \
  --input-dir user-instructions
```

### 2. Staged Update (Draft)

Injects the instructions as a **draft** file (`.gemini/user-instructions.draft.json`). This allows for manual review or UI-based approval flows before the instructions are made active.

```bash
go run syncer/cmd/inject-user-instructions/main.go \
  --input-dir user-instructions \
  --draft
```

**Promotion Workflow:**
1.  Run with `--draft` to stage changes.
2.  Review the `.gemini/user-instructions.draft.json` in the cluster (or via a UI).
3.  Once approved, run the command *without* `--draft` to overwrite the main file with the approved content.

## Flags

*   `--input-dir`: (Default: `user-instructions`) The directory containing the JSON instruction files. Expected structure: `<namespace>/<filename>.json`.
*   `--draft`: (Default: `false`) If set, writes to `.gemini/user-instructions.draft.json` instead of the main file.
*   `--kubeconfig`: (Optional) Path to your kubeconfig file. Defaults to `~/.kube/config`.

```

## Alternatives Considered

1. ConfigMap Staging:
 * Concept: Create a ConfigMap for drafts and link it in ConfigDir.
 * Verdict: More complex. ConfigDir's Inline source is much simpler for text-based configs and doesn't require managing extra lifecycle resources like ConfigMaps.

2. Custom CRD (`InstructionProposal`):
 * Concept: Create a CRD to hold the proposal state (Pending, Approved).
 * Verdict: Overkill for this use case. Using the filesystem abstraction of ConfigDir (draft file vs. main file) is consistent with how the agent consumes data.

3. GitOps (Pull Request):
 * Concept: The tool opens a PR to the repo containing the ConfigDir definition.       * Verdict: If ConfigDir is managed by GitOps (e.g., ArgoCD), modifying it in-cluster will cause drift. If that's the case, this tool should be modified to write to the Git repo instead of the K8s API. However, assuming ConfigDir is used for dynamic injection here, the API approach is correct.