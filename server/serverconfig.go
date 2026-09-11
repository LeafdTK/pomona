package main

import (
	"encoding/json"
	"os"
)

// Settings that belong to the server rather than to any one account: the OAuth
// app credentials it brokers with. Set them once and everyone using this server
// connects Slack with a click.
type ServerSettings struct {
	Slack OAuthApp `json:"slack"`
}

type OAuthApp struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// Configured reports whether the click-to-connect flow is available.
func (a OAuthApp) Configured() bool { return a.ClientID != "" && a.ClientSecret != "" }

func (s *Store) ServerSettings() ServerSettings {
	var settings ServerSettings
	if raw, err := s.vault.OpenFrom(s.path("server.enc"), "server"); err == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	// Environment wins, so a deployment can hand these in without a UI.
	if id := os.Getenv("POMONA_SLACK_CLIENT_ID"); id != "" {
		settings.Slack.ClientID = id
	}
	if secret := os.Getenv("POMONA_SLACK_CLIENT_SECRET"); secret != "" {
		settings.Slack.ClientSecret = secret
	}
	return settings
}

func (s *Store) SetServerSettings(settings ServerSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.vault.SealTo(s.path("server.enc"), "server", raw)
}
