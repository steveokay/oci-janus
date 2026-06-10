// Package delivery implements HTTP webhook dispatch with SSRF protection and HMAC signing.
package delivery

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// privateRanges is the list of CIDRs that webhook destinations must never resolve to.
// Per CLAUDE.md §4.9: block private IPs, loopback, link-local, and cloud metadata endpoint.
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"0.0.0.0/8",
		"169.254.0.0/16", // link-local / AWS metadata (169.254.169.254)
		"100.64.0.0/10",  // shared address space (RFC 6598)
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("invalid private CIDR: " + cidr)
		}
		privateRanges = append(privateRanges, network)
	}
}

// ValidateURL checks that the destination URL is safe to deliver to:
// - scheme must be https
// - hostname must not resolve to any private/loopback/link-local IP
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

// isPrivateIP returns true if ip falls in any private range.
func isPrivateIP(ip net.IP) bool {
	for _, network := range privateRanges {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
