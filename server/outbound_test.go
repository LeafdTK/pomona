package main

import (
	"net"
	"testing"
)

// On a hosted server a reader's URL may not reach the cluster, the metadata
// service, or the server itself. On a laptop, anything goes.
func TestUserURLsCannotReachInside(t *testing.T) {
	was := hostedMode
	defer func() { hostedMode = was }()

	hostedMode = true
	for _, bad := range []string{
		"http://127.0.0.1:7777/api/health",
		"http://localhost/",
		"http://10.244.4.46/",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/",
		"http://[::1]/",
		"http://[fd00::1]/",
		"http://pomona.orchard-pomona.svc.cluster.local:7777/",
		"http://kubernetes.default.svc/",
		"http://something.internal/",
		"ftp://example.com/x.ics",
		"file:///etc/passwd",
		"http://user:pw@example.com/",
		"not a url",
	} {
		if err := checkUserURL(bad); err == nil {
			t.Errorf("%q was allowed", bad)
		}
	}
	for _, good := range []string{"https://calendar.google.com/calendar/ical/x/basic.ics", "http://example.com/feed.json"} {
		if err := checkUserURL(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}

	hostedMode = false
	if err := checkUserURL("http://127.0.0.1:7792/c.ics"); err != nil {
		t.Errorf("a local calendar on a local server was refused: %v", err)
	}
	if err := checkUserURL("file:///etc/passwd"); err == nil {
		t.Error("a file URL was allowed even locally")
	}
}

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "172.31.255.255", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fd12::1", "fe80::1", "224.0.0.1"} {
		if publicIP(net.ParseIP(s)) {
			t.Errorf("%s judged public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "142.250.72.14", "2606:4700::1111", "100.128.0.1", "172.32.0.1"} {
		if !publicIP(net.ParseIP(s)) {
			t.Errorf("%s judged private", s)
		}
	}
}
