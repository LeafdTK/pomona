package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// One server, many people.
//
// Each account gets its own directory, its own sources, its own briefs, and its
// own encryption key derived from the server key. Nobody can see anyone else's
// data through the API, and the files on disk are separately encrypted.
//
// The server can decrypt all of them, and has to: it is the thing that fetches
// your Slack and calls Claude, and it does that at 07:30 whether or not you are
// awake. That is the trade this design makes, and it is why "run it yourself"
// is the honest advice for anything sensitive.

const passwordIterations = 600_000

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Salt      string    `json:"salt"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
	// Who signed in without an email: "slack:<team>:<user>". Such an account
	// has no password and no email; the identity is the whole key.
	Identity string `json:"identity,omitempty"`
}

func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func newUser(email, name, password string) (*User, error) {
	email = normaliseEmail(email)
	if !strings.Contains(email, "@") {
		return nil, errors.New("that doesn't look like an email address")
	}
	if len(password) < 8 {
		return nil, errors.New("use a password of at least 8 characters")
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return nil, err
	}

	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	if name == "" {
		name = strings.SplitN(email, "@", 2)[0]
	}

	return &User{
		ID:        hex.EncodeToString(id),
		Email:     email,
		Name:      name,
		Salt:      base64.StdEncoding.EncodeToString(salt),
		Hash:      base64.StdEncoding.EncodeToString(hash),
		CreatedAt: time.Now(),
	}, nil
}

// newIdentityUser is an account with no email and no password: whoever
// proves the identity to Slack is its owner.
func newIdentityUser(identity, name string) (*User, error) {
	if identity == "" {
		return nil, errors.New("no identity to make an account from")
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	return &User{ID: hex.EncodeToString(id), Name: name, Identity: identity, CreatedAt: time.Now()}, nil
}

// Matches reports whether a password is this account's, in constant time.
func (u *User) Matches(password string) bool {
	if u.Hash == "" {
		return false // no password path at all
	}
	salt, err := base64.StdEncoding.DecodeString(u.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(u.Hash)
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ── The account list ────────────────────────────────────

func (s *Store) Users() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, len(s.users))
	copy(out, s.users)
	return out
}

func (s *Store) loadUsers() error {
	raw, err := s.vault.OpenFrom(s.path("users.enc"), "users")
	if os.IsNotExist(err) {
		s.mu.Lock()
		s.users = nil
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	var users []User
	if err := json.Unmarshal(raw, &users); err != nil {
		return err
	}
	s.mu.Lock()
	s.users = users
	s.mu.Unlock()
	return nil
}

func (s *Store) saveUsersLocked() error {
	raw, err := json.Marshal(s.users)
	if err != nil {
		return err
	}
	return s.vault.SealTo(s.path("users.enc"), "users", raw)
}

func (s *Store) CreateUser(email, name, password string) (*User, error) {
	user, err := newUser(email, name, password)
	if err != nil {
		return nil, err
	}
	return s.addUser(user)
}

func (s *Store) CreateIdentityUser(identity, name string) (*User, error) {
	user, err := newIdentityUser(identity, name)
	if err != nil {
		return nil, err
	}
	return s.addUser(user)
}

func (s *Store) addUser(user *User) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if user.Email != "" && existing.Email == user.Email {
			return nil, errors.New("there's already an account with that email")
		}
		if user.Identity != "" && existing.Identity == user.Identity {
			return nil, errors.New("there's already an account for that identity")
		}
	}
	// Saved before it exists: an account the disk refused must not live on
	// in memory, working until the next restart and then gone.
	before := s.users
	s.users = append(append([]User{}, before...), *user)
	if err := s.saveUsersLocked(); err != nil {
		s.users = before
		return nil, fmt.Errorf("couldn't write the account list: %w", err)
	}
	if err := os.MkdirAll(s.path("users", user.ID, "briefs"), 0o700); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Store) UserByEmail(email string) *User {
	email = normaliseEmail(email)
	if email == "" {
		return nil // identity accounts have no email, and must not match a blank
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.users {
		if s.users[i].Email == email {
			clone := s.users[i]
			return &clone
		}
	}
	return nil
}

func (s *Store) UserByIdentity(identity string) *User {
	if identity == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.users {
		if s.users[i].Identity == identity {
			clone := s.users[i]
			return &clone
		}
	}
	return nil
}

// DeleteUser removes an account and everything it stored. The device list
// is the caller's to clean, since it lives in Pairing.
func (s *Store) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.users[:0]
	for _, u := range s.users {
		if u.ID != id {
			kept = append(kept, u)
		}
	}
	s.users = kept
	if err := s.saveUsersLocked(); err != nil {
		return err
	}
	return os.RemoveAll(s.path("users", id))
}

func (s *Store) UserByID(id string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.users {
		if s.users[i].ID == id {
			clone := s.users[i]
			return &clone
		}
	}
	return nil
}
