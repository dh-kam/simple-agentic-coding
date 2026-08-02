package agent

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// validateURL protects against SSRF by blocking private IPs and non-http(s) schemes.
func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (only http/https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if isPrivateHost(host) {
		return fmt.Errorf("private/loopback host %q blocked (SSRF protection)", host)
	}
	return nil
}

func isPrivateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	// Hostnames like "localhost" or "*.internal"
	host = strings.ToLower(host)
	blocked := []string{"localhost", "metadata.google.internal", "metadata"}
	for _, b := range blocked {
		if host == b {
			return true
		}
	}
	return false
}
