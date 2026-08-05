package agent

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// validateURL protects against SSRF. It enforces an http(s) scheme, then
// resolves the host and requires every resolved IP to be public.
//
// Resolving is the key step a ParseIP-only check misses: it normalizes the
// non-standard IP encodings a model can craft (decimal 2130706433, hex
// 0x7f000001, octal 0177.0.0.1 — all 127.0.0.1) and reveals hostnames that
// point at private ranges. It returns the validated IPs so the caller can
// pin the dial address, which defeats DNS rebinding between validation and
// connection.
func validateURL(ctx context.Context, rawURL string) ([]net.IP, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("scheme %q not allowed (only http/https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}

	// Literal IP in a standard textual form: classify directly.
	if ip := net.ParseIP(host); ip != nil {
		if reason := classifyIP(ip); reason != "" {
			return nil, fmt.Errorf("%s host %q blocked (SSRF protection)", reason, host)
		}
		return []net.IP{ip}, nil
	}

	// Hostname or non-standard IP encoding: resolve and verify every address.
	// The resolver normalizes decimal/hex/octal IPs and surfaces any private
	// address a domain resolves to.
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ipp := range ips {
		if reason := classifyIP(ipp.IP); reason != "" {
			return nil, fmt.Errorf("%s host %q (→ %s) blocked (SSRF protection)", reason, host, ipp.IP)
		}
	}
	out := make([]net.IP, 0, len(ips))
	for _, ipp := range ips {
		out = append(out, ipp.IP)
	}
	return out, nil
}

// reservedRanges are non-public networks that net.IP's own predicates miss.
// They still reach infrastructure a fetch has no business touching: carrier
// NAT and benchmark ranges are routable inside many hosting environments, and
// 0.0.0.0/8 is a documented alias for the local host on Linux.
var reservedRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{
		"100.64.0.0/10",      // RFC 6598 carrier-grade NAT
		"198.18.0.0/15",      // RFC 2544 benchmarking
		"192.0.0.0/24",       // RFC 6890 IETF protocol assignments
		"0.0.0.0/8",          // "this network" — 0.x.y.z reaches localhost
		"255.255.255.255/32", // limited broadcast
		"::/128",             // unspecified
		"100::/64",           // RFC 6666 discard-only
	} {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// classifyIP returns a short reason for blocking the IP, or "" if it is
// acceptable. Cloud metadata endpoints (169.254.169.254) live in the
// link-local range, which is blocked here.
func classifyIP(ip net.IP) string {
	if ip.IsLoopback() {
		return "loopback"
	}
	if ip.IsPrivate() {
		return "private"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "link-local"
	}
	if ip.IsUnspecified() {
		return "unspecified"
	}
	if ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return "multicast"
	}
	for _, n := range reservedRanges {
		if n.Contains(ip) {
			return "reserved"
		}
	}
	return ""
}
