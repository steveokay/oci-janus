package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// Sentinel errors returned by Service methods. Handlers map these to HTTP/gRPC status codes.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account locked")
	ErrAccountDisabled    = errors.New("account disabled")
	ErrTokenRevoked       = errors.New("token has been revoked")
	ErrKeyExpired         = errors.New("api key has expired")
)

const (
	tokenTTL        = 5 * time.Minute
	lockoutDuration = 15 * time.Minute
	maxFailedLogins = 5
	// rawSecretLen is the number of random bytes for API key secrets.
	rawSecretLen = 32
)

// Claims is the JWT payload for all tokens issued by registry-auth.
// It extends the standard RegisteredClaims with registry-specific fields.
type Claims struct {
	jwt.RegisteredClaims
	TenantID string             `json:"tenant_id"`
	Access   []RepositoryAccess `json:"access"`
}

// RepositoryAccess describes a scope granted within a single token.
type RepositoryAccess struct {
	Type    string   `json:"type"`    // always "repository"
	Name    string   `json:"name"`    // "org/repo"
	Actions []string `json:"actions"` // e.g. ["push","pull"]
}

// JWKSResponse is the JSON Web Key Set returned by the /.well-known/jwks.json endpoint.
type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

// JWK represents one RSA public key in JWKS format.
type JWK struct {
	Kty string `json:"kty"` // "RSA"
	Use string `json:"use"` // "sig"
	Kid string `json:"kid"`
	Alg string `json:"alg"` // "RS256"
	N   string `json:"n"`   // base64url-encoded modulus
	E   string `json:"e"`   // base64url-encoded exponent
}

// Service is the core authentication business logic.
type Service struct {
	users   userRepo
	apiKeys apiKeyRepo
	redis   *redis.Client
	privKey *rsa.PrivateKey
	pubKey  *rsa.PublicKey
	keyID   string
}

// New constructs a Service by parsing the base64-encoded PEM keys from config.
func New(
	users *repository.UserRepository,
	apiKeys *repository.APIKeyRepository,
	rdb *redis.Client,
	privKeyB64, pubKeyB64, keyID string,
) (*Service, error) {
	privKey, err := parsePrivateKey(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	pubKey, err := parsePublicKey(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT public key: %w", err)
	}
	return &Service{
		users:   users,
		apiKeys: apiKeys,
		redis:   rdb,
		privKey: privKey,
		pubKey:  pubKey,
		keyID:   keyID,
	}, nil
}

// IssueToken signs and returns a JWT for the given user with the requested access scopes.
func (s *Service) IssueToken(ctx context.Context, userID, tenantID string, access []RepositoryAccess) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "registry-auth",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{"registry-core"},
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
		TenantID: tenantID,
		Access:   access,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.keyID
	signed, err := tok.SignedString(s.privKey)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// ValidateToken parses and validates the token string, then checks the Redis
// revocation list. Returns the parsed claims on success.
func (s *Service) ValidateToken(ctx context.Context, tokenStr string) (*Claims, error) {
	var claims Claims
	tok, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.pubKey, nil
	})
	if err != nil || !tok.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
	}

	// Check revocation list — the key TTL matches token lifetime so an expired
	// token's revocation entry is self-cleaning (no background cleanup needed).
	// See RevokeToken for the TTL derivation that makes this guarantee hold.
	revoked, err := s.isRevoked(ctx, claims.ID)
	if err != nil {
		return nil, fmt.Errorf("check revocation: %w", err)
	}
	if revoked {
		return nil, ErrTokenRevoked
	}
	return &claims, nil
}

// RevokeToken stores the token's JTI in Redis so ValidateToken rejects it.
//
// SEC-005: The Redis TTL must equal the token's remaining lifetime — not a fixed value.
// This way the revocation entry expires exactly when the JWT itself would expire,
// preventing both memory leaks (TTL too long) and revocation bypass (TTL too short).
// ValidateToken relies on this coupling to avoid a separate GC process for stale entries.
func (s *Service) RevokeToken(ctx context.Context, claims *Claims) error {
	// Compute remaining lifetime from the token's own expiry claim.
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		// Token already expired — no need to store a revocation entry; it is
		// already invalid and any existing Redis entry will have auto-expired.
		return nil
	}
	// Store the JTI with a TTL equal to the remaining token lifetime so the
	// entry disappears exactly when the token would naturally cease to be valid.
	return s.redis.Set(ctx, revokedKey(claims.ID), "1", ttl).Err()
}

// Login validates credentials, enforces account lockout, and issues a token.
func (s *Service) Login(ctx context.Context, tenantID uuid.UUID, username, password string) (string, error) {
	user, err := s.AuthenticateUser(ctx, tenantID, username, password)
	if err != nil {
		return "", err
	}
	return s.IssueToken(ctx, user.ID.String(), user.TenantID.String(), nil)
}

