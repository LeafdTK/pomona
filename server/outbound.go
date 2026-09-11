package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fetching a URL somebody typed.
//
// A custom source or a calendar link is an address the reader chose, and on
// a hosted server the reader is a stranger. Left alone, "fetch this URL for
// me" reaches everything the pod can: the cluster's other services, the
// cloud metadata endpoint, this very server on loopback. So every hop of
// such a fetch dials through a check that refuses private, loopback and
// link-local addresses, after resolution, which is the only place a DNS
// name can be judged.
//
// On a laptop the guard is off: a calendar served from localhost is a normal
// thing to have, and anything on that machine is already yours.

// userClient is for URLs the reader supplied. The first-party clients (Slack,
// GitHub, Anthropic, the museum) keep the plain one.
var userClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:         guardedDial,
		MaxIdleConns:        8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return checkUserURL(req.URL.String())
	},
}

// checkUserURL is the cheap, pre-dial part: scheme and shape.
func checkUserURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("that isn't a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https can be read, not %s", u.Scheme)
	}
	if u.User != nil {
		return errors.New("a URL with a username in it can't be read")
	}
	if hostedMode && !publicName(u.Hostname()) {
		return fmt.Errorf("%s isn't a public address", u.Hostname())
	}
	return nil
}

// guardedDial resolves the name, refuses anything that is not a public
// address, and connects to the first that is. Checking after resolution is
// what stops a public name that points at 10.0.0.1.
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if !hostedMode {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	if !publicName(host) {
		return nil, fmt.Errorf("%s isn't a public address", host)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, fmt.Errorf("%s resolves to a private address", host)
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var last error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

// publicName is the name-level refusal: the cluster's own suffixes, and
// literal addresses that are not public.
func publicName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") ||
		strings.HasSuffix(h, ".cluster.local") || strings.HasSuffix(h, ".svc") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return publicIP(ip)
	}
	return true
}

// publicIP refuses loopback, private, link-local, multicast, unspecified,
// the IPv6 unique-local range and the cloud metadata address.
func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// Carrier-grade NAT and the metadata address family.
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return false
		}
		if v4[0] == 169 && v4[1] == 254 {
			return false
		}
		return true
	}
	// IPv4-mapped IPv6 was handled above by To4; ULA fc00::/7 is IsPrivate.
	return true
}
