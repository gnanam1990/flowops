// Package ascprails implements the durable restricted-egress worker for the
// ASCP escrow-call/1 payment rail.
package ascprails

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnsafeDestination = errors.New("unsafe seller destination")
	ErrResolution        = errors.New("seller destination resolution failed")
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Connector interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type restrictedTransportConfig struct {
	Resolver  Resolver
	Connector Connector
}

// NewRestrictedTransport creates the only supported seller transport. URL
// validation still runs immediately before every request, while DialContext
// independently resolves again to defeat DNS rebinding between validation and
// connection. Proxies and transparent decompression are disabled.
func NewRestrictedTransport() (http.RoundTripper, error) {
	return newRestrictedTransport(restrictedTransportConfig{})
}

func newRestrictedTransport(config restrictedTransportConfig) (http.RoundTripper, error) {
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Connector == nil {
		config.Connector = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	guard := networkGuard{resolver: config.Resolver, connector: config.Connector}
	transport := &http.Transport{
		Proxy: nil, DialContext: guard.DialContext, DisableCompression: true, ForceAttemptHTTP2: true,
		MaxIdleConns: 20, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second,
		MaxResponseHeaderBytes: 64 << 10, ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &validatingTransport{resolver: config.Resolver, next: transport}, nil
}

type validatingTransport struct {
	resolver Resolver
	next     http.RoundTripper
}

func (*validatingTransport) ascpRestrictedTransport() {}

func (t *validatingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.Host != request.URL.Host || request.RequestURI != "" ||
		len(request.TransferEncoding) != 0 || len(request.Trailer) != 0 {
		return nil, ErrUnsafeDestination
	}
	canonical, err := validateDestination(request.Context(), request.URL.String(), t.resolver)
	if err != nil || canonical.String() != request.URL.String() {
		return nil, ErrUnsafeDestination
	}
	return t.next.RoundTrip(request)
}

type networkGuard struct {
	resolver  Resolver
	connector Connector
}

func (g networkGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, ErrUnsafeDestination
	}
	addresses, err := resolvePublic(ctx, g.resolver, host)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, candidate := range addresses {
		connection, dialErr := g.connector.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		failures = append(failures, dialErr)
	}
	return nil, fmt.Errorf("dial seller destination: %w", errors.Join(failures...))
}

func validateDestination(ctx context.Context, raw string, resolver Resolver) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, ErrUnsafeDestination
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, ErrUnsafeDestination
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	if port != "443" {
		return nil, ErrUnsafeDestination
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, ErrUnsafeDestination
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
			return nil, ErrUnsafeDestination
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrResolution, err)
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublic(address) {
			return nil, ErrUnsafeDestination
		}
		result = append(result, address)
	}
	return result, nil
}

var blockedRanges = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:2::/48", "2001:10::/28",
	"2001:20::/28", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16",
)

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func isPublic(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedRanges {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
