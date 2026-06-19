package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// sanitizeURLForError returns a redacted form of rawURL safe to embed in error
// messages and `webhook_deliveries.last_error`. Operators commonly carry a
// per-endpoint auth token in the URL itself (query param or userinfo), so the
// raw URL must never reach a row a low-privilege reader could see — and even
// after PENTEST-027 gates the list route to admins, persisting the cleartext
// secret in the DB is still a defence-in-depth problem.
//
// Returns scheme://host[:port][/path] with query string + userinfo stripped.
// If parsing fails (which it shouldn't — the URL passed ValidateURL at create
// time) the function returns the literal "[redacted url]" rather than echoing
// the raw input.
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

// stripURLFromError unwraps an *url.Error so the URL field (which the stdlib
// formats verbosely as "<Op> <URL>: <Err>") doesn't carry credentials into the
// caller's error string. Non-url.Error values are returned unchanged. Paired
// with sanitizeURLForError in DeliverWithResult.
func stripURLFromError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// retryDelays is the ordered list of wait durations before each attempt.
// Attempt 1 fires immediately (next_attempt_at = now). Subsequent delays:
// 5s → 30s → 5m → 30m → 2h  (per CLAUDE.md §4.9)
var retryDelays = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
}

// NextRetryAt returns the next attempt time given the current attempt count (0-based).
// Returns zero time if no more retries should be scheduled (exhausted).
func NextRetryAt(attempts int) (time.Time, bool) {
	if attempts >= len(retryDelays) {
		return time.Time{}, false
	}
	return time.Now().Add(retryDelays[attempts]), true
}

// Dispatcher sends signed HTTP POST requests to webhook endpoints.
type Dispatcher struct {
	client *http.Client
}

// NewDispatcher creates a Dispatcher with an SSRF-protected HTTP client that uses
// a custom dialer to block connections to private IP ranges.
func NewDispatcher(timeoutSecs int) *Dispatcher {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

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
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip != nil && isPrivateIP(ip) {
					return nil, fmt.Errorf("SSRF protection: blocked connection to private IP %s", ipStr)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}

	return &Dispatcher{
		client: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(timeoutSecs) * time.Second,
		},
	}
}

// maxResponseBytes caps the response body we read from a webhook endpoint.
// Webhook receivers are expected to ACK with a small JSON document or empty
// body; an unbounded read would let a hostile (or merely buggy) endpoint
// stream arbitrary bytes back at us until our request timeout fires, burning
// CPU and bandwidth on every worker (PENTEST-007).
const maxResponseBytes = 8 * 1024

// Deliver sends a single webhook delivery attempt. Returns an error if the
// endpoint is unreachable, returns a non-2xx status, or the context is cancelled.
func (d *Dispatcher) Deliver(ctx context.Context, targetURL string, payload []byte, hmacKey []byte) error {
	_, _, err := d.DeliverWithResult(ctx, targetURL, payload, hmacKey)
	return err
}

// DeliverWithResult is the same as Deliver but also returns the HTTP status code
// (0 when the request failed before a response was received) and the elapsed
// duration in milliseconds. The synchronous TestDispatch handler uses this so
// the dashboard can surface the actual response code to the operator.
func (d *Dispatcher) DeliverWithResult(ctx context.Context, targetURL string, payload []byte, hmacKey []byte) (int, int64, error) {
	sig := computeHMAC(payload, hmacKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return 0, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registry-Signature", "sha256="+sig)
	req.Header.Set("User-Agent", "registry-webhook/1.0")

	start := time.Now()
	resp, err := d.client.Do(req)
	if err != nil {
		// PENTEST-027: don't echo the raw URL — it may contain a token in
		// query or userinfo. The error string is persisted in
		// webhook_deliveries.last_error and shown back to admins via the
		// management API; a sanitised scheme://host/path is plenty to
		// debug a connectivity failure. http.Client.Do returns *url.Error
		// which would otherwise stuff the full URL back in via %w — strip
		// it so only the underlying transport error reaches the caller.
		return 0, time.Since(start).Milliseconds(), fmt.Errorf("HTTP POST to %s: %w", sanitizeURLForError(targetURL), stripURLFromError(err))
	}
	// PENTEST-007: cap the body we drain. A malicious or broken endpoint
	// streaming unbounded bytes back would otherwise tie up this worker
	// goroutine for the full request timeout while we discard data.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
	}()

	durMs := time.Since(start).Milliseconds()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, durMs, fmt.Errorf("endpoint returned status %d", resp.StatusCode)
	}
	return resp.StatusCode, durMs, nil
}

// computeHMAC returns the lowercase hex HMAC-SHA256 of payload using key.
func computeHMAC(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
