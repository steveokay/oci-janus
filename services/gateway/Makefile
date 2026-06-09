SERVICE := gateway
BINARY  := bin/$(SERVICE)

.PHONY: build test lint test-integration clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/server

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./internal/testutil/integration/...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
