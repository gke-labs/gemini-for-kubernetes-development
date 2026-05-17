# Build the repo-sandbox binary
FROM golang:1.26.2 AS builder
WORKDIR /app

# Copy the Go Modules manifests
COPY go.work go.work
COPY go.work.sum go.work.sum
COPY go.mod go.mod
COPY go.sum go.sum
COPY repo-agent/go.mod repo-agent/go.mod
COPY repo-agent/go.sum repo-agent/go.sum
COPY overseer/go.mod overseer/go.mod
COPY overseer/go.sum overseer/go.sum
COPY agentsandboxes/go.mod agentsandboxes/go.mod
COPY agentsandboxes/go.sum agentsandboxes/go.sum
COPY dev/tools/go.mod dev/tools/go.mod
COPY dev/tools/go.sum dev/tools/go.sum
COPY services/container-registry/go.mod services/container-registry/go.mod
COPY services/container-registry/go.sum services/container-registry/go.sum
COPY factory/go.mod factory/go.mod
COPY factory/go.sum factory/go.sum

# Copy the source code
COPY agentsandboxes/ agentsandboxes/
COPY repo-agent/pkg/ repo-agent/pkg/
COPY repo-agent/api/ repo-agent/api/
COPY repo-agent/cmd/repo-sandbox/ repo-agent/cmd/repo-sandbox/
COPY repo-agent/cmd/gemini-stream-processor/ repo-agent/cmd/gemini-stream-processor/

WORKDIR /app/repo-agent
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /repo-sandbox ./cmd/repo-sandbox/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /gemini-stream-processor ./cmd/gemini-stream-processor/

# Envbuilder root
FROM ghcr.io/coder/envbuilder
COPY --from=builder /repo-sandbox /repo-agent/repo-sandbox
COPY --from=builder /gemini-stream-processor /usr/local/bin/gemini-stream-processor
ENV KANIKO_DIR=/.envbuilder
