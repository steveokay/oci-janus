// Package upstream provides an SSRF-protected HTTP client for fetching from upstream OCI registries.
package upstream

import (
	"context"
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

var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"0.0.0.0/8",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range cidrs {
		_, network, _ := net.ParseCIDR(cidr)
		privateRanges = append(privateRanges, network)
	}
}

func isPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateUpstreamURL checks that the URL scheme is HTTPS and the hostname does not resolve
// to a private/loopback address (SSRF pre-registration guard).
func ValidateUpstreamURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("upstream URL must use HTTPS")
	}
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("DNS lookup for %q: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("upstream host %q resolves to a private address", host)
		}
	}
	return nil
}

// Credentials holds auth credentials for an upstream registry.
type Credentials struct {
	Type     string // "none" | "basic" | "token"
	Username string
	Password string
}

// ManifestResult is the response from FetchManifest.
type ManifestResult struct {
	Digest    string
	MediaType string
	Body      []byte
}

// Client is an SSRF-protected HTTP client for upstream OCI registries.
type Client struct {
	http     *http.Client
	maxBytes int64
}

// New builds a Client with SSRF-protected transport, timeout and response size cap.
func New(timeoutSecs int, maxBytes int64) *Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ia := range ips {
				if isPrivateIP(ia.IP) {
					return nil, fmt.Errorf("upstream address %q resolves to a private IP: SSRF blocked", host)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(timeoutSecs) * time.Second,
		},
		maxBytes: maxBytes,
	}
}

// tokenResponse is a partial parse of the Docker/OCI token endpoint response.
type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

// fetchToken performs the Bearer token fetch from the realm in the WWW-Authenticate header.
func (c *Client) fetchToken(ctx context.Context, wwwAuth string, creds Credentials) (string, error) {
	// Parse: Bearer realm="...",service="...",scope="..."
	params := parseBearerChallenge(wwwAuth)
	realm, ok := params["realm"]
	if !ok {
		return "", errors.New("no realm in WWW-Authenticate")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, nil)
	if err != nil {
		return "", err
	}

	q := req.URL.Query()
	if svc, ok := params["service"]; ok {
		q.Set("service", svc)
	}
	if scope, ok := params["scope"]; ok {
		q.Set("scope", scope)
	}
	req.URL.RawQuery = q.Encode()

	if creds.Type == "basic" || creds.Type == "token" {
		req.SetBasicAuth(creds.Username, creds.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("token fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	tok := tr.Token
	if tok == "" {
		tok = tr.AccessToken
	}
	if tok == "" {
		return "", errors.New("token endpoint returned empty token")
	}
	return tok, nil
}

// doWithAuth executes req, handles a 401 Bearer challenge, and retries once with a token.
func (c *Client) doWithAuth(ctx context.Context, req *http.Request, creds Credentials) (*http.Response, error) {
	// first attempt — no token
	resp, err := c.http.Do(req.Clone(ctx))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	wwwAuth := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(wwwAuth, "Bearer ") {
		return nil, fmt.Errorf("upstream returned 401 with unsupported challenge: %s", wwwAuth)
	}

	token, err := c.fetchToken(ctx, wwwAuth, creds)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream token: %w", err)
	}

	// retry with token
	req2 := req.Clone(ctx)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := c.http.Do(req2)
	if err != nil {
		return nil, err
	}
	return resp2, nil
}

// FetchManifest retrieves an OCI/Docker manifest from the upstream registry.
// Reference may be a tag or a digest.
func (c *Client) FetchManifest(ctx context.Context, upstreamURL, image, reference string, creds Credentials) (*ManifestResult, error) {
	rawURL := fmt.Sprintf("%s/v2/%s/manifests/%s", strings.TrimRight(upstreamURL, "/"), image, reference)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))

	resp, err := c.doWithAuth(ctx, req, creds)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream manifest fetch returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024)) // 4 MiB manifest cap
	if err != nil {
		return nil, fmt.Errorf("read manifest body: %w", err)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		digest = resp.Header.Get("Content-Digest")
	}
	mediaType := resp.Header.Get("Content-Type")
	if idx := strings.Index(mediaType, ";"); idx != -1 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}

	return &ManifestResult{
		Digest:    digest,
		MediaType: mediaType,
		Body:      body,
	}, nil
}

// FetchBlob retrieves a blob from the upstream registry.
// Returns a ReadCloser (caller must close), the content-length (-1 if unknown), and content-type.
func (c *Client) FetchBlob(ctx context.Context, upstreamURL, image, digest string, creds Credentials) (io.ReadCloser, int64, string, error) {
	rawURL := fmt.Sprintf("%s/v2/%s/blobs/%s", strings.TrimRight(upstreamURL, "/"), image, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, "", err
	}

	resp, err := c.doWithAuth(ctx, req, creds)
	if err != nil {
		return nil, 0, "", err
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, 0, "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, "", fmt.Errorf("upstream blob fetch returned %d", resp.StatusCode)
	}

	size := resp.ContentLength
	ct := resp.Header.Get("Content-Type")
	body := io.LimitReader(resp.Body, c.maxBytes)
	return io.NopCloser(body), size, ct, nil
}

// parseBearerChallenge parses the comma-separated key=value pairs from a
// Bearer WWW-Authenticate header value.
//
// PENTEST-009: the previous implementation split on every `,` which broke for
// quoted values that legitimately contain commas (e.g. `scope="repository:foo,bar:pull"`).
// This parser walks the header tracking quote state so commas inside `"..."`
// do not separate pairs. It is intentionally permissive (does not error on
// malformed input) — unparseable segments are silently skipped, matching the
// behaviour of common Docker client implementations.
func parseBearerChallenge(header string) map[string]string {
	header = strings.TrimPrefix(header, "Bearer ")
	params := make(map[string]string)
	for _, segment := range splitCommaRespectingQuotes(header) {
		segment = strings.TrimSpace(segment)
		idx := strings.IndexByte(segment, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(segment[:idx])
		val := strings.TrimSpace(segment[idx+1:])
		// Strip a single layer of surrounding quotes, then unescape \" and \\.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = unescapeQuoted(val[1 : len(val)-1])
		}
		if key != "" {
			params[key] = val
		}
	}
	return params
}

// splitCommaRespectingQuotes splits s on top-level commas, treating any comma
// inside a double-quoted run as literal. Backslash-escapes inside the quoted
// run (\" and \\) are honoured so the closing quote is detected correctly.
func splitCommaRespectingQuotes(s string) []string {
	var out []string
	var buf strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(s):
			// Keep the escape sequence verbatim; unescapeQuoted handles it later.
			buf.WriteByte(c)
			buf.WriteByte(s[i+1])
			i++
		case c == '"':
			inQuote = !inQuote
			buf.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, buf.String())
			buf.Reset()
		default:
			buf.WriteByte(c)
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// unescapeQuoted resolves the backslash-escapes permitted inside an
// RFC 7230 quoted-string: \" → " and \\ → \. Any other \X is left as X.
func unescapeQuoted(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
