# Gemini Guide for Container Registry

This service implements a minimal OCI container registry tailored for the Gemini CLI and agentic sandboxes.

## Architecture

- `main.go`: Entry point for the service.
- `pkg/registry/`: Core logic for the OCI registry implementation.
    - `handler.go`: HTTP handlers for the OCI v2 API.
    - `auth.go`: Authentication logic integrated with sandbox credentials.
    - `storage.go`: Interface and implementations for blob and manifest storage.
    - `cleanup.go`: Logic for garbage collecting old or orphaned images.

## Implementation Strategy

1. **OCI v2 API**: Support the basic flow of `POST` (upload start) -> `PATCH` (upload chunk) -> `PUT` (upload finish) for blobs, and `PUT` for manifests.
2. **Authentication**:
    - Validate `Authorization: Bearer <token>` header.
    - The token should be validated against the environment it's running in (e.g., checking if it's a valid GitHub token for the assigned user).
3. **Storage**:
    - Initial implementation will use a local directory (e.g., `/data`).
    - Images should be stored in a way that they can be easily linked to a specific sandbox.
4. **Cleanup**:
    - The registry should monitor the lifecycle of sandboxes.
    - When a sandbox is deleted, its associated images should be queued for deletion.

## Future Work

- Implement image signing and verification.
- Add support for remote storage backends (e.g., GCS) if persistence is required.
- Enhance the cleanup logic to handle disk pressure.

## Key Dependencies

- `net/http`: Standard library for the web server.
- `k8s.io/client-go`: To interact with the Kubernetes API for sandbox tracking and authentication.
