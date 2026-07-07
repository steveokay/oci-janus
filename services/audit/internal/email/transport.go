// Package email implements the FUT-019 Phase 3 email notification channel:
// a pluggable transport (Resend HTTP API or SMTP) plus a send loop that drains
// the email_deliveries queue.
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// smtpDialTimeout bounds the TCP/TLS dial so a hung or unreachable SMTP server
// cannot stall the single-goroutine send loop forever. net/smtp ignores the
// request context on its own, so the timeout is applied at the dialer.
const smtpDialTimeout = 15 * time.Second

// Message is one rendered email ready to send.
type Message struct {
	To       string
	ToName   string
	Subject  string
	HTMLBody string
	TextBody string
}

// Transport sends a single message via a concrete provider. Send returns a
// redacted, retryable error; Name identifies the provider for the delivery log.
type Transport interface {
	Send(ctx context.Context, msg Message) error
	Name() string
}

// DecryptedConfig is the transport config with secrets already decrypted,
// built by the caller (send loop) from an email_transport_config row.
type DecryptedConfig struct {
	Provider     string
	FromAddress  string
	FromName     string
	ResendAPIKey string
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPTLSMode  string
}

// fromHeader renders the RFC5322 From value: "Name <addr>" or "addr".
func (c DecryptedConfig) fromHeader() string {
	if c.FromName != "" {
		return fmt.Sprintf("%s <%s>", c.FromName, c.FromAddress)
	}
	return c.FromAddress
}

// NewTransport builds the concrete Transport for cfg.Provider.
func NewTransport(cfg DecryptedConfig) (Transport, error) {
	switch cfg.Provider {
	case "resend":
		if cfg.ResendAPIKey == "" {
			return nil, fmt.Errorf("resend transport: api key not set")
		}
		return &resendTransport{
			apiKey:   cfg.ResendAPIKey,
			from:     cfg.fromHeader(),
			endpoint: "https://api.resend.com/emails",
			client:   &http.Client{Timeout: 15 * time.Second},
		}, nil
	case "smtp":
		if cfg.SMTPHost == "" {
			return nil, fmt.Errorf("smtp transport: host not set")
		}
		return &smtpTransport{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown email provider %q", cfg.Provider)
	}
}

// Backoff returns the retry delay for a given (1-based) attempt number, clamped
// to the last bucket. Mirrors the webhook dispatcher schedule.
func Backoff(attempt int) time.Duration {
	sched := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(sched) {
		return sched[len(sched)-1]
	}
	return sched[attempt-1]
}

// MaxAttempts is the retry ceiling; on the MaxAttempts-th failure the delivery
// flips to 'failed'.
const MaxAttempts = 5

// ── Resend ───────────────────────────────────────────────────────────

type resendTransport struct {
	apiKey   string
	from     string
	endpoint string
	client   *http.Client
}

func (t *resendTransport) Name() string { return "resend" }

func (t *resendTransport) Send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(map[string]any{
		"from":    t.from,
		"to":      []string{msg.To},
		"subject": msg.Subject,
		"html":    msg.HTMLBody,
		"text":    msg.TextBody,
	})
	if err != nil {
		return fmt.Errorf("marshal resend body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// net.Error may embed the URL but never the key; still, redact defensively.
		return fmt.Errorf("resend send failed: %s", redact(err.Error(), t.apiKey))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("resend returned %d: %s", resp.StatusCode, redact(string(snippet), t.apiKey))
}

// redact removes any occurrence of secret from s so provider errors can't leak
// credentials into logs / last_error.
func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

// ── SMTP ─────────────────────────────────────────────────────────────

type smtpTransport struct {
	cfg DecryptedConfig
}

func (t *smtpTransport) Name() string { return "smtp" }

func (t *smtpTransport) Send(ctx context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", t.cfg.SMTPHost, t.cfg.SMTPPort)
	raw := buildMIME(t.cfg.fromHeader(), msg)

	var err error
	switch t.cfg.SMTPTLSMode {
	case "implicit":
		err = t.sendImplicitTLS(ctx, addr, msg.To, raw)
	default: // starttls / none
		err = t.sendStartTLS(ctx, addr, msg.To, raw)
	}
	if err != nil {
		return fmt.Errorf("smtp send failed: %s", redact(err.Error(), t.cfg.SMTPPassword))
	}
	return nil
}

// sendImplicitTLS dials a TLS socket first (port 465 style) then speaks SMTP.
// The bounded tls.Dialer replaces the timeout-less tls.Dial so a hung server
// trips smtpDialTimeout instead of blocking the send loop indefinitely (FIX 1).
func (t *smtpTransport) sendImplicitTLS(ctx context.Context, addr, to string, raw []byte) error {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: smtpDialTimeout},
		Config:    &tls.Config{ServerName: t.cfg.SMTPHost, MinVersion: tls.VersionTLS12},
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, t.cfg.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return err
	}
	return t.deliver(c, to, raw)
}

// sendStartTLS dials a plaintext socket with a bounded timeout, optionally
// upgrades it via STARTTLS (when the mode is "starttls" and the server offers
// the extension), then speaks SMTP. Replaces the timeout-less smtp.SendMail
// (which net/smtp dials with no deadline) so a hung server trips smtpDialTimeout
// (FIX 1).
func (t *smtpTransport) sendStartTLS(ctx context.Context, addr, to string, raw []byte) error {
	conn, err := (&net.Dialer{Timeout: smtpDialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, t.cfg.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if t.cfg.SMTPTLSMode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: t.cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
				_ = c.Close()
				return err
			}
		}
	}
	return t.deliver(c, to, raw)
}

// deliver runs the shared SMTP conversation over an already-established client:
// optional auth (only when a username is configured), MAIL/RCPT/DATA, then QUIT.
// Both the implicit-TLS and STARTTLS paths funnel through here (DRY).
func (t *smtpTransport) deliver(c *smtp.Client, to string, raw []byte) error {
	defer func() { _ = c.Close() }()
	if t.cfg.SMTPUsername != "" {
		auth := smtp.PlainAuth("", t.cfg.SMTPUsername, t.cfg.SMTPPassword, t.cfg.SMTPHost)
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(t.cfg.FromAddress); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(raw); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// stripCRLF removes carriage returns and line feeds from a header value so a
// crafted To / Subject cannot inject additional SMTP headers (e.g. a Bcc: line).
// Header-injection defense for the SMTP path (FIX 3); the Resend path is
// JSON-encoded and therefore unaffected.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// buildMIME renders a minimal multipart/alternative message (text + html).
// To / Subject / From are run through stripCRLF so untrusted values cannot smuggle
// extra header lines into the message.
func buildMIME(from string, msg Message) []byte {
	const boundary = "janus-mime-boundary-8f2c"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", stripCRLF(from))
	fmt.Fprintf(&b, "To: %s\r\n", stripCRLF(msg.To))
	fmt.Fprintf(&b, "Subject: %s\r\n", stripCRLF(msg.Subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", boundary, msg.TextBody)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n", boundary, msg.HTMLBody)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}
