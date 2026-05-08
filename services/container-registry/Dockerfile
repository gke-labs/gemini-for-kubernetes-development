# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build the container-registry binary
FROM golang:1.26.2 AS builder
WORKDIR /app

# Copy the Go Modules manifests
COPY go.work go.work
COPY go.work.sum go.work.sum
COPY go.mod go.mod
COPY go.sum go.sum
COPY repo-agent/go.mod repo-agent/go.mod
COPY repo-agent/go.sum repo-agent/go.sum
COPY agentsandboxes/go.mod agentsandboxes/go.mod
COPY agentsandboxes/go.sum agentsandboxes/go.sum
COPY dev/tools/go.mod dev/tools/go.mod
COPY dev/tools/go.sum dev/tools/go.sum
COPY services/container-registry/go.mod services/container-registry/go.mod
COPY services/container-registry/go.sum services/container-registry/go.sum

# Copy the source code
COPY services/container-registry/ services/container-registry/

WORKDIR /app/services/container-registry
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -v -o /container-registry ./cmd/container-registry/

# Final image
FROM alpine:3.18
RUN apk add --no-cache ca-certificates
COPY --from=builder /container-registry /usr/local/bin/container-registry
EXPOSE 5000
ENTRYPOINT ["/usr/local/bin/container-registry"]
