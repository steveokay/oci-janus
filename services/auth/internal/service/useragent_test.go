package service

import "testing"

func TestParseDeviceLabel(t *testing.T) {
	cases := []struct{ ua, want string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36", "Chrome on macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36", "Chrome on Windows"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0", "Firefox on Linux"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", "Safari on macOS"},
		{"docker/24.0.7 go/go1.21.3 git-commit/311b9ff0aa2b os/linux arch/amd64", "Docker CLI"},
		{"", "Unknown device"},
		{"curl/8.4.0", "curl/8.4.0"},
	}
	for _, c := range cases {
		if got := parseDeviceLabel(c.ua); got != c.want {
			t.Errorf("parseDeviceLabel(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}
