# Build stage
FROM ghcr.io/coder/envbuilder
ENV KANIKO_DIR=/.envbuilder
# injecting devcontainer.json via `devcontainerConfigRef` in RGD
COPY issue-sandbox/gemini-cli-entrypoint.sh /issue-agent/gemini-cli-entrypoint.sh