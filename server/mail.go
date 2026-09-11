package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// One kind of email: a sign-in code. Sent through Resend, which is a single
// HTTPS call and needs no SMTP anything. With no key set the email door is
// simply not there, and the sign-in page does not draw it.

// resendBase is where Resend lives; a test points it at a local server.
func resendBase() string { return envOr("POMONA_RESEND_BASE", "https://api.resend.com") }

func mailConfigured() bool { return os.Getenv("POMONA_RESEND_KEY") != "" }

func mailFrom() string { return envOr("POMONA_MAIL_FROM", "Pomona <pomona@orchard.my>") }

func sendMail(ctx context.Context, to, subject, text string) error {
	key := os.Getenv("POMONA_RESEND_KEY")
	if key == "" {
		return errors.New("no mail sender configured")
	}
	body, _ := json.Marshal(map[string]any{
		"from": mailFrom(), "to": []string{to}, "subject": subject, "text": text,
	})
	req, err := newRequest(ctx, "POST", resendBase()+"/emails", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		raw, _ := readAll(res.Body)
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &payload)
		if payload.Message == "" {
			payload.Message = fmt.Sprintf("resend answered %d", res.StatusCode)
		}
		return errors.New(payload.Message)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
