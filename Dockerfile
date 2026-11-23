# syntax=docker/dockerfile:1
# Build natively on the builder and cross-compile the CLI for the target image.
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY proto ./proto
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/agend ./cmd/agend \
    && mkdir -p /out/home/.config/agend /out/workspace

# No target-architecture RUN steps: both amd64 and arm64 build without QEMU.
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
LABEL org.opencontainers.image.source="https://github.com/agend-sh/cli" \
      org.opencontainers.image.url="https://agend.sh" \
      org.opencontainers.image.title="agend-sh" \
      org.opencontainers.image.description="Persistent Linux environments for AI agents, with interactive terminals and HTTPS previews." \
      org.opencontainers.image.licenses="MIT" \
      io.modelcontextprotocol.server.name="io.github.agend-sh/agend-sh"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=0755 /out/agend /usr/local/bin/agend
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/agend-entrypoint
COPY LICENSE /usr/share/licenses/agend/LICENSE
COPY --from=build --chown=10001:10001 /out/home/ /home/agend/
COPY --from=build --chown=10001:10001 /out/workspace/ /workspace/
ENV HOME=/home/agend \
    AGEND_NO_AUTOUPDATE=1 \
    AGEND_LOCAL_ROOT=/workspace
USER 10001:10001
WORKDIR /workspace
ENTRYPOINT ["agend-entrypoint"]
CMD ["mcp"]
