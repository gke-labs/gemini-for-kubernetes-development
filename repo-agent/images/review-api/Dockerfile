# Build stage
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
COPY repo-agent/cmd/review-api/ repo-agent/cmd/review-api/
COPY repo-agent/version.go repo-agent/version.go

WORKDIR /app/repo-agent
ARG VERSION=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X github.com/gke-labs/gemini-for-kubernetes-development/repo-agent.GitCommit=$VERSION" -o /review-api cmd/review-api/main.go

# Final stage
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /review-api /review-api
EXPOSE 8080
CMD ["/review-api"]
