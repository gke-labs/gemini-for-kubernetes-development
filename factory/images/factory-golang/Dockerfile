# --------------------------------

# Build the gh CLI
FROM golang:1.26.3 AS build-gh
RUN GOBIN=/bin go install github.com/cli/cli/v2/cmd/gh@v2.82.1

# --------------------------------

# Build gemini-cli: v0.64.0 with google-gemini/gemini-cli#29778, which makes
# ACP mode (acpd's sessions) truncate tool output as -p and interactive
# mode do, instead of sending a huge shell output to the model whole. The
# same tarball npm would publish (npm pack of the bundle), installed below
# in place of @google/gemini-cli. Pinned by commit; back to the npm package,
# pinned, once a release has the fix.
FROM node:20-bookworm AS build-gemini-cli
ARG GEMINI_CLI_REPO=https://github.com/barney-s/gemini-cli.git
ARG GEMINI_CLI_COMMIT=1fe45046ee2487f5e002fdf58f6cd8cde2ef8ede
RUN git clone --filter=blob:none "${GEMINI_CLI_REPO}" /src && \
    cd /src && git checkout --detach "${GEMINI_CLI_COMMIT}" && \
    npm ci --ignore-scripts --no-audit --no-fund && \
    npm run bundle && \
    npm pack --ignore-scripts && \
    mv google-gemini-cli-*.tgz /gemini-cli.tgz

# --------------------------------

# Build libghostty-vt, the terminal emulator the daemon's terminal sessions
# use (factory/pkg/termd, build tag libghostty): a static library that
# depends only on libc. The signed release tarballs, checked against
# their published SHA256SUMS, built offline with Zig (the deps tarball
# holds every Zig package). See factory/design/daemon-terminal-sessions.md.
FROM golang:1.26.3 AS build-libghostty
ARG ZIG_VERSION=0.17.0
ARG ZIG_SHA256=1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026
ARG LIBGHOSTTY_VT_VERSION=1.0.0
ARG LIBGHOSTTY_VT_SHA256=4a6ef758be721923e1969e7b81b443dcc92d8dd3fb5a39600c03a8817a753676
ARG LIBGHOSTTY_VT_DEPS_SHA256=4d5ea5e448e4c7ea8fb55ccbaa8668f2a97d44f438cee831e2b7ecd162d85c29
RUN apt-get update && apt-get install -y --no-install-recommends xz-utils && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN curl -fsSLo zig.tar.xz "https://ziglang.org/download/${ZIG_VERSION}/zig-x86_64-linux-${ZIG_VERSION}.tar.xz" && \
    echo "${ZIG_SHA256}  zig.tar.xz" | sha256sum -c - && \
    mkdir /opt/zig && tar -xJf zig.tar.xz -C /opt/zig --strip-components=1 && rm zig.tar.xz
RUN base="https://release.files.ghostty.org/libghostty-vt/${LIBGHOSTTY_VT_VERSION}" && \
    curl -fsSLO "${base}/libghostty-vt-${LIBGHOSTTY_VT_VERSION}.tar.gz" && \
    curl -fsSLO "${base}/libghostty-vt-${LIBGHOSTTY_VT_VERSION}-deps.tar.gz" && \
    echo "${LIBGHOSTTY_VT_SHA256}  libghostty-vt-${LIBGHOSTTY_VT_VERSION}.tar.gz" | sha256sum -c - && \
    echo "${LIBGHOSTTY_VT_DEPS_SHA256}  libghostty-vt-${LIBGHOSTTY_VT_VERSION}-deps.tar.gz" | sha256sum -c - && \
    tar -xzf "libghostty-vt-${LIBGHOSTTY_VT_VERSION}.tar.gz" && \
    tar -xzf "libghostty-vt-${LIBGHOSTTY_VT_VERSION}-deps.tar.gz"
RUN cd "libghostty-vt-${LIBGHOSTTY_VT_VERSION}" && \
    ZIG_GLOBAL_CACHE_DIR=/tmp/zig-cache /opt/zig/zig build \
      --prefix /opt/libghostty-vt \
      --system "../libghostty-vt-${LIBGHOSTTY_VT_VERSION}-deps" \
      -Demit-lib-vt -Doptimize=ReleaseFast -Dcpu=baseline && \
    rm -rf /tmp/zig-cache

