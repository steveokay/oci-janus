// Package webhook implements the FUT-019 webhook notification channel: a
// signed HTTP POST transport (with SSRF protection) plus a send loop that
// drains the notification_webhook_deliveries queue.
//
// The SSRF dialer + HMAC signing + URL-redaction helpers are copied from
// services/webhook/internal/delivery (dispatcher.go + ssrf.go). Extracting a
// shared libs/webhook consumed by both services is a deferred follow-up (it
// would touch registry-webhook, out of scope for this build).
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxAttempts is the retry ceiling; on the MaxAttempts-th failure the delivery
// flips to 'failed'.
const MaxAttempts = 5

// Backoff returns the retry delay for a given (1-based) attempt, clamped to the
// last bucket. Mirrors the email + registry-webhook schedule.
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

// maxResponseBytes caps the response body we drain from the endpoint (PENTEST-007).
const maxResponseBytes = 8 * 1024

// Poster sends one signed POST to the org webhook. Constructed with an
// SSRF-blocking dialer so a hostile/misconfigured URL can't reach internal
// services even after ValidateURL (defence-in-depth against DNS rebinding).
type Poster struct {
	client *http.Client
}

// NewPoster builds a Poster with an SSRF-protected HTTP client (blocks private
// IP ranges, validates every resolved IP then dials by literal to close the
// DNS-rebinding gap — copied from registry-webhook's dispatcher).
func NewPoster() *Poster {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}
			var dialIP string
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					return nil, fmt.Errorf("SSRF protection: unparseable IP %q for %s", ipStr, host)
				}
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("SSRF protection: blocked connection to private IP %s", ipStr)
				}
				if dialIP == "" {
					dialIP = ip.String()
				}
			}
			if dialIP == "" {
				return nil, fmt.Errorf("SSRF protection: no IPs resolved for %s", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(dialIP, port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &Poster{client: &http.Client{Transport: transport, Timeout: 20 * time.Second}}
}

// newPosterForTest builds a Poster with a stock client (no SSRF dialer) so the
// signing/POST path is unit-testable against httptest loopback. NOT used in
// production wiring.
func newPosterForTest() *Poster {
	return &Poster{client: &http.Client{Timeout: 5 * time.Second}}
}

// Post signs body with the HMAC secret and POSTs it to targetURL. Returns the
// HTTP status (0 when the request failed before a response) and a redacted
// error. The raw URL never reaches the error (it may carry a token in the query
// / userinfo) — sanitised scheme://host/path only.
func (p *Poster) Post(ctx context.Context, targetURL string, body, secret []byte) (int, error) {
	sig := computeHMAC(body, secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registry-Signature", "sha256="+sig)
	req.Header.Set("User-Agent", "registry-audit-notify/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST to %s: %w", sanitizeURLForError(targetURL), stripURLFromError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("endpoint returned status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// buildPayload renders the generic signed notification envelope.
func buildPayload(category, subject, summary, link, tenantID string, ts time.Time) []byte {
	b, _ := json.Marshal(map[string]any{
		"event":     "notification",
		"category":  category,
		"subject":   subject,
		"summary":   summary,
		"link":      link,
		"tenant_id": tenantID,
		"timestamp": ts.UTC().Format(time.RFC3339),
	})
	return b
}

func computeHMAC(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// ── SSRF + URL redaction (copied from services/webhook/internal/delivery) ──

var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8",
		"0.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10",
		"::1/128", "fc00::/7", "fe80::/10",
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("invalid private CIDR: " + cidr)
		}
		privateRanges = append(privateRanges, network)
	}
}

// ValidateURL checks the destination is HTTPS and doesn't resolve to a private/
// loopback/link-local IP. Called on the config PUT path so a bad URL is
// rejected before it's ever stored.
func ValidateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("webhook URL must use HTTPS (got %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook URL has no host")
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("DNS lookup for %q: %w", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("webhook destination %q resolves to private IP %s — blocked (SSRF protection)", host, addr)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	for _, network := range privateRanges {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func sanitizeURLForError(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "[redacted url]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func stripURLFromError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
