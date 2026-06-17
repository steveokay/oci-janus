// Package upstream_test contains unit tests for SSRF validation and pure helper functions.
// Tests that require DNS lookups are skipped — only pure in-memory logic is covered here.
package upstream

import (
	"net"
	"testing"
)

// ── isPrivateIP tests ─────────────────────────────────────────────────────────

func TestIsPrivateIP_RFC1918_10Net_ReturnsTrue(t *testing.T) {
	cases := []string{
		"10.0.0.1",
		"10.255.255.255",
		"10.1.2.3",
	}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if !isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = false, want true", c)
		}
	}
}

func TestIsPrivateIP_RFC1918_172Net_ReturnsTrue(t *testing.T) {
	cases := []string{
		"172.16.0.1",
		"172.31.255.255",
		"172.20.10.5",
	}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if !isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = false, want true", c)
		}
	}
}

func TestIsPrivateIP_RFC1918_192Net_ReturnsTrue(t *testing.T) {
	cases := []string{
		"192.168.0.1",
		"192.168.255.255",
		"192.168.100.100",
	}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if !isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = false, want true", c)
		}
	}
}

func TestIsPrivateIP_Loopback_ReturnsTrue(t *testing.T) {
	cases := []string{"127.0.0.1", "127.0.0.2"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if !isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = false, want true", c)
		}
	}
}

func TestIsPrivateIP_LinkLocal_ReturnsTrue(t *testing.T) {
	// 169.254.169.254 is the AWS metadata endpoint
	ip := net.ParseIP("169.254.169.254")
	if !isPrivateIP(ip) {
		t.Error("isPrivateIP(169.254.169.254) = false, want true (metadata endpoint)")
	}
}

func TestIsPrivateIP_IPv6Loopback_ReturnsTrue(t *testing.T) {
	ip := net.ParseIP("::1")
	if !isPrivateIP(ip) {
		t.Error("isPrivateIP(::1) = false, want true")
	}
}

func TestIsPrivateIP_PublicIP_ReturnsFalse(t *testing.T) {
	publicIPs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"204.79.197.200",
		"151.101.1.140",
	}
	for _, c := range publicIPs {
		ip := net.ParseIP(c)
		if isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%q) = true, want false for public IP", c)
		}
	}
}

// ── ValidateUpstreamURL — pure validation (no DNS) ───────────────────────────

func TestValidateUpstreamURL_HTTPScheme_ReturnsError(t *testing.T) {
	// HTTP (not HTTPS) should be rejected immediately without DNS lookup.
	err := ValidateUpstreamURL("http://registry-1.docker.io")
	if err == nil {
		t.Error("expected error for HTTP scheme, got nil")
	}
}

func TestValidateUpstreamURL_InvalidURL_ReturnsError(t *testing.T) {
	err := ValidateUpstreamURL("://not-a-url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

// ── parseBearerChallenge tests ────────────────────────────────────────────────

func TestParseBearerChallenge_FullHeader_ParsesAllParams(t *testing.T) {
	header := `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/ubuntu:pull"`
	params := parseBearerChallenge(header)

	if params["realm"] != "https://auth.docker.io/token" {
		t.Errorf("realm = %q, want %q", params["realm"], "https://auth.docker.io/token")
	}
	if params["service"] != "registry.docker.io" {
		t.Errorf("service = %q, want %q", params["service"], "registry.docker.io")
	}
	if params["scope"] != "repository:library/ubuntu:pull" {
		t.Errorf("scope = %q, want %q", params["scope"], "repository:library/ubuntu:pull")
	}
}

func TestParseBearerChallenge_RealmOnly_ReturnsRealm(t *testing.T) {
	header := `Bearer realm="https://auth.example.com/token"`
	params := parseBearerChallenge(header)

	if params["realm"] != "https://auth.example.com/token" {
		t.Errorf("realm = %q, want %q", params["realm"], "https://auth.example.com/token")
	}
	if _, ok := params["service"]; ok {
		t.Error("expected no service key")
	}
}

func TestParseBearerChallenge_EmptyHeader_ReturnsEmptyMap(t *testing.T) {
	params := parseBearerChallenge("Bearer ")
	if len(params) != 0 {
		t.Errorf("expected empty map, got %v", params)
	}
}

func TestParseBearerChallenge_WithoutBearerPrefix_StillParses(t *testing.T) {
	// parseBearerChallenge trims "Bearer " — if not present, it parses the raw string.
	header := `realm="https://example.com/token"`
	params := parseBearerChallenge(header)
	if params["realm"] != "https://example.com/token" {
		t.Errorf("realm = %q, want %q", params["realm"], "https://example.com/token")
	}
}
