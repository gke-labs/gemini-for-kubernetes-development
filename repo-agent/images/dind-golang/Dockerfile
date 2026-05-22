# Build the gh CLI
FROM golang:1.26.1 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1

# -----------------------

FROM ubuntu:24.04

ARG DOCKER_VERSION="5:27.5.1-1~ubuntu.24.04~noble"

ENV DEBIAN_FRONTEND="noninteractive"

# 1. Install prerequisites for adding apt repositories
RUN apt-get update && apt-get -y install --no-install-recommends \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg \
    wget \
    jq

# 2. Add apt repositories (Docker, NodeSource, Google Cloud, Helm)
RUN mkdir -p /etc/apt/keyrings && \
    # Docker
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc && \
    chmod a+r /etc/apt/keyrings/docker.asc && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo \"$VERSION_CODENAME\") stable" > /etc/apt/sources.list.d/docker.list && \
    # NodeSource
    curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_20.x nodistro main" > /etc/apt/sources.list.d/nodesource.list && \
    # Google Cloud
    curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /etc/apt/keyrings/cloud.google.com.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/cloud.google.com.gpg] https://packages.cloud.google.com/apt cloud-sdk main" > /etc/apt/sources.list.d/google-cloud-sdk.list && \
    # Helm
    curl -fsSL https://packages.buildkite.com/helm-linux/helm-debian/gpgkey | gpg --dearmor -o /etc/apt/keyrings/helm.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/helm.gpg] https://packages.buildkite.com/helm-linux/helm-debian/any/ any main" > /etc/apt/sources.list.d/helm-stable-debian.list

# 3. Install all apt packages
RUN apt-get update && \
    apt-get install -y \
    docker-ce=${DOCKER_VERSION} \
    docker-ce-cli=${DOCKER_VERSION} \
    containerd.io \
    docker-buildx-plugin \
    docker-compose-plugin \
    iproute2 \
    iptables \
    nodejs \
    python3 \
    python3-pip \
    python3-yaml \
    python3-requests \
    python-is-python3 \
    python3-venv \
    git \
    tmux \
    sudo \
    helm \
    build-essential \
    google-cloud-cli \
    ripgrep

# ------- Install go -------
ENV GO_VERSION=1.26.1
ENV ARCH=amd64

# Download, extract, and set up environment variables
RUN wget -c https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz -O go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && \
    rm go.tar.gz && \
    ln -sf /usr/local/go/bin/go /usr/local/bin/go

ENV PATH=$PATH:/usr/local/go/bin
ENV GOTOOLCHAIN=auto

# -------- Install global Node.js packages --------
RUN npm install -g @google/gemini-cli @anthropic-ai/claude-code

# -------- Download binary releases --------
# Install Kind
RUN curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.26.0/kind-linux-amd64 && \
    chmod +x ./kind && \
    mv ./kind /usr/local/bin/kind

# Install Kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/kubectl

# Install Kustomize
ENV KUSTOMIZE_VERSION=v5.3.0
RUN curl -sSfL "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_$(go env GOOS)_$(go env GOARCH).tar.gz" | tar xz \
 && mv kustomize /usr/local/bin/

# Install code-server
RUN curl -fsSL https://code-server.dev/install.sh | sh && \
    code-server --install-extension golang.go --install-extension vscodevim.vim

# -------- Install Go tools --------
# Install yq
RUN go install github.com/mikefarah/yq/v4@latest && \
    ln -s $(go env GOPATH)/bin/yq /usr/local/bin/yq

# Install setup-envtest and download assets
RUN go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.22 && \
    $(go env GOPATH)/bin/setup-envtest use

# (Optional) Export KUBEBUILDER_ASSETS so it's available for 'go test' automatically.
# The path below is the default location setup-envtest extracts the binaries to.
ENV KUBEBUILDER_ASSETS=/root/.local/share/kubebuilder-envtest/k8s/1.35.0-linux-amd64

COPY --from=build-gh /bin/gh /bin/gh
COPY repo-agent/scripts/start-dockerd.sh /usr/local/bin/start-dockerd.sh
RUN chmod +x /usr/local/bin/start-dockerd.sh

WORKDIR /workspaces
