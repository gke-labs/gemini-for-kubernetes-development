FROM golang:1.26.3 AS builder

WORKDIR /app
# The factory module has no local module dependencies, so build it
# standalone (no go.work / sibling modules needed).
COPY factory/go.mod factory/go.mod
COPY factory/go.sum factory/go.sum
COPY factory/ factory/

WORKDIR /app/factory
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -o factory ./main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /app/factory/factory /factory
USER 65532:65532

ENTRYPOINT ["/factory", "token-daemon"]
