package ascprails

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	v, ok := r[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]netip.Addr(nil), v...), nil
}

type sequenceResolver struct {
	values [][]netip.Addr
	calls  int
}

func (r *sequenceResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	i := r.calls
	r.calls++
	if i >= len(r.values) {
		i = len(r.values) - 1
	}
	return append([]netip.Addr(nil), r.values[i]...), nil
}

type countingConnector struct{ calls int }

func (c *countingConnector) DialContext(context.Context, string, string) (net.Conn, error) {
	c.calls++
	return nil, errors.New("dial blocked in test")
}

func TestDestinationRejectsSSRFAndAmbiguousRoutes(t *testing.T) {
	resolver := staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}, "private.example": {netip.MustParseAddr("10.0.0.1")}, "mixed.example": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}}
	bad := []string{"http://public.example/", "https://private.example/", "https://mixed.example/", "https://127.0.0.1/", "https://169.254.169.254/latest/meta-data", "https://user:pass@public.example/", "https://public.example:8443/", "https://public.example/path#fragment", "file:///etc/passwd", "https://public.example/" + strings.Repeat("a", maxDestinationURLBytes)}
	for _, raw := range bad {
		if _, err := validateDestination(context.Background(), raw, resolver); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if parsed, err := validateDestination(context.Background(), "https://public.example/path", resolver); err != nil || parsed.String() != "https://public.example/path" {
		t.Fatalf("public destination=%v err=%v", parsed, err)
	}
}

func TestDialRevalidatesDNSAndNeverConnectsAfterRebind(t *testing.T) {
	resolver := &sequenceResolver{values: [][]netip.Addr{{netip.MustParseAddr("8.8.8.8")}, {netip.MustParseAddr("127.0.0.1")}}}
	connector := &countingConnector{}
	if _, err := validateDestination(context.Background(), "https://seller.example/path", resolver); err != nil {
		t.Fatal(err)
	}
	guard := networkGuard{resolver: resolver, connector: connector}
	if _, err := guard.DialContext(context.Background(), "tcp", "seller.example:443"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
	if connector.calls != 0 {
		t.Fatalf("connector calls=%d", connector.calls)
	}
}

func TestValidatingTransportRejectsHostOverrideBeforeNetwork(t *testing.T) {
	connector := &countingConnector{}
	transport, err := newRestrictedTransport(restrictedTransportConfig{Resolver: staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://public.example/pay", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "attacker.example"
	if _, err := transport.RoundTrip(request); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
	if connector.calls != 0 {
		t.Fatalf("connector calls=%d", connector.calls)
	}
}

func TestValidatingTransportRejectsAlternateRoutingSurfaces(t *testing.T) {
	for _, mutate := range []func(*http.Request){
		func(request *http.Request) { request.RequestURI = "/alternate" },
		func(request *http.Request) { request.TransferEncoding = []string{"chunked"} },
		func(request *http.Request) { request.Trailer = http.Header{"X-Route": {"alternate"}} },
	} {
		connector := &countingConnector{}
		transport, err := newRestrictedTransport(restrictedTransportConfig{Resolver: staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}, Connector: connector})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://public.example/pay", nil)
		if err != nil {
			t.Fatal(err)
		}
		mutate(request)
		if _, err := transport.RoundTrip(request); !errors.Is(err, ErrUnsafeDestination) {
			t.Fatalf("error=%v", err)
		}
		if connector.calls != 0 {
			t.Fatalf("connector calls=%d", connector.calls)
		}
	}
}

func TestPublicAddressClassification(t *testing.T) {
	values := map[string]bool{"8.8.8.8": true, "2606:4700:4700::1111": true, "10.0.0.1": false, "100.64.0.1": false,
		"192.0.2.1": false, "127.0.0.1": false, "169.254.1.1": false, "::1": false, "fc00::1": false,
		"64:ff9b::a00:1": false, "2001::1": false, "2001:db8::1": false, "2002:0808:0808::1": false,
		"fec0::1": false}
	for raw, want := range values {
		if got := isPublic(netip.MustParseAddr(raw)); got != want {
			t.Errorf("isPublic(%s)=%v want %v", raw, got, want)
		}
	}
}

func TestProductionRestrictedTransportHasUnforgeableMarker(t *testing.T) {
	transport, err := NewRestrictedTransport()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := transport.(interface{ ascpRestrictedTransport() }); !ok {
		t.Fatal("production transport lacks restricted marker")
	}
}
