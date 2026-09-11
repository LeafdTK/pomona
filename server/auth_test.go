package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A code is one use, one address, ten minutes, five guesses.
func TestOTPExpiresAndLimitsAttempts(t *testing.T) {
	o := NewOTPs()
	code, err := o.Issue("Seb@Example.com")
	if err != nil || len(code) != 6 {
		t.Fatalf("issue: %v %q", err, code)
	}
	for i := 0; i < otpAttempts; i++ {
		if err := o.Check("seb@example.com", "000000"); err == nil && code != "000000" {
			t.Fatal("a wrong code was accepted")
		}
	}
	if err := o.Check("seb@example.com", code); err == nil {
		t.Error("the right code still worked after five wrong ones")
	}

	code, _ = o.Issue("seb@example.com")
	if err := o.Check("seb@example.com", " "+code+" "); err != nil {
		t.Errorf("the right code, with whitespace, was refused: %v", err)
	}
	if err := o.Check("seb@example.com", code); err == nil {
		t.Error("a code worked twice")
	}

	code, _ = o.Issue("late@example.com")
	o.mu.Lock()
	o.codes["late@example.com"].expires = time.Now().Add(-time.Second)
	o.mu.Unlock()
	if err := o.Check("late@example.com", code); err == nil {
		t.Error("an expired code was accepted")
	}
}

// A link code belongs to the state that asked for it and dies on first use.
func TestLinkCodeIsSingleUseAndBoundToState(t *testing.T) {
	l := NewLinks()
	code, err := l.Issue("state-abcdefghijklmnop", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Claim(code, "some-other-state-xxxx"); err == nil {
		t.Error("a code was redeemed with the wrong state")
	}
	code, _ = l.Issue("state-abcdefghijklmnop", "user1")
	user, err := l.Claim(code, "state-abcdefghijklmnop")
	if err != nil || user != "user1" {
		t.Errorf("claim = %q, %v", user, err)
	}
	if _, err := l.Claim(code, "state-abcdefghijklmnop"); err == nil {
		t.Error("a code was redeemed twice")
	}
}

// Only Chrome's auth-flow origin or loopback may receive a link code.
func TestRedirectURIMustBeChromiumApp(t *testing.T) {
	good := []string{
		"https://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org/link",
		"https://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org/",
		"http://127.0.0.1:7777/link",
		"http://localhost/x",
	}
	bad := []string{
		"https://evil.example/abcdefghijklmnopabcdefghijklmnop.chromiumapp.org/",
		"https://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org.evil.example/",
		"https://ABCDEFGHIJKLMNOPABCDEFGHIJKLMNOP.chromiumapp.org/",
		"http://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org/",
		"https://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org/#frag",
		"http://10.0.0.5/",
		"",
	}
	for _, u := range good {
		if !validLinkRedirect(u) {
			t.Errorf("%q should be allowed", u)
		}
	}
	for _, u := range bad {
		if validLinkRedirect(u) {
			t.Errorf("%q should be refused", u)
		}
	}
}

func TestLimiterSlidesItsWindow(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allowAt("k", 3, time.Minute, now) {
			t.Fatalf("call %d refused", i)
		}
	}
	if l.allowAt("k", 3, time.Minute, now.Add(10*time.Second)) {
		t.Error("a fourth call inside the window was allowed")
	}
	if !l.allowAt("k", 3, time.Minute, now.Add(61*time.Second)) {
		t.Error("the window did not slide")
	}
	if !l.allowAt("other", 3, time.Minute, now) {
		t.Error("keys leak into each other")
	}
}

