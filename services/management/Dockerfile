# syntax=docker/dockerfile:1.7
FROM golang:1.25-bookworm AS builder

WORKDIR /build

COPY go.work go.work.sum ./
COPY libs/ libs/
COPY proto/gen/go/ proto/gen/go/

ARG SERVICE
COPY services/${SERVICE}/ services/${SERVICE}/

RUN cd /build/libs && CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./cmd/healthcheck

WORKDIR /build/services/${SERVICE}
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=builder /bin/healthcheck /healthcheck
COPY --from=builder /bin/server /server

EXPOSE 8085

USER 65532:65532

ENTRYPOINT ["/server"]
