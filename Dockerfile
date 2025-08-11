# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS builder
WORKDIR /src
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/deployagent

FROM alpine:3.20
WORKDIR /app
RUN apk add --no-cache bash ca-certificates tzdata docker-cli
COPY --from=builder /out/deployagent /app/deployagent
COPY config.yaml /app/config.yaml
COPY web /app/web
COPY scripts /app/scripts
RUN mkdir -p /app/uploads /app/logs && chmod +x /app/scripts/*.sh || true

EXPOSE 8080
ENTRYPOINT ["/app/deployagent"]

