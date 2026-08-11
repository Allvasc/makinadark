# Stage 1: Builder
FROM golang:1.24-alpine AS builder

WORKDIR /app

ENV GOTOOLCHAIN=auto

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build CGO-free binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/makinadark-api cmd/api/main.go

# Stage 2: Minimal Runner
FROM alpine:latest

WORKDIR /app

# Install ca-certificates, ffmpeg, fonts, and timezones
RUN apk --no-cache add ca-certificates ffmpeg tzdata font-dejavu ttf-dejavu

# Copy binary from builder (secrets are injected at runtime via env vars / env_file,
# never baked into the image)
COPY --from=builder /app/makinadark-api /app/makinadark-api

# Create media directories
RUN mkdir -p /app/media/images /app/media/audio /app/media/music /app/media/output /app/data

EXPOSE 8080

CMD ["/app/makinadark-api"]
