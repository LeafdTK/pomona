# Changelog

## 0.2.0

The first release anyone but its author can run.

- Setup is one flow in both the extension and the server's own pages: pick a server, sign in, choose how much of Slack it may read, tick the rooms it must never read, hand it a Claude key if it needs one, pick the hour. Name, role and timezone are inferred from what you connect.
- Sign in with Slack or with a code sent by email. No passwords on a hosted server.
- The extension links to a server without copying anything, through Chrome's auth popup.
- The brief is ready *by* the hour you pick, not started at it, and the extension opens it the moment the browser starts if it was written while you were away.
- Old to-dos are judged again every morning: a deck for Tuesday's sync has lapsed by Wednesday, and an undated one is let go after two mornings.
- Ownership: a room shares a name with a repository you push to, so it is yours; a room you were just added to, or one that lit up, surfaces on its own.
- Every settings claim about what is stored is now true, and `/privacy` says all of it.
- A Dockerfile, a vault key from the environment, rate limits on every door, security headers, and a release workflow.

## 0.1.0

The morning it started working.
