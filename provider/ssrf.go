package provider

import (
	"fmt"
	"net"
	"net/url"
)

// ValidateBaseURL checks that a caller-supplied base URL is safe to contact:
//   - Empty string is always allowed (callers fall back to the default URL).
//   - Scheme must be "https".
//   - Resolved IP must not be loopback, link-local, or RFC 1918 private.
func ValidateBaseURL(rawURL string) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("base_url must use https scheme, got %q", u.Scheme)
	}
	hostname := u.Hostname()
	if hostname == "" {
		return fmt.Errorf("base_url has no hostname")
	}

	// If the hostname is already an IP literal, check it directly without DNS.
	if ip := net.ParseIP(hostname); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("base_url points to a private/internal address: %s", hostname)
		}
		return nil
	}

	// Resolve via DNS and reject if any returned address is private.
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return fmt.Errorf("base_url hostname could not be resolved: %w", err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && isPrivateIP(ip) {
			return fmt.Errorf("base_url resolves to a private/internal address: %s", addr)
		}
	}
	return nil
}

// privateCIDRs lists all private/internal IP ranges that should never be
// contacted via a user-supplied URL.
var privateCIDRs = func() []*net.IPNet {
	ranges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16", // link-local (AWS metadata, etc.)
		"127.0.0.0/8",    // loopback
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local
	}
	var nets []*net.IPNet
	for _, r := range ranges {
		_, n, _ := net.ParseCIDR(r)
		if n != nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

func isPrivateIP(ip net.IP) bool {
	for _, network := range privateCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
