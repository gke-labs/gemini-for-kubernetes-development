# --------------------------------

# Build the gh CLI
FROM golang:1.26.3 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1

# --------------------------------

# Build gemini-cli: v0.64.0 with google-gemini/gemini-cli#29778, which makes
# ACP mode (acpd's sessions) truncate tool output as -p and interactive
# mode do, instead of sending a huge shell output to the model whole. The
# same tarball npm would publish (npm pack of the bundle), installed below
# in place of @google/gemini-cli. Pinned by commit; back to the npm package,
# pinned, once a release has the fix.
FROM node:20-bookworm AS build-gemini-cli
ARG GEMINI_CLI_REPO=https://github.com/barney-s/gemini-cli.git
ARG GEMINI_CLI_COMMIT=1fe45046ee2487f5e002fdf58f6cd8cde2ef8ede
RUN git clone --filter=blob:none "${GEMINI_CLI_REPO}" /src && \
    cd /src && git checkout --detach "${GEMINI_CLI_COMMIT}" && \
    npm ci --ignore-scripts --no-audit --no-fund && \
    npm run bundle && \
    npm pack --ignore-scripts && \
    mv google-gemini-cli-*.tgz /gemini-cli.tgz

# --------------------------------

# Build factory binary
FROM golang:1.26.3 AS build-factory
WORKDIR /app/factory
COPY factory/go.mod factory/go.sum ./
RUN go mod download
COPY factory/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /factory .



# Main image
FROM golang:1.26.3
ENV GOTOOLCHAIN=auto

# We want a functional python, because gemini-cli likes to use python
RUN apt-get update && apt-get install -y python3 python3-pip python3-yaml python-is-python3 python3-venv

RUN apt-get update && apt-get install -y git tmux nodejs npm sudo curl apt-transport-https ca-certificates gnupg jq ripgrep

# Install Google Cloud CLI
RUN echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list && \
    curl -fsSL --retry 5 --retry-connrefused --retry-delay 3 https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    apt-get update && apt-get install -y google-cloud-cli google-cloud-cli-gke-gcloud-auth-plugin kubectl

COPY --from=build-gemini-cli /gemini-cli.tgz /tmp/gemini-cli.tgz
RUN npm install -g /tmp/gemini-cli.tgz @anthropic-ai/claude-code && rm /tmp/gemini-cli.tgz && gemini --version

# Antigravity CLI (agy) is a Go binary, not an npm package. It self-updates
# in the background unless this is exactly "true" — the image pins it.
# --compressed: the server sometimes sends the script gzipped even when not
# asked to, and bash then chokes on the raw bytes.
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true
RUN curl -fsSL --compressed --retry 5 --retry-connrefused --retry-delay 3 https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin && agy --version

# agy has no ACP mode; acpd's antigravity engine runs Google's separate
# agy_acp_server instead (see factory/pkg/acpd/session.go, which names
# this directory). Pinned by version and digest. One RUN, so the zip is
# not kept in a layer; python3 because the image has no unzip, which
# also drops the executable bits.
ARG AGY_ACP_SERVER_VERSION=1.2.1
ARG AGY_ACP_SERVER_SHA256=9fbf0bd584a26478161f637cabd75113f72541c842d148f578ef1a6a9edcb843
RUN curl -fsSL --retry 5 --retry-connrefused --retry-delay 3 -o /tmp/agy-acp-server.zip \
      "https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-${AGY_ACP_SERVER_VERSION}-linux-x86_64.zip" && \
    echo "${AGY_ACP_SERVER_SHA256}  /tmp/agy-acp-server.zip" | sha256sum -c - && \
    python3 -m zipfile -e /tmp/agy-acp-server.zip /opt/agy-acp-server && \
    rm /tmp/agy-acp-server.zip && \
    chmod +x /opt/agy-acp-server/agy_acp_server.par /opt/agy-acp-server/localharness_external

# Install code-server
RUN curl -fsSL --retry 5 --retry-connrefused --retry-delay 3 https://code-server.dev/install.sh | sh
RUN code-server --install-extension golang.go --install-extension vscodevim.vim

COPY --from=build-gh /bin/gh /bin/gh
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
