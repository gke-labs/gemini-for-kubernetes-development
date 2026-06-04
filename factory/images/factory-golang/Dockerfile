# --------------------------------

# Build the gh CLI
FROM golang:1.26.2 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1

# --------------------------------

# Build envd
FROM golang:1.26.2 AS build-envd
ENV GOTOOLCHAIN=auto
RUN git clone --depth 1 --branch 2026.20 https://github.com/e2b-dev/infra.git /infra && \
    cd /infra/packages/envd && \
    sed -i 's/oomWrapperScript := fmt.Sprintf.*/oomWrapperScript := fmt.Sprintf("exec \\\"\${@}\\\" # %d", niceDelta)/' internal/services/process/handler/handler.go && \
    # Future: To inherit full container environment in envd instead of stripping it, uncomment the following line:
    sed -i 's/var formattedVars \[\]string/formattedVars := os.Environ()/' internal/services/process/handler/handler.go && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /envd .

# --------------------------------

# Build factory binary
FROM golang:1.26.2 AS build-factory
WORKDIR /app/factory
COPY factory/go.mod factory/go.sum ./
RUN go mod download
COPY factory/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /factory .



# Main image
FROM golang:1.26.2
ENV GOTOOLCHAIN=auto

# We want a functional python, because gemini-cli likes to use python
RUN apt-get update && apt-get install -y python3 python3-pip python3-yaml python-is-python3 python3-venv

RUN apt-get update && apt-get install -y git tmux nodejs npm sudo curl apt-transport-https ca-certificates gnupg jq ripgrep

# Install Google Cloud CLI
RUN echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list && \
    curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    apt-get update && apt-get install -y google-cloud-cli

RUN npm install -g @google/gemini-cli @anthropic-ai/claude-code

# Install code-server
RUN curl -fsSL https://code-server.dev/install.sh | sh
RUN code-server --install-extension golang.go --install-extension vscodevim.vim

COPY --from=build-gh /bin/gh /bin/gh
COPY --from=build-envd /envd /usr/local/bin/envd
COPY --from=build-factory /factory /usr/local/bin/factory

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

RUN apt-get install -y vim less 

# Install setup-envtest and download assets
RUN go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.22 && \
    $(go env GOPATH)/bin/setup-envtest use

# (Optional) Export KUBEBUILDER_ASSETS so it's available for 'go test' automatically.
# The path below is the default location setup-envtest extracts the binaries to.
ENV KUBEBUILDER_ASSETS=/root/.local/share/kubebuilder-envtest/k8s/1.36.0-linux-amd64

WORKDIR /workspaces
ENTRYPOINT ["/usr/local/bin/factory", "daemon"]
