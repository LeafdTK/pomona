package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Connecting Slack by clicking a button rather than pasting a token.
//
// The server holds one Slack app's credentials, set up once by whoever runs it.
// Everyone using that server then just clicks Connect: Slack asks them what
// they're allowing, and hands the server a token that belongs to them alone.
//
// The consent screen shows exactly the scopes for the tier they picked, so
// "public channels only" really does mean Slack will not return a DM.

const slackAuthorize = "https://slack.com/oauth/v2/authorize"
const slackAccess = "https://slack.com/api/oauth.v2.access"

// A short-lived note of who started which OAuth run. The callback arrives from
// Slack without our own credentials, so the state parameter is the only thing
// tying it back to an account.
type pending struct {
	userID  string // empty for a sign-in: the account is made when Slack answers
	access  string
	next    string // where to send the browser afterwards
	browser string // hash of the cookie the starting browser was given
	expires time.Time
}

// The cookie that ties a Slack run to the browser that started it. Without
// it, a state minted in one browser could be finished in another: an
// attacker's connect link approved by a victim would put the victim's Slack
// token in the attacker's account, and a sign-in state completed by the
// attacker could sign a victim's browser into the attacker's account.
const oauthCookie = "pomona_oauth"

func hashNonce(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}

type OAuthStates struct {
	mu     sync.Mutex
	states map[string]pending
}

func NewOAuthStates() *OAuthStates {
	return &OAuthStates{states: map[string]pending{}}
}

// Begin starts a run for a signed-in account. It returns the state for the
// authorize URL and the nonce for the browser's cookie.
func (o *OAuthStates) Begin(userID, access string) (state, nonce string, err error) {
	return o.begin(pending{userID: userID, access: access})
}

// BeginSignIn is a run with no account behind it yet.
func (o *OAuthStates) BeginSignIn(access, next string) (state, nonce string, err error) {
	return o.begin(pending{access: access, next: next})
}

func (o *OAuthStates) begin(p pending) (string, string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	state := hex.EncodeToString(raw[:24])
	nonce := hex.EncodeToString(raw[24:])
	p.browser = hashNonce(nonce)

	o.mu.Lock()
	defer o.mu.Unlock()
	// Drop anything stale while we're here.
	for key, p := range o.states {
		if time.Now().After(p.expires) {
			delete(o.states, key)
		}
	}
	p.expires = time.Now().Add(10 * time.Minute)
	o.states[state] = p
	return state, nonce, nil
}

// Claim finishes a run: the state from Slack, and the nonce from the cookie
// of the browser that came back. Both have to match, once.
func (o *OAuthStates) Claim(state, nonce string) (pending, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, found := o.states[state]
	delete(o.states, state) // one use only
	if !found || time.Now().After(p.expires) {
		return pending{}, false
	}
	if subtle.ConstantTimeCompare([]byte(p.browser), []byte(hashNonce(nonce))) != 1 {
		return pending{}, false
	}
	return p, true
}

