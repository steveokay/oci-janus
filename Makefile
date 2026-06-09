SHELL := /bin/bash
SERVICES := auth audit core gc gateway metadata proxy scanner signer storage tenant webhook
GO := go
BUF := buf

.PHONY: all build test lint proto dev-certs clean help \
        $(addprefix build-,$(SERVICES)) \
        $(addprefix test-,$(SERVICES)) \
        $(addprefix lint-,$(SERVICES))

all: proto build test

## build-all: Build all service binaries
build: $(addprefix build-,$(SERVICES))
$(addprefix build-,$(SERVICES)): build-%:
	$(MAKE) -C services/$* build

## test-all: Run unit tests for all services and libs
test: test-libs $(addprefix test-,$(SERVICES))
test-libs:
	cd libs && $(GO) test -race ./...
$(addprefix test-,$(SERVICES)): test-%:
	$(MAKE) -C services/$* test

## lint-all: Run golangci-lint across all modules
lint: lint-libs $(addprefix lint-,$(SERVICES))
lint-libs:
	cd libs && golangci-lint run ./...
$(addprefix lint-,$(SERVICES)): lint-%:
	$(MAKE) -C services/$* lint

## proto: Regenerate all protobuf stubs
proto:
	$(BUF) generate --template proto/buf.gen.yaml

## proto-lint: Lint proto files
proto-lint:
	cd proto && $(BUF) lint

## proto-breaking: Check for breaking proto changes against main
proto-breaking:
	cd proto && $(BUF) breaking --against '.git#branch=main'

## test-integration: Run integration tests (requires Docker)
test-integration:
	@for svc in $(SERVICES); do \
		echo "==> Integration tests: $$svc"; \
		$(MAKE) -C services/$$svc test-integration; \
	done

## dev-certs: Generate self-signed mTLS certs for local development
dev-certs:
	@mkdir -p certs
	@./scripts/gen-dev-certs.sh

## clean: Remove build artifacts
clean:
	@for svc in $(SERVICES); do \
		$(MAKE) -C services/$$svc clean; \
	done
	rm -rf certs/

## help: Show this help
help:
	@grep -E '^##' Makefile | sed 's/## //'
