# Build stage
FROM golang:1.24 AS builder
WORKDIR /app

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

COPY issue-sidecar/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /issue-sidecar .

# Final stage
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /issue-sidecar /issue-sidecar
CMD ["/issue-sidecar"]