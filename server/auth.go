package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Getting an account on a server that is not on your machine.
//
// On loopback nobody has to prove anything: a browser there could read the
// data directory itself. Anywhere else there are two doors and no passwords.
// Slack, because the person is about to connect it anyway and one consent
// screen is enough. Or a six digit code sent to an email address, because not
// everyone wants to hand over Slack before they have seen a single brief.
//
// Both end the same way: a device token for this browser, exactly as pairing
// does. The third piece, linking, is how a browser extension gets that token
// without anyone copying it: the extension opens the sign-in page in a popup,
// the page hands back a one-time code, and the extension trades it in.

// ── Email codes ─────────────────────────────────────────

const (
	otpTTL      = 10 * time.Minute
	otpAttempts = 5
)

type otpEntry struct {
	salt     []byte
	hash     []byte
	expires  time.Time
	attempts int
}

type OTPs struct {
	mu    sync.Mutex
	codes map[string]*otpEntry
}

func NewOTPs() *OTPs { return &OTPs{codes: map[string]*otpEntry{}} }

// Issue mints a code for an address, replacing any outstanding one. Only the
// hash is kept: a server that stores codes in the clear has turned one
// email leak into a login.
func (o *OTPs) Issue(email string) (string, error) {
	code, err := sixDigits()
	if err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for key, e := range o.codes {
		if time.Now().After(e.expires) {
			delete(o.codes, key)
		}
	}
	o.codes[normaliseEmail(email)] = &otpEntry{
		salt: salt, hash: hashCode(salt, code), expires: time.Now().Add(otpTTL),
	}
	return code, nil
}

// Check burns the code on success and counts the failures.
func (o *OTPs) Check(email, code string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := normaliseEmail(email)
	e := o.codes[key]
	if e == nil || time.Now().After(e.expires) {
		delete(o.codes, key)
		return errors.New("that code has expired: ask for a new one")
	}
	if e.attempts >= otpAttempts {
		delete(o.codes, key)
		return errors.New("too many wrong codes: ask for a new one")
	}
	if subtle.ConstantTimeCompare(hashCode(e.salt, strings.TrimSpace(code)), e.hash) != 1 {
		e.attempts++
		return fmt.Errorf("that code isn't right (%d attempts left)", otpAttempts-e.attempts)
	}
	delete(o.codes, key)
	return nil
}

func hashCode(salt []byte, code string) []byte {
	sum := sha256.Sum256(append(append([]byte{}, salt...), []byte(code)...))
	return sum[:]
}

// ── Link codes ──────────────────────────────────────────

const linkTTL = 90 * time.Second

type linkEntry struct {
	state   string
	userID  string
	expires time.Time
}

type Links struct {
	mu    sync.Mutex
	codes map[string]linkEntry
}

func NewLinks() *Links { return &Links{codes: map[string]linkEntry{}} }

// Issue mints a code the signed-in page hands to the extension. It is bound
// to the state the extension chose, so a code minted for one browser cannot
// be redeemed by another that happens to see it.
func (l *Links) Issue(state, userID string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw)
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, e := range l.codes {
		if time.Now().After(e.expires) {
			delete(l.codes, key)
		}
	}
	l.codes[code] = linkEntry{state: state, userID: userID, expires: time.Now().Add(linkTTL)}
	return code, nil
}

// Claim trades a code for the account it was minted for, once.
func (l *Links) Claim(code, state string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, found := l.codes[code]
	delete(l.codes, code)
	if !found || time.Now().After(e.expires) {
		return "", errors.New("that link expired: try connecting again")
	}
	if subtle.ConstantTimeCompare([]byte(e.state), []byte(state)) != 1 {
		return "", errors.New("that link was started by a different browser")
	}
	return e.userID, nil
}

// The only places a link may send a token-bearing code: Chrome's extension
// auth-flow origin, or a loopback address for a browser that lacks it.
var linkRedirect = regexp.MustCompile(`^(https://[a-p]{32}\.chromiumapp\.org/|http://(127\.0\.0\.1|localhost)(:\d+)?/)`)

func validLinkRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && linkRedirect.MatchString(raw) && u.Fragment == ""
}

// ── Handlers ────────────────────────────────────────────

// emailStart sends a code. It answers the same whether or not the address
// has an account, so it cannot be used to find out who is here.
func (s *Server) emailStart(w http.ResponseWriter, r *http.Request) {
	if !mailConfigured() {
		fail(w, http.StatusNotFound, errors.New("this server can't send email; sign in with Slack"))
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	email := normaliseEmail(body.Email)
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \r\n") {
		fail(w, http.StatusBadRequest, errors.New("that doesn't look like an email address"))
		return
	}
	if !s.limits.Allow("otp:ip:"+clientIP(r), 10, time.Minute) || !s.limits.Allow("otp:"+email, 1, time.Minute) ||
		!s.limits.Allow("otp:all", 120, time.Hour) {
		fail(w, http.StatusTooManyRequests, errors.New("a code was sent a moment ago; check your inbox, or wait a minute"))
		return
	}
	code, err := s.otps.Issue(email)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	text := fmt.Sprintf("Your Pomona sign-in code is %s\n\nIt works for ten minutes, once. If you didn't ask for it, ignore this.", code)
	if err := sendMail(r.Context(), email, "Your Pomona code: "+code, text); err != nil {
		fail(w, http.StatusBadGateway, errors.New("the code couldn't be sent: "+err.Error()))
		return
	}
	ok(w, map[string]any{"sent": true})
}

// emailVerify trades a code for a device token, making the account if this
// is the first time that address has been here.
func (s *Server) emailVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.vault.Locked() {
		fail(w, http.StatusLocked, ErrLocked)
		return
	}
	if !s.limits.Allow("verify:ip:"+clientIP(r), 20, time.Minute) {
		fail(w, http.StatusTooManyRequests, errors.New("too many tries; wait a minute"))
		return
	}
	if err := s.otps.Check(body.Email, body.Code); err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	user := s.store.UserByEmail(body.Email)
	if user == nil {
		// No password: this account is entered with a code every time.
		created, err := s.store.CreateUser(body.Email, "", randomSecret())
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		user = created
	}
	token, err := s.pairing.Issue(user.ID, deviceName(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"token": token, "user": publicUser(user)})
}

// slackSignIn starts Slack for someone with no account yet. The tier they
// picked decides the scopes, so the consent screen is the whole permission
// story: nothing is asked for later that was not shown here.
func (s *Server) slackSignIn(w http.ResponseWriter, r *http.Request) {
	app := s.store.ServerSettings().Slack
	if !app.Configured() {
		http.Error(w, "this server has no Slack app configured", http.StatusPreconditionFailed)
		return
	}
	if !s.limits.Allow("slack:ip:"+clientIP(r), 20, time.Minute) {
		http.Error(w, "too many tries; wait a minute", http.StatusTooManyRequests)
		return
	}
	access := r.URL.Query().Get("tier")
	switch access {
	case AccessPublic, AccessDMs, AccessPrivate, AccessAll:
	default:
		access = AccessPublic
	}
	next := safeNext(r.URL.Query().Get("next"))
	state, nonce, err := s.oauth.BeginSignIn(access, next)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setOAuthCookie(w, r, nonce)
	query := url.Values{
		"client_id":    {app.ClientID},
		"user_scope":   {strings.Join(ScopesFor(access), ",")},
		"redirect_uri": {s.redirectURI(r)},
		"state":        {state},
	}
	authorize := slackAuthorize
	if workspace := slackDomain(defaultWorkspace(r)); workspace != "" {
		authorize = fmt.Sprintf("https://%s.slack.com/oauth/v2/authorize", workspace)
	}
	http.Redirect(w, r, authorize+"?"+query.Encode(), http.StatusFound)
}

// defaultWorkspace is the Slack the sign-in page sends people to: the one
// asked for, else the one this server was set up for.
func defaultWorkspace(r *http.Request) string {
	if ws := r.URL.Query().Get("workspace"); ws != "" {
		return ws
	}
	return envOr("POMONA_SLACK_WORKSPACE", "")
}

