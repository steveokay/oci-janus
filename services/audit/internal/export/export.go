// Package export ships audit events to per-tenant SIEM destinations
// (futures.md Tier 1 #4 — audit log streaming).
//
// Three wire formats are supported in v1:
//   - syslog_rfc5424  — RFC 5424 framed over TCP (plain or TLS)
//   - cef             — ArcSight Common Event Format, transported over syslog
//   - webhook         — POST JSON over HTTPS with HMAC or bearer auth
//
// The exporter is invoked inline by the audit service's eventconsumer
// after each successful audit_events INSERT. v1 ships with in-process
// retry (3 attempts, exponential backoff capped at 5s); exhausted
// attempts increment `audit_export_configs.dlx_depth` so the operator
// sees a "stuck events" counter on the dashboard. A true async DLX
// queue is a Phase 2 follow-up tracked in docs/SIEM-EXPORT.md — the
// MVP closes the procurement gate without committing to the queue
// infrastructure up front.
//
// SSRF guard: target_url is validated at config write time against
// the same private-CIDR blocklist services/proxy uses for upstream
// registries. A repeat check fires at delivery time too because DNS
// resolution can shift between write + send.
package export

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is the wire-shape the exporter renders + ships. Mirrors the
// audit_events row (plus tenant context) and is decoupled from the
// repository struct so the renderer doesn't have to import the
// repository package.
type Event struct {
	ID         string
	TenantID   string
	ActorID    string
	ActorType  string
	ActorIP    string
	Action     string
	Resource   string
	Outcome    string
	Metadata   json.RawMessage
	OccurredAt time.Time
}

// Config carries the runtime knobs the exporter needs to ship one
// event. Constructed by the caller from the AES-256-GCM-decrypted
// audit_export_configs row.
type Config struct {
	TenantID    string
	Format      string // "syslog_rfc5424" | "cef" | "webhook"
	TargetURL   string
	HMACSecret  string // plaintext; "" when unset
	BearerToken string // plaintext; "" when unset
}

// MaxAttempts caps in-process retries before the exporter gives up
// and increments dlx_depth. Kept small so a slow SIEM doesn't stall
// the audit consumer's main loop — the audit DB insert has already
// succeeded by the time we get here, so a few seconds of retry is
// the right trade-off.
const MaxAttempts = 3

// Deliver applies the configured format renderer + retry loop. Returns
// the rendered wire payload (so callers can show "this is what we
// sent" on the Test endpoint) and the last error. The error is nil
// only when one of MaxAttempts succeeded.
func Deliver(ctx context.Context, cfg Config, evt Event) (rendered string, err error) {
	rendered, err = render(cfg, evt)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	// SSRF defence-in-depth — validate every time, not just at config
	// write time. DNS can shift between writes; an attacker who
	// briefly resolves a public hostname to a private IP would
	// otherwise slip past the write-time check.
	if err := guardTargetURL(cfg.TargetURL); err != nil {
		return rendered, fmt.Errorf("target url: %w", err)
	}

	backoff := time.Second
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return rendered, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
		err = ship(ctx, cfg, rendered)
		if err == nil {
			return rendered, nil
		}
		slog.WarnContext(ctx, "audit export attempt failed",
			"tenant_id", cfg.TenantID, "format", cfg.Format,
			"attempt", attempt+1, "max", MaxAttempts, "err", err,
		)
	}
	return rendered, err
}

// render dispatches to the per-format encoder.
func render(cfg Config, evt Event) (string, error) {
	switch cfg.Format {
	case "syslog_rfc5424":
		return renderSyslog(evt), nil
	case "cef":
		return renderCEF(evt), nil
	case "webhook":
		return renderWebhook(evt)
	default:
		return "", fmt.Errorf("unsupported format %q", cfg.Format)
	}
}

