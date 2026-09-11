package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// Pairing a browser to the server, the way you pair a phone to a television:
// the server shows a six digit code, you type it into the extension once, and
// the extension keeps a long device token from then on.
//
// Six digits is only a million possibilities, which is not much on its own, so
// the code lives for two minutes, dies after five wrong guesses, and only one
// is outstanding at a time.

const (
	codeTTL     = 2 * time.Minute
	maxAttempts = 5
	tokenBytes  = 32
	maxDevices  = 16
)

type Device struct {
	Name     string    `json:"name"`
	Token    string    `json:"token"`
	UserID   string    `json:"userId"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen"`
}

// A code outstanding for one account. Each account has at most one, and
// nobody's code can touch anybody else's: a shared slot let a stranger burn
// the attempts on whoever's code was live.
type pairCode struct {
	code     string
	expires  time.Time
	attempts int
}

type Pairing struct {
	mu    sync.Mutex
	codes map[string]*pairCode // by account

	devices []Device
	save    func([]Device) error
}

func NewPairing(devices []Device, save func([]Device) error) *Pairing {
	return &Pairing{devices: devices, save: save, codes: map[string]*pairCode{}}
}

// Begin mints a code for one account, to show the person who owns it. Any
// previous code of theirs stops working.
func (p *Pairing) Begin(userID string) (string, time.Time, error) {
	code, err := sixDigits()
	if err != nil {
		return "", time.Time{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.codes {
		if time.Now().After(c.expires) {
			delete(p.codes, id)
		}
	}
	entry := &pairCode{code: code, expires: time.Now().Add(codeTTL)}
	p.codes[userID] = entry
	return code, entry.expires, nil
}

// Adopt issues a token without a code. Only ever called for connections that
// arrived over loopback, where anything able to reach us could already read the
// data directory, so a code would be ceremony rather than security.
//
// The same browser asking twice gets the same device back rather than a new
// one. Minting one per call meant a browser that lost its token could push
// every other device off the end of the list, which is a strange way to be
// signed out of your own machine. Sharing a device between two local browsers
// of the same name is not a weakening: on loopback they already have equal
// access to everything the token protects.
func (p *Pairing) Adopt(userID, name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range p.devices {
		if p.devices[i].UserID == userID && p.devices[i].Name == deviceLabel(name) {
			p.devices[i].LastSeen = time.Now()
			token := p.devices[i].Token
			if err := p.save(p.devices); err != nil {
				return "", err
			}
			return token, nil
		}
	}
	return p.issueLocked(userID, name)
}

// Claim exchanges a correct code for a device token, once, on the account
// the code was minted for. Every live code is compared, in constant time,
// and a wrong guess counts against every one of them: a guesser learns
// nothing about which accounts have codes out.
func (p *Pairing) Claim(code, name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	matched := ""
	live := 0
	for userID, c := range p.codes {
		if time.Now().After(c.expires) || c.attempts >= maxAttempts {
			delete(p.codes, userID)
			continue
		}
		live++
		if subtle.ConstantTimeCompare([]byte(code), []byte(c.code)) == 1 {
			matched = userID
		}
	}
	if live == 0 {
		return "", errors.New("that code has expired: ask for a new one")
	}
	if matched == "" {
		left := maxAttempts
		for _, c := range p.codes {
			c.attempts++
			if maxAttempts-c.attempts < left {
				left = maxAttempts - c.attempts
			}
		}
		return "", fmt.Errorf("that code isn't right (%d attempts left)", left)
	}

	// Correct: burn the code before doing anything else.
	delete(p.codes, matched)
	return p.issueLocked(matched, name)
}

// Issue mints a fresh token for a sign-in that proved itself off this
// machine. Never a reused one: a token that came back on every sign-in
// could never be rotated, and two strangers' browsers both called "Chrome"
// would share it.
func (p *Pairing) Issue(userID, name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.issueLocked(userID, name)
}

func deviceLabel(name string) string {
	if name == "" {
		return "A browser"
	}
	return name
}

func (p *Pairing) issueLocked(userID, name string) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	p.devices = append(p.devices, Device{
		Name: deviceLabel(name), Token: token, UserID: userID,
		PairedAt: time.Now(), LastSeen: time.Now(),
	})

	// Full, for this account: drop whichever of its devices has gone longest
	// without being used. Dropping the oldest by pairing date instead throws
	// out the browser you use every morning in favour of one you paired later
	// and never opened again. The cap is per account: a server-wide one let
	// seventeen sign-ups sign everyone else out.
	for {
		mine := []int{}
		for i := range p.devices {
			if p.devices[i].UserID == userID {
				mine = append(mine, i)
			}
		}
		if len(mine) <= maxDevices {
			break
		}
		stalest := mine[0]
		for _, i := range mine {
			if p.devices[i].LastSeen.Before(p.devices[stalest].LastSeen) {
				stalest = i
			}
		}
		p.devices = append(p.devices[:stalest], p.devices[stalest+1:]...)
	}
	if err := p.save(p.devices); err != nil {
		return "", err
	}
	return token, nil
}

// A browser that has not been seen for this long is signed out: a token
// left in an old profile should not work forever.
const deviceIdle = 90 * 24 * time.Hour

// Whose returns the account a token belongs to, or "" if it belongs to nobody.
func (p *Pairing) Whose(token string) string {
	if token == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.devices {
		if subtle.ConstantTimeCompare([]byte(token), []byte(p.devices[i].Token)) == 1 {
			if !p.devices[i].LastSeen.IsZero() && time.Since(p.devices[i].LastSeen) > deviceIdle {
				p.devices = append(p.devices[:i], p.devices[i+1:]...)
				_ = p.save(p.devices)
				return ""
			}
			// Written through now and then, not on every request: eviction has
			// to survive a restart, but re-encrypting the device list for each
			// poll of a progress endpoint would be absurd.
			if time.Since(p.devices[i].LastSeen) > time.Hour {
				p.devices[i].LastSeen = time.Now()
				_ = p.save(p.devices)
			} else {
				p.devices[i].LastSeen = time.Now()
			}
			return p.devices[i].UserID
		}
	}
	return ""
}

// Reload re-reads the device list once the vault is open. At startup the
// server is locked, so the encrypted list can't be read yet and every paired
// browser would look like a stranger until this runs.
func (p *Pairing) Reload(devices []Device) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.devices = devices
}