// The brief starts before the ready-by time, so it exists when the hour
// arrives, and never before midnight of its own day.
func TestDueNowFiresBeforeReadyBy(t *testing.T) {
	cfg := defaultConfig()
	cfg.Profile.Timezone = "America/Denver"
	cfg.Schedule.Time = "07:00"
	loc, _ := time.LoadLocation("America/Denver")
	at := func(h, m int) time.Time { return time.Date(2026, 9, 10, h, m, 0, 0, loc) }

	if fire, _ := dueNow(cfg, at(6, 30), ""); fire {
		t.Error("fired half an hour early")
	}
	if fire, _ := dueNow(cfg, at(6, 41), ""); !fire {
		t.Error("did not fire twenty minutes before ready-by")
	}
	if fire, _ := dueNow(cfg, at(9, 15), ""); !fire {
		t.Error("a late morning should still catch up")
	}
	if fire, _ := dueNow(cfg, at(22, 20), ""); fire {
		t.Error("an account made at ten in the evening had today's brief written on the spot")
	}
	cfg.Schedule.Time = "00:10"
	// Yesterday's is written; tonight must not start tomorrow's early.
	if fire, _ := dueNow(cfg, time.Date(2026, 9, 9, 23, 55, 0, 0, loc), "2026-09-09"); fire {
		t.Error("a ten-past-midnight brief started the evening before")
	}
	if fire, _ := dueNow(cfg, time.Date(2026, 9, 10, 0, 1, 0, 0, loc), ""); !fire {
		t.Error("a ten-past-midnight brief did not start at midnight")
	}
}

// The server's own pages, served over a real hostname, must pass CORS.
func TestCORSAllowsOwnHost(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := cors(inner)
	try := func(origin, host string) string {
		req := httptest.NewRequest("OPTIONS", "http://"+host+"/api/config", nil)
		req.Host = host
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Access-Control-Allow-Origin")
	}
	if got := try("https://pomona.leafd.dev", "pomona.leafd.dev"); got != "https://pomona.leafd.dev" {
		t.Errorf("own host refused: %q", got)
	}
	if got := try("https://evil.example", "pomona.leafd.dev"); got != "" {
		t.Errorf("a stranger's origin was allowed: %q", got)
	}
	if got := try("chrome-extension://abc", "pomona.leafd.dev"); got != "chrome-extension://abc" {
		t.Errorf("the extension was refused: %q", got)
	}
}

// POMONA_VAULT_KEY keeps the key off the data volume.
func TestVaultKeyFromEnv(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	t.Setenv("POMONA_VAULT_KEY", base64.StdEncoding.EncodeToString(key))
	v := OpenVault(filepath.Join(dir, "vault.json"))
	if err := v.OpenAuto(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vault.json.key")); err == nil {
		t.Error("a key file was written even though the key came from the environment")
	}
	if err := v.SealTo(filepath.Join(dir, "x.enc"), "p", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// A second process with the same env reads it back; one without cannot.
	again := OpenVault(filepath.Join(dir, "vault.json"))
	_ = again.OpenAuto()
	if got, err := again.OpenFrom(filepath.Join(dir, "x.enc"), "p"); err != nil || string(got) != "hello" {
		t.Errorf("same key did not read back: %q %v", got, err)
	}
	t.Setenv("POMONA_VAULT_KEY", "not base64!")
	if err := OpenVault(filepath.Join(dir, "vault.json")).OpenAuto(); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Errorf("a bad key was accepted: %v", err)
	}
}

// An account made by Slack has no email and no password, and is found by its
// identity alone. An emailed-code account keeps no usable password either.
func TestIdentityAccounts(t *testing.T) {
	dir := t.TempDir()
	v := OpenVault(filepath.Join(dir, "vault.json"))
	if err := v.OpenAuto(); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir, v)

	a, err := s.CreateIdentityUser("slack:T1:U1", "seb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIdentityUser("slack:T1:U1", "again"); err == nil {
		t.Error("the same identity made two accounts")
	}
	if got := s.UserByIdentity("slack:T1:U1"); got == nil || got.ID != a.ID {
		t.Error("identity lookup failed")
	}
	if s.UserByEmail("") != nil {
		t.Error("a blank email matched an identity account")
	}
	if a.Matches("") || a.Matches("anything") {
		t.Error("an identity account has a password path")
	}

	b, err := s.CreateIdentityUser("slack:T1:U2", "reem")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(a.ID); err != nil {
		t.Fatal(err)
	}
	if s.UserByID(a.ID) != nil || s.UserByID(b.ID) == nil {
		t.Error("delete removed the wrong account")
	}
	if _, err := os.Stat(filepath.Join(dir, "users", a.ID)); err == nil {
		t.Error("the deleted account's directory survived")
	}
}
