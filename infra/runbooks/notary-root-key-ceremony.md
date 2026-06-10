# Notary v2 Root Key Ceremony

> **Security classification:** CRITICAL.
> The root key signs all TUF (The Update Framework) trust roots for the platform.
> Compromise of the root key allows an attacker to distribute malicious images undetected.
> This ceremony must be performed by two authorised operators with physical presence or verified video.

---

## Overview

Notary v2 uses TUF metadata to provide secure image distribution with cryptographic provenance. The key hierarchy is:

```
Root key (offline, HSM or cold storage)
  └─ Targets key (delegated per-tenant, online)
       └─ Snapshots key (online, managed by Notary server)
            └─ Timestamp key (online, rotated automatically)
```

The root key is **never stored online**. It is used only to sign the TUF root metadata and to delegate to targets keys. After the ceremony, the root key is sealed and stored offline.

---

## Prerequisites

Before the ceremony:

- [ ] Two authorised operators present (or on verified video call)
- [ ] Air-gapped laptop or HSM device prepared
- [ ] Notation CLI installed: `notation version` ≥ 1.0
- [ ] `cosign` installed: `cosign version` ≥ 2.0
- [ ] Ceremony log sheet printed and ready for signatures
- [ ] USB drives (×2) for key backup, verified wiped
- [ ] Faraday bag for offline storage

---

## 1. Generate Root Key (Offline)

Perform on air-gapped machine or HSM:

```bash
# Option A: Software key (air-gapped laptop)
# Generate a 4096-bit RSA root key
openssl genrsa -aes256 -out root.key 4096
# Use a strong passphrase (≥ 32 chars). Record on paper, store separately from key.
openssl rsa -in root.key -pubout -out root.pub

# Option B: HSM (YubiKey 5 or similar PKCS#11 device)
# Generate key directly on HSM — key material never exported
pkcs11-tool --module /usr/lib/opensc-pkcs11.so \
  --login --keypairgen --key-type rsa:4096 \
  --label "registry-tuf-root" --id 01

# Record the key fingerprint in the ceremony log
openssl rsa -in root.key -pubout -outform DER | sha256sum
```

---

## 2. Initialise TUF Root Metadata

```bash
# Create TUF repository structure
mkdir -p tuf-repo/{keys,repository}

# Using notation (preferred for OCI-native registries)
notation cert generate-test --default "registry-root-$(date +%Y%m%d)"

# Or using cosign for Sigstore-compatible root
cosign generate-key-pair --output-key-file root-signing

# Initialise TUF root document
cat > tuf-repo/root.json <<EOF
{
  "_type": "root",
  "spec_version": "1.0.0",
  "version": 1,
  "expires": "$(date -d '+1 year' --utc +%Y-%m-%dT%H:%M:%SZ)",
  "keys": {
    "<root-key-id>": {
      "keytype": "rsa",
      "keyval": { "public": "<root-public-key-pem>" }
    }
  },
  "roles": {
    "root":      { "keyids": ["<root-key-id>"],      "threshold": 1 },
    "targets":   { "keyids": ["<targets-key-id>"],   "threshold": 1 },
    "snapshot":  { "keyids": ["<snapshot-key-id>"],  "threshold": 1 },
    "timestamp": { "keyids": ["<timestamp-key-id>"], "threshold": 1 }
  }
}
EOF

# Sign root metadata with root key
cosign sign-blob --key root-signing.key tuf-repo/root.json \
  --output-signature tuf-repo/root.json.sig
```

---

## 3. Upload TUF Metadata to Storage

TUF metadata is stored in `registry-storage` under the `tuf/<tenant_id>/` prefix.

```bash
# Upload signed root metadata
mc cp tuf-repo/root.json myminio/registry/tuf/global/1.root.json
mc cp tuf-repo/root.json.sig myminio/registry/tuf/global/1.root.json.sig

# Verify upload
mc ls myminio/registry/tuf/global/
```

---

## 4. Delegate Targets Key to `registry-signer`

The `registry-signer` service uses a targets key loaded from Vault Transit (configured in `vault/init.sh`). Link the targets key to the root:

```bash
# In Vault: the registry-signer key was created during vault-init
# Get the public key for the targets delegation
VAULT_ADDR=http://localhost:8200 VAULT_TOKEN=dev-root-token \
  vault read transit/keys/registry-signer

# Add the targets key to root.json under "roles.targets.keyids"
# Re-sign root.json with root key and increment version to 2
# Upload 2.root.json to storage
```

---

## 5. Seal and Store Root Key

After the ceremony, the root key must be taken offline immediately:

```bash
# Verify all TUF operations work before sealing
cosign verify --key root.pub registry.example.com/test/image:latest

# Encrypt root key for long-term storage
gpg --symmetric --cipher-algo AES256 --output root.key.gpg root.key

# Wipe plaintext key from disk
shred -u root.key

# Copy encrypted backups to two separate USB drives
cp root.key.gpg /media/usb1/registry-root-key-$(date +%Y%m%d).gpg
cp root.key.gpg /media/usb2/registry-root-key-$(date +%Y%m%d).gpg

# Seal USB drives in tamper-evident envelopes
# Store in separate physical locations (e.g., two different office safes)
```

---

## 6. Ceremony Sign-Off

Both operators must sign the ceremony log confirming:

| Item | Operator 1 | Operator 2 |
|---|---|---|
| Root key generated on air-gapped machine | ☐ | ☐ |
| Root key fingerprint recorded | ☐ | ☐ |
| TUF root metadata signed and uploaded | ☐ | ☐ |
| Targets delegation verified | ☐ | ☐ |
| Plaintext root key destroyed | ☐ | ☐ |
| Two encrypted backups stored separately | ☐ | ☐ |

Ceremony date: _______________  
Operator 1: _______________  
Operator 2: _______________

---

## 7. Root Key Rotation

Root key rotation requires another ceremony:

1. Retrieve encrypted root key backup and decrypt in air-gapped environment
2. Generate new root key
3. Create new `root.json` with version incremented, signed by **both** old and new root keys (cross-signed for continuity)
4. Upload new root metadata and distribute updated trust root to all tenants
5. Seal new root key per §5 above
6. Destroy old root key backups

**Frequency:** Every 2 years or immediately upon suspected compromise.

---

## 8. Emergency Revocation

If the root key is suspected to be compromised:

1. Immediately contact all tenant administrators
2. Revoke all signatures via TUF timestamp rotation (mark all manifests as expired)
3. Emergency ceremony to generate new root key (steps 1–6 above)
4. Re-sign all active manifests with the new targets key
5. Publish incident report
