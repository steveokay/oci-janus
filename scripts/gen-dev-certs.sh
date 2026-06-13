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

SERVICES="auth core storage metadata proxy scanner signer webhook audit gc tenant gateway management"

# Install openssl if running inside Alpine (best-effort — skipped if no network)
if ! command -v openssl > /dev/null 2>&1; then
  apk add --no-cache openssl > /dev/null 2>&1 || true
fi

# If openssl still unavailable and certs already exist, nothing to do
if ! command -v openssl > /dev/null 2>&1; then
  if [ -f "$CERTS_DIR/ca.crt" ]; then
    echo "[cert-init] openssl unavailable but certs already exist — skipping generation."
    exit 0
  fi
  echo "[cert-init] ERROR: openssl not available and no certs found." >&2
  exit 1
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
# SANs are required: Go 1.15+ rejects certs with CN only (no SAN) during TLS verification.
for svc in $SERVICES; do
  if [ ! -f "$CERTS_DIR/$svc.crt" ]; then
    echo "[cert-init] Generating cert for registry-$svc..."
    openssl genrsa -out "$CERTS_DIR/$svc.key" 2048 2>/dev/null
    openssl req -new \
      -key "$CERTS_DIR/$svc.key" \
      -out "$CERTS_DIR/$svc.csr" \
      -subj "/CN=registry-$svc/O=registry-dev" 2>/dev/null
    EXT_FILE=$(mktemp)
    printf "subjectAltName=DNS:registry-%s,DNS:localhost\n" "$svc" > "$EXT_FILE"
    openssl x509 -req -days 365 \
      -in "$CERTS_DIR/$svc.csr" \
      -CA "$CERTS_DIR/ca.crt" \
      -CAkey "$CERTS_DIR/ca.key" \
      -CAcreateserial \
      -extfile "$EXT_FILE" \
      -out "$CERTS_DIR/$svc.crt" 2>/dev/null
    rm -f "$CERTS_DIR/$svc.csr" "$EXT_FILE"
  fi
done

# Set certificates world-readable so service containers can verify TLS peers.
chmod 644 "$CERTS_DIR"/*.crt
# Private keys: owned by uid 65532 (distroless non-root user), not world-readable.
# Do NOT use chmod a+r on private keys — they must be readable only by the owning process.
chown 65532:65532 "$CERTS_DIR"/*.key 2>/dev/null || true  # best-effort; may fail outside Docker
chmod 600 "$CERTS_DIR"/*.key

echo "[cert-init] Dev certs ready in $CERTS_DIR/"
