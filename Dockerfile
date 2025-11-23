# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache \
    git \
    gcc \
    musl-dev \
    opus-dev

WORKDIR /build

# Copy go mod files and source
COPY go.mod ./
COPY . .

# Generate go.sum and download dependencies
RUN go mod tidy && go mod download

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -o kvazar ./cmd/kvazar

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (ffmpeg, yt-dlp, and opus)
RUN apk add --no-cache \
    ffmpeg \
    opus \
    python3 \
    py3-pip \
    && pip3 install --no-cache-dir --break-system-packages yt-dlp

# Create non-root user
RUN addgroup -S kvazar && adduser -S kvazar -G kvazar

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/kvazar .

# Change ownership
RUN chown -R kvazar:kvazar /app

# Switch to non-root user
USER kvazar

# Run the bot
CMD ["./kvazar"]
