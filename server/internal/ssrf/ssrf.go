package ssrf

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateURL ensures only http(s) and optionally blocks private networks.
func ValidateURL(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme %q not allowed", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("missing hostname")
	}
	if !allowPrivate {
		if err := assertPublicHost(host); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func assertPublicHost(host string) error {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("private host blocked")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no ips for host")
	}
	for _, ip := range ips {
		if isPrivateOrLocal(ip) {
			return fmt.Errorf("private ip blocked: %s", ip)
		}
	}
	return nil
}

func isPrivateOrLocal(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// Extra CGNAT / metadata ranges
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}

// SanitizeUserAgent strips control chars and truncates.
func SanitizeUserAgent(ua string) string {
	ua = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, ua)
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "P2PPlayer/1.0"
	}
	if len(ua) > 256 {
		return ua[:256]
	}
	return ua
}
