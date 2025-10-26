# Build stage
FROM ghcr.io/coder/envbuilder
ENV KANIKO_DIR=/.envbuilder
# injecting devcontainer.json via `devcontainerConfigRef` in RGD
#COPY review-sandbox/devcontainer.json /repo-agent/devcontainer.json
COPY review-sandbox/entrypoint.sh /repo-agent/entrypoint.sh