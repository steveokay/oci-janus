package webhook

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// senderRepo is the subset of repository.Repository the send loop depends on.
// Declared as an interface here so the loop is unit-testable without Postgres.
type senderRepo interface {
	GetNotificationWebhookConfig(ctx context.Context, tenantID uuid.UUID) (*repository.NotificationWebhookConfig, error)
	ClaimPendingWebhookDeliveries(ctx context.Context, now time.Time, limit int) ([]*repository.WebhookDelivery, error)
	MarkWebhookDelivered(ctx context.Context, id uuid.UUID, responseStatus int) error
	MarkWebhookFailed(ctx context.Context, id uuid.UUID, attempts int, next time.Time, failed bool, responseStatus int, errMsg string) error
}

// Sender drains the notification_webhook_deliveries queue and posts each row to
// the tenant's org webhook. Idle (no DB work) when the KEK is unset.
type Sender struct {
	repo         senderRepo
	kek          []byte
	interval     time.Duration
	batch        int
	platformHost string // absolute-link base for the payload's link field
	poster       *Poster
	// post is the injection point for the POST call (defaults to poster.Post;
	// overridden in tests to avoid the network).
	post func(ctx context.Context, url string, body, secret []byte) (int, error)
}

// NewSender returns a Sender ready to Start. interval ~20s + batch 20 mirror the
// email sender cadence.
func NewSender(repo senderRepo, kek []byte, platformHost string) *Sender {
	p := NewPoster()
	s := &Sender{
		repo: repo, kek: kek, interval: 20 * time.Second, batch: 20,
		platformHost: platformHost, poster: p,
	}
	s.post = p.Post
	return s
}

// Start runs the send loop until ctx is cancelled. Best-effort: per-tick errors
// log and continue.
func (s *Sender) Start(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runTick(ctx)
		}
	}
}

// runTick claims a batch of due deliveries and posts each. Idles when the KEK is
// unset so the whole channel disables cleanly.
func (s *Sender) runTick(ctx context.Context) {
	if len(s.kek) == 0 {
		return // webhook channel disabled
	}
	now := time.Now().UTC()
	rows, err := s.repo.ClaimPendingWebhookDeliveries(ctx, now, s.batch)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook sender: claim failed", "err", err)
		return
	}
	// One tenant in single mode; config loaded lazily + cached per tick.
	type resolved struct {
		url    string
		secret []byte
		ok     bool // config present + enabled + secret decrypted
	}
	cache := map[uuid.UUID]resolved{}
	for _, d := range rows {
		r, seen := cache[d.TenantID]
		if !seen {
			r = s.resolve(ctx, d.TenantID)
			cache[d.TenantID] = r
		}
		if !r.ok {
			// Config disabled/absent/secret-less. The row was leased
			// (next_attempt_at = now()+1min); a bare continue would re-claim it
			// forever. Age it toward terminal state via fail().
			s.fail(ctx, d, 0, errors.New("webhook transport not enabled or not configured"))
			continue
		}
		body := buildPayload(d.Category, d.Subject, d.BodySummary, s.link(d.Link), d.TenantID.String(), now)
		code, err := s.post(ctx, r.url, body, r.secret)
		if err != nil {
			s.fail(ctx, d, code, err)
			continue
		}
		if err := s.repo.MarkWebhookDelivered(ctx, d.ID, code); err != nil {
			slog.ErrorContext(ctx, "FUT-019 webhook sender: mark delivered failed", "err", err, "id", d.ID)
		}
	}
}

// resolve loads + decrypts a tenant's webhook config. ok=false (no error
// surfaced) when the tenant has no config, it's disabled, or the secret is
// missing/undecryptable — the caller ages the row toward terminal state.
func (s *Sender) resolve(ctx context.Context, tenantID uuid.UUID) (r struct {
	url    string
	secret []byte
	ok     bool
}) {
	cfg, err := s.repo.GetNotificationWebhookConfig(ctx, tenantID)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook sender: load config failed", "err", err)
		return
	}
	if cfg == nil || !cfg.Enabled || cfg.URL == "" || len(cfg.SecretEnc) == 0 {
		return
	}
	secret, err := aes.Decrypt(cfg.SecretEnc, s.kek)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook sender: decrypt secret failed", "err", err)
		return
	}
	r.url, r.secret, r.ok = cfg.URL, secret, true
	return
}

// fail records a send failure, computing the next attempt via Backoff and
// flipping the row to 'failed' once the retry budget is exhausted.
func (s *Sender) fail(ctx context.Context, d *repository.WebhookDelivery, code int, sendErr error) {
	attempts := d.Attempts + 1
	failed := attempts >= MaxAttempts
	next := time.Now().UTC().Add(Backoff(attempts))
	if err := s.repo.MarkWebhookFailed(ctx, d.ID, attempts, next, failed, code, sendErr.Error()); err != nil {
		slog.ErrorContext(ctx, "FUT-019 webhook sender: mark failed errored", "err", err, "id", d.ID)
	}
}

// link builds the absolute payload link when a platform host is configured,
// else the raw (possibly relative) link.
func (s *Sender) link(raw string) string {
	if s.platformHost != "" && raw != "" {
		return s.platformHost + raw
	}
	return raw
}
