package evidencefetch

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Connector interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type networkGuard struct {
	resolver  Resolver
	connector Connector
}

var blockedRanges = mustPrefixes(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"100::/64",
	"2001:db8::/32",
)

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}

func validateURL(ctx context.Context, raw string, resolver Resolver) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, newError(CodeInvalidRequest, "url must contain 1 to 2048 characters", nil)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, newError(CodeInvalidRequest, "url is malformed", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, newError(CodeUnsafeURL, "only http and https URLs are allowed", nil)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, newError(CodeInvalidRequest, "url must include a host", nil)
	}
	if parsed.User != nil {
		return nil, newError(CodeUnsafeURL, "url credentials are not allowed", nil)
	}
	if parsed.Fragment != "" {
		return nil, newError(CodeInvalidRequest, "url fragments are not allowed", nil)
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	if (parsed.Scheme == "http" && port != "80") || (parsed.Scheme == "https" && port != "443") {
		return nil, newError(CodeUnsafeURL, "only the default port for the URL scheme is allowed", nil)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, newError(CodeInvalidRequest, "url port is invalid", err)
	}
	if _, err := resolvePublic(ctx, resolver, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolvePublic(ctx context.Context, resolver Resolver, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		literal = literal.Unmap()
		if !isPublic(literal) {
			return nil, newError(CodeUnsafeURL, "host resolves to a non-public address", nil)
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, newError(CodeResolutionFailed, "host resolution failed", err)
	}
	if len(addresses) == 0 {
		return nil, newError(CodeResolutionFailed, "host did not resolve to an address", nil)
	}
	public := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublic(address) {
			return nil, newError(CodeUnsafeURL, "host resolves to a non-public address", nil)
		}
		public = append(public, address)
	}
	return public, nil
}

func isPublic(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedRanges {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (g networkGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, newError(CodeUnsafeURL, "upstream address is invalid", err)
	}
	addresses, err := resolvePublic(ctx, g.resolver, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, candidate := range addresses {
		connection, dialErr := g.connector.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("dial public upstream: %w", lastErr)
}
