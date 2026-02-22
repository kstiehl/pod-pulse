# Dockerfile
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache \
    gcc \
    musl-dev \
    gpgme-dev \
    libgpg-error-dev \
    linux-headers \
    btrfs-progs-dev
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build,id=go-build-cache \
    --mount=type=cache,target=/go/pkg/mod,id=go-mod-cache \
    CGO_ENABLED=1 go build -o /pod-pulse

FROM alpine
COPY --from=builder /pod-pulse /pod-pulse
