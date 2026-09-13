package webguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.18.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "0.0.0.0", "224.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::7f00:1", "2001:db8::1"} {
		if PublicIP(netip.MustParseAddr(raw)) {
			t.Error("accepted", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicIP(netip.MustParseAddr(raw)) {
			t.Error("rejected", raw)
		}
	}
}
func TestDNSMixedAnswersAndRebinding(t *testing.T) {
	p, _ := New("http://proxy:7890", "43.160.204.72")
	ips := []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	calls := 0
	p.Lookup = func(context.Context, string) ([]netip.Addr, error) { calls++; return ips, nil }
	target, e := p.destination(context.Background(), "example.com:443")
	if e != nil || target != "1.1.1.1:443" {
		t.Fatal(target, e)
	}
	ips = append(ips, netip.MustParseAddr("127.0.0.1"))
	if _, e = p.destination(context.Background(), "example.com:443"); e == nil {
		t.Fatal("mixed DNS answers accepted")
	}
	ips = []netip.Addr{netip.MustParseAddr("43.160.204.72")}
	if _, e = p.destination(context.Background(), "example.com:443"); e == nil {
		t.Fatal("VPS address accepted")
	}
	for _, target := range []string{"localhost:443", "service.internal:443", "example.com:22", "metadata.google.internal:80"} {
		if _, e = p.destination(context.Background(), target); e == nil {
			t.Fatal(target)
		}
	}
	if calls != 3 {
		t.Fatal("unexpected resolution", calls)
	}
}
func TestProxyPinsIPBeforeForwarding(t *testing.T) {
	var destination string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destination = r.Host
		client, buffer, _ := w.(http.Hijacker).Hijack()
		defer client.Close()
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
	}))
	defer upstream.Close()
	p, _ := New(upstream.URL, "")
	p.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	c, e := p.dial(context.Background(), "tcp", "example.com:443")
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if destination != "1.1.1.1:443" {
		t.Fatal("hostname re-resolved by upstream", destination)
	}
}
func TestProxyRejectsPrivateRequests(t *testing.T) {
	p, _ := New("http://127.0.0.1:1", "")
	p.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	for _, target := range []string{"http://127.0.0.1/", "http://example.com/"} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "http://example.com/", strings.NewReader("x")))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
