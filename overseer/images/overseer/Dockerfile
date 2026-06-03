FROM golang:1.26.2 AS builder

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
COPY factory/go.mod factory/go.mod
COPY factory/go.sum factory/go.sum

# Copy the source code
COPY agentsandboxes/ agentsandboxes/
COPY repo-agent/pkg/ repo-agent/pkg/
COPY repo-agent/api/ repo-agent/api/
COPY overseer/ overseer/
COPY factory/ factory/

WORKDIR /app/factory
RUN go build -o factory ./main.go

FROM node:20-slim

# Install tools
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    bash \
    git \
    curl \
    jq \
    gh \
    procps \
    tmux \
    ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/

# Install gemini-cli
RUN npm install -g @google/gemini-cli

WORKDIR /workspaces

ENV FACTORY_LOGS=/workspaces/logs/factory/

COPY --from=builder /app/factory/factory /usr/local/bin/factory
COPY overseer/images/overseer/run.sh /workspaces/run.sh
COPY overseer/images/overseer/summarize.sh /workspaces/summarize.sh
COPY overseer/images/overseer/prompt/ /workspaces/prompt/
RUN chmod +x /workspaces/run.sh /workspaces/summarize.sh

ENTRYPOINT ["/workspaces/run.sh"]
