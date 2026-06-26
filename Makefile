SHELL := /bin/bash
SERVICES := auth audit core gc gateway metadata proxy scanner signer storage tenant webhook
GO := go
BUF := buf

.PHONY: all build test lint proto dev-certs seed-dev dev-bootstrap clean help \
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

## seed-dev: Load dev-seed SQL files into the running local postgres container
## Usage: make seed-dev   (stack must be up: docker compose up -d)
seed-dev:
	@echo "==> Loading infra/dev-seed/service_accounts.sql into registry_auth …"
	@docker exec -i docker-compose-postgres-1 \
		psql -U registry -d registry_auth \
		< infra/dev-seed/service_accounts.sql
	@echo "==> Done. Verify with:"
	@echo "    docker exec docker-compose-postgres-1 psql -U registry -d registry_auth -c \"SELECT id, name, disabled_at FROM service_accounts WHERE tenant_id = '98dbe36b-ef28-4903-b25c-bff1b2921c9e';\""

## dev-bootstrap: Run the registry-auth bootstrap CLI against the running local
## stack to create the dev admin + tenant. This is what replaces the dev-seed
## admin migrations once Phase 2.6 of REDESIGN-001 deletes them. Idempotent:
## safe to run multiple times against the same DB (admin-already-exists exits
## cleanly with code 2). Stack must be up: docker compose up -d
##
## Default dev creds (must match the legacy dev-seed for backwards compat):
##   email:    admin@dev.local
##   username: admin
##   password: Admin1234!
##   tenant:   98dbe36b-ef28-4903-b25c-bff1b2921c9e (named "Development")
dev-bootstrap:
	@echo "==> Bootstrapping local stack with dev admin admin@dev.local …"
	@printf 'Admin1234!\n' | docker exec -i docker-compose-registry-auth-1 \
		/server bootstrap \
		--admin-email admin@dev.local \
		--admin-username admin \
		--admin-password-stdin \
		--tenant-name "Development" \
		--tenant-id 98dbe36b-ef28-4903-b25c-bff1b2921c9e
	@echo "==> Done. Login with admin / Admin1234! on tenant 98dbe36b-…"

## clean: Remove build artifacts
clean:
	@for svc in $(SERVICES); do \
		$(MAKE) -C services/$$svc clean; \
	done
	rm -rf certs/

## help: Show this help
help:
	@grep -E '^##' Makefile | sed 's/## //'
