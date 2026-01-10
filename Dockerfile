# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')" \
    -o /app/calbridge \
    ./cmd/calbridge

# Final stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 calbridge && \
    adduser -u 1000 -G calbridge -s /bin/sh -D calbridge

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/calbridge /app/calbridge
COPY ui/ /app/ui/

# Copy entrypoint script
COPY scripts/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# Create data directory
RUN mkdir -p /app/data && chown -R calbridge:calbridge /app

# Switch to non-root user
USER calbridge

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/healthz || exit 1

# Set entrypoint
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/calbridge"]
