# Build the gh CLI
FROM golang:1.25.7 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1

# -----------------------

FROM ubuntu:24.04

ARG DOCKER_VERSION="5:27.5.1-1~ubuntu.24.04~noble"

ENV DEBIAN_FRONTEND="noninteractive"
RUN echo 'Acquire::http::Timeout "60";' > /etc/apt/apt.conf.d/99timeout && \
    echo 'Acquire::https::Timeout "60";' >> /etc/apt/apt.conf.d/99timeout

RUN for i in {1..5}; do apt-get update && break || (echo "apt-get update failed, retrying in 10s..." && sleep 10); done && \
    apt-get -y install --no-install-recommends ca-certificates curl gnupg && \
    rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc && \
    chmod a+r /etc/apt/keyrings/docker.asc && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
    $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list

RUN curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_20.x nodistro main" > /etc/apt/sources.list.d/nodesource.list

RUN for i in {1..5}; do apt-get update && break || (echo "apt-get update failed, retrying in 10s..." && sleep 10); done && \
    apt-get install -y --no-install-recommends \
    docker-ce=${DOCKER_VERSION} \
    docker-ce-cli=${DOCKER_VERSION} \
    containerd.io \
    docker-buildx-plugin \
    docker-compose-plugin \
    iproute2 \
    iptables \
    nodejs \
    wget \
    git \
    tmux \
    sudo \
    python3 \
    python3-pip \
    python3-yaml \
    python3-requests \
    python-is-python3 \
    python3-venv \
    && rm -rf /var/lib/apt/lists/*

# ------- Install go -------
ENV GO_VERSION=1.25.7
ENV ARCH=amd64

# Download, extract, and set up environment variables
RUN wget -c https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz -O go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && \
    rm go.tar.gz

ENV PATH=$PATH:/usr/local/go/bin
ENV GOTOOLCHAIN=auto

RUN npm install -g @google/gemini-cli

# Install Kind
RUN curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.26.0/kind-linux-amd64 && \
    chmod +x ./kind && \
    mv ./kind /usr/local/bin/kind

# Install Kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/kubectl

# Install Helm
RUN curl -fsSL https://packages.buildkite.com/helm-linux/helm-debian/gpgkey | gpg --dearmor | tee /usr/share/keyrings/helm.gpg > /dev/null && \
    echo "deb [signed-by=/usr/share/keyrings/helm.gpg] https://packages.buildkite.com/helm-linux/helm-debian/any/ any main" | tee /etc/apt/sources.list.d/helm-stable-debian.list > /dev/null && \
    for i in {1..5}; do apt-get update && break || (echo "apt-get update failed, retrying in 10s..." && sleep 10); done && \
    apt-get install -y --no-install-recommends helm && \
    rm -rf /var/lib/apt/lists/*

# Install code-server
RUN curl -fsSL https://code-server.dev/install.sh | sh
RUN for i in {1..5}; do code-server --install-extension golang.go --install-extension vscodevim.vim && break || sleep 5; done

COPY --from=build-gh /bin/gh /bin/gh
COPY repo-agent/scripts/start-dockerd.sh /usr/local/bin/start-dockerd.sh
RUN chmod +x /usr/local/bin/start-dockerd.sh

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

WORKDIR /workspaces
