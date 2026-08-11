package evidencefetch

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, errors.New("host not found")
	}
	return append([]netip.Addr(nil), addresses...), nil
}

func TestValidateURLRejectsSSRFAndAmbiguousTargets(t *testing.T) {
	t.Parallel()
	resolver := staticResolver{
		"public.example":  {netip.MustParseAddr("8.8.8.8")},
		"mixed.example":   {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")},
		"private.example": {netip.MustParseAddr("192.168.1.4")},
	}
	tests := []struct {
		name string
		url  string
		code Code
	}{
		{"loopback IPv4", "http://127.0.0.1/", CodeUnsafeURL},
		{"loopback IPv6", "http://[::1]/", CodeUnsafeURL},
		{"private DNS", "https://private.example/", CodeUnsafeURL},
		{"mixed DNS", "https://mixed.example/", CodeUnsafeURL},
		{"link local metadata", "http://169.254.169.254/latest/meta-data", CodeUnsafeURL},
		{"CGNAT", "http://100.64.0.1/", CodeUnsafeURL},
		{"unspecified", "http://0.0.0.0/", CodeUnsafeURL},
		{"documentation range", "http://192.0.2.1/", CodeUnsafeURL},
		{"credentials", "https://user:secret@public.example/", CodeUnsafeURL},
		{"non-default port", "https://public.example:8443/", CodeUnsafeURL},
		{"unsupported scheme", "file:///etc/passwd", CodeUnsafeURL},
		{"fragment", "https://public.example/path#part", CodeInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateURL(context.Background(), test.url, resolver)
			if err == nil || ErrorCode(err) != test.code {
				t.Fatalf("validateURL() error = %v, want code %s", err, test.code)
			}
		})
	}
}

func TestValidateURLAllowsPublicDefaultPorts(t *testing.T) {
	t.Parallel()
	resolver := staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}
	for _, raw := range []string{"http://public.example/path", "http://public.example:80/path", "https://public.example/path", "https://public.example:443/path"} {
		if _, err := validateURL(context.Background(), raw, resolver); err != nil {
			t.Fatalf("validateURL(%q) error = %v", raw, err)
		}
	}
}

func TestPublicAddressClassification(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true,
		"10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false,
		"127.0.0.1": false, "169.254.1.1": false, "::1": false,
		"fc00::1": false, "fe80::1": false, "2001:db8::1": false,
	}
	for raw, expected := range tests {
		if actual := isPublic(netip.MustParseAddr(raw)); actual != expected {
			t.Errorf("isPublic(%s) = %v, want %v", raw, actual, expected)
		}
	}
}