# --------------------------------

# Build factory binary: cgo, linking libghostty-vt statically (libghostty
# tag). Builds without the tag - the factory CLI anywhere else - stay pure
# Go, with termd's pure-Go emulator.
FROM golang:1.26.3 AS build-factory
RUN apt-get update && apt-get install -y --no-install-recommends pkg-config && rm -rf /var/lib/apt/lists/*
COPY --from=build-libghostty /opt/libghostty-vt /opt/libghostty-vt
ENV PKG_CONFIG_PATH=/opt/libghostty-vt/lib/pkgconfig
WORKDIR /app/factory
COPY factory/go.mod factory/go.sum ./
RUN go mod download
COPY factory/ ./
RUN CGO_ENABLED=1 GOOS=linux go build -tags libghostty -ldflags="-s -w" -v -o /factory .



# Main image
FROM golang:1.26.3
ENV GOTOOLCHAIN=auto

# We want a functional python, because gemini-cli likes to use python
RUN apt-get update && apt-get install -y python3 python3-pip python3-yaml python-is-python3 python3-venv

RUN apt-get update && apt-get install -y git tmux nodejs npm sudo curl apt-transport-https ca-certificates gnupg jq ripgrep tini

# Install Google Cloud CLI
RUN echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list && \
    curl -fsSL --retry 5 --retry-connrefused --retry-delay 3 https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    apt-get update && apt-get install -y google-cloud-cli google-cloud-cli-gke-gcloud-auth-plugin kubectl

COPY --from=build-gemini-cli /gemini-cli.tgz /tmp/gemini-cli.tgz
RUN npm install -g /tmp/gemini-cli.tgz @anthropic-ai/claude-code && rm /tmp/gemini-cli.tgz && gemini --version

# Antigravity CLI (agy) is a Go binary, not an npm package. It self-updates
# in the background unless this is exactly "true" — the image pins it.
# --compressed: the server sometimes sends the script gzipped even when not
# asked to, and bash then chokes on the raw bytes.
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true
RUN curl -fsSL --compressed --retry 5 --retry-connrefused --retry-delay 3 https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin && agy --version

# acpd's antigravity engine runs agy's own ACP server (agy acp), whose
# conversations are the CLI's: Google's separate agy_acp_server is no
# longer installed.

# Install code-server
RUN curl -fsSL --retry 5 --retry-connrefused --retry-delay 3 https://code-server.dev/install.sh | sh
RUN code-server --install-extension golang.go --install-extension vscodevim.vim

COPY --from=build-gh /bin/gh /bin/gh
COPY --from=build-factory /factory /usr/local/bin/factory

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go

RUN apt-get install -y vim less 

# Install setup-envtest and the envtest binaries (etcd, kube-apiserver),
# at one pinned version linked to a fixed path, so that 'go test' finds them
# through KUBEBUILDER_ASSETS. Unpinned, "setup-envtest use" fetched whatever
# was newest at build time while KUBEBUILDER_ASSETS named an older version,
# and every envtest test failed with "etcd: no such file or directory".
# A repository that wants another version sets KUBEBUILDER_ASSETS itself
# (the overseer's env), e.g. to what its warm-workspace script set up.
ARG ENVTEST_K8S_VERSION=1.37.0
RUN go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.22 && \
    mkdir -p /usr/local/kubebuilder && \
    ln -sfn "$($(go env GOPATH)/bin/setup-envtest use -p path ${ENVTEST_K8S_VERSION})" /usr/local/kubebuilder/bin && \
    test -x /usr/local/kubebuilder/bin/etcd && test -x /usr/local/kubebuilder/bin/kube-apiserver
ENV KUBEBUILDER_ASSETS=/usr/local/kubebuilder/bin

WORKDIR /workspaces
# tini as PID 1 reaps the zombies builds and tests leave; factory's
# sandbox pods set the same command (sandbox.DaemonCommand).
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/factory", "daemon"]
