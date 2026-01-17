# --------------------------------
# Build the repo-sandbox binary
FROM golang:1.24 AS build-repo-sandbox
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

COPY cmd/repo-sandbox/ cmd/repo-sandbox/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /repo-sandbox ./cmd/repo-sandbox

# --------------------------------

# Build the gh CLI
FROM golang:1.24 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1


# --------------------------------

# Main image
FROM golang:1.24

# We want a functional python, because gemini-cli likes to use python
RUN apt-get update && apt-get install -y python3 python3-pip python3-yaml python-is-python3 python3-venv

RUN apt-get update && apt-get install -y git tmux nodejs npm sudo curl

RUN npm install -g @google/gemini-cli

# Install code-server
RUN curl -fsSL https://code-server.dev/install.sh | sh
RUN code-server --install-extension golang.go --install-extension vscodevim.vim

COPY --from=build-repo-sandbox /repo-sandbox /repo-agent/repo-sandbox
COPY --from=build-gh /bin/gh /bin/gh

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

WORKDIR /workspaces
ENTRYPOINT ["/repo-agent/repo-sandbox", "agent"]