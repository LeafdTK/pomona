// Pomona: a morning brief, written from your own sources by your own Claude.
//
// The server holds the sources, the schedule and the writing. The browser
// extension is a face for it: it pairs once with a six digit code and after
// that just reads and renders.
//
// Everything on disk is encrypted. The server starts locked and can do nothing
// until someone unlocks it, so a cold machine holds no usable credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		addr       = flag.String("addr", envOr("POMONA_ADDR", "127.0.0.1:7777"), "address to listen on")
		dataDir    = flag.String("data", envOr("POMONA_DATA", defaultDataDir()), "where to keep the encrypted data")
		passphrase = flag.Bool("passphrase", false, "require a passphrase to unlock, instead of keeping the key on disk")
	)
	flag.Parse()

	if err := run(*addr, *dataDir, *passphrase); err != nil {
		fmt.Fprintln(os.Stderr, "pomona:", err)
		os.Exit(1)
	}
}

// isLocalAddr reports whether we're only reachable from this machine.
func isLocalAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pomona"
	}
	return filepath.Join(home, ".pomona")
}

func run(addr, dataDir string, wantPassphrase bool) error {
	if err := os.MkdirAll(filepath.Join(dataDir, "briefs"), 0o700); err != nil {
		return err
	}
	loadedEnv := loadEnv(dataDir)

	vault := OpenVault(filepath.Join(dataDir, "vault.json"))
	store := NewStore(dataDir, vault)

	// Unless asked for a passphrase, come up ready to work: a server that needs
	// unlocking by hand can't write your brief at 07:30.
	if !wantPassphrase && !vault.UsesPassphrase() {
		if err := vault.OpenAuto(); err != nil {
			return err
		}
		if err := store.Load(); err != nil {
			return err
		}
	}

	devices, _ := store.Devices()
	pairing := NewPairing(devices, store.SaveDevices)

	hostedMode = !isLocalAddr(addr)
	writer := newWriter()
	server := &Server{
		store: store, vault: vault, pairing: pairing, brief: writer,
		oauth: NewOAuthStates(), otps: NewOTPs(), links: NewLinks(), limits: NewLimiter(),
		passphraseMode: wantPassphrase, hosted: hostedMode,
	}

	banner(vault, addr, dataDir, loadedEnv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go schedule(ctx, store, vault, writer)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()

	log.Printf("listening on http://%s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func banner(vault *Vault, addr, dataDir string, loadedEnv []string) {
	fmt.Printf("\n  Pomona %s\n  data  %s\n", version, dataDir)
	if len(loadedEnv) > 0 {
		fmt.Printf("  env   %s\n", strings.Join(loadedEnv, ", "))
	}
	if !claudeAvailable() {
		fmt.Println("\n  ! The claude CLI isn't on PATH, so the subscription mode won't work.")
		fmt.Println("    Install Claude Code, or set an API key in the extension.")
	}
	if vault.Locked() {
		fmt.Println("\n  Locked. Unlock it from the browser before Pomona can do anything.")
	} else {
		fmt.Printf("\n  Open  http://%s\n", addr)
	}
	fmt.Println()
}

// schedule writes the brief so it is ready by the configured hour, and
// catches up if the machine was asleep when it should have run.
// attempt is one account's retry state. A failed morning used to retry every
// single minute for the rest of the day, which is a fine way to turn one
// outage into fourteen hundred.
type attempt struct {
	fails int
	next  time.Time
}

// backoff is how long to wait after the nth consecutive failure: five minutes,
// then ten, twenty, forty, and never more than an hour.
func backoff(fails int) time.Duration {
	switch {
	case fails <= 0:
		return 0
	case fails > 4:
		// Cap before shifting: 5m << 39 overflows int64 and comes out
		// negative, which a test caught and a Tuesday would not have.
		return time.Hour
	}
	return 5 * time.Minute << uint(fails-1)
}

// readyLead is how long before the "ready by" time the writing starts: a
// Slack sweep is bounded at eight minutes, the threads and the write take a
// few more, so the page exists when the hour arrives rather than starting
// to exist.
const readyLead = 20 * time.Minute

// dueNow decides, from the reader's own clock, whether their brief should be
// written on this tick. Pure, so the timezone rule can be tested without a
// scheduler or a store.
func dueNow(cfg *Config, now time.Time, lastRun string) (bool, string) {
	now = now.In(cfg.Location())
	today := DayKey(now)
	if !cfg.Schedule.Enabled || lastRun == today {
		return false, today
	}
	if cfg.Schedule.WeekdaysOnly && (now.Weekday() == time.Saturday || now.Weekday() == time.Sunday) {
		return false, today
	}
	var hour, minute int
	if _, err := fmt.Sscanf(cfg.Schedule.Time, "%d:%d", &hour, &minute); err != nil {
		return false, today
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location()).Add(-readyLead)
	if at.Day() != now.Day() {
		// A ready-by time just after midnight starts the evening before,
		// which would be yesterday's brief: hold it to midnight instead.
		at = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
	return !now.Before(at), today
}

func schedule(ctx context.Context, store *Store, vault *Vault, writer *Writer) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	lastRun := map[string]string{}
	tries := map[string]*attempt{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if vault.Locked() {
			continue
		}

		// Every account has its own hour and its own timezone, so this walks
		// all of them on their own clocks.
		now := time.Now()
		for _, user := range store.Users() {
			mine := store.For(user.ID)
			cfg := mine.Config()

			fire, today := dueNow(cfg, now, lastRun[user.ID])
			if !fire {
				continue
			}
			if try := tries[user.ID]; try != nil && now.Before(try.next) {
				continue // still backing off from the last failure
			}
			if existing, _ := mine.Brief(today); existing != nil {
				lastRun[user.ID] = today
				continue
			}

			log.Printf("writing today's brief for %s", user.Email)
			if _, err := writer.Generate(ctx, mine, "scheduled"); err != nil {
				try := tries[user.ID]
				if try == nil {
					try = &attempt{}
					tries[user.ID] = try
				}
				try.fails++
				try.next = now.Add(backoff(try.fails))
				log.Printf("the brief failed for %s (attempt %d, next in %s): %v",
					user.Email, try.fails, backoff(try.fails).Round(time.Minute), err)
				continue
			}
			lastRun[user.ID] = today
			delete(tries, user.ID)
		}
	}
}
