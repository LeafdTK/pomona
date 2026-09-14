package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type Server struct {
	store   *Store
	vault   *Vault
	pairing *Pairing
	brief   *Writer
	oauth   *OAuthStates
	otps    *OTPs
	links   *Links
	limits  *Limiter

	// Started with --passphrase, so the browser has to set one before anything
	// works. Without it the server keeps its own key and is ready immediately.
	passphraseMode bool

	// Listening off loopback: strangers can reach this, so accounts come from
	// Slack or an emailed code, never from a password form.
	hosted bool
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Open: needed before there's anything to authenticate with.
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/unlock", s.unlock)
	mux.HandleFunc("POST /api/pair", s.pair)
	mux.HandleFunc("POST /api/pair/auto", s.pairAuto)
	mux.HandleFunc("POST /api/pair/exchange", s.pairExchange)
	mux.HandleFunc("POST /api/signup", s.signup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/auth/email", s.emailStart)
	mux.HandleFunc("POST /api/auth/email/verify", s.emailVerify)
	mux.HandleFunc("GET /auth/slack", s.slackSignIn)

	// Paired only.
	mux.Handle("GET /api/config", s.guard(s.getConfig))
	mux.Handle("PUT /api/config", s.guard(s.putConfig))
	mux.Handle("GET /api/sources", s.guard(s.getSources))
	mux.Handle("POST /api/sources/test", s.guard(s.testSource))
	mux.Handle("GET /api/briefs", s.guard(s.listBriefs))
	mux.Handle("GET /api/briefs/{id}", s.guard(s.getBrief))
	mux.Handle("POST /api/briefs", s.guard(s.generate))
	mux.Handle("GET /api/briefs/progress", s.guard(s.progress))
	mux.Handle("GET /api/plate", s.guard(s.plate))
	mux.Handle("GET /api/profile/guess", s.guard(s.guessProfile))
	mux.Handle("POST /api/briefs/{id}/done", s.guard(s.setDone))
	mux.Handle("GET /api/memory", s.guard(s.getMemory))
	mux.Handle("POST /api/memory", s.guard(s.remember))
	mux.Handle("GET /api/mutes", s.guard(s.getMutes))
	mux.Handle("POST /api/mutes", s.guard(s.mute))
	mux.Handle("POST /api/mutes/unmute", s.guard(s.unmute))
	mux.Handle("POST /api/attention", s.guard(s.noteAttention))
	mux.Handle("GET /api/attention/faded", s.guard(s.fadedPlaces))
	mux.Handle("POST /api/refresh", s.guard(s.refresh))
	mux.Handle("GET /api/usage", s.guard(s.usage))
	mux.Handle("POST /api/github/connect", s.guard(s.githubConnect))
	mux.Handle("GET /api/signals", s.guard(s.signals))
	mux.Handle("GET /api/ownership", s.guard(s.ownership))
	mux.Handle("POST /api/ownership/own", s.guard(s.own))
	mux.Handle("POST /api/ownership/disown", s.guard(s.disown))
	mux.Handle("POST /api/attention/revive", s.guard(s.revivePlace))
	mux.Handle("POST /api/memory/forget", s.guard(s.forget))
	mux.Handle("POST /api/lock", s.guard(s.lock))
	mux.Handle("GET /api/account", s.guard(s.account))
	mux.Handle("POST /api/account/forget", s.guard(s.forgetEverything))
	mux.Handle("POST /api/logout", s.guard(s.logout))
	mux.Handle("POST /api/account/devices/others", s.guard(s.signOutOthers))
	mux.Handle("POST /api/account/delete", s.guard(s.deleteAccount))
	mux.Handle("POST /api/pair/code", s.guard(s.pairCode))
	mux.Handle("POST /api/link", s.guard(s.linkStart))
	mux.Handle("GET /api/slack/channels", s.guard(s.slackChannels))
	mux.Handle("POST /api/claude/test", s.guard(s.claudeTest))

	// Slack's callback arrives from Slack, so it can't carry our own token.
	mux.HandleFunc("GET /api/slack/callback", s.slackCallback)
	mux.Handle("POST /api/slack/connect", s.guard(s.slackConnect))
	mux.Handle("POST /api/slack/disconnect", s.guard(s.slackDisconnect))
	mux.Handle("GET /api/server", s.guard(s.getServerSettings))
	mux.Handle("PUT /api/server", s.guard(s.putServerSettings))

	// The pages themselves, so the server is usable without the extension.
	s.webRoutes(mux)

	return secure(cors(mux), s.hosted)
}

// secure sets the headers a page on the open internet should carry. The
// pages are served from this origin and talk only to it; the one outside
// image is the day's painting.
func secure(next http.Handler, hosted bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data: https://*.clevelandart.org; connect-src 'self'; frame-ancestors 'none'")
		if hosted && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// The extension is a browser page, so it needs permission to talk to us at
// all. Only the extension and locally served dev pages get it: an arbitrary
// website must not be able to reach a server sitting on your loopback.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigin(origin) || ownOrigin(origin, r) {
			if origin == "" {
				origin = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "content-type, authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedOrigin(origin string) bool {
	switch {
	case origin == "": // curl, or the extension's service worker
		return true
	case strings.HasPrefix(origin, "chrome-extension://"):
		return true
	case strings.HasPrefix(origin, "http://localhost:"), strings.HasPrefix(origin, "http://127.0.0.1:"):
		return true // dev previews
	default:
		return false
	}
}

// ownOrigin is the server's own pages calling it over a real hostname: the
// preflight carries that hostname as the origin, and it has to be let in.
func ownOrigin(origin string, r *http.Request) bool {
	if origin == "" || r.Host == "" {
		return false
	}
	return origin == "https://"+r.Host || origin == "http://"+r.Host
}

// A request carries a device token; the token names an account; the handler
// only ever sees that account's data.
type handler func(http.ResponseWriter, *http.Request, *UserStore, *User)

func (s *Server) guard(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.vault.Locked() {
			fail(w, http.StatusLocked, ErrLocked)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		userID := s.pairing.Whose(token)
		if userID == "" {
			fail(w, http.StatusUnauthorized, errors.New("this browser isn't signed in"))
			return
		}
		user := s.store.UserByID(userID)
		if user == nil {
			fail(w, http.StatusUnauthorized, errors.New("that account no longer exists"))
			return
		}
		h(w, r, s.store.For(userID), user)
	})
}

// ── Open endpoints ──────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{
		"ok":        true,
		"version":   version,
		"locked":    s.vault.Locked(),
		"paired":    s.pairing.Count() > 0,
		"claudeCLI": claudeAvailable(),
		"gh":        !s.hosted && ghAvailable(),
		// "local" is what lets a browser sign itself in with no proof. On a
		// hosted server it is never true, whatever the socket says: a proxy
		// moved into the pod must not turn every visitor into the owner.
		"local": !s.hosted && isLoopback(r),
		// Only true when someone asked for a passphrase and hasn't set one yet.
		"needsSetup": s.passphraseMode && !s.vault.Exists(),
		// Whether locking means anything here. Without a passphrase it doesn't.
		"passphrase": s.vault.Exists(),
		"accounts":   len(s.store.Users()),
		"hosted":     s.hosted,
		// Which doors are open for someone who is not on this machine.
		"auth": map[string]bool{
			"slack": s.store.ServerSettings().Slack.Configured(),
			"email": mailConfigured(),
		},
	})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.vault.Create(body.Passphrase); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.afterUnlock(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) unlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	// With no passphrase there is nothing to type: the key is on disk, and
	// unlocking just means picking it back up.
	if !s.vault.Exists() {
		if err := s.vault.OpenAuto(); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
	} else if err := s.vault.Unlock(body.Passphrase); err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.afterUnlock(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.vault.Locked() {
		fail(w, http.StatusLocked, ErrLocked)
		return
	}
	if !s.limits.Allow("pair:ip:"+clientIP(r), 10, time.Minute) {
		fail(w, http.StatusTooManyRequests, errors.New("too many tries; wait a minute"))
		return
	}
	token, err := s.pairing.Claim(strings.TrimSpace(body.Code), deviceName(r))
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	ok(w, map[string]any{"token": token})
}

