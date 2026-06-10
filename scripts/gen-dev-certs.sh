#!/usr/bin/env sh
# Generates self-signed mTLS certificates for all registry services.
# Idempotent — skips certs that already exist.
#
# Usage (local):  ./scripts/gen-dev-certs.sh
# Usage (Docker): CERTS_DIR=/certs sh /scripts/gen-dev-certs.sh
#
# Outputs (all in $CERTS_DIR, default: ./certs/):
#   ca.key / ca.crt                      — dev CA (self-signed, 10 year)
#   <service>.key / <service>.crt        — per-service leaf cert (1 year)
set -eu

CERTS_DIR="${CERTS_DIR:-$(dirname "$0")/../certs}"
mkdir -p "$CERTS_DIR"

SERVICES="auth core storage metadata proxy scanner signer webhook audit gc tenant gateway"

# Install openssl if running inside Alpine
if ! command -v openssl > /dev/null 2>&1; then
  apk add --no-cache openssl > /dev/null 2>&1
fi

# Generate CA
if [ ! -f "$CERTS_DIR/ca.crt" ]; then
  echo "[cert-init] Generating dev CA..."
  openssl genrsa -out "$CERTS_DIR/ca.key" 4096 2>/dev/null
  openssl req -new -x509 -days 3650 \
    -key "$CERTS_DIR/ca.key" \
    -out "$CERTS_DIR/ca.crt" \
    -subj "/CN=dev-registry-ca/O=registry-dev" 2>/dev/null
  echo "[cert-init] CA ready: $CERTS_DIR/ca.crt"
fi

# Generate per-service leaf certs
for svc in $SERVICES; do
  if [ ! -f "$CERTS_DIR/$svc.crt" ]; then
    echo "[cert-init] Generating cert for registry-$svc..."
    openssl genrsa -out "$CERTS_DIR/$svc.key" 2048 2>/dev/null
    openssl req -new \
      -key "$CERTS_DIR/$svc.key" \
      -out "$CERTS_DIR/$svc.csr" \
      -subj "/CN=registry-$svc/O=registry-dev" 2>/dev/null
    openssl x509 -req -days 365 \
      -in "$CERTS_DIR/$svc.csr" \
      -CA "$CERTS_DIR/ca.crt" \
      -CAkey "$CERTS_DIR/ca.key" \
      -CAcreateserial \
      -out "$CERTS_DIR/$svc.crt" 2>/dev/null
    rm -f "$CERTS_DIR/$svc.csr"
  fi
done

# Make all certs world-readable so non-root service containers (uid 65532) can read them.
chmod a+r "$CERTS_DIR"/*.crt "$CERTS_DIR"/*.key 2>/dev/null || true

echo "[cert-init] Dev certs ready in $CERTS_DIR/"
