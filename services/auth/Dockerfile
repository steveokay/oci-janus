# syntax=docker/dockerfile:1.7
FROM golang:1.23-bookworm AS builder

WORKDIR /build

# Copy workspace and libs first for better layer caching
COPY go.work go.work.sum ./
COPY libs/ libs/
COPY proto/gen/go/ proto/gen/go/

ARG SERVICE
COPY services/${SERVICE}/ services/${SERVICE}/

WORKDIR /build/services/${SERVICE}
# GOWORK=off: build against this service's own go.mod, not the workspace.
# The workspace is for local development only; Docker builds must be self-contained.
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=builder /bin/server /server

EXPOSE 50051 8080

USER 65532:65532

ENTRYPOINT ["/server"]
