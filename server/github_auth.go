package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitHub without pasting anything.
//
// On the reader's own machine the gh CLI is usually already signed in, and
// `gh auth token` prints a token that works. That is one click, and it is
// the right default for a loopback server: anything that can reach it could
// already run gh itself. The token is broader than a read-only PAT would be
// (gh asks for repo, read:org, workflow); Pomona only ever reads with it, and
// the page says so.

var ghBinary = "gh" // overridden in tests

func ghAvailable() bool {
	_, err := exec.LookPath(ghBinary)
	return err == nil
}

// ghToken asks the gh CLI for its token. No shell: the binary is exec'd
// directly with fixed arguments.
func ghToken(ctx context.Context) (string, error) {
	ctx, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ghBinary, "auth", "token")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("gh auth token: %s", truncate(detail, 200))
	}
	token := strings.TrimSpace(out.String())
	if token == "" {
		return "", errors.New("gh is installed but not signed in: run `gh auth login` first")
	}
	return token, nil
}

// ConnectGitHubViaGH takes the gh CLI's token, checks it works, and records
// it as this account's GitHub source. Returns the login it belongs to.
func ConnectGitHubViaGH(ctx context.Context, u *UserStore) (string, error) {
	token, err := ghToken(ctx)
	if err != nil {
		return "", err
	}
	var me struct {
		Login string `json:"login"`
	}
	if err := githubGet(ctx, token, "/user", &me); err != nil || me.Login == "" {
		return "", errors.New("gh's token was not accepted by GitHub; try `gh auth refresh`")
	}

	cfg := u.Config()
	if cfg.Sources["github"] == nil {
		cfg.Sources["github"] = map[string]string{}
	}
	cfg.Sources["github"]["token"] = token
	cfg.Sources["github"]["enabled"] = "true"
	cfg.Sources["github"]["via"] = "gh"
	cfg.Sources["github"]["login"] = me.Login
	if err := u.SetConfig(cfg); err != nil {
		return "", err
	}
	inferSoon(u)
	return me.Login, nil
}
