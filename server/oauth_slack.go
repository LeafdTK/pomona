package main

import (
	"crypto/rand"
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
	userID  string
	access  string
	expires time.Time
}

type OAuthStates struct {
	mu     sync.Mutex
	states map[string]pending
}

func NewOAuthStates() *OAuthStates {
	return &OAuthStates{states: map[string]pending{}}
}

func (o *OAuthStates) Begin(userID, access string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	state := hex.EncodeToString(raw)

	o.mu.Lock()
	defer o.mu.Unlock()
	// Drop anything stale while we're here.
	for key, p := range o.states {
		if time.Now().After(p.expires) {
			delete(o.states, key)
		}
	}
	o.states[state] = pending{userID: userID, access: access, expires: time.Now().Add(10 * time.Minute)}
	return state, nil
}

func (o *OAuthStates) Claim(state string) (pending, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, found := o.states[state]
	delete(o.states, state) // one use only
	if !found || time.Now().After(p.expires) {
		return pending{}, false
	}
	return p, true
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
	state, err := s.oauth.Begin(user.ID, access)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

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
	finish := func(note string) {
		http.Redirect(w, r, "/settings?connected="+url.QueryEscape(note), http.StatusFound)
	}

	if slackErr := r.URL.Query().Get("error"); slackErr != "" {
		finish("Slack said: " + slackErr)
		return
	}

	p, valid := s.oauth.Claim(r.URL.Query().Get("state"))
	if !valid {
		finish("That Slack link expired. Try Connect again.")
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
		finish("Couldn't reach Slack: " + err.Error())
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
			Name   string `json:"name"`
			Domain string `json:"domain"`
		} `json:"team"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || !payload.OK {
		finish(explainSlackError(payload.Error, string(body)))
		return
	}
	if payload.AuthedUser.AccessToken == "" {
		finish("Slack returned no user token. The app needs user scopes, not bot scopes.")
		return
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
		finish("Couldn't save that: " + err.Error())
		return
	}

	finish(fmt.Sprintf("Connected to %s", payload.Team.Name))
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
