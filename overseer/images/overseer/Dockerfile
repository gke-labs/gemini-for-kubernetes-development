FROM golang:1.26.1 AS builder

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
COPY overseer/ overseer/

WORKDIR /app/overseer
RUN go build -o overseer-cli ./cmd/overseer-cli/main.go

FROM node:20-slim

ENV DEBIAN_FRONTEND="noninteractive"

RUN echo 'Acquire::http::Timeout "60";' > /etc/apt/apt.conf.d/99timeout && \
    echo 'Acquire::https::Timeout "60";' >> /etc/apt/apt.conf.d/99timeout

# Install tools
RUN for i in 1 2 3 4 5; do apt-get update && break || (echo "apt-get update failed, retrying in 10s... ($i/5)" && sleep 10); done && \
    apt-get install -y --no-install-recommends \
    bash \
    git \
    curl \
    jq \
    gh \
    procps \
    ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/

# Install gemini-cli
RUN npm install -g @google/gemini-cli

WORKDIR /workspaces

COPY --from=builder /app/overseer/overseer-cli /usr/local/bin/overseer-cli
COPY overseer/images/overseer/run.sh /workspaces/run.sh
COPY overseer/images/overseer/prompt/ /workspaces/prompt/
RUN chmod +x /workspaces/run.sh

ENTRYPOINT ["/workspaces/run.sh"]
