package email

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// senderRepo is the subset of repository.Repository the send loop depends on.
// Declaring it as an interface here (rather than taking the concrete repo) keeps
// the loop unit-testable without Postgres. email→repository is acyclic:
// repository never imports this package.
type senderRepo interface {
	GetEmailTransportConfig(ctx context.Context, tenantID uuid.UUID) (*repository.EmailTransportConfig, error)
	ClaimPendingEmailDeliveries(ctx context.Context, now time.Time, limit int) ([]*repository.EmailDelivery, error)
	MarkEmailSent(ctx context.Context, id uuid.UUID, provider string) error
	MarkEmailFailed(ctx context.Context, id uuid.UUID, attempts int, next time.Time, failed bool, errMsg string) error
}

// Sender drains the email_deliveries queue and sends via the configured
// transport. Constructed in server.go and started in a goroutine alongside the
// scheduler runner. Disabled (idle) when the KEK is unset or config is disabled.
type Sender struct {
	repo         senderRepo
	kek          []byte
	interval     time.Duration
	batch        int
	platformHost string // absolute-link base for CTA URLs
	// buildTransport constructs the concrete Transport from a decrypted config.
	// Defaults to NewTransport; overridden in tests to inject a fake transport.
	buildTransport func(cfg DecryptedConfig) (Transport, error)
}

// NewSender returns a Sender ready to Start. interval ~20s + batch 20 mirror the
// webhook dispatcher cadence; platformHost is the public base URL used to build
// absolute CTA links (empty → links fall back to relative paths).
func NewSender(repo senderRepo, kek []byte, platformHost string) *Sender {
	return &Sender{
		repo:           repo,
		kek:            kek,
		interval:       20 * time.Second,
		batch:          20,
		platformHost:   platformHost,
		buildTransport: NewTransport,
	}
}

// Start runs the send loop until ctx is cancelled. Best-effort: per-tick errors
// log and continue; a single bad delivery never stalls the queue.
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

// runTick claims a batch of due deliveries and sends each one. Idles (no DB
// work) when the KEK is unset so the whole channel disables cleanly.
func (s *Sender) runTick(ctx context.Context) {
	if len(s.kek) == 0 {
		return // email disabled
	}
	// One tenant in single mode; rows carry tenant_id, so the transport config
	// is loaded lazily per tenant seen in the batch (cached for the tick).
	now := time.Now().UTC()
	rows, err := s.repo.ClaimPendingEmailDeliveries(ctx, now, s.batch)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 sender: claim failed", "err", err)
		return
	}
	transportByTenant := map[uuid.UUID]Transport{}
	for _, d := range rows {
		tr, err := s.transportFor(ctx, d.TenantID, transportByTenant)
		if err != nil {
			s.fail(ctx, d, err)
			continue
		}
		if tr == nil {
			// Config disabled/absent. ClaimPendingEmailDeliveries already leased
			// this row (next_attempt_at = now()+1min), so a plain `continue` would
			// leave it pending and re-claim it every minute forever (unbounded
			// churn). Age it toward a terminal state instead: fail() bumps attempts,
			// applies Backoff, and flips to 'failed' at MaxAttempts (FIX 2).
			s.fail(ctx, d, errors.New("email transport not enabled or not configured"))
			continue
		}
		msg := renderMessage(s.platformHost, d)
		if err := tr.Send(ctx, msg); err != nil {
			s.fail(ctx, d, err)
			continue
		}
		if err := s.repo.MarkEmailSent(ctx, d.ID, tr.Name()); err != nil {
			slog.ErrorContext(ctx, "FUT-019 sender: mark sent failed", "err", err, "id", d.ID)
		}
	}
}

// transportFor loads + caches a tenant's transport, returning nil (no error)
// when the tenant has no config or it is disabled.
func (s *Sender) transportFor(ctx context.Context, tenantID uuid.UUID, cache map[uuid.UUID]Transport) (Transport, error) {
	if tr, ok := cache[tenantID]; ok {
		return tr, nil
	}
	cfg, err := s.repo.GetEmailTransportConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		cache[tenantID] = nil
		return nil, nil
	}
	dec, err := decryptConfig(s.kek, cfg)
	if err != nil {
		return nil, err
	}
	tr, err := s.buildTransport(dec)
	if err != nil {
		return nil, err
	}
	cache[tenantID] = tr
	return tr, nil
}

