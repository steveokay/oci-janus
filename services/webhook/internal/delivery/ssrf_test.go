// Package delivery_test tests the SSRF protection helpers in ssrf.go.
// DNS lookups are not made against real external hosts — tests use IP-literal
// URLs (e.g. https://1.2.3.4) or loopback addresses that trigger the block.
package delivery

import (
	"net"
	"testing"
)

// TestIsPrivateIP_privateRanges exercises all configured private CIDR blocks to
// confirm they are all recognised as private.
func TestIsPrivateIP_privateRanges(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		// RFC 1918 ranges
		{name: "10.x.x.x — RFC 1918", ip: "10.0.0.1", want: true},
		{name: "10.255.255.255", ip: "10.255.255.255", want: true},
		{name: "172.16.x.x — RFC 1918", ip: "172.16.0.1", want: true},
		{name: "172.31.x.x — RFC 1918", ip: "172.31.255.255", want: true},
		{name: "192.168.x.x — RFC 1918", ip: "192.168.0.1", want: true},
		// Loopback
		{name: "127.0.0.1 — loopback", ip: "127.0.0.1", want: true},
		{name: "127.255.255.255 — loopback end", ip: "127.255.255.255", want: true},
		// Link-local / AWS metadata
		{name: "169.254.169.254 — AWS metadata", ip: "169.254.169.254", want: true},
		{name: "169.254.0.1 — link-local", ip: "169.254.0.1", want: true},
		// Shared address space (RFC 6598)
		{name: "100.64.0.1 — RFC 6598", ip: "100.64.0.1", want: true},
		// IPv6 loopback and ULA
		{name: "::1 — IPv6 loopback", ip: "::1", want: true},
		{name: "fc00::1 — IPv6 ULA", ip: "fc00::1", want: true},
		{name: "fe80::1 — IPv6 link-local", ip: "fe80::1", want: true},
		// Public addresses
		{name: "8.8.8.8 — public", ip: "8.8.8.8", want: false},
		{name: "1.1.1.1 — public", ip: "1.1.1.1", want: false},
		{name: "203.0.113.1 — TEST-NET-3 (public doc range)", ip: "203.0.113.1", want: false},
		{name: "2001:db8::1 — IPv6 doc range", ip: "2001:db8::1", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("net.ParseIP(%q) returned nil", tc.ip)
			}
			got := isPrivateIP(ip)
			if got != tc.want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// TestValidateURL_httpSchemeRejected confirms that http:// URLs are rejected
// because CLAUDE.md §4.9 requires HTTPS-only webhook endpoints.
func TestValidateURL_httpSchemeRejected(t *testing.T) {
	err := ValidateURL("http://example.com/hook")
	if err == nil {
		t.Error("expected error for http:// webhook URL, got nil")
	}
}

// TestValidateURL_nonHttpSchemeRejected ensures non-http/https schemes are blocked.
func TestValidateURL_nonHttpSchemeRejected(t *testing.T) {
	err := ValidateURL("ftp://example.com/hook")
	if err == nil {
		t.Error("expected error for ftp:// webhook URL, got nil")
	}
}

// TestValidateURL_malformedURL verifies that a completely invalid URL returns an error.
func TestValidateURL_malformedURL(t *testing.T) {
	// A URL with an invalid character that url.Parse will reject.
	err := ValidateURL("://not-a-url")
	if err == nil {
		t.Error("expected error for malformed URL")
	}
}

// TestValidateURL_noHost verifies that a URL with no hostname is rejected.
func TestValidateURL_noHost(t *testing.T) {
	err := ValidateURL("https:///path")
	if err == nil {
		t.Error("expected error for URL with no host")
	}
}
