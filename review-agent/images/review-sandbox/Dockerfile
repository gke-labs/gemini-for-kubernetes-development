# Build stage
FROM ghcr.io/coder/envbuilder
ENV KANIKO_DIR=/.envbuilder
# injecting devcontainer.json via `devcontainerConfigRef` in RGD
#COPY review-sandbox/devcontainer/devcontainer.json /review-agent/devcontainer.json
COPY review-sandbox/devcontainer/entrypoint.sh /review-agent/entrypoint.sh