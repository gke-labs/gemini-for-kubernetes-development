FROM node:20-alpine

# Install tools
RUN apk add --no-cache bash git curl jq github-cli

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/

# Install gemini-cli
RUN npm install -g @google/gemini-cli

WORKDIR /workspaces

COPY images/overseer/run.sh /workspaces/run.sh
COPY images/overseer/system_prompt.txt /workspaces/system_prompt.txt
RUN chmod +x /workspaces/run.sh

ENTRYPOINT ["/workspaces/run.sh"]
