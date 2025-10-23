# Build stage
FROM ghcr.io/coder/envbuilder
ENV KANIKO_DIR=/.envbuilder
# injecting devcontainer.json via `devcontainerConfigRef` in RGD
COPY issue-sandbox/entrypoint.sh /issue-agent/entrypoint.sh