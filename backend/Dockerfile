# syntax=docker/dockerfile:1

# ── Builder ──────────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Copy source including vendor
COPY backend/ ./

# Build with vendor for reliable offline builds
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=vendor -trimpath -ldflags="-s -w" -o /gateway ./cmd/main.go

# ── Runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata curl

# Non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

COPY --from=builder /gateway /app/gateway
COPY backend/config/ /app/config/

RUN chown -R appuser:appgroup /app
USER appuser

WORKDIR /app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -sf http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/app/gateway"]
