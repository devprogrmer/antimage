FROM golang:1.23-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git make

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build binaries
RUN CGO_ENABLED=1 go build -o antimage-panel ./cmd/panel
RUN CGO_ENABLED=1 go build -o antimage-node ./cmd/node
RUN CGO_ENABLED=1 go build -o antimage-ctl ./cmd/ctl

FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates sqlite

# Copy binaries from builder
COPY --from=builder /build/antimage-panel /usr/local/bin/
COPY --from=builder /build/antimage-node /usr/local/bin/
COPY --from=builder /build/antimage-ctl /usr/local/bin/

# Copy web assets (built separately)
COPY --from=builder /build/web/dist /app/web/dist

# Create data directory
RUN mkdir -p /data

# Expose panel port
EXPOSE 8080

# Expose node gRPC port
EXPOSE 50051

# Default command runs panel
CMD ["/usr/local/bin/antimage-panel", "--db", "/data/antimage.db", "--listen", "0.0.0.0:8080"]
