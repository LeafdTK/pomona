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

type Pairing struct {
	mu       sync.Mutex
	code     string
	expires  time.Time
	attempts int

	devices []Device
	save    func([]Device) error
}

func NewPairing(devices []Device, save func([]Device) error) *Pairing {
	return &Pairing{devices: devices, save: save}
}

// Begin mints a code to show the user. Any previous one stops working.
func (p *Pairing) Begin() (string, time.Time, error) {
	code, err := sixDigits()
	if err != nil {
		return "", time.Time{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code, p.expires, p.attempts = code, time.Now().Add(codeTTL), 0
	return code, p.expires, nil
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

// Claim exchanges a correct code for a device token, once.
func (p *Pairing) Claim(code, userID, name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.code == "" || time.Now().After(p.expires) {
		p.code = ""
		return "", errors.New("that code has expired: ask the server for a new one")
	}
	if p.attempts >= maxAttempts {
		p.code = ""
		return "", errors.New("too many wrong codes: ask the server for a new one")
	}

	// Constant time, so the number of wrong guesses is the only signal.
	if subtle.ConstantTimeCompare([]byte(code), []byte(p.code)) != 1 {
		p.attempts++
		return "", fmt.Errorf("that code isn't right (%d attempts left)", maxAttempts-p.attempts)
	}

	// Correct: burn the code before doing anything else.
	p.code = ""
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

	// Full: drop whichever device has gone longest without being used. Dropping
	// the oldest by pairing date instead throws out the browser you use every
	// morning in favour of one you paired later and never opened again.
	for len(p.devices) > maxDevices {
		stalest := 0
		for i := range p.devices {
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

// Whose returns the account a token belongs to, or "" if it belongs to nobody.
func (p *Pairing) Whose(token string) string {
	if token == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.devices {
		if subtle.ConstantTimeCompare([]byte(token), []byte(p.devices[i].Token)) == 1 {
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

func sixDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