// CreateUser hashes the password and persists the user record.
func (s *Service) CreateUser(ctx context.Context, tenantID uuid.UUID, username, email, password string) (*repository.User, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := argon2pkg.Hash(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return s.users.Create(ctx, repository.CreateUserRequest{
		TenantID:     tenantID,
		Username:     username,
		Email:        email,
		PasswordHash: hash,
	})
}

// CreateAPIKey generates a random secret, stores its argon2id hash, and returns
// the raw secret. The raw secret is never stored and cannot be recovered later.
func (s *Service) CreateAPIKey(ctx context.Context, tenantID, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (key *repository.APIKey, rawSecret string, err error) {
	raw := make([]byte, rawSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate secret: %w", err)
	}
	rawSecret = hex.EncodeToString(raw) // 64-char lowercase hex

	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		return nil, "", fmt.Errorf("hash secret: %w", err)
	}

	key, err = s.apiKeys.Create(ctx, repository.CreateAPIKeyRequest{
		TenantID:  tenantID,
		UserID:    userID,
		Name:      name,
		KeyHash:   hash,
		KeyPrefix: rawSecret[:12], // first 12 chars for display, not a secret
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, "", err
	}
	return key, rawSecret, nil
}

// ValidateAPIKey looks up the key by ID, checks expiry, and verifies the secret hash.
func (s *Service) ValidateAPIKey(ctx context.Context, keyID uuid.UUID, rawSecret string) (*repository.APIKey, error) {
	key, err := s.apiKeys.GetByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return nil, ErrKeyExpired
	}

	ok, err := argon2pkg.Verify(rawSecret, key.KeyHash)
	if err != nil {
		return nil, fmt.Errorf("verify key: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}

	// Detach from request context: LastUsed updates are best-effort and must not
	// block or fail the auth response. A 5-second timeout prevents goroutine leaks
	// if the database is slow or temporarily unavailable.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.apiKeys.TouchLastUsed(ctx, key.ID)
	}()

	return key, nil
}

// AuthenticateUser validates credentials and returns the authenticated user without
// issuing a token. It enforces account lockout so callers can then issue a
// custom-scoped token (e.g. the Docker token endpoint).
func (s *Service) AuthenticateUser(ctx context.Context, tenantID uuid.UUID, username, password string) (*repository.User, error) {
	user, err := s.users.GetByUsername(ctx, tenantID, username)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !user.IsActive {
		return nil, ErrAccountDisabled
	}
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		return nil, ErrAccountLocked
	}

	ok, err := argon2pkg.Verify(password, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		count, ferr := s.users.RecordFailedLogin(ctx, user.ID)
		if ferr == nil && count >= maxFailedLogins {
			lockUntil := time.Now().Add(lockoutDuration)
			_ = s.users.LockUntil(ctx, user.ID, lockUntil)
		}
		return nil, ErrInvalidCredentials
	}

	_ = s.users.ResetFailedLogins(ctx, user.ID)
	return user, nil
}

// GetUserByID returns the user record for the given ID.
func (s *Service) GetUserByID(ctx context.Context, id uuid.UUID) (*repository.User, error) {
	return s.users.GetByID(ctx, id)
}

// ListAPIKeys returns all active API keys owned by the given user.
func (s *Service) ListAPIKeys(ctx context.Context, userID uuid.UUID) ([]*repository.APIKey, error) {
	return s.apiKeys.ListByUser(ctx, userID)
}

// DeleteAPIKey soft-deletes an API key owned by the given user.
func (s *Service) DeleteAPIKey(ctx context.Context, keyID, userID uuid.UUID) error {
	return s.apiKeys.Delete(ctx, keyID, userID)
}

// JWKS returns the public key set for JWT verification by other services.
func (s *Service) JWKS() JWKSResponse {
	return JWKSResponse{Keys: []JWK{rsaToJWK(s.pubKey, s.keyID)}}
}

// CheckIPRateLimit returns ErrRateLimited when the given IP has exceeded
// maxAuthFailures failed attempts in the past minute.
func (s *Service) CheckIPRateLimit(ctx context.Context, ip string) error {
	key := "rl:auth:fail:" + ip
	count, err := s.redis.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err // Redis failure — fail open (do not block legitimate users)
	}
	if count >= 10 {
		return ErrRateLimited
	}
	return nil
}

// ErrRateLimited is returned when per-IP auth failure rate limit is exceeded.
var ErrRateLimited = errors.New("too many failed authentication attempts")

// RecordAuthFailure increments the per-IP failure counter with a 60-second window.
func (s *Service) RecordAuthFailure(ctx context.Context, ip string) {
	key := "rl:auth:fail:" + ip
	pipe := s.redis.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 60*time.Second)
	_, _ = pipe.Exec(ctx)
}

// revokedKey returns the Redis key used to track JTI revocation.
func revokedKey(jti string) string { return "jwt:revoked:" + jti }

func (s *Service) isRevoked(ctx context.Context, jti string) (bool, error) {
	_, err := s.redis.Get(ctx, revokedKey(jti)).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return err == nil, err
}

// rsaToJWK encodes an RSA public key as a JSON Web Key.
func rsaToJWK(pub *rsa.PublicKey, kid string) JWK {
	return JWK{
		Kty: "RSA",
		Use: "sig",
		Kid: kid,
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   encodeExponent(pub.E),
	}
}

// encodeExponent encodes the RSA public exponent as big-endian base64url with
// leading zero bytes stripped (per RFC 7517).
func encodeExponent(e int) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(e))
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return base64.RawURLEncoding.EncodeToString(b[i:])
}

func parsePrivateKey(b64 string) (*rsa.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fall back to PKCS1 format
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}

func parsePublicKey(b64 string) (*rsa.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not RSA")
	}
	return rsaKey, nil
}
