# Build the repo-sandbox binary
FROM golang:1.25.7 AS builder
WORKDIR /app

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download
# Prebuilds for speed
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v k8s.io/klog/v2 golang.org/x/crypto/ssh


# Copy the llm directory
COPY pkg/ pkg/
COPY api/ api/

COPY cmd/repo-sandbox/ cmd/repo-sandbox
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /repo-sandbox ./cmd/repo-sandbox/

# Envbuilder root
FROM ghcr.io/coder/envbuilder
COPY --from=builder /repo-sandbox /repo-agent/repo-sandbox
ENV KANIKO_DIR=/.envbuilder
