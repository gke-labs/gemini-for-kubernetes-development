FROM golang:1.26.3 AS builder

WORKDIR /app
# The tokenusage package only uses the standard library, so build the
# overseer module standalone (no go.work / sibling modules needed).
COPY overseer/go.mod overseer/go.mod
COPY overseer/go.sum overseer/go.sum
COPY overseer/ overseer/

WORKDIR /app/overseer
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -o token-usage ./cmd/token-usage/main.go

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /app/overseer/token-usage .
USER 65532:65532

ENTRYPOINT ["/token-usage"]