// renderSyslog emits an RFC 5424 line. Severity is mapped from
// `outcome` (success=info / failure=warning) — IANA facility 13
// ("log audit") because that's what the spec carved out for audit
// logs. SD-PARAM keys are the audit event's structured fields so
// downstream SIEMs can parse without regex.
func renderSyslog(evt Event) string {
	const facility = 13 // log audit
	severity := 6       // info
	if evt.Outcome != "success" {
		severity = 4 // warning
	}
	pri := facility*8 + severity

	// Structured data block. RFC 5424 §6.3 allows arbitrary
	// `[id key="value"]` segments; we use one segment id keyed by
	// the platform identifier `oci-janus@53430` (53430 = arbitrary
	// private enterprise number — replace with a real PEN when one
	// is registered).
	sd := fmt.Sprintf(`[oci-janus@53430 tenant_id=%q actor_id=%q actor_type=%q actor_ip=%q resource=%q outcome=%q event_id=%q]`,
		escapeSDValue(evt.TenantID),
		escapeSDValue(evt.ActorID),
		escapeSDValue(evt.ActorType),
		escapeSDValue(evt.ActorIP),
		escapeSDValue(evt.Resource),
		escapeSDValue(evt.Outcome),
		escapeSDValue(evt.ID),
	)

	hostname := "registry-audit"
	appName := "oci-janus"
	procID := "-"
	msgID := evt.Action

	// `1` is the RFC 5424 version. msg is intentionally empty — the SD
	// block carries everything an SIEM cares about.
	return fmt.Sprintf("<%d>1 %s %s %s %s %s %s -",
		pri,
		evt.OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		hostname, appName, procID, msgID, sd)
}

// renderCEF emits the ArcSight Common Event Format (CEF) line.
// CEF:Version|Vendor|Product|ProductVersion|EventID|EventName|Severity|Extensions
// Most SIEMs that accept CEF actually wrap it in a syslog framing line;
// the transport code (shipSyslog) takes care of that. The renderer
// returns the raw CEF body.
func renderCEF(evt Event) string {
	severity := 3 // low/info
	if evt.Outcome != "success" {
		severity = 7 // high
	}
	// Order matches CEF spec: pipes inside fields must be escaped.
	body := fmt.Sprintf("CEF:0|oci-janus|registry|1.0|%s|%s|%d|%s",
		escapeCEFHeader(evt.Action),
		escapeCEFHeader(evt.Action),
		severity,
		formatCEFExtensions(evt),
	)
	return body
}

// renderWebhook emits the JSON body sent in the HTTPS POST. The
// envelope mirrors the audit event row for consumer ergonomics.
func renderWebhook(evt Event) (string, error) {
	body, err := json.Marshal(map[string]any{
		"id":          evt.ID,
		"tenant_id":   evt.TenantID,
		"actor_id":    evt.ActorID,
		"actor_type":  evt.ActorType,
		"actor_ip":    evt.ActorIP,
		"action":      evt.Action,
		"resource":    evt.Resource,
		"outcome":     evt.Outcome,
		"metadata":    json.RawMessage(evt.Metadata),
		"occurred_at": evt.OccurredAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ship routes to the per-format transport.
func ship(ctx context.Context, cfg Config, rendered string) error {
	switch cfg.Format {
	case "syslog_rfc5424", "cef":
		return shipSyslog(ctx, cfg.TargetURL, rendered)
	case "webhook":
		return shipWebhook(ctx, cfg, rendered)
	default:
		return fmt.Errorf("unsupported format %q", cfg.Format)
	}
}

// shipSyslog dials the target host (TCP or TLS) and writes one line
// terminated by `\n`. The URL scheme picks the transport:
//   - syslog+tcp://host:port → plain TCP (only acceptable for dev /
//     test SIEMs on the same host; real deployments must use TLS)
//   - syslog+tls://host:port → TCP with TLS via the system trust
//     store. No client cert support in v1 — most enterprise SIEMs
//     accept a bare TLS upgrade and authenticate via the source IP
//     in their pipeline config.
func shipSyslog(ctx context.Context, target, line string) error {
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Host == "" {
		return errors.New("missing host in syslog url")
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var conn net.Conn
	switch u.Scheme {
	case "syslog+tcp":
		conn, err = dialer.DialContext(ctx, "tcp", u.Host)
	case "syslog+tls":
		conn, err = tls.DialWithDialer(dialer, "tcp", u.Host, &tls.Config{
			MinVersion: tls.VersionTLS12,
		})
	default:
		return fmt.Errorf("unsupported syslog scheme %q (expected syslog+tcp or syslog+tls)", u.Scheme)
	}
	if err != nil {
		return fmt.Errorf("dial syslog: %w", err)
	}
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return fmt.Errorf("write syslog: %w", err)
	}
	return nil
}

// shipWebhook POSTs the rendered JSON to the target HTTPS URL with
// HMAC-SHA256 (`X-Signature: sha256=<hex>`) when hmac_secret is
// configured; bearer token otherwise. HTTPS-only enforced at the
// transport — http:// targets are rejected (no production SIEM
// should accept audit events over plaintext anyway).
func shipWebhook(ctx context.Context, cfg Config, body string) error {
	u, err := url.Parse(cfg.TargetURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		// Local-dev escape hatch — accept http://localhost,
		// http://127.0.0.1, or http://host.docker.internal so the
		// smoke test (Go HTTP server on localhost:port) can drive the
		// path without a TLS cert. Anywhere else, refuse.
		devHost := strings.HasPrefix(u.Host, "localhost") ||
			strings.HasPrefix(u.Host, "127.0.0.1") ||
			strings.HasPrefix(u.Host, "host.docker.internal")
		if !(u.Scheme == "http" && devHost) {
			return fmt.Errorf("webhook url must be HTTPS (got %s)", u.Scheme)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TargetURL, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "oci-janus-audit-export/1.0")

	if cfg.HMACSecret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.HMACSecret))
		_, _ = mac.Write([]byte(body))
		req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	} else if cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	}

	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()
	// Drain a small prefix so connections can be reused.
	_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("webhook returned %d", resp.StatusCode)
}

