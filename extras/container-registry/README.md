# Container Registry Service

A lightweight, Go-based OCI-compliant container registry designed for use within agentic sandboxes.

## Purpose

This service provides a simple, transient container registry that allows agentic sandboxes to push images and have them run within the same Kubernetes cluster. It is optimized for temporary storage and integrates with sandbox-specific authentication.

## Key Features

- **OCI Compliant**: Implements a subset of the OCI Distribution Specification (v2 API) necessary for pushing and pulling images.
- **Transient Storage**: Designed to throw away images once they are no longer needed (e.g., when the associated sandbox is deleted).
- **Integrated Authentication**: Uses sandbox credentials (e.g., GitHub tokens or Kubernetes service account tokens) for authentication.
- **In-Cluster Optimized**: Runs as a Kubernetes service, accessible by nodes and pods within the cluster.

## Architecture

The registry is implemented as a Go service using the standard `net/http` library to keep dependencies to a minimum.

### Components

- **API Handler**: Handles OCI v2 API requests.
- **Auth Middleware**: Validates incoming requests against sandbox credentials.
- **Storage Provider**: Manages image blobs and manifests, initially using local disk storage or in-memory storage.
- **Cleanup Manager**: Periodically removes images that are no longer associated with active sandboxes.

## Usage

The registry is typically accessed via its Kubernetes service DNS name:
`http://container-registry.repo-agent-system.svc.cluster.local:5000`

### Pushing an Image

```bash
docker tag my-image container-registry.repo-agent-system.svc.cluster.local:5000/my-image
docker push container-registry.repo-agent-system.svc.cluster.local:5000/my-image
```

### Pulling an Image

Nodes can pull images directly using the service DNS name. Note that nodes may need to be configured to allow insecure registries if TLS is not enabled.
