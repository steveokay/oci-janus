// Package service — useragent.go: a tiny, dependency-free User-Agent classifier
// that turns a raw UA string into a short human label for the session list
// ("Chrome on macOS", "Docker CLI"). It is deliberately coarse: browser family
// + OS family is all the session UI needs, and a heuristic parser avoids adding
// a UA-parsing dependency. The raw UA is stored alongside for the tooltip.
package service

import "strings"

// parseDeviceLabel returns a short "<client> on <os>" label, or a coarse
// fallback. It never returns an empty string.
func parseDeviceLabel(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return "Unknown device"
	}
	// Non-browser clients first (their UAs don't carry an OS token we surface).
	if strings.HasPrefix(ua, "docker/") || strings.Contains(ua, "docker/") {
		return "Docker CLI"
	}

	client := browserFamily(ua)
	os := osFamily(ua)
	if client == "" {
		// Unknown but non-empty: show the leading token (e.g. "curl/8.4.0"),
		// trimmed, so the row is still identifiable without dumping the whole UA.
		if i := strings.IndexByte(ua, ' '); i > 0 {
			return ua[:i]
		}
		return ua
	}
	if os == "" {
		return client
	}
	return client + " on " + os
}

// browserFamily returns the browser name, or "" if not a recognised browser.
// Order matters: Edge/Chrome both contain "Chrome"; Chrome contains "Safari".
func browserFamily(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		return "Safari"
	default:
		return ""
	}
}

// osFamily returns the OS name, or "" if not recognised.
func osFamily(ua string) string {
	switch {
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "macOS"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		return "iOS"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "Linux"
	default:
		return ""
	}
}
