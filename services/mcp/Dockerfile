# syntax=docker/dockerfile:1.7
#
# registry-mcp — Model Context Protocol server for OCI-Janus registry.
#
# Two ways to run this image:
#
# 1. Compose (HTTP transport, always-on for Cursor remote / continue.dev):
#    docker compose --profile mcp up -d registry-mcp
#
# 2. Claude Desktop stdio (spawned per session):
#    Configure ~/Library/Application Support/Claude/claude_desktop_config.json
#    to `docker run -i --rm ... steveokay/oci-janus-mcp:latest`. See docs/MCP.md.
FROM golang:1.25-bookworm AS builder

WORKDIR /build

# Copy workspace and libs first for better layer caching.
COPY go.work go.work.sum ./
COPY libs/ libs/
COPY proto/gen/go/ proto/gen/go/

ARG SERVICE=mcp
COPY services/${SERVICE}/ services/${SERVICE}/

# Build the shared healthcheck helper (same pattern the other services use).
RUN cd /build/libs && CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./cmd/healthcheck

WORKDIR /build/services/${SERVICE}
# GOWORK=off ensures the Docker build resolves modules through go.mod
# alone — the workspace file is only for local dev.
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=builder /bin/healthcheck /healthcheck
COPY --from=builder /bin/server /server

# 8092 is the MCP HTTP transport port (compose default). Stdio mode
# doesn't need EXPOSE — it uses stdin/stdout only.
EXPOSE 8092

USER 65532:65532

# Default entrypoint runs the server. `docker run -i` for stdio mode
# just wires the client's stdin/stdout through.
ENTRYPOINT ["/server"]
