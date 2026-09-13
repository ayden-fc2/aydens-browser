// Package webguard is the browser's sole egress path. It resolves and validates
// destinations, then pins the upstream proxy connection to that public IP.
package webguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var deniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

type Proxy struct {
	Upstream *url.URL
	Deny     map[string]bool
	Lookup   func(context.Context, string) ([]netip.Addr, error)
}

func New(upstream, deny string) (*Proxy, error) {
	u, e := url.Parse(upstream)
	if e != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("SEARCH_PROXY_URL must be an HTTP proxy without credentials")
	}
	p := &Proxy{Upstream: u, Deny: map[string]bool{}, Lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
	for _, ip := range strings.Split(deny, ",") {
		p.Deny[strings.TrimSpace(ip)] = true
	}
	return p, nil
}
func (p *Proxy) destination(ctx context.Context, address string) (string, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil || (port != "80" && port != "443") {
		return "", errors.New("only public HTTP/HTTPS allowed")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return "", errors.New("local hostname forbidden")
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".arpa"} {
		if strings.HasSuffix(host, suffix) {
			return "", errors.New("local hostname forbidden")
		}
	}
	ips, e := p.Lookup(ctx, host)
	if e != nil || len(ips) == 0 {
		return "", errors.New("DNS resolution failed")
	}
	for _, ip := range ips {
		if !PublicIP(ip) || p.Deny[ip.Unmap().String()] {
			return "", errors.New("nonpublic destination forbidden")
		}
	}
	// Prefer IPv4 on NAS. Never ask the upstream proxy to resolve the hostname again.
	chosen := ips[0]
	for _, ip := range ips {
		if ip.Is4() {
			chosen = ip
			break
		}
	}
	return net.JoinHostPort(chosen.String(), port), nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
func (p *Proxy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	pinned, e := p.destination(ctx, address)
	if e != nil {
		return nil, e
	}
	proxyAddr := p.Upstream.Host
	if p.Upstream.Port() == "" {
		proxyAddr = net.JoinHostPort(p.Upstream.Hostname(), "80")
	}
	c, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
	if e != nil {
		return nil, e
	}
	c.SetDeadline(time.Now().Add(40 * time.Second))
	if _, e = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", pinned, pinned); e != nil {
		c.Close()
		return nil, e
	}
	reader := bufio.NewReader(c)
	r, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if e != nil {
		c.Close()
		return nil, e
	}
	if r.StatusCode != 200 {
		c.Close()
		return nil, errors.New("egress unavailable")
	}
	return bufferedConn{c, reader}, nil
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	if r.Method == "CONNECT" {
		_, port, e := net.SplitHostPort(r.Host)
		if e != nil || port != "443" {
			http.Error(w, "forbidden", 403)
			return
		}
		upstream, e := p.dial(ctx, "tcp", r.Host)
		if e != nil {
			http.Error(w, "destination unavailable or forbidden", 403)
			return
		}
		defer upstream.Close()
		h, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "unavailable", 500)
			return
		}
		client, buffer, e := h.Hijack()
		if e != nil {
			return
		}
		defer client.Close()
		client.SetDeadline(time.Now().Add(40 * time.Second))
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if buffer.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffer); upstream.Close(); close(done) }()
		io.Copy(client, upstream)
		client.Close()
		<-done
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "read only", 405)
		return
	}
	if r.URL.Scheme != "http" || r.URL.User != nil {
		http.Error(w, "forbidden", 403)
		return
	}
	transport := &http.Transport{Proxy: nil, DialContext: p.dial, DisableKeepAlives: true, ResponseHeaderTimeout: 20 * time.Second}
	defer transport.CloseIdleConnections()
	req := r.Clone(ctx)
	req.RequestURI = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	res, e := transport.RoundTrip(req)
	if e != nil {
		http.Error(w, "destination unavailable or forbidden", 403)
		return
	}
	defer res.Body.Close()
	for k, v := range res.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(res.StatusCode)
	io.Copy(w, io.LimitReader(res.Body, 8<<20))
}
