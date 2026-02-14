# --------------------------------
# Build the repo-sandbox binary
FROM golang:1.25 AS build-repo-sandbox
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

COPY cmd/repo-sandbox/ cmd/repo-sandbox/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /repo-sandbox ./cmd/repo-sandbox

# --------------------------------

# Build the gh CLI
FROM golang:1.25 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1

# -----------------------

FROM ubuntu:24.04

ARG DOCKER_VERSION="5:27.5.1-1~ubuntu.24.04~noble"

ENV DEBIAN_FRONTEND="noninteractive"
RUN apt-get update && apt-get -y install ca-certificates curl

RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc && 
    chmod a+r /etc/apt/keyrings/docker.asc && 
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu 
    $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list && 
    apt-get update && 
    apt-get install -qqy 
    docker-ce=${DOCKER_VERSION} 
    docker-ce-cli=${DOCKER_VERSION} 
    containerd.io 
    docker-buildx-plugin 
    docker-compose-plugin 
    iproute2 
    iptables

# ------- Install go -------
#RUN apt-get update && apt-get install -y golang-go
## wget -c https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
ENV GO_VERSION=1.25.0
ENV ARCH=amd64

# Download, extract, and set up environment variables
RUN apt-get update && apt-get install -y wget
RUN wget -c https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz -O go.tar.gz && 
    tar -C /usr/local -xzf go.tar.gz && 
    rm go.tar.gz

ENV PATH=$PATH:/usr/local/go/bin
ENV GOTOOLCHAIN=auto

# -------- Install python --------
# We want a functional python, because gemini-cli likes to use python
RUN apt-get update && apt-get install -y python3 python3-pip python3-yaml python-is-python3 python3-venv
RUN apt-get update && apt-get install -y git tmux nodejs npm sudo curl

RUN npm install -g @google/gemini-cli

# Install Kind
RUN curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.26.0/kind-linux-amd64 && 
    chmod +x ./kind && 
    mv ./kind /usr/local/bin/kind

# Install Kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && 
    chmod +x kubectl && 
    mv kubectl /usr/local/bin/kubectl

# Install code-server
RUN curl -fsSL https://code-server.dev/install.sh | sh
RUN code-server --install-extension golang.go --install-extension vscodevim.vim

COPY --from=build-repo-sandbox /repo-sandbox /repo-agent/repo-sandbox
COPY --from=build-gh /bin/gh /bin/gh
COPY scripts/start-dockerd.sh /usr/local/bin/start-dockerd.sh
RUN chmod +x /usr/local/bin/start-dockerd.sh

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

WORKDIR /workspaces
ENTRYPOINT ["/repo-agent/repo-sandbox", "agent"]
