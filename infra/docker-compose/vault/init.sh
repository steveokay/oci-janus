#!/bin/sh
# Vault dev-mode bootstrap — runs once via vault-init service.
# Enables the Transit secrets engine and creates the registry signing key.
set -e

echo "[vault-init] enabling transit secrets engine..."
vault secrets enable transit || echo "[vault-init] transit already enabled, continuing"

echo "[vault-init] creating registry-signer key (ecdsa-p256)..."
vault write -f transit/keys/registry-signer type=ecdsa-p256 || echo "[vault-init] key already exists, continuing"

# Set key as exportable=false (key material never leaves Vault)
vault write transit/keys/registry-signer/config \
  exportable=false \
  allow_plaintext_backup=false

echo "[vault-init] creating registry-signer policy..."
vault policy write registry-signer - <<EOF
path "transit/sign/registry-signer" {
  capabilities = ["update"]
}
path "transit/verify/registry-signer" {
  capabilities = ["update"]
}
path "transit/keys/registry-signer" {
  capabilities = ["read"]
}
EOF

echo "[vault-init] creating app token for registry-signer service..."
vault token create \
  -policy=registry-signer \
  -ttl=0 \
  -renewable=false \
  -id=registry-signer-dev-token \
  -display-name=registry-signer || echo "[vault-init] token already exists, continuing"

echo "[vault-init] done."
echo ""
echo "  VAULT_ADDR=http://localhost:8200"
echo "  VAULT_TOKEN=registry-signer-dev-token"
echo "  VAULT_COSIGN_PATH=transit/sign/registry-signer"
