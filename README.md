# Pomona

A morning brief, written from the things you're actually in the middle of. One
page a day: what to move, what's waiting on you, who you're seeing, and a
public-domain painting to look at while you read it.

Named for the goddess of orchards: she kept the fruit, you keep the day.

Two pieces. A **Go server** holds your sources, your schedule and your
credentials, and runs Claude. A **Chrome extension** pairs with it once, using a
six digit code the way you pair a phone to a television, and after that just
reads and renders.

## Why a server

The extension used to do everything itself, and it hit a wall: on a Claude
subscription the Messages API serves a browser token **Haiku 4.5 and nothing
else**. Opus and Sonnet come back `429 rate_limit_error` no matter how long you
wait, because it isn't a rate limit. Anthropic scopes those tokens to Claude
Code and checks the system prompt.

The server sidesteps that honestly: it runs the `claude` CLI you already have,
which *is* Claude Code, so every model is available and your plan covers it. No
API key, nothing spoofed.

## Running it

```bash
go build -o /usr/local/bin/pomona ./server
pomona
```

That's the install. It listens on `127.0.0.1:7777`, keeps its data in
`~/.pomona`, and prints one line:

```
  Open  http://127.0.0.1:7777
```

Open it. There is no account, no passphrase and no pairing code: a browser on
your own machine adopts itself, because anything local could already read
`~/.pomona`, so a code would be ceremony rather than security. Say who you are,
connect a source, press **Write one now**.

The Chrome extension is optional and gets you the same pages plus a toolbar
button: `chrome://extensions` → Developer mode → **Load unpacked** → this
folder. It finds the server and adopts itself the same way.

### If the server isn't on your machine

Reachable from elsewhere, it stops trusting the network and prints a six digit
code every two minutes, which you type once into the browser. Codes die after
five wrong guesses and burn on use.

### If you want a passphrase

```bash
pomona --passphrase
```

Then the key is wrapped under it rather than kept beside the data, and the
server starts locked after every restart. Worth knowing what you trade: a locked
server cannot write your 07:30 brief until someone is awake to unlock it.

## Sources

Everything is read-only and lives on the server.

| Source | What it needs | What it reads |
|---|---|---|
| **Calendar** | Your calendar's secret `.ics` address | Today and tomorrow's meetings, attendees, locations |
| **Slack** | A user token (`xoxp-`), scoped to what you pick | Mentions from the last day, in as much of Slack as you allow |
| **GitHub** | A PAT with `repo` + `notifications` | Review requests, assigned issues, your open PRs, notifications |
| **Linear** | A personal API key | Issues assigned to you, anything you follow that moved |
| **Anything else** | A URL | JSON, RSS/Atom, or plain text: a status page, a changelog, your own API |

Calendar is an `.ics` URL, not OAuth: Google Calendar → Settings → your calendar
→ *Secret address in iCal format*. Apple and Outlook publish the same. Recurring
meetings, exceptions, moved occurrences and timezones are handled by
`server/ics.go`, which has the tests to prove it.

A source that fails doesn't sink the brief. The others still run, and the
colophon says which one didn't answer.

## Encryption

Everything on disk is AES-256-GCM, with per-purpose keys derived by HKDF from a
master key. By default that key sits in the data directory at 0600, which still
protects the thing most likely to leak: a copy of the folder in a backup or a
synced drive. With `--passphrase` it is instead wrapped under PBKDF2-SHA256 at
600,000 iterations and never written down, and a cold server yields nothing.

```
Sebastian  in ciphertext: 0        health, cold    : locked=true
Zach       in ciphertext: 0        brief, locked   : 401
Orchard    in ciphertext: 0        wrong passphrase: rejected
```

**What neither mode does**, and cannot: hide the key from whoever runs the
process. The server does all the work, so it must hold usable credentials for
your sources and for Claude, and root on that machine can read them out of
memory. Run the server yourself and that is moot. No cryptography lets an
untrusted host make authenticated calls for you without being able to make them
for itself, so a hosted Pomona could never be end to end encrypted while also
writing your brief.

CORS admits only the extension and locally served pages, so a random website
can't reach a server on your loopback.

### Slack, as narrow as you like

Choose what Pomona may read, and `dev/slack-app.mjs` builds an app asking for
exactly those scopes and nothing more:

| Choice | Scopes |
|---|---|
| Public channels only | `search:read.public` |
| Public channels and DMs | `+ search:read.im`, `search:read.mpim` |
| Public and private channels | `+ search:read.private` |
| Everything you can see | all of those `+ search:read.files` |

```bash
node dev/slack-app.mjs dms            # print a link that pre-fills Slack's form
node dev/slack-app.mjs dms --create   # or create it outright, if you ran `slack login`
```

It's enforced twice: Slack refuses anything outside the token's scopes, and the
connector only runs the queries your choice allows, so a token that turns out
broader than you meant still isn't used that way.

Create the app **in a workspace, not an organisation**. If `slack login` put you
at organisation level, use the link rather than `--create`, and pick your
workspace from the dropdown. An org-owned app can't be installed to a single
workspace, and `org_deploy_enabled` cannot be turned off once set.

## Prompt injection

Everything a source returns is untrusted: a Slack message or an issue title can
carry text aimed at the model. Items are delimited and labelled as data, the
system prompt says to report instructions rather than follow them, and the
renderer never puts model output through `innerHTML`.

The server also runs Claude with **every built-in tool disabled**. This matters
more than it sounds: an empty allow-list does *not* disable them. Measured, the
model reached for Bash and ran `gh` against a real PR. Each tool is named in
`--disallowed-tools`, and `--setting-sources ""` keeps your CLAUDE.md, project
settings and MCP servers out of the brief.

## Working on it

```bash
go test ./server/...                      # vault, pairing, ICS
node dev/check.mjs                        # extension preflight
node dev/e2e.mjs                          # the whole thing, against a real server
go build -o /tmp/pomona ./server && /tmp/pomona &
node dev/make-preview.mjs && node dev/serve.mjs
```

The previews are the real extension pages talking to a real server over the same
HTTP API; only `chrome.*` is stubbed. Serve them rather than opening off disk,
since they're ES modules.

Editing the brief or options page needs a refresh. Editing `src/background.js`
needs the reload button on the extension card. Editing the server needs a
rebuild.

## Design

Orchard's crimson and warm greige, set classically: Didot for the plate titles,
Optima (drawn from Greek inscription) for anything in capitals, Roman numerals
on the movements, a running meander between them, laurel where something's been
won. Greek letterforms stand in for Latin ones in display capitals only
(`TØP TØ-DØS`), never in the brief's own words, which stay searchable.

Plates come from the Cleveland Museum of Art's open access collection: CC0, no
key, one per day, chosen deterministically so regenerating doesn't reshuffle the
art.
