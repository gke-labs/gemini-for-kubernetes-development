# Build the issue-sandbox binary
FROM golang:1.24 AS builder
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

COPY dev-sandbox/ dev-sandbox/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /dev-sandbox ./dev-sandbox

# Envbuilder root
FROM ghcr.io/coder/envbuilder
COPY --from=builder /dev-sandbox /repo-agent/dev-sandbox
ENV KANIKO_DIR=/.envbuilder