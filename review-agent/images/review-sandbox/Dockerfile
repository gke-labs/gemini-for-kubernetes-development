# Build stage
FROM ghcr.io/coder/envbuilder
ENV KANIKO_DIR=/.envbuilder
COPY review-sandbox/devcontainer/devcontainer.json /review-agent/devcontainer.json
COPY review-sandbox/devcontainer/entrypoint.sh /review-agent/entrypoint.sh