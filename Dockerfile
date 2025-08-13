# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.21 AS builder
WORKDIR /src
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/deployagent

FROM ubuntu:24.04
WORKDIR /app
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        tzdata \
        docker.io \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/deployagent /app/deployagent
COPY config.yaml /app/config.yaml
COPY web /app/web
COPY scripts /app/scripts
RUN mkdir -p /app/uploads /app/logs && chmod +x /app/scripts/*.sh || true

EXPOSE 8080
ENTRYPOINT ["/app/deployagent"]