// fail records a send failure, computing the next attempt via Backoff and
// flipping the row to 'failed' once the retry budget is exhausted.
func (s *Sender) fail(ctx context.Context, d *repository.EmailDelivery, sendErr error) {
	attempts := d.Attempts + 1
	failed := attempts >= MaxAttempts
	next := time.Now().UTC().Add(Backoff(attempts))
	if err := s.repo.MarkEmailFailed(ctx, d.ID, attempts, next, failed, sendErr.Error()); err != nil {
		slog.ErrorContext(ctx, "FUT-019 sender: mark failed errored", "err", err, "id", d.ID)
	}
}

// decryptConfig opens the two secret BYTEA columns with the KEK and maps the
// non-secret columns straight across. An empty (nil) ciphertext column yields an
// empty secret field — the transport factory then rejects a config that needs
// the missing secret (e.g. resend with no api key).
func decryptConfig(kek []byte, cfg *repository.EmailTransportConfig) (DecryptedConfig, error) {
	dec := DecryptedConfig{
		Provider:     cfg.Provider,
		FromAddress:  cfg.FromAddress,
		FromName:     cfg.FromName,
		SMTPHost:     cfg.SMTPHost,
		SMTPPort:     cfg.SMTPPort,
		SMTPUsername: cfg.SMTPUsername,
		SMTPTLSMode:  cfg.SMTPTLSMode,
	}
	if len(cfg.ResendAPIKeyEnc) > 0 {
		pt, err := aes.Decrypt(cfg.ResendAPIKeyEnc, kek)
		if err != nil {
			return DecryptedConfig{}, fmt.Errorf("decrypt resend api key: %w", err)
		}
		dec.ResendAPIKey = string(pt)
	}
	if len(cfg.SMTPPasswordEnc) > 0 {
		pt, err := aes.Decrypt(cfg.SMTPPasswordEnc, kek)
		if err != nil {
			return DecryptedConfig{}, fmt.Errorf("decrypt smtp password: %w", err)
		}
		dec.SMTPPassword = string(pt)
	}
	return dec, nil
}

// renderMessage builds the HTML + plaintext bodies for one delivery (spec §6.1).
// The CTA points at the absolute URL (platformHost + d.Link) when a host is
// configured, else falls back to the relative link. User-supplied summary /
// category are HTML-escaped so a crafted event body can't inject markup.
func renderMessage(platformHost string, d *repository.EmailDelivery) Message {
	// Resolve the CTA target: absolute when both host and link are present,
	// otherwise the raw (possibly relative) link.
	cta := d.Link
	if platformHost != "" && d.Link != "" {
		cta = platformHost + d.Link
	}

	// Resolve the footer "Manage preferences" link the same way as the CTA:
	// absolute when a host is configured, else the relative settings path (FIX 4).
	const prefsPath = "/settings/notifications"
	prefs := prefsPath
	if platformHost != "" {
		prefs = platformHost + prefsPath
	}

	summaryHTML := html.EscapeString(d.BodySummary)
	categoryHTML := html.EscapeString(d.Category)
	ctaHTML := html.EscapeString(cta)
	prefsHTML := html.EscapeString(prefs)

	// Plaintext body.
	text := d.BodySummary
	if cta != "" {
		text += "\n\n" + cta
	}
	text += fmt.Sprintf(
		"\n\nYou're receiving this because %s email is enabled. Manage preferences → %s",
		d.Category, prefs,
	)

	// HTML body — minimal, self-contained markup.
	var htmlBody string
	htmlBody += fmt.Sprintf("<p>%s</p>", summaryHTML)
	if cta != "" {
		htmlBody += fmt.Sprintf(`<p><a href="%s">View in Janus</a></p>`, ctaHTML)
	}
	htmlBody += fmt.Sprintf(
		`<hr><p style="color:#888;font-size:12px">You're receiving this because %s email is enabled. `+
			`<a href="%s">Manage preferences</a></p>`,
		categoryHTML, prefsHTML,
	)

	return Message{
		To:       d.ToAddress,
		Subject:  d.Subject,
		HTMLBody: htmlBody,
		TextBody: text,
	}
}
