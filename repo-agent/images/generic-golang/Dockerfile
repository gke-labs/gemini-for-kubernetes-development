# --------------------------------
# Build the dev-sandbox binary
FROM golang:1.24 AS build-dev-sandbox
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

COPY cmd/dev-sandbox/ cmd/dev-sandbox/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /dev-sandbox ./cmd/dev-sandbox

# --------------------------------

# Build the gh CLI
FROM golang:1.24 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1


# --------------------------------

# Main image
FROM golang:1.24

RUN apt-get update && apt-get install -y git tmux nodejs npm

RUN npm install -g @google/gemini-cli

COPY --from=build-dev-sandbox /dev-sandbox /repo-agent/dev-sandbox
COPY --from=build-gh /bin/gh /bin/gh

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

WORKDIR /workspaces
ENTRYPOINT ["/repo-agent/dev-sandbox", "agent"]