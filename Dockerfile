# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build mimorid binary
RUN CGO_ENABLED=0 GOOS=linux go build -o mimorid ./cmd/mimorid

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates wget

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/mimorid .

# Expose gRPC and HTTP ports
EXPOSE 4000 4001

# Default environment variables
ENV MIMORI_ADDR=:4000
ENV MIMORI_DATA=/data
ENV MIMORI_LOG_FORMAT=json

# Run the binary
CMD ["./mimorid"]
