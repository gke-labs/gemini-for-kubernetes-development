# --------------------------------
# Build the repo-sandbox binary
FROM golang:1.25.7 AS build-repo-sandbox
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

# Copy the source code
COPY agentsandboxes/ agentsandboxes/
COPY repo-agent/pkg/ repo-agent/pkg/
COPY repo-agent/api/ repo-agent/api/
COPY repo-agent/cmd/repo-sandbox/ repo-agent/cmd/repo-sandbox/

# We don't use 'go mod download' because it doesn't support go.work
# Instead we build and let it download.

WORKDIR /app/repo-agent
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /repo-sandbox ./cmd/repo-sandbox

# --------------------------------

# Build the gh CLI
FROM golang:1.25.7 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1


# --------------------------------

# Main image
FROM golang:1.25.7
ENV GOTOOLCHAIN=auto

# We want a functional python, because gemini-cli likes to use python
RUN apt-get update && apt-get install -y python3 python3-pip python3-yaml python-is-python3 python3-venv

RUN apt-get update && apt-get install -y git tmux nodejs npm sudo curl apt-transport-https ca-certificates gnupg jq

# Install Google Cloud CLI
RUN echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list && \
    curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    apt-get update && apt-get install -y google-cloud-cli

RUN npm install -g @google/gemini-cli @anthropic-ai/claude-code

# Install code-server
RUN curl -fsSL https://code-server.dev/install.sh | sh
RUN code-server --install-extension golang.go --install-extension vscodevim.vim

COPY --from=build-repo-sandbox /repo-sandbox /repo-agent/repo-sandbox
COPY --from=build-gh /bin/gh /bin/gh

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

WORKDIR /workspaces
ENTRYPOINT ["/repo-agent/repo-sandbox", "agent"]
