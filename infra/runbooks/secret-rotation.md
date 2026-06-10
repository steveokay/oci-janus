# Secret Rotation Runbook

> **Applies to:** Production Kubernetes deployments using External Secrets Operator.
> Run rotations during low-traffic windows. All procedures are zero-downtime.

---

## 1. JWT Signing Key Rotation (`registry-auth`)

The JWT RS256 key pair signs all access tokens. Rotation adds a new key, waits for tokens signed with the old key to expire (max 300 s TTL), then removes the old key.

**Duration:** ~10 minutes

```bash
# 1. Generate new RSA-4096 key pair
openssl genrsa -out /tmp/jwt-new.pem 4096
openssl rsa -in /tmp/jwt-new.pem -pubout -out /tmp/jwt-new.pub

NEW_PRIVATE=$(base64 -w0 < /tmp/jwt-new.pem)
NEW_PUBLIC=$(base64 -w0 < /tmp/jwt-new.pub)
OLD_PUBLIC=$(vault kv get -field=JWT_PUBLIC_KEY_B64 secret/registry/auth)

# 2. Add new signing key; keep old public key so in-flight tokens remain valid
vault kv patch secret/registry/auth \
  JWT_PRIVATE_KEY_B64="$NEW_PRIVATE" \
  JWT_PUBLIC_KEYS_B64="${OLD_PUBLIC},${NEW_PUBLIC}"

# 3. Sync and rolling restart
kubectl annotate externalsecret registry-auth-secret force-sync=$(date +%s) -n registry
kubectl rollout restart deployment/registry-auth -n registry
kubectl rollout status deployment/registry-auth -n registry

# 4. Wait for old tokens to expire (max JWT TTL = 300 s)
echo "Waiting 5 minutes for old tokens to expire..."
sleep 300

# 5. Remove old public key
vault kv patch secret/registry/auth JWT_PUBLIC_KEYS_B64="$NEW_PUBLIC"
kubectl annotate externalsecret registry-auth-secret force-sync=$(date +%s) -n registry
kubectl rollout restart deployment/registry-auth -n registry

# 6. Verify JWKS now contains exactly 1 key
curl -s https://your-registry.example.com/auth/.well-known/jwks.json | jq '.keys | length'
# Expected: 1

# 7. Securely delete temp files
shred -u /tmp/jwt-new.pem /tmp/jwt-new.pub 2>/dev/null || rm /tmp/jwt-new.pem /tmp/jwt-new.pub
```

**Rollback:** Restore old `JWT_PRIVATE_KEY_B64` and `JWT_PUBLIC_KEYS_B64` from your secrets manager backup and restart.

---

## 2. Upstream Credential Encryption Key (`registry-proxy`)

`CREDENTIAL_KEY_HEX` is a 32-byte AES-256-GCM key encrypting upstream registry passwords in the `proxy` DB. Rotation requires re-encrypting all stored credentials before deploying the new key.

**Duration:** 5–15 minutes

```bash
# 1. Generate new key
NEW_KEY=$(openssl rand -hex 32)

# 2. Run re-encryption job (reads all upstream_registries rows, decrypts with
#    old key, re-encrypts with new key, writes back atomically)
OLD_KEY=$(vault kv get -field=CREDENTIAL_KEY_HEX secret/registry/proxy)
DB_DSN=$(vault kv get -field=DB_DSN secret/registry/proxy)

kubectl run proxy-reencrypt --restart=Never -n registry \
  --image=ghcr.io/steveokay/registry-proxy:latest \
  --env="OLD_CREDENTIAL_KEY_HEX=$OLD_KEY" \
  --env="NEW_CREDENTIAL_KEY_HEX=$NEW_KEY" \
  --env="DB_DSN=$DB_DSN" \
  -- /server --mode=reencrypt-credentials

kubectl wait --for=condition=complete pod/proxy-reencrypt -n registry --timeout=120s
kubectl logs pod/proxy-reencrypt -n registry
kubectl delete pod/proxy-reencrypt -n registry

# 3. Update secret and restart
vault kv patch secret/registry/proxy CREDENTIAL_KEY_HEX="$NEW_KEY"
kubectl annotate externalsecret registry-proxy-secret force-sync=$(date +%s) -n registry
kubectl rollout restart deployment/registry-proxy -n registry
kubectl rollout status deployment/registry-proxy -n registry
```

