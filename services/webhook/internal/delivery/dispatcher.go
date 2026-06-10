package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

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

// Deliver sends a single webhook delivery attempt. Returns an error if the
// endpoint is unreachable, returns a non-2xx status, or the context is cancelled.
func (d *Dispatcher) Deliver(ctx context.Context, targetURL string, payload []byte, hmacKey []byte) error {
	sig := computeHMAC(payload, hmacKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registry-Signature", "sha256="+sig)
	req.Header.Set("User-Agent", "registry-webhook/1.0")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP POST to %s: %w", targetURL, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("endpoint returned status %d", resp.StatusCode)
	}
	return nil
}

// computeHMAC returns the lowercase hex HMAC-SHA256 of payload using key.
func computeHMAC(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
