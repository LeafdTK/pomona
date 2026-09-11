package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Brief is one morning: what Claude wrote, plus how it was made.
type Brief struct {
	ID             string          `json:"id"` // local day, 2026-09-08
	CreatedAt      time.Time       `json:"createdAt"`
	Trigger        string          `json:"trigger"`
	Data           json.RawMessage `json:"data"`
	Painting       *Painting       `json:"painting,omitempty"`
	Model          string          `json:"model"`
	DowngradedFrom string          `json:"downgradedFrom,omitempty"`
	Sources        []SourceReport  `json:"sources"`
	Done           []int           `json:"done"`
	Usage          []Usage         `json:"usage,omitempty"` // what writing it cost
}

// SourceReport is how each connector fared, so the page can say what the brief
// was written without.
type SourceReport struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

type Painting struct {
	Caption string `json:"caption"`
	Image   string `json:"image"`
	Credit  string `json:"credit"`
	Link    string `json:"link"`
}

// Note is something worth carrying into future mornings.
type Note struct {
	Text string `json:"text"`
	On   string `json:"on"`
}

const (
	briefHistory = 30
	memoryLimit  = 24
)

func DayKey(t time.Time) string { return t.Format("2006-01-02") }

// Store is the data directory: the account list, and a directory per account.
// Every byte it writes goes through the vault.
type Store struct {
	dir   string
	vault *Vault

	mu    sync.RWMutex
	users []User

	// Read-modify-write on a user's own files. UserStore is a per-request view
	// with no state of its own, so the lock has to live on the thing they all
	// share, or two clicks arriving together lose one another's edit.
	edit sync.Mutex
}

func NewStore(dir string, vault *Vault) *Store {
	return &Store{dir: dir, vault: vault}
}

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.dir}, parts...)...)
}

// Load reads the account list once the vault is open, and moves a pre-accounts
// data directory into its first account so nobody loses their briefs.
func (s *Store) Load() error {
	if err := s.loadUsers(); err != nil {
		return err
	}
	return s.migrateLegacy()
}

// For is one account's view of the store. Every read and write below is scoped
// to it, so no request can reach another account's data.
func (s *Store) For(userID string) *UserStore {
	return &UserStore{store: s, id: userID}
}

// UserStore is the whole data API, bound to one account.
type UserStore struct {
	store *Store
	id    string
}

func (u *UserStore) path(parts ...string) string {
	return u.store.path(append([]string{"users", u.id}, parts...)...)
}

// Each account's files get their own key, derived from the server key, so a
// mistake in one account's code path can't decrypt another's.
func (u *UserStore) purpose(kind string) string { return "user/" + u.id + "/" + kind }

// ID is which account this view belongs to.
func (u *UserStore) ID() string { return u.id }

func (u *UserStore) Config() *Config {
	cfg := defaultConfig()
	raw, err := u.store.vault.OpenFrom(u.path("config.enc"), u.purpose("config"))
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return defaultConfig()
	}
	return cfg
}

func (u *UserStore) SetConfig(next *Config) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.path("briefs"), 0o700); err != nil {
		return err
	}
	return u.store.vault.SealTo(u.path("config.enc"), u.purpose("config"), raw)
}

// migrateLegacy lifts a single-user data directory into an account, once.
func (s *Store) migrateLegacy() error {
	legacy := s.path("config.enc")
	if _, err := os.Stat(legacy); err != nil {
		return nil // nothing to move
	}
	if len(s.Users()) > 0 {
		return nil // already migrated
	}

	user, err := s.CreateUser("you@localhost", "You", "pomona-local-account")
	if err != nil {
		return err
	}
	target := s.path("users", user.ID)

	// The old files were sealed with the old purposes, so re-seal rather than
	// rename: the key changes with the account.
	if raw, err := s.vault.OpenFrom(legacy, "config"); err == nil {
		_ = s.vault.SealTo(filepath.Join(target, "config.enc"), "user/"+user.ID+"/config", raw)
	}
	if raw, err := s.vault.OpenFrom(s.path("memory.enc"), "memory"); err == nil {
		_ = s.vault.SealTo(filepath.Join(target, "memory.enc"), "user/"+user.ID+"/memory", raw)
	}
	if entries, err := os.ReadDir(s.path("briefs")); err == nil {
		for _, e := range entries {
			if raw, err := s.vault.OpenFrom(s.path("briefs", e.Name()), "briefs"); err == nil {
				_ = s.vault.SealTo(filepath.Join(target, "briefs", e.Name()), "user/"+user.ID+"/briefs", raw)
			}
		}
	}

	_ = os.Remove(legacy)
	_ = os.Remove(s.path("memory.enc"))
	_ = os.RemoveAll(s.path("briefs"))
	return nil
}