**Rollback:** Restore old `CREDENTIAL_KEY_HEX` and restart. DB data is still encrypted with the old key if re-encryption was incomplete.

---

## 3. Database Password Rotation

Each service connects via its own PostgreSQL role. Rotate one service at a time.

```bash
SERVICE=auth    # change to: metadata | proxy | tenant | audit | webhook
NEW_PASS=$(openssl rand -base64 24 | tr -d '+=/')

# 1. Change password in PostgreSQL
psql "$SUPERUSER_DSN" -c "ALTER USER registry_${SERVICE}_app PASSWORD '${NEW_PASS}';"

# 2. Update secret
vault kv patch secret/registry/${SERVICE} \
  DB_DSN="postgres://registry_${SERVICE}_app:${NEW_PASS}@postgres:5432/registry_${SERVICE}?sslmode=require"

# 3. Sync and rolling restart — pgxpool reconnects gracefully
kubectl annotate externalsecret registry-${SERVICE}-secret force-sync=$(date +%s) -n registry
kubectl rollout restart deployment/registry-${SERVICE} -n registry
kubectl rollout status deployment/registry-${SERVICE} -n registry
```

---

## 4. RabbitMQ Credential Rotation

```bash
NEW_PASS=$(openssl rand -base64 24 | tr -d '+=/')

# 1. Add new user with same permissions
rabbitmqctl add_user registry_new "$NEW_PASS"
rabbitmqctl set_permissions -p / registry_new ".*" ".*" ".*"

# 2. Update RABBITMQ_URL secret for all consumers (core, scanner, webhook, audit)
for svc in core scanner webhook audit; do
  vault kv patch secret/registry/${svc} \
    RABBITMQ_URL="amqp://registry_new:${NEW_PASS}@rabbitmq:5672/"
  kubectl annotate externalsecret registry-${svc}-secret force-sync=$(date +%s) -n registry
  kubectl rollout restart deployment/registry-${svc} -n registry
  kubectl rollout status deployment/registry-${svc} -n registry
done

# 3. Remove old user
rabbitmqctl delete_user registry_old
```

---

## 5. MinIO / Storage Credential Rotation

```bash
NEW_SECRET=$(openssl rand -base64 24 | tr -d '+=/')

# 1. Create new access key
mc admin user add myminio registry_new "$NEW_SECRET"
mc admin policy attach myminio readwrite --user registry_new

# 2. Update secret and restart storage service
vault kv patch secret/registry/storage \
  STORAGE_MINIO_ACCESS_KEY="registry_new" \
  STORAGE_MINIO_SECRET_KEY="$NEW_SECRET"
kubectl annotate externalsecret registry-storage-secret force-sync=$(date +%s) -n registry
kubectl rollout restart deployment/registry-storage -n registry
kubectl rollout status deployment/registry-storage -n registry

# 3. Remove old key
mc admin user remove myminio registry_old
```

---

## Rotation Schedule

| Secret | Frequency | Owner |
|---|---|---|
| JWT signing key pair | Every 90 days | Platform team |
| Proxy credential encryption key | Every 180 days | Platform team |
| Database passwords (all services) | Every 90 days | Platform / DBA |
| RabbitMQ credentials | Every 90 days | Platform team |
| Storage credentials | Every 90 days | Platform team |
| mTLS certificates | Automated — cert-manager (90-day certs) | cert-manager |

Record every rotation in the change log: date, operator, secret rotated, verification outcome.
