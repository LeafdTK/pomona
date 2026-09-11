package main

import (
	"testing"
	"time"
)

func board(t *testing.T) *Pairing {
	t.Helper()
	return NewPairing(nil, func([]Device) error { return nil })
}

// Adopting twice from the same browser used to mint a device each time, which
// is how sixteen test runs pushed a real browser off the end of the list.
func TestAdoptReusesTheSameBrowser(t *testing.T) {
	p := board(t)
	first, err := p.Adopt("u1", "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Adopt("u1", "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("a second adopt issued a new token: %q then %q", first, second)
	}
	if len(p.devices) != 1 {
		t.Errorf("got %d devices, want 1", len(p.devices))
	}

	// A different browser, or a different account, is a different device.
	if other, _ := p.Adopt("u1", "Firefox"); other == first {
		t.Error("a different browser got the same token")
	}
	if other, _ := p.Adopt("u2", "Chrome"); other == first {
		t.Error("a different account got the same token")
	}
	if len(p.devices) != 3 {
		t.Errorf("got %d devices, want 3", len(p.devices))
	}
}

// The device you use every morning must outlive the ones you never opened
// again, whatever order they were paired in.
func TestEvictionDropsTheStalest(t *testing.T) {
	p := board(t)

	daily, err := p.Adopt("u1", "the one I use")
	if err != nil {
		t.Fatal(err)
	}
	// Pair it early, then keep using it.
	p.devices[0].PairedAt = time.Now().Add(-72 * time.Hour)

	for i := 0; i < maxDevices; i++ {
		if _, err := p.Adopt("u1", "throwaway "+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			p.Whose(daily) // still in daily use
		}
	}

	if len(p.devices) != maxDevices {
		t.Fatalf("got %d devices, want the cap of %d", len(p.devices), maxDevices)
	}
	if p.Whose(daily) != "u1" {
		t.Error("the device in daily use was evicted in favour of ones never opened again")
	}
}

func TestUnknownTokenBelongsToNobody(t *testing.T) {
	p := board(t)
	if _, err := p.Adopt("u1", "Chrome"); err != nil {
		t.Fatal(err)
	}
	if who := p.Whose("not-a-real-token"); who != "" {
		t.Errorf("an unknown token resolved to %q", who)
	}
	if who := p.Whose(""); who != "" {
		t.Errorf("an empty token resolved to %q", who)
	}
}

func TestRevokeOthersKeepsTheOneAsking(t *testing.T) {
	p := board(t)
	mine, _ := p.Adopt("u1", "Chrome")
	p.Adopt("u1", "an old laptop")
	p.Adopt("u1", "a phone")
	theirs, _ := p.Adopt("u2", "someone else's browser")

	dropped, err := p.RevokeOthers("u1", mine)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 2 {
		t.Errorf("signed out %d devices, want 2", dropped)
	}
	if p.Whose(mine) != "u1" {
		t.Error("the browser asking signed itself out")
	}
	if p.Whose(theirs) != "u2" {
		t.Error("another account's device was signed out too")
	}
	if len(p.devices) != 2 {
		t.Errorf("got %d devices, want 2", len(p.devices))
	}
}
