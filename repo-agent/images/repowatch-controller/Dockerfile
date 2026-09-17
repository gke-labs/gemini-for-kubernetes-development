# Build the manager binary
FROM golang:1.26.3 as builder

WORKDIR /workspace
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
COPY repo-agent/cmd/repowatch-controller/ repo-agent/cmd/repowatch-controller/
COPY factory/ factory/

# Build
WORKDIR /workspace/repo-agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager cmd/repowatch-controller/main.go

# Build the factory CLI from monorepo source (same pattern as overseer's image);
# the controller execs it to run agent tasks — process invocation only, no Go imports.
WORKDIR /workspace/factory
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o factory ./main.go

# Install kubectl: the factory CLI's envd layer shells out to it (sandbox
# lookup, port-forward), same requirement overseer's image satisfies.
WORKDIR /workspace
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/repo-agent/manager .
COPY --from=builder /workspace/factory/factory /usr/local/bin/factory
COPY --from=builder /workspace/kubectl /usr/local/bin/kubectl
USER 65532:65532

ENTRYPOINT ["/manager"]