// ── Briefs ──────────────────────────────────────────────

func (u *UserStore) SaveBrief(b *Brief) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.path("briefs"), 0o700); err != nil {
		return err
	}
	if err := u.store.vault.SealTo(u.path("briefs", b.ID+".enc"), u.purpose("briefs"), raw); err != nil {
		return err
	}
	return u.prune()
}

// Briefs, newest first.
func (u *UserStore) Briefs() ([]*Brief, error) {
	entries, err := os.ReadDir(u.path("briefs"))
	if err != nil {
		return []*Brief{}, nil // no briefs yet is not an error
	}
	out := []*Brief{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".enc") {
			continue
		}
		raw, err := u.store.vault.OpenFrom(u.path("briefs", e.Name()), u.purpose("briefs"))
		if err != nil {
			continue // a damaged file shouldn't hide the rest
		}
		var b Brief
		if json.Unmarshal(raw, &b) == nil {
			out = append(out, &b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (u *UserStore) Brief(id string) (*Brief, error) {
	briefs, err := u.Briefs()
	if err != nil {
		return nil, err
	}
	for _, b := range briefs {
		if id == "" || b.ID == id {
			return b, nil
		}
	}
	return nil, nil
}

// prune keeps the last few mornings and deletes the rest. Old briefs are
// summaries of other people's messages; there is no reason to hoard them.
func (u *UserStore) prune() error {
	briefs, err := u.Briefs()
	if err != nil {
		return err
	}
	keep := u.Config().KeepDays
	if keep <= 0 || keep > briefHistory {
		keep = briefHistory
	}
	for i, b := range briefs {
		if i >= keep {
			_ = os.Remove(u.path("briefs", b.ID+".enc"))
		}
	}
	return nil
}

// Forget removes everything this account has stored, briefs and notes both.
func (u *UserStore) ForgetEverything() error {
	if err := os.RemoveAll(u.path("briefs")); err != nil {
		return err
	}
	_ = os.Remove(u.path("memory.enc"))
	_ = os.Remove(u.path("attention.enc"))
	_ = os.Remove(u.path("signals.enc"))
	_ = os.Remove(u.path("ownership.enc"))
	_ = os.Remove(u.path("usage.enc"))
	return os.MkdirAll(u.path("briefs"), 0o700)
}

// SetDone records which to-dos you ticked, which is what tomorrow's brief reads
// to tell "cleared" from "still carrying this".
func (u *UserStore) SetDone(id string, done []int) error {
	u.store.edit.Lock()
	defer u.store.edit.Unlock()
	b, err := u.Brief(id)
	if err != nil || b == nil {
		return err
	}
	b.Done = done
	return u.SaveBrief(b)
}

// ── Memory ──────────────────────────────────────────────

func (u *UserStore) Memory() []Note {
	notes := []Note{}
	if raw, err := u.store.vault.OpenFrom(u.path("memory.enc"), u.purpose("memory")); err == nil {
		_ = json.Unmarshal(raw, &notes)
	}
	return notes
}

// Attention is the record of what this reader keeps ignoring. It lives beside
// the memory notes and is encrypted the same way: it is a portrait of somebody's
// attention, which is not something to leave lying in plaintext.
func (u *UserStore) Attention() Ledger {
	ledger := Ledger{}
	if raw, err := u.store.vault.OpenFrom(u.path("attention.enc"), u.purpose("attention")); err == nil {
		_ = json.Unmarshal(raw, &ledger)
	}
	return ledger
}

func (u *UserStore) SaveAttention(ledger Ledger) error {
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	return u.store.vault.SealTo(u.path("attention.enc"), u.purpose("attention"), raw)
}

// NoteAttention reads, changes and writes the ledger under one call, so two
// clicks arriving together cannot lose one another's edit.
func (u *UserStore) NoteAttention(change func(Ledger)) error {
	u.store.edit.Lock()
	defer u.store.edit.Unlock()
	ledger := u.Attention()
	change(ledger)
	return u.SaveAttention(ledger)
}

// ── Signals ─────────────────────────────────────────────

// Signals is everything gathered so far, or a fresh store if there is none
// yet. A file that will not open is treated as no file: a cold start is a slow
// morning, a refusal to write the brief is a broken one.
func (u *UserStore) Signals() *SignalStore {
	store := newSignalStore()
	if raw, err := u.store.vault.OpenFrom(u.path("signals.enc"), u.purpose("signals")); err == nil {
		_ = json.Unmarshal(raw, store)
	}
	store.ready()
	return store
}

func (u *UserStore) SaveSignals(store *SignalStore) error {
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.path(), 0o700); err != nil {
		return err
	}
	return u.store.vault.SealTo(u.path("signals.enc"), u.purpose("signals"), raw)
}

// Ownership is the map of what this reader owns, empty if never computed.
func (u *UserStore) Ownership() Ownership {
	own := Ownership{}
	if raw, err := u.store.vault.OpenFrom(u.path("ownership.enc"), u.purpose("ownership")); err == nil {
		_ = json.Unmarshal(raw, &own)
	}
	return own
}

func (u *UserStore) SaveOwnership(own Ownership) error {
	raw, err := json.Marshal(own)
	if err != nil {
		return err
	}
	return u.store.vault.SealTo(u.path("ownership.enc"), u.purpose("ownership"), raw)
}

// ── Usage ───────────────────────────────────────────────

const usageKeep = 30 * 24 * time.Hour

// Usage is the last month of what Claude cost this account.
func (u *UserStore) Usage() []Usage {
	out := []Usage{}
	if raw, err := u.store.vault.OpenFrom(u.path("usage.enc"), u.purpose("usage")); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// NoteUsage appends and forgets what is older than a month.
func (u *UserStore) NoteUsage(spent []Usage, now time.Time) error {
	if len(spent) == 0 {
		return nil
	}
	u.store.edit.Lock()
	defer u.store.edit.Unlock()
	all := append(u.Usage(), spent...)
	kept := all[:0]
	for _, x := range all {
		if now.Sub(x.At) <= usageKeep {
			kept = append(kept, x)
		}
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return u.store.vault.SealTo(u.path("usage.enc"), u.purpose("usage"), raw)
}

// Remember keeps new notes, newest first, ignoring ones it already holds.
func (u *UserStore) Remember(texts []string, on string) error {
	notes := u.Memory()
	seen := map[string]bool{}
	for _, n := range notes {
		seen[strings.ToLower(n.Text)] = true
	}
	fresh := []Note{}
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		fresh = append(fresh, Note{Text: t, On: on})
	}
	if len(fresh) == 0 {
		return nil
	}
	notes = append(fresh, notes...)
	if len(notes) > memoryLimit {
		notes = notes[:memoryLimit]
	}
	return u.writeMemory(notes)
}

func (u *UserStore) Forget(text string) error {
	kept := []Note{}
	for _, n := range u.Memory() {
		if n.Text != text {
			kept = append(kept, n)
		}
	}
	return u.writeMemory(kept)
}

func (u *UserStore) writeMemory(notes []Note) error {
	raw, err := json.Marshal(notes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.path(), 0o700); err != nil {
		return err
	}
	return u.store.vault.SealTo(u.path("memory.enc"), u.purpose("memory"), raw)
}

// ── Paired devices ──────────────────────────────────────

func (s *Store) Devices() ([]Device, error) {
	devices := []Device{}
	raw, err := s.vault.OpenFrom(s.path("devices.enc"), "devices")
	if err != nil {
		return devices, nil // locked or absent: start empty
	}
	return devices, json.Unmarshal(raw, &devices)
}

func (s *Store) SaveDevices(devices []Device) error {
	raw, err := json.Marshal(devices)
	if err != nil {
		return err
	}
	return s.vault.SealTo(s.path("devices.enc"), "devices", raw)
}
