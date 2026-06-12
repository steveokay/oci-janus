SERVICE := core
BINARY  := bin/$(SERVICE)

# OCI conformance settings. The conformance container joins the compose
# network directly so it can reach registry-core by service name. Auth
# is reached via registry-auth:8080 (internal hostname) during the test
# run; registry-core is restarted with that realm and then restored after.
CONFORMANCE_IMAGE     ?= ghcr.io/opencontainers/distribution-spec/conformance:v1.1.0
CONFORMANCE_ROOT_URL  ?= http://registry-core:8080
CONFORMANCE_NETWORK   ?= registry
CONFORMANCE_NAMESPACE ?= conformance/test
CONFORMANCE_USERNAME  ?= conformance
CONFORMANCE_PASSWORD  ?= Conform1234!dev

# Path to the compose file and project name (matches the running dev stack).
COMPOSE_FILE    := ../../infra/docker-compose/docker-compose.yml
COMPOSE_PROJECT := docker-compose

.PHONY: build test lint test-integration test-conformance clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/server

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./internal/testutil/integration/...

lint:
	golangci-lint run ./...

# Run the OCI Distribution Spec v1.1 conformance suite against the live dev stack.
#
# Steps performed automatically:
#   1. Rebuild the registry-core image so the suite tests the current code.
#   2. Seed the conformance user into the auth DB (idempotent).
#   3. Restart registry-core with AUTH_REALM=http://registry-auth:8080/auth/token
#      so the WWW-Authenticate challenges are reachable from inside the container network.
#   4. Run the conformance container on the compose network.
#   5. Restore registry-core with the default host-facing AUTH_REALM.
#
# Results (HTML + JSON) are written to /tmp/conformance-results.
# Requires: docker compose stack running in infra/docker-compose/.
test-conformance:
	@echo "============================================================"
	@echo "  OCI Distribution Spec v1.1 conformance"
	@echo "  Registry:  $(CONFORMANCE_ROOT_URL)"
	@echo "  Namespace: $(CONFORMANCE_NAMESPACE)"
	@echo "  Network:   $(CONFORMANCE_NETWORK)"
	@echo "============================================================"
	@echo "--- [1/5] Building registry-core image..."
	docker compose -f $(COMPOSE_FILE) -p $(COMPOSE_PROJECT) build registry-core
	@echo "--- [2/5] Seeding conformance user (idempotent)..."
	docker exec $(COMPOSE_PROJECT)-postgres-1 psql -U registry -d registry_auth -c \
		"INSERT INTO users (id, tenant_id, username, email, password_hash, is_active) \
		VALUES ('00000000-0000-0000-0000-000000000003', \
		        '98dbe36b-ef28-4903-b25c-bff1b2921c9e', \
		        'conformance', 'conformance@dev.local', \
		        '$$argon2id$$v=19$$m=65536,t=3,p=2$$nzGi4w5n1X/PxLHwHdo/pQ$$UUz56fCariQ+Nfu+ga7xUAqIN/wcVOHchS3fBRQlCdE', \
		        true) \
		ON CONFLICT (tenant_id, username) DO NOTHING;"
	@echo "--- [3/5] Restarting registry-core with internal auth realm..."
	AUTH_REALM=http://registry-auth:8080/auth/token \
		docker compose -f $(COMPOSE_FILE) -p $(COMPOSE_PROJECT) \
		up -d --no-deps --wait registry-core
	@echo "--- [4/5] Running OCI conformance suite..."
	@mkdir -p /tmp/conformance-results
	docker run --rm \
		--network $(CONFORMANCE_NETWORK) \
		-v /tmp/conformance-results:/results \
		-e OCI_ROOT_URL="$(CONFORMANCE_ROOT_URL)" \
		-e OCI_NAMESPACE="$(CONFORMANCE_NAMESPACE)" \
		-e OCI_USERNAME="$(CONFORMANCE_USERNAME)" \
		-e OCI_PASSWORD="$(CONFORMANCE_PASSWORD)" \
		-e OCI_TEST_PULL=1 \
		-e OCI_TEST_PUSH=1 \
		-e OCI_TEST_CONTENT_DISCOVERY=1 \
		-e OCI_TEST_CONTENT_MANAGEMENT=1 \
		-e OCI_HIDE_SKIPPED_WORKFLOWS=false \
		-e OCI_DEBUG=false \
		-e OCI_REPORT_DIR=/results \
		$(CONFORMANCE_IMAGE)
	@echo "--- [5/5] Restoring registry-core with host auth realm..."
	docker compose -f $(COMPOSE_FILE) -p $(COMPOSE_PROJECT) \
		up -d --no-deps --wait registry-core
	@echo "============================================================"
	@echo "  Results written to /tmp/conformance-results"
	@echo "============================================================"

clean:
	rm -rf bin/
