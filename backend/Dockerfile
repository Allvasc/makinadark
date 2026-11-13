# Stage 1: Builder - matches go.mod's toolchain requirement (go 1.25+), so
# the build no longer downloads a newer Go toolchain on every build
# (golang:1.24 + GOTOOLCHAIN=auto did exactly that).
FROM golang:1.27-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO-free static binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-w -s" -o /app/makinadark-api ./cmd/api

# Stage 2: Minimal runner (pinned instead of the moving alpine:latest)
FROM alpine:3.24

WORKDIR /app

# ffmpeg for rendering, DejaVu for burned-in subtitles, tzdata so TZ works,
# ca-certificates for every outbound HTTPS call.
RUN apk --no-cache add ca-certificates ffmpeg tzdata font-dejavu

# Secrets are injected at runtime via env vars / env_file, never baked in.
COPY --from=builder /app/makinadark-api /app/makinadark-api

RUN mkdir -p /app/media/images /app/media/audio /app/media/output /app/media/temp /app/data

EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1

CMD ["/app/makinadark-api"]