// setOAuthCookie gives the browser its half of the run: ten minutes,
// HttpOnly, sent only on top-level navigations to us, Secure whenever the
// request came over TLS.
func setOAuthCookie(w http.ResponseWriter, r *http.Request, nonce string) {
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: nonce, Path: "/", MaxAge: 600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

func oauthNonce(r *http.Request) string {
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// ── The two ends of the dance ───────────────────────────

// slackConnect hands back the URL to send the browser to.
func (s *Server) slackConnect(w http.ResponseWriter, r *http.Request, u *UserStore, user *User) {
	app := s.store.ServerSettings().Slack
	if app.ClientID == "" || app.ClientSecret == "" {
		fail(w, http.StatusPreconditionFailed, errors.New(
			"this server has no Slack app configured yet; add one in settings and everyone here can connect with a click"))
		return
	}

	settings := u.Config().Sources["slack"]
	access := settings["access"]
	if access == "" {
		access = AccessPublic
	}
	state, nonce, err := s.oauth.Begin(user.ID, access)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	setOAuthCookie(w, r, nonce)

	query := url.Values{
		"client_id":    {app.ClientID},
		"user_scope":   {strings.Join(ScopesFor(access), ",")},
		"redirect_uri": {s.redirectURI(r)},
		"state":        {state},
	}
	// Sending someone to slack.com means a workspace picker first. Sending them
	// to their own workspace's host skips straight to the consent screen.
	authorize := slackAuthorize
	if workspace := slackDomain(settings["workspace"]); workspace != "" {
		authorize = fmt.Sprintf("https://%s.slack.com/oauth/v2/authorize", workspace)
	}
	ok(w, map[string]any{"url": authorize + "?" + query.Encode()})
}

// slackCallback is where Slack sends the browser back. It has no bearer token
// on it, so everything hangs off the state parameter.
func (s *Server) slackCallback(w http.ResponseWriter, r *http.Request) {
	p, valid := s.oauth.Claim(r.URL.Query().Get("state"), oauthNonce(r))

	// A connect lands back on settings; a sign-in lands wherever it began,
	// which is the setup page or the extension's link page.
	back := "/settings"
	if valid && p.userID == "" {
		back = p.next
	}
	finish := func(note string, fragment string) {
		target := back + "?connected=" + url.QueryEscape(note)
		if fragment != "" {
			target += "#" + fragment
		}
		http.Redirect(w, r, target, http.StatusFound)
	}

	if slackErr := r.URL.Query().Get("error"); slackErr != "" {
		finish("Slack said: "+slackErr, "")
		return
	}
	if !valid {
		finish("That Slack link expired, or was started in a different browser. Try again.", "")
		return
	}

	app := s.store.ServerSettings().Slack
	form := url.Values{
		"client_id":     {app.ClientID},
		"client_secret": {app.ClientSecret},
		"code":          {r.URL.Query().Get("code")},
		"redirect_uri":  {s.redirectURI(r)},
	}

	res, err := httpClient.PostForm(slackAccess, form)
	if err != nil {
		finish("Couldn't reach Slack: "+err.Error(), "")
		return
	}
	defer res.Body.Close()
	body, _ := readAll(res.Body)

	var payload struct {
		OK         bool   `json:"ok"`
		Error      string `json:"error"`
		AuthedUser struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
			Scope       string `json:"scope"`
		} `json:"authed_user"`
		Team struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Domain string `json:"domain"`
		} `json:"team"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || !payload.OK {
		finish(explainSlackError(payload.Error, string(body)), "")
		return
	}
	if payload.AuthedUser.AccessToken == "" {
		finish("Slack returned no user token. The app needs user scopes, not bot scopes.", "")
		return
	}

	// A sign-in: the Slack identity names the account, made now if it is
	// new, and this browser is signed into it. The token goes back to the
	// page in the fragment, which never reaches a server log.
	fragment := ""
	if p.userID == "" {
		identity := "slack:" + payload.Team.ID + ":" + payload.AuthedUser.ID
		user := s.store.UserByIdentity(identity)
		if user == nil {
			created, err := s.store.CreateIdentityUser(identity, slackHandle(r, payload.AuthedUser.AccessToken))
			if err != nil {
				finish("Couldn't make your account: "+err.Error(), "")
				return
			}
			user = created
		}
		token, err := s.pairing.Issue(user.ID, deviceName(r))
		if err != nil {
			finish("Couldn't sign this browser in: "+err.Error(), "")
			return
		}
		p.userID = user.ID
		fragment = "token=" + token
	}

	// Straight into that account's own config, encrypted with their own key.
	store := s.store.For(p.userID)
	defer inferSoon(store) // Slack can now say who this is
	cfg := store.Config()
	if cfg.Sources == nil {
		cfg.Sources = map[string]map[string]string{}
	}
	if cfg.Sources["slack"] == nil {
		cfg.Sources["slack"] = map[string]string{}
	}
	cfg.Sources["slack"]["token"] = payload.AuthedUser.AccessToken
	cfg.Sources["slack"]["access"] = p.access
	if domain := slackDomain(cfg.Sources["slack"]["workspace"]); domain == "" && payload.Team.Domain != "" {
		cfg.Sources["slack"]["workspace"] = payload.Team.Domain
	}
	cfg.Sources["slack"]["workspaceName"] = payload.Team.Name
	cfg.Sources["slack"]["enabled"] = "true"
	if err := store.SetConfig(cfg); err != nil {
		finish("Couldn't save that: "+err.Error(), fragment)
		return
	}

	finish(fmt.Sprintf("Connected to %s", payload.Team.Name), fragment)
}

// slackHandle asks Slack who a fresh token belongs to, for the account's
// display name. One call; a failure just means a blank name until the
// profile inference fills it.
func slackHandle(r *http.Request, token string) string {
	client := &slackClient{ctx: r.Context(), token: token, granted: map[string]bool{}, nextAt: map[string]time.Time{}}
	var me struct {
		User string `json:"user"`
	}
	if err := client.call("auth.test", nil, &me); err != nil {
		return ""
	}
	return me.User
}

// slackDomain accepts "hackclub", "hackclub.slack.com" or the full URL, and
// returns just the part Slack wants.
func slackDomain(raw string) string {
	name := strings.TrimSpace(strings.ToLower(raw))
	name = strings.TrimPrefix(strings.TrimPrefix(name, "https://"), "http://")
	name = strings.TrimSuffix(strings.TrimSuffix(name, "/"), ".slack.com")
	if name == "" || strings.ContainsAny(name, "/. ") {
		return ""
	}
	return name
}

// The redirect has to match what's registered on the Slack app exactly, so it
// is built from the address this request actually arrived on.
func (s *Server) redirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/slack/callback", scheme, r.Host)
}

// Slack's errors are terse and some of them are about your workspace rather
// than about you, which is worth saying out loud.
func explainSlackError(code, body string) string {
	switch code {
	case "scope_not_allowed_on_enterprise":
		return "Your Slack is an Enterprise Grid org, and it doesn't allow the permissions that tier " +
			"asks for. Set 'What Pomona may read' to Public channels only and connect again."
	case "access_denied":
		return "You declined that on Slack, so nothing was connected."
	case "invalid_scope", "invalid_scope_requested":
		return "Slack rejected those permissions. Try a narrower tier."
	case "bad_redirect_uri", "invalid_redirect_uri":
		return "The redirect URL doesn't match the one on your Slack app. Copy the one in settings into it."
	case "":
		return "Slack refused: " + clip(body, 120)
	default:
		return "Slack refused: " + code
	}
}

// slackDisconnect forgets the token without touching anything else.
func (s *Server) slackDisconnect(w http.ResponseWriter, r *http.Request, u *UserStore, _ *User) {
	cfg := u.Config()
	if cfg.Sources["slack"] != nil {
		delete(cfg.Sources["slack"], "token")
		delete(cfg.Sources["slack"], "workspace")
		cfg.Sources["slack"]["enabled"] = "false"
	}
	if err := u.SetConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"ok": true})
}
