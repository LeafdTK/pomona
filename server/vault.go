package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
)

// Everything Pomona keeps is encrypted at rest: your briefs, your source
// tokens, your Claude credentials.
//
// A master key is generated once and never written down. What goes on disk is
// that key wrapped with a key derived from your passphrase, so a stolen disk,
// a backup, or a snooping host gets ciphertext and nothing else. The server
// starts locked and can do nothing at all until someone unlocks it.
//
// By default there is no passphrase: the key is generated once and kept in the
// data directory at 0600. That still protects the thing most likely to leak,
// which is a copy of the folder in a backup or a synced drive, and it is the
// only way the server can write your brief at 07:30 without someone being
// awake to unlock it. Pass --passphrase if you would rather type one; then the
// key is wrapped and the server starts locked.
//
// What neither mode does, and cannot: hide the key from whoever runs the
// process. To call Claude the server must hold a usable credential, and root
// on that machine can read it out of memory.

const (
	kdfIterations = 600_000
	vaultVersion  = 1
)

type vaultFile struct {
	Version    int    `json:"version"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Wrapped    string `json:"wrapped"`
	Check      string `json:"check"`
}

type Vault struct {
	path string

	mu     sync.RWMutex
	master []byte // in memory only, nil when locked
}

func OpenVault(path string) *Vault { return &Vault{path: path} }

// OpenAuto is the no-passphrase default: make a key the first time, and load it
// every time after, so the server comes up ready to work.
func (v *Vault) OpenAuto() error {
	keyPath := v.path + ".key"

	if raw, err := os.ReadFile(keyPath); err == nil {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(key) != 32 {
			return errors.New("the key file is damaged; delete it and Pomona will make a new one, losing old briefs")
		}
		v.mu.Lock()
		v.master = key
		v.mu.Unlock()
		return nil
	}

	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(master)), 0o600); err != nil {
		return err
	}
	v.mu.Lock()
	v.master = master
	v.mu.Unlock()
	return nil
}

// UsesPassphrase reports whether this data directory was set up the locked way.
func (v *Vault) UsesPassphrase() bool { return v.Exists() }

func (v *Vault) Exists() bool {
	_, err := os.Stat(v.path)
	return err == nil
}

func (v *Vault) Locked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.master == nil
}

// Create sets the passphrase and mints the master key. Called once.
func (v *Vault) Create(passphrase string) error {
	if v.Exists() {
		return errors.New("this server already has a vault")
	}
	if len(passphrase) < 8 {
		return errors.New("use a passphrase of at least 8 characters")
	}

	master := make([]byte, 32)
	salt := make([]byte, 16)
	if _, err := rand.Read(master); err != nil {
		return err
	}
	if _, err := rand.Read(salt); err != nil {
		return err
	}

	kek, err := pbkdf2.Key(sha256.New, passphrase, salt, kdfIterations, 32)
	if err != nil {
		return err
	}
	wrapped, err := seal(kek, master)
	if err != nil {
		return err
	}
	// A known plaintext under the master key, so a wrong passphrase is a clean
	// "wrong passphrase" rather than garbage further down.
	check, err := seal(master, []byte("pomona"))
	if err != nil {
		return err
	}

	raw, err := json.MarshalIndent(vaultFile{
		Version: vaultVersion, Iterations: kdfIterations,
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Wrapped: base64.StdEncoding.EncodeToString(wrapped),
		Check:   base64.StdEncoding.EncodeToString(check),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(v.path, raw, 0o600); err != nil {
		return err
	}

	v.mu.Lock()
	v.master = master
	v.mu.Unlock()
	return nil
}

func (v *Vault) Unlock(passphrase string) error {
	raw, err := os.ReadFile(v.path)
	if err != nil {
		return errors.New("no vault yet: set a passphrase first")
	}
	var file vaultFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return errors.New("the vault file is damaged")
	}

	salt, err := base64.StdEncoding.DecodeString(file.Salt)
	if err != nil {
		return errors.New("the vault file is damaged")
	}
	wrapped, err := base64.StdEncoding.DecodeString(file.Wrapped)
	if err != nil {
		return errors.New("the vault file is damaged")
	}

	kek, err := pbkdf2.Key(sha256.New, passphrase, salt, file.Iterations, 32)
	if err != nil {
		return err
	}
	master, err := open(kek, wrapped)
	if err != nil {
		return errors.New("wrong passphrase")
	}

	v.mu.Lock()
	v.master = master
	v.mu.Unlock()
	return nil
}

func (v *Vault) Lock() {
	v.mu.Lock()
	if v.master != nil {
		for i := range v.master {
			v.master[i] = 0
		}
		v.master = nil
	}
	v.mu.Unlock()
}

// subkey gives each kind of record its own key, so a nonce reused in one place
// can't affect another.
func (v *Vault) subkey(purpose string) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.master == nil {
		return nil, ErrLocked
	}
	return hkdf.Key(sha256.New, v.master, nil, "pomona/"+purpose, 32)
}

var ErrLocked = errors.New("the vault is locked")

// SealTo writes plaintext to a file as ciphertext.
func (v *Vault) SealTo(path, purpose string, plaintext []byte) error {
	key, err := v.subkey(purpose)
	if err != nil {
		return err
	}
	sealed, err := seal(key, plaintext)
	if err != nil {
		return err
	}
	// Write beside, then rename: a crash mid-write leaves the old file whole
	// rather than a truncated one that will never decrypt again. Cheap at one
	// write a day; essential once the signal store is written every run.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// OpenFrom reads a file written by SealTo.
func (v *Vault) OpenFrom(path, purpose string) ([]byte, error) {
	key, err := v.subkey(purpose)
	if err != nil {
		return nil, err
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return open(key, sealed)
}

// ── AES-256-GCM, nonce prefixed ─────────────────────────

func seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func open(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("ciphertext is too short")
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
