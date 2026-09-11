package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")

	v := OpenVault(path)
	if !v.Locked() {
		t.Fatal("a fresh vault should be locked")
	}
	if err := v.Create("correct horse battery"); err != nil {
		t.Fatal(err)
	}

	secret := []byte(`{"token":"xoxp-super-secret"}`)
	blob := filepath.Join(dir, "config.enc")
	if err := v.SealTo(blob, "config", secret); err != nil {
		t.Fatal(err)
	}

	// On disk it must not contain the plaintext anywhere.
	onDisk, _ := os.ReadFile(blob)
	if bytes.Contains(onDisk, []byte("xoxp")) {
		t.Fatal("the token is readable on disk")
	}

	// A cold server can't read it.
	cold := OpenVault(path)
	if _, err := cold.OpenFrom(blob, "config"); err != ErrLocked {
		t.Fatalf("locked vault should refuse, got %v", err)
	}
	if err := cold.Unlock("wrong passphrase"); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("expected a wrong-passphrase error, got %v", err)
	}
	if err := cold.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	got, err := cold.OpenFrom(blob, "config")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("round trip failed: %v %s", err, got)
	}

	// A different purpose must not decrypt it.
	if _, err := cold.OpenFrom(blob, "briefs"); err == nil {
		t.Fatal("a subkey for another purpose decrypted the config")
	}

	cold.Lock()
	if !cold.Locked() {
		t.Fatal("Lock() left it unlocked")
	}
}

func TestPairing(t *testing.T) {
	p := NewPairing(nil, func([]Device) error { return nil })

	if _, err := p.Claim("123456", "x"); err == nil {
		t.Fatal("claiming with no code outstanding should fail")
	}

	code, expires, err := p.Begin("user1")
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 || time.Until(expires) > codeTTL+time.Second {
		t.Fatalf("bad code %q / expiry %v", code, expires)
	}

	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	for i := 0; i < maxAttempts; i++ {
		if _, err := p.Claim(wrong, "x"); err == nil {
			t.Fatal("wrong code accepted")
		}
	}
	if _, err := p.Claim(code, "x"); err == nil {
		t.Fatal("the code should be dead after too many attempts")
	}

	code, _, _ = p.Begin("user1")
	token, err := p.Claim(code, "Chrome")
	if err != nil || len(token) != tokenBytes*2 {
		t.Fatalf("claim failed: %v %q", err, token)
	}
	if p.Whose(token) != "user1" {
		t.Fatal("a paired token wasn't recognised")
	}
	if p.Whose("not-a-token") != "" || p.Whose("") != "" {
		t.Fatal("recognised a token it never issued")
	}
	if _, err := p.Claim(code, "again"); err == nil {
		t.Fatal("a code was reusable")
	}
	for _, d := range p.DevicesFor("user1") {
		if d.Token != "" {
			t.Fatal("Devices() leaked a token")
		}
	}
}

// Two accounts on one server must not be able to see each other, on disk or
// through a token.
func TestAccountsAreSeparate(t *testing.T) {
	dir := t.TempDir()
	vault := OpenVault(filepath.Join(dir, "vault.json"))
	if err := vault.OpenAuto(); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir, vault)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	alice, err := store.CreateUser("alice@example.com", "Alice", "alice's password")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.CreateUser("bob@example.com", "Bob", "bob's password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser("ALICE@example.com", "", "another"); err == nil {
		t.Fatal("the same email was allowed twice")
	}

	// Each account's config is its own.
	aliceCfg := store.For(alice.ID).Config()
	aliceCfg.Profile.Name = "Alice"
	aliceCfg.Sources = map[string]map[string]string{"slack": {"enabled": "true", "token": "xoxp-alice-secret"}}
	if err := store.For(alice.ID).SetConfig(aliceCfg); err != nil {
		t.Fatal(err)
	}
	if got := store.For(bob.ID).Config().Profile.Name; got != "" {
		t.Fatalf("Bob can see Alice's profile: %q", got)
	}

	// And each account's briefs.
	if err := store.For(alice.ID).SaveBrief(&Brief{ID: "2026-09-08", Data: []byte(`{"header":{"greeting":"alice only"}}`)}); err != nil {
		t.Fatal(err)
	}
	if briefs, _ := store.For(bob.ID).Briefs(); len(briefs) != 0 {
		t.Fatalf("Bob can see %d of Alice's briefs", len(briefs))
	}

	// Alice's Slack token must not be readable with Bob's key.
	blob := filepath.Join(dir, "users", alice.ID, "config.enc")
	if _, err := vault.OpenFrom(blob, "user/"+bob.ID+"/config"); err == nil {
		t.Fatal("Bob's key decrypted Alice's config")
	}
	onDisk, _ := os.ReadFile(blob)
	if bytes.Contains(onDisk, []byte("xoxp-alice-secret")) {
		t.Fatal("a Slack token is readable on disk")
	}

	// Passwords.
	if !alice.Matches("alice's password") || alice.Matches("bob's password") {
		t.Fatal("password checking is wrong")
	}

	// A token names exactly one account.
	pairing := NewPairing(nil, store.SaveDevices)
	aliceToken, _ := pairing.Adopt(alice.ID, "Chrome")
	bobToken, _ := pairing.Adopt(bob.ID, "Firefox")
	if pairing.Whose(aliceToken) != alice.ID || pairing.Whose(bobToken) != bob.ID {
		t.Fatal("tokens resolve to the wrong account")
	}
	if len(pairing.DevicesFor(alice.ID)) != 1 {
		t.Fatal("Alice sees the wrong number of devices")
	}
	if err := pairing.Revoke(aliceToken); err != nil {
		t.Fatal(err)
	}
	if pairing.Whose(aliceToken) != "" {
		t.Fatal("a revoked token still works")
	}
	if pairing.Whose(bobToken) != bob.ID {
		t.Fatal("revoking one device logged out another")
	}
}
