SERVICE := core
BINARY  := bin/$(SERVICE)

# OCI conformance image and overridable connection settings.
# Override for non-local environments:
#   make test-conformance CONFORMANCE_ROOT_URL=http://myhost:8081 \
#                         CONFORMANCE_USERNAME=myuser CONFORMANCE_PASSWORD=mypass
CONFORMANCE_IMAGE     ?= ghcr.io/opencontainers/distribution-spec/conformance:v1.1.0
CONFORMANCE_ROOT_URL  ?= http://localhost:8081
CONFORMANCE_NAMESPACE ?= conformance/test
CONFORMANCE_USERNAME  ?= conformance
CONFORMANCE_PASSWORD  ?= Conform1234!dev

.PHONY: build test lint test-integration test-conformance clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/server

test:
	go test -race ./...

test-integration:
	go test -race -tags integration ./internal/testutil/integration/...

lint:
	golangci-lint run ./...

# Run the OCI Distribution Spec v1.1 conformance suite against a live registry.
# The suite container uses --network host so it can reach $(CONFORMANCE_ROOT_URL).
# Results (HTML + JSON) are written to /tmp/conformance-results on the host.
test-conformance:
	@echo "============================================================"
	@echo "  OCI Distribution Spec v1.1 conformance"
	@echo "  Registry:  $(CONFORMANCE_ROOT_URL)"
	@echo "  Namespace: $(CONFORMANCE_NAMESPACE)"
	@echo "============================================================"
	@mkdir -p /tmp/conformance-results
	docker run --rm \
		--network host \
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

clean:
	rm -rf bin/
