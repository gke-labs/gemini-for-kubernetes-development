# Design Doc: ConfigDir Controller

## 1. Motivation

The core motivation is to manage and store configuration files typically found in a for example, `.gemini` directory within a Kubernetes cluster. These directories can contain a variety of configuration artifacts, such as prompts, tool schemas, and model configurations.

A naive approach of storing the entire directory content within a single Custom Resource (CR) is infeasible due to the 1.5MB size limit for objects in etcd. This limitation necessitates a more flexible and distributed way to represent a project's configuration folder structure within the cluster.

This document proposes a new controller and a set of Custom Resource Definitions (CRDs) to address this challenge, allowing configuration files to be sourced from various locations, including external URLs, other Kubernetes resources like ConfigMaps, and dedicated CRs for larger files.

## 2. Goals

*   Define a primary CRD, `ConfigDir`, to act as a logical representation of a configuration folder, example `.gemini` folder.
*   Support sourcing file content from multiple, distinct backends:
    *   Inline content for small files.
    *   References to `ConfigMap` and `Secret` resources.
    *   References to external URLs.
    *   References `ConfigFile` resources.
*   The controller will reconcile these disparate sources into a unified, usable format for applications within the cluster (e.g., by aggregating them into a common ConfigMap or Volume).
*   The design should be extensible to support other sourcing strategies in the future (e.g., Git repositories).
*   A sidecar container can be used to sync the `ConfigDir` to the pod's filesystem. The same docker image can be used as init container for one time syncing.

## 3. Proposed Design

The design centers around new CRDs:
- `ConfigDir`
- `ConfigFile`

### 3.1. `ConfigDir` CRD

This is the top-level CRD that logically represents the config directory. It contains a list of files, where each entry specifies the file's path and its content source.

**Example `ConfigDir` CR:**

```yaml
apiVersion: "configdir.gke.io/v1alpha1"
kind: "ConfigDir"
metadata:
  name: "my-review-agent-config"
  namespace: "gemini-system"
spec:
  # The controller will use this selector to find all ConfigMap
  # objects associated with this project config.
  fileContentSelector:
    matchLabels:
      project: my-review-agent
  files:
    - path: "prompts/summarize.txt"
      source:
        inline: "Summarize the following code changes..."

    - path: "tools/linter-config.json"
      source:
        configMapRef:
          name: "linter-configs"
          key: "config.json"

    - path: "credentials/api-key.txt"
      source:
        secretRef:
          name: "gemini-api-keys"
          key: "review-agent-key"

    - path: "assets/logo.png"
      source:
        # The key 'logo.png' must exist in a ConfigMap
        # object matching the fileContentSelector above.
        fileContentKey: "logo.png"

    - path: "external/onboarding.md"
      source:
        url:
          location: "https://example.com/onboarding-docs/v1.md"
          sha256: "c3ab8ff13720e8ad9047dd39466b3c8974e592c2fa383d4a3960714caef0c4f2" # Optional, for integrity
          # Optional secret for auth headers (e.g., "Authorization: Bearer <token>")
          secretRef:
            name: "external-url-auth"
            key: "auth-header"
```

### 3.2. `ConfigFile` CRD

This CRD is a simple data holder, analogous to a `ConfigMap` or `Secret`, but intended for project-specific, non-sensitive, and potentially large files that are part of a `ConfigDir`. Its primary purpose is to overcome the size limitations of a single CR. The content would be base64-encoded.

**Example `ConfigFile` CR:**

```yaml
apiVersion: "configdir.gke.io/v1alpha1"
kind: "ConfigFile"
metadata:
  name: "my-review-agent-assets"
  namespace: "gemini-system"
  labels:
    project: my-review-agent
spec:
  files:
    - path: "logo.png"
      content: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
    - path: "another-large-file.bin"
      content: "..."
      continued: my-review-agent-assets-continued # second part of the file ...
```

### 3.3. Controller Logic

There should not be much logic in the controller.

### 3.4 Sidecar or Init container

The `configdir` sidecar/init container will:
1.  Watch for `ConfigDir` resources.
2.  On reconciliation, it would read the `spec.files` list.
3.  For each file, it fetches the content from the specified source (`inline`, `ConfigMap`, `Secret`, `URL`, or `ConfigMap`).
4.  Writes the data to the local pod filesystem.

## 4. Alternative Designs Considered

### Alternative 1: Git Repository Source (GitOps Model)

Instead of defining each file, the `ConfigDir` could point to a Git repository.

```yaml
spec:
  source:
    git:
      repository: "https://github.com/my-org/review-agent-configs.git"
      branch: "main"
      path: ".gemini" # Path within the repo to sync from
      secretRef: # For private repositories
        name: "git-credentials"
```

*   **Pros:** Enables a GitOps workflow. Configuration is version-controlled, auditable, and managed through pull requests.
*   **Cons:** Significantly increases controller complexity (requires a Git client, handling credentials, polling or webhook integration). This functionality is already well-served by tools like Flux and ArgoCD.

### Alternative 2: OCI Artifact Source

The `.gemini` folder could be packaged as an OCI image and stored in a container registry.

```yaml
spec:
  source:
    oci:
      registry: "ghcr.io"
      repository: "my-org/review-agent-config"
      tag: "v1.2.3"
```

*   **Pros:** Leverages existing, robust registry infrastructure. Artifacts are immutable and versioned.
*   **Cons:** Requires a separate build-and-push step in the CI/CD pipeline for configuration, which may be unfamiliar to users.

### Alternative 3: Single CRD with Direct Volume Mounting (CSI Driver)

A more advanced approach would involve a custom Container Storage Interface (CSI) driver. The `ConfigDir` CR would define the files, and the CSI driver would be responsible for fetching the content from the various sources and mounting it directly into the pod's filesystem at runtime.

*   **Pros:** The most seamless experience for the end-user pod. No intermediate ConfigMap is needed. Files appear "magically" in the container.
*   **Cons:** A CSI driver is a highly complex piece of software to develop and maintain, involving deep integration with the kubelet.

## 5. Conclusion

The proposed design using `ConfigDir` and `ConfigFile` provides a flexible and Kubernetes-native solution that balances power with implementation simplicity. It solves the immediate problem of storing `.gemini` folders while remaining extensible. The Git and OCI alternatives are powerful but can be considered as future enhancements on top of the core model.
