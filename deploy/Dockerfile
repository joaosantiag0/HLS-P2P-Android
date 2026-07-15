# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=build /out/server /app/server
RUN mkdir -p /app/data/cache && chown -R app:app /app
USER app
ENV LISTEN_ADDR=:8080 \
    CACHE_DIR=/app/data/cache \
    CACHE_MAX_BYTES=2147483648 \
    ALLOW_PRIVATE_NET=false
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
CMD ["/app/server"]