// afterUnlock reads the things that were unreadable while locked.
func (s *Server) afterUnlock() error {
	if err := s.store.Load(); err != nil {
		return err
	}
	devices, err := s.store.Devices()
	if err != nil {
		return err
	}
	s.pairing.Reload(devices)
	return nil
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.vault.Locked() {
		fail(w, http.StatusLocked, ErrLocked)
		return
	}
	// Off this machine there are no passwords: sign in with Slack or a code.
	if s.hosted || !isLoopback(r) {
		fail(w, http.StatusForbidden, errors.New("this server makes accounts through Slack or an emailed code, not a password"))
		return
	}

	user, err := s.store.CreateUser(body.Email, body.Name, body.Password)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	token, err := s.pairing.Adopt(user.ID, deviceName(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"token": token, "user": publicUser(user)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.vault.Locked() {
		fail(w, http.StatusLocked, ErrLocked)
		return
	}
	if !s.limits.Allow("login:ip:"+clientIP(r), 10, time.Minute) {
		fail(w, http.StatusTooManyRequests, errors.New("too many tries; wait a minute"))
		return
	}

	user := s.store.UserByEmail(body.Email)
	// Same answer, and the same amount of work, either way: without the
	// dummy hash an unknown address answers in a millisecond and a known one
	// in a few hundred, which is a yes or no to whoever is asking.
	if user == nil {
		(&User{Salt: "AAAAAAAAAAAAAAAAAAAAAA==", Hash: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}).Matches(body.Password)
	}
	if user == nil || !user.Matches(body.Password) {
		fail(w, http.StatusUnauthorized, errors.New("that email and password don't match"))
		return
	}
	token, err := s.pairing.Adopt(user.ID, deviceName(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"token": token, "user": publicUser(user)})
}

func publicUser(u *User) map[string]any {
	return map[string]any{"id": u.ID, "email": u.Email, "name": u.Name}
}

func deviceName(r *http.Request) string {
	agent := r.Header.Get("User-Agent")
	switch {
	case strings.Contains(agent, "Chrome"):
		return "Chrome"
	case strings.Contains(agent, "Safari"):
		return "Safari"
	case strings.Contains(agent, "Firefox"):
		return "Firefox"
	default:
		return "A browser"
	}
}

// pairAuto hands a token to anything on this machine. A browser on your own
// laptop shouldn't have to prove anything to a server on your own laptop: if
// something local were hostile it could read ~/.pomona directly.
func (s *Server) pairAuto(w http.ResponseWriter, r *http.Request) {
	if s.hosted || !isLoopback(r) {
		fail(w, http.StatusForbidden, errors.New("automatic pairing only works on this machine; use a pairing code"))
		return
	}
	if s.vault.Locked() {
		fail(w, http.StatusLocked, ErrLocked)
		return
	}
	// Only meaningful for a single-account server: with several accounts there
	// is no way to know which one the browser meant, so it has to sign in.
	users := s.store.Users()
	if len(users) > 1 {
		fail(w, http.StatusConflict, errors.New("this server has several accounts, so please sign in"))
		return
	}

	var user *User
	if len(users) == 1 {
		user = &users[0]
	} else {
		// First run on your own machine: make the account silently. The
		// password is random and never shown: this account is entered by
		// being on this machine, not by typing anything.
		created, err := s.store.CreateUser("you@localhost", "You", randomSecret())
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		user = created
	}

	token, err := s.pairing.Adopt(user.ID, deviceName(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"token": token, "user": publicUser(user)})
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ── Paired endpoints ────────────────────────────────────

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	ok(w, u.Config())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	var cfg Config
	if err := decode(r, &cfg); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	before := u.Config()
	// Typing a schedule time is choosing it; inference must not move it after.
	if cfg.Schedule.Time != "" && cfg.Schedule.Time != before.Schedule.Time {
		cfg.Schedule.Set = true
	}
	// A typed value stops being an inferred one.
	if cfg.Profile.Inferred == nil {
		cfg.Profile.Inferred = before.Profile.Inferred
	}
	for field, was := range map[string]string{"name": before.Profile.Name, "role": before.Profile.Role, "timezone": before.Profile.Timezone} {
		now := map[string]string{"name": cfg.Profile.Name, "role": cfg.Profile.Role, "timezone": cfg.Profile.Timezone}[field]
		if now != was && cfg.Profile.Inferred != nil {
			delete(cfg.Profile.Inferred, field)
		}
	}
	// Pasted secrets arrive with whatever the clipboard added: a trailing
	// newline, or a space where a terminal wrapped the line. Neither kind of
	// secret ever contains whitespace, so all of it goes.
	cfg.Claude.APIKey = strings.Join(strings.Fields(cfg.Claude.APIKey), "")
	cfg.Claude.OAuthToken = strings.Join(strings.Fields(cfg.Claude.OAuthToken), "")
	if err := u.SetConfig(&cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	// A new token is the moment the sources can say who this is.
	if tokensChanged(before, &cfg) {
		inferSoon(u)
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) getSources(w http.ResponseWriter, r *http.Request, _ *UserStore, _ *User) {
	type described struct {
		ID     string  `json:"id"`
		Name   string  `json:"name"`
		Icon   string  `json:"iconKey"`
		Blurb  string  `json:"blurb"`
		Help   string  `json:"help"`
		Fields []Field `json:"fields"`
	}
	out := []described{}
	for _, c := range Collectors() {
		out = append(out, described{c.ID, c.Name, c.IconKey, c.Blurb, c.Help, c.Fields})
	}
	ok(w, out)
}

func (s *Server) testSource(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	if !s.limits.Allow("test:"+u.ID(), 30, time.Hour) {
		fail(w, http.StatusTooManyRequests, errors.New("too many tests; wait a while"))
		return
	}
	var body struct {
		ID     string        `json:"id"`
		Custom *CustomSource `json:"custom"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	cfg := u.Config()
	window := Window{Now: time.Now(), Since: time.Now().Add(-time.Duration(cfg.Lookback) * time.Hour)}

	var items []Item
	var err error
	if body.Custom != nil {
		items, err = fetchCustom(r.Context(), *body.Custom)
	} else {
		c := collectorByID(body.ID)
		if c == nil {
			fail(w, http.StatusBadRequest, errors.New("no such source"))
			return
		}
		items, _, err = c.Fetch(r.Context(), cfg.Sources[body.ID], window)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	// Return what it actually found, not just how much. "0 items" tells you
	// nothing about whether the source is broken or the day was quiet.
	found := []map[string]any{}
	for _, it := range items {
		found = append(found, map[string]any{
			"kind": it.Kind, "title": it.Title, "url": it.URL,
			"when": it.Time, "body": clip(it.Body, 160),
			// Tags decide ranking and grouping, so a source that looks wrong is
			// usually a tag that is missing.
			"tags": it.Tags, "origin": it.Origin,
		})
	}
	sample := ""
	if len(items) > 0 {
		sample = items[0].Title
	}
	ok(w, map[string]any{"count": len(items), "sample": sample, "items": found})
}

func (s *Server) listBriefs(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	briefs, err := u.Briefs()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type summary struct {
		ID        string    `json:"id"`
		CreatedAt time.Time `json:"createdAt"`
		Model     string    `json:"model"`
	}
	out := []summary{}
	for _, b := range briefs {
		out = append(out, summary{b.ID, b.CreatedAt, b.Model})
	}
	ok(w, out)
}

func (s *Server) getBrief(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	id := r.PathValue("id")
	if id == "latest" {
		id = ""
	}
	b, err := u.Brief(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if b == nil {
		fail(w, http.StatusNotFound, errors.New("no brief yet"))
		return
	}
	ok(w, b)
}

// progress is polled while a brief is being written. It costs nothing to
// answer, because the answer is already in memory.
func (s *Server) progress(w http.ResponseWriter, _ *http.Request, u *UserStore, _ *User) {
	ok(w, s.brief.Progress(u.ID()))
}

// plate is today's artwork, which the page can show before the brief exists.
// The plate is chosen from the date alone, so asking early gives the same
// painting the finished brief will carry.
func (s *Server) plate(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	painting, err := PickPainting(r.Context(), DayKey(u.Config().Now()))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	ok(w, painting)
}

// guessProfile asks the connected sources who this account belongs to, so
// setup can show an answer instead of an empty form.
func (s *Server) guessProfile(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	ok(w, GuessProfile(r.Context(), u.Config()))
}

// generate starts the write and answers at once. A brief takes minutes,
// nearly all of it reading Slack, and a request held open that long is cut
// off by any proxy in front of a hosted server. The page polls progress and
// picks the brief up when the board says it is done.
func (s *Server) generate(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	if !s.limits.Allow("write:"+u.ID(), 6, time.Hour) {
		fail(w, http.StatusTooManyRequests, errors.New("six briefs an hour is plenty; try again later"))
		return
	}
	if s.brief.Progress(u.ID()).Running() {
		w.WriteHeader(http.StatusAccepted)
		ok(w, map[string]any{"started": false, "running": true})
		return
	}
	go func() {
		ctx, done := context.WithTimeout(context.Background(), 20*time.Minute)
		defer done()
		if _, err := s.brief.Generate(ctx, u, "manual"); err != nil {
			log.Printf("brief for %s failed: %v", u.ID(), err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	ok(w, map[string]any{"started": true, "running": true})
}

func (s *Server) setDone(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	var body struct {
		Done []int `json:"done"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := u.SetDone(r.PathValue("id"), body.Done); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	ok(w, u.Memory())
}

// remember keeps something the reader told the brief directly. Until now the
// only way into memory was the model deciding to write a note, so there was no
// way to correct it: a brief that kept handing you somebody else's work would
// keep doing it every morning.
func (s *Server) remember(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	var body struct {
		Text string `json:"text"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		fail(w, http.StatusBadRequest, errors.New("nothing to remember"))
		return
	}
	if err := u.Remember([]string{body.Text}, DayKey(u.Config().Now())); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, u.Memory()) // same shape as GET, so the two agree
}

// githubConnect takes the gh CLI's token, so GitHub is one click on a
// machine that already has gh signed in.
func (s *Server) githubConnect(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	if !ghAvailable() {
		fail(w, http.StatusNotFound, errors.New("the gh CLI is not on this server's PATH; paste a token instead"))
		return
	}
	login, err := ConnectGitHubViaGH(r.Context(), u)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	ok(w, map[string]any{"login": login, "via": "gh"})
}

// usage sums what Claude has cost lately, by day, model and purpose. A
// system that cannot say what it spends cannot be made cheaper.
func (s *Server) usage(w http.ResponseWriter, _ *http.Request, u *UserStore, _ *User) {
	now := u.Config().Now()
	today, week := DayKey(now), now.Add(-7*24*time.Hour)
	sum := func() map[string]any {
		return map[string]any{"calls": 0, "input": 0, "output": 0, "cacheRead": 0, "costUSD": 0.0}
	}
	add := func(m map[string]any, x Usage) {
		m["calls"] = m["calls"].(int) + 1
		m["input"] = m["input"].(int) + x.Input
		m["output"] = m["output"].(int) + x.Output
		m["cacheRead"] = m["cacheRead"].(int) + x.CacheRead
		m["costUSD"] = m["costUSD"].(float64) + x.CostUSD
	}
	out := map[string]any{"today": sum(), "last7": sum(), "byModel": map[string]any{}, "byPurpose": map[string]any{}}
	for _, x := range u.Usage() {
		if DayKey(x.At.In(now.Location())) == today {
			add(out["today"].(map[string]any), x)
		}
		if x.At.After(week) {
			add(out["last7"].(map[string]any), x)
			for field, key := range map[string]string{"byModel": x.Model, "byPurpose": x.Purpose} {
				group := out[field].(map[string]any)
				if group[key] == nil {
					group[key] = sum()
				}
				add(group[key].(map[string]any), x)
			}
		}
	}
	ok(w, out)
}

// refresh reads every source into the store without writing a brief. Like
// generate, it starts the job and answers; the page polls progress.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	if !s.limits.Allow("refresh:"+u.ID(), 6, time.Hour) {
		fail(w, http.StatusTooManyRequests, errors.New("six reads an hour is plenty; try again later"))
		return
	}
	if s.brief.Progress(u.ID()).Running() {
		w.WriteHeader(http.StatusAccepted)
		ok(w, map[string]any{"started": false, "running": true})
		return
	}
	go func() {
		ctx, done := context.WithTimeout(context.Background(), 15*time.Minute)
		defer done()
		if _, err := s.brief.Refresh(ctx, u); err != nil {
			log.Printf("refresh for %s failed: %v", u.ID(), err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	ok(w, map[string]any{"started": true, "running": true})
}

// signals lists what the store holds, newest first, for looking at. Bodies
// are clipped: this is for seeing what was gathered, not for reading Slack.
func (s *Server) signals(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	sig := u.Signals()
	since := time.Time{}
	if raw := r.URL.Query().Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			since = t
		}
	}
	rows := []map[string]any{}
	for _, it := range sig.Items {
		if !it.Time.After(since) {
			continue
		}
		rows = append(rows, map[string]any{
			"key": it.Key, "source": it.Source, "kind": it.Kind, "origin": it.Origin,
			"title": it.Title, "body": clip(it.Body, 200), "url": it.URL, "tags": it.Tags,
			"time": it.Time, "firstSeen": it.FirstSeen, "updates": it.Updates,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i]["time"].(time.Time).After(rows[j]["time"].(time.Time))
	})
	if len(rows) > 60 {
		rows = rows[:60]
	}
	ok(w, map[string]any{
		"refreshedAt": sig.RefreshedAt, "stored": len(sig.Items),
		"channels": len(sig.Channels), "threads": len(sig.Threads), "items": rows,
	})
}

// ownership is what was inferred, with the reasons, so it can be disagreed with.
func (s *Server) ownership(w http.ResponseWriter, _ *http.Request, u *UserStore, _ *User) {
	own := u.Ownership()
	rows := []map[string]any{}
	for _, place := range own.Ranked(40) {
		o := own[place]
		rows = append(rows, map[string]any{
			"origin": displayOrigin(place), "kind": o.Kind, "score": o.Score,
			"why": o.Why, "pinned": o.Pinned, "lastActive": o.LastActive,
		})
	}
	ok(w, rows)
}

func (s *Server) own(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	s.setOwnership(w, r, u, true)
}

func (s *Server) disown(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	s.setOwnership(w, r, u, false)
}

// setOwnership records what the reader said and recomputes the map at once,
// so the page shows the answer rather than a promise about tomorrow.
func (s *Server) setOwnership(w http.ResponseWriter, r *http.Request, u *UserStore, owns bool) {
	var body struct {
		Origin string `json:"origin"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	origin := strings.TrimSpace(body.Origin)
	if origin == "" {
		fail(w, http.StatusBadRequest, errors.New("which place?"))
		return
	}
	cfg := u.Config()
	if owns {
		cfg.Owns = addMute(cfg.Owns, origin) // same list semantics: once, normalised
		cfg.Disowns = dropMute(cfg.Disowns, origin)
	} else {
		cfg.Disowns = addMute(cfg.Disowns, origin)
		cfg.Owns = dropMute(cfg.Owns, origin)
	}
	if err := u.SetConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	own := InferOwnership(r.Context(), cfg, u.Signals(), u.Ownership(), cfg.Now())
	if err := u.SaveOwnership(own); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.ownership(w, r, u, nil)
}

// noteAttention records that the reader did something with an item, or said it
// did not belong. Both are worth more than any amount of guessing about what
// somebody wants, and both are what stops a place fading by mistake.
func (s *Server) noteAttention(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	var body struct {
		Origin string `json:"origin"`
		Signal string `json:"signal"` // "acted" or "rejected"
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Origin) == "" {
		ok(w, map[string]any{"noted": false}) // nothing to attribute it to
		return
	}

	now := time.Now()
	err := u.NoteAttention(func(l Ledger) {
		if body.Signal == "rejected" {
			l.Rejected(body.Origin, now)
			return
		}
		l.Acted(body.Origin, now)
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"noted": true})
}

// fadedPlaces is what stopped being gathered without anybody asking. Showing
// this is the price of doing it at all.
func (s *Server) fadedPlaces(w http.ResponseWriter, _ *http.Request, u *UserStore, _ *User) {
	ledger := u.Attention()
	out := []map[string]any{}
	for _, origin := range ledger.Faded() {
		a := ledger[origin]
		out = append(out, map[string]any{
			"origin": origin, "shown": a.Shown, "faded": a.Faded,
		})
	}
	ok(w, out)
}

func (s *Server) revivePlace(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	var body struct {
		Origin string `json:"origin"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := u.NoteAttention(func(l Ledger) { l.Revive(body.Origin) }); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"revived": body.Origin})
}

// Muting a place, and unmuting it. A mute you cannot undo is a trap, so the
// two always ship together.
func (s *Server) getMutes(w http.ResponseWriter, _ *http.Request, u *UserStore, _ *User) {
	ok(w, u.Config().Mutes)
}

func (s *Server) mute(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	s.setMutes(w, r, u, addMute)
}

func (s *Server) unmute(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	s.setMutes(w, r, u, dropMute)
}

func (s *Server) setMutes(w http.ResponseWriter, r *http.Request, u *UserStore, change func([]string, string) []string) {
	var body struct {
		Origin string `json:"origin"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Origin) == "" {
		fail(w, http.StatusBadRequest, errors.New("no place named"))
		return
	}

	cfg := u.Config()
	cfg.Mutes = change(cfg.Mutes, body.Origin)
	if err := u.SetConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, cfg.Mutes)
}

func (s *Server) forget(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	var body struct {
		Text string `json:"text"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := u.Forget(body.Text); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) lock(w http.ResponseWriter, r *http.Request, _ *UserStore, _ *User) {
	// Locking without a passphrase is a lever any account could pull on
	// everyone: the server would just pick the key back up, after a gap.
	if !s.passphraseMode || s.hosted {
		fail(w, http.StatusConflict, errors.New("this server has no passphrase to lock with"))
		return
	}
	s.vault.Lock()
	ok(w, map[string]any{"ok": true})
}

// The client secret is never sent back out; the page only needs to know
// whether one is set.
func (s *Server) getServerSettings(w http.ResponseWriter, r *http.Request, _ *UserStore, _ *User) {
	settings := s.store.ServerSettings()
	ok(w, map[string]any{
		"slack": map[string]any{
			"clientId":    settings.Slack.ClientID,
			"configured":  settings.Slack.Configured(),
			"redirectURI": s.redirectURI(r),
		},
	})
}

func (s *Server) putServerSettings(w http.ResponseWriter, r *http.Request, _ *UserStore, _ *User) {
	// On a hosted server the Slack app is the operator's, set in the
	// environment: an account that could rewrite it could point everyone's
	// sign-in at an app of its own.
	if s.hosted || os.Getenv("POMONA_SLACK_CLIENT_ID") != "" || os.Getenv("POMONA_SLACK_CLIENT_SECRET") != "" {
		fail(w, http.StatusConflict, errors.New("the Slack app is set by whoever runs this server"))
		return
	}
	var body ServerSettings
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	// An empty secret means "leave it alone", so saving the page doesn't wipe it.
	if body.Slack.ClientSecret == "" {
		body.Slack.ClientSecret = s.store.ServerSettings().Slack.ClientSecret
	}
	if err := s.store.SetServerSettings(body); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

func (s *Server) account(w http.ResponseWriter, r *http.Request, _ *UserStore, user *User) {
	ok(w, map[string]any{
		"user":    publicUser(user),
		"devices": s.pairing.DevicesFor(user.ID),
	})
}

// forgetEverything drops this account's briefs and notes. The point of the
// button is that it works without asking anyone.
func (s *Server) forgetEverything(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	if err := u.ForgetEverything(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

// deleteAccount is leaving: every file, every token, every browser. Nothing
// is kept and nothing is asked.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, _ *UserStore, user *User) {
	if err := s.pairing.RevokeUser(user.ID); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.DeleteUser(user.ID); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"deleted": true})
}

// claudeTest asks the account's Claude one tiny question, so a bad key or
// token is found at setup rather than at twenty to seven tomorrow.
func (s *Server) claudeTest(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	if !s.limits.Allow("claudetest:"+u.ID(), 20, time.Hour) {
		fail(w, http.StatusTooManyRequests, errors.New("too many tests; wait a while"))
		return
	}
	ctx, done := context.WithTimeout(r.Context(), 60*time.Second)
	defer done()
	cfg := u.Config()
	written, err := AskClaude(ctx, cfg, Ask{
		System: "Answer with the single word: ready", Prompt: "Are you there?",
		Models: cfg.Small(), MaxTokens: 16, Purpose: "test",
	})
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	ok(w, map[string]any{"ok": true, "model": written.Model})
}

// slackChannels lists the rooms the token can see, for choosing which ones
// Pomona must never read. Names only.
func (s *Server) slackChannels(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	settings := u.Config().Sources["slack"]
	if settings == nil || settings["token"] == "" {
		fail(w, http.StatusPreconditionFailed, errors.New("connect Slack first"))
		return
	}
	rooms, err := slackRooms(r.Context(), settings)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	ok(w, rooms)
}

// signOutOthers ends every other browser signed into this account.
func (s *Server) signOutOthers(w http.ResponseWriter, r *http.Request, _ *UserStore, user *User) {
	dropped, err := s.pairing.RevokeOthers(user.ID, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"signedOut": dropped})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *UserStore, _ *User) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err := s.pairing.Revoke(token); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}

// ── Plumbing ────────────────────────────────────────────

func decode(r *http.Request, into any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(into); err != nil {
		return errors.New("that request wasn't valid JSON")
	}
	return nil
}

func ok(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // tokens travel in some of these
	_ = json.NewEncoder(w).Encode(payload)
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