// DevicesFor lists one account's browsers, never the tokens themselves.
func (p *Pairing) DevicesFor(userID string) []Device {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Device{}
	for _, d := range p.devices {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	// Never hand the tokens back out.
	for i := range out {
		out[i].Token = ""
	}
	return out
}

func (p *Pairing) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.devices)
}

// Revoke forgets one device, by its own token.
func (p *Pairing) Revoke(token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := []Device{}
	for _, d := range p.devices {
		if subtle.ConstantTimeCompare([]byte(d.Token), []byte(token)) != 1 {
			kept = append(kept, d)
		}
	}
	p.devices = kept
	return p.save(p.devices)
}

// RevokeOthers forgets every one of an account's devices except the one asking.
// A device list is a list of live credentials, so being able to see them is not
// much use without being able to end them.
func (p *Pairing) RevokeOthers(userID, keep string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := []Device{}
	dropped := 0
	for _, d := range p.devices {
		mine := d.UserID == userID
		asking := subtle.ConstantTimeCompare([]byte(d.Token), []byte(keep)) == 1
		if mine && !asking {
			dropped++
			continue
		}
		kept = append(kept, d)
	}
	p.devices = kept
	return dropped, p.save(p.devices)
}

// RevokeUser forgets every device of an account that is being deleted.
func (p *Pairing) RevokeUser(userID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := []Device{}
	for _, d := range p.devices {
		if d.UserID != userID {
			kept = append(kept, d)
		}
	}
	p.devices = kept
	return p.save(p.devices)
}

func sixDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