// linkStart is called by the signed-in page the extension opened. It mints
// the code and says where to send the browser.
func (s *Server) linkStart(w http.ResponseWriter, r *http.Request, _ *UserStore, user *User) {
	var body struct {
		State       string `json:"state"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(body.State) < 16 || !validLinkRedirect(body.RedirectURI) {
		fail(w, http.StatusBadRequest, errors.New("that link request isn't one a browser extension would make"))
		return
	}
	if s.hosted && !strings.HasPrefix(body.RedirectURI, "https://") {
		fail(w, http.StatusBadRequest, errors.New("on a hosted server a link can only go back to the extension"))
		return
	}
	if !s.limits.Allow("link:"+user.ID, 10, time.Hour) {
		fail(w, http.StatusTooManyRequests, errors.New("too many links; wait an hour"))
		return
	}
	code, err := s.links.Issue(body.State, user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	target, _ := url.Parse(body.RedirectURI)
	q := target.Query()
	q.Set("state", body.State)
	q.Set("code", code)
	target.RawQuery = q.Encode()
	ok(w, map[string]any{"url": target.String()})
}

// pairExchange is the extension trading its link code for a token.
func (s *Server) pairExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State string `json:"state"`
		Code  string `json:"code"`
		Name  string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if s.vault.Locked() {
		fail(w, http.StatusLocked, ErrLocked)
		return
	}
	if !s.limits.Allow("exchange:ip:"+clientIP(r), 20, time.Minute) {
		fail(w, http.StatusTooManyRequests, errors.New("too many tries; wait a minute"))
		return
	}
	userID, err := s.links.Claim(body.Code, body.State)
	if err != nil {
		fail(w, http.StatusUnauthorized, err)
		return
	}
	user := s.store.UserByID(userID)
	if user == nil {
		fail(w, http.StatusUnauthorized, errors.New("that account no longer exists"))
		return
	}
	token, err := s.pairing.Issue(user.ID, deviceName(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"token": token, "user": publicUser(user)})
}

// pairCode mints a six digit code for the signed-in account, to type into
// a browser that cannot run the link flow. It used to be printed to the
// server's terminal, which is not a place anyone on a hosted server can see.
func (s *Server) pairCode(w http.ResponseWriter, r *http.Request, _ *UserStore, user *User) {
	code, until, err := s.pairing.Begin(user.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"code": code, "expires": until})
}

// ── Small helpers ───────────────────────────────────────

// safeNext is where a sign-in may land afterwards: one of our own pages,
// with its query, and nothing else. A prefix check on "/" let "/\evil.com"
// through, which every browser reads as "//evil.com", and the token rode
// along in the fragment. So the path is matched exactly, the query is
// re-encoded from parsed parts, and anything odd becomes the front door.
func safeNext(raw string) string {
	if strings.ContainsAny(raw, "\\\r\n\t ") {
		return "/welcome"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return "/welcome"
	}
	switch u.Path {
	case "/", "/welcome", "/link", "/settings":
	default:
		return "/welcome"
	}
	out := url.URL{Path: u.Path, RawQuery: u.Query().Encode()}
	return out.String()
}

// clientIP is the address to rate-limit on. Behind a proxy the socket is
// the proxy, so the forwarded headers have to be read, but a client can
// write those headers too, so only what a proxy in front of us appended is
// believed: Cloudflare's own header first, else the rightmost forwarded
// address that is not a private one, else the socket. Direct-to-origin
// traffic can still choose its own key, which is why every door also has a
// per-target limit.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	viaProxy := ip != nil && !publicIP(ip)
	if !viaProxy {
		return host
	}
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" && net.ParseIP(cf) != nil {
		return cf
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[i]))
		if candidate != nil && publicIP(candidate) {
			return candidate.String()
		}
	}
	return host
}

// randomSecret is a password nobody will ever type: accounts made by a code
// or a Slack sign-in have no password path at all.
func randomSecret() string {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
