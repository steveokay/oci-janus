.PHONY: build test lint tidy openapi

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/server ./cmd/server

# Regenerate the OpenAPI description of the BFF from the route table into
# ../../docs/openapi.json, then the Postman collection from that spec. CI runs
# this and fails if either committed artifact is stale (see the openapi job in
# ci-management.yml), so both can never drift from the handlers.
openapi:
	go run ./cmd/openapi-gen
	go run ./cmd/postman-gen

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