// guardTargetURL is the SSRF check. Same private-CIDR blocklist as
// services/proxy. Localhost is allowed for dev smoke tests but only
// for `http://localhost*` or `http://127.0.0.1*` (TLS deployments
// don't typically point at loopback).
func guardTargetURL(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Host == "" {
		return errors.New("missing host")
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "host.docker.internal" {
		return nil // dev escape hatch (host.docker.internal is the
		// canonical way to reach the dev machine from inside a docker
		// container — the audit container only knows about the docker
		// network; the operator's webhook receiver typically lives on
		// the host OS during smoke tests)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("target resolves to private IP %s (SSRF block)", ip)
		}
	}
	return nil
}

// isPrivateIP returns true for the RFC 1918 / loopback / link-local /
// carrier-grade-NAT / unique-local / link-local-v6 ranges. Matches
// services/proxy's blocklist (PENTEST-NETWORK-001 era).
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// 100.64.0.0/10 (carrier-grade NAT) is not flagged by net.IP.IsPrivate.
	cgnat := net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	if ip4 := ip.To4(); ip4 != nil && cgnat.Contains(ip4) {
		return true
	}
	return false
}

// escapeSDValue escapes the characters RFC 5424 §6.3.3 reserves
// inside SD-PARAM values: `\`, `"`, and `]`.
func escapeSDValue(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`)
	return r.Replace(s)
}

// escapeCEFHeader escapes characters CEF reserves in header fields:
// `\`, `|`. Newlines are also stripped because CEF is line-framed.
func escapeCEFHeader(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `|`, `\|`, "\n", " ", "\r", " ")
	return r.Replace(s)
}

// escapeCEFExt escapes characters CEF reserves in extension values:
// `\`, `=`. Newlines stripped.
func escapeCEFExt(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `=`, `\=`, "\n", " ", "\r", " ")
	return r.Replace(s)
}

// formatCEFExtensions emits the CEF key=value extension list. CEF
// extensions are space-delimited; we use the canonical short names
// where applicable (rt = receiptTime, src = source IP, suser =
// source user, act = action, outcome = outcome). The platform-
// specific fields ride on a custom `cs1`/`cs1Label` pair per CEF
// convention.
func formatCEFExtensions(evt Event) string {
	parts := []string{
		"rt=" + escapeCEFExt(evt.OccurredAt.UTC().Format("Jan 02 2006 15:04:05.000")),
		"src=" + escapeCEFExt(evt.ActorIP),
		"suser=" + escapeCEFExt(evt.ActorID),
		"act=" + escapeCEFExt(evt.Action),
		"outcome=" + escapeCEFExt(evt.Outcome),
		"cs1Label=tenant_id",
		"cs1=" + escapeCEFExt(evt.TenantID),
		"cs2Label=resource",
		"cs2=" + escapeCEFExt(evt.Resource),
		"cs3Label=event_id",
		"cs3=" + escapeCEFExt(evt.ID),
	}
	if len(evt.Metadata) > 0 {
		parts = append(parts,
			"cs4Label=metadata_b64",
			"cs4="+base64.StdEncoding.EncodeToString(evt.Metadata),
		)
	}
	return strings.Join(parts, " ")
}
