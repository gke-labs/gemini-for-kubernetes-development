FROM golang:1.25.7 AS builder

WORKDIR /app
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the go source
COPY api/ api/
COPY pkg/ pkg/
COPY cmd/overseer-cli/ cmd/overseer-cli/
RUN go build -o overseer-cli ./cmd/overseer-cli/main.go

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
    ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/

# Install gemini-cli
RUN npm install -g @google/gemini-cli

WORKDIR /workspaces

COPY --from=builder /app/overseer-cli /usr/local/bin/overseer-cli
COPY images/overseer/run.sh /workspaces/run.sh
COPY images/overseer/system_prompt.txt /workspaces/system_prompt.txt
RUN chmod +x /workspaces/run.sh

ENTRYPOINT ["/workspaces/run.sh"]
