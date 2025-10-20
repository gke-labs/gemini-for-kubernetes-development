# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY review-ui/api/go.mod ./
COPY review-ui/api/go.sum ./
RUN go mod download
COPY review-ui/api/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o /main .

# Final stage
FROM alpine:latest
WORKDIR /
COPY --from=builder /main /main
EXPOSE 8080
CMD ["/main"]
