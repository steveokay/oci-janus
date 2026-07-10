package prregistry

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

func TestVerify(t *testing.T) {
	kek := testKEK()
	secret := "topsecret-webhook-key"
	body := []byte(`{"action":"opened","number":1}`)
	goodSig := signGitHub(secret, body)

	enabledCfg := func() repository.PRRegistryConfig {
		return repository.PRRegistryConfig{
			TenantID:         uuid.New(),
			Enabled:          true,
			WebhookSecretEnc: sealSecret(t, secret, kek),
		}
	}

	cases := []struct {
		name    string
		kek     []byte
		cfg     repository.PRRegistryConfig
		body    []byte
		sig     string
		wantErr error
	}{
		{
			name: "valid signature",
			kek:  kek,
			cfg:  enabledCfg(),
			body: body,
			sig:  goodSig,
		},
		{
			name:    "tampered body",
			kek:     kek,
			cfg:     enabledCfg(),
			body:    []byte(`{"action":"opened","number":2}`),
			sig:     goodSig,
			wantErr: ErrSignatureMismatch,
		},
		{
			name:    "wrong secret in header",
			kek:     kek,
			cfg:     enabledCfg(),
			body:    body,
			sig:     signGitHub("different-secret", body),
			wantErr: ErrSignatureMismatch,
		},
		{
			name:    "malformed header no prefix",
			kek:     kek,
			cfg:     enabledCfg(),
			body:    body,
			sig:     "deadbeef",
			wantErr: ErrSignatureMismatch,
		},
		{
			name:    "malformed header bad hex",
			kek:     kek,
			cfg:     enabledCfg(),
			body:    body,
			sig:     "sha256=nothex!!",
			wantErr: ErrSignatureMismatch,
		},
		{
			name: "disabled config",
			kek:  kek,
			cfg: repository.PRRegistryConfig{
				Enabled:          false,
				WebhookSecretEnc: sealSecret(t, secret, kek),
			},
			body:    body,
			sig:     goodSig,
			wantErr: ErrFeatureDisabled,
		},
		{
			name: "no stored secret",
			kek:  kek,
			cfg: repository.PRRegistryConfig{
				Enabled:          true,
				WebhookSecretEnc: nil,
			},
			body:    body,
			sig:     goodSig,
			wantErr: ErrFeatureDisabled,
		},
		{
			name:    "KEK unset",
			kek:     nil,
			cfg:     enabledCfg(),
			body:    body,
			sig:     goodSig,
			wantErr: ErrFeatureDisabled,
		},
		{
			name: "ciphertext undecryptable with this KEK",
			kek:  kek,
			cfg: repository.PRRegistryConfig{
				Enabled:          true,
				WebhookSecretEnc: []byte("garbage-not-a-valid-gcm-blob-xxxxxxxxxxxx"),
			},
			body:    body,
			sig:     goodSig,
			wantErr: ErrSignatureMismatch,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(newFakeStore(), &fakePublisher{}, c.kek)
			err := s.Verify(c.cfg, c.body, c.sig)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Verify() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Verify() = %v, want %v", err, c.wantErr)
			}
		})
	}
}
