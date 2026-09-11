# What Pomona reads, keeps, and sends

The same text the server serves at `/privacy`.

## What it reads

**Slack.** Only what the tier you picked allows, and Slack enforces that: the token is issued with the scopes for that tier and nothing else.

| Tier | Scopes asked for | Which means |
|---|---|---|
| Public channels only | `users:read`, `channels:read`, `channels:history` | public channels you are in, and your own name |
| Public channels and DMs | + `im:read`, `im:history`, `mpim:read`, `mpim:history` | + your direct messages and group DMs |
| Public and private channels | + `groups:read`, `groups:history` | + private channels you are in |
| Everything I can see | all of the above + `search:read` | + Slack search, which finds a mention of you in any room in one call |

Channels you tick as off limits are never fetched at all. Direct messages are read only on a tier that includes them.

**GitHub.** Pull requests waiting on your review, issues assigned to you, your open pull requests, notifications you are part of, the repositories you can push to, and releases in ones you own. Read-only, with a token you give it.

**Calendar.** Today and tomorrow, from a private iCal link you paste. No Google account access.

**Linear.** Issues assigned to you and ones you follow that moved, with a personal API key.

## What it keeps

- **Signals.** Clipped excerpts of what it read, at most five hundred characters for a message and about two hundred per line of a conversation, so tomorrow can tell what is new from what it already saw. Deleted three days after they were last seen. Slack user ids are turned into names before storage; the member list is never stored.
- **Briefs.** The pages it wrote, for as many days as you choose in settings. Seven by default, thirty at most.
- **Notes.** Up to two dozen short notes it learned across mornings, and what you told it to forget. All visible and deletable in settings.
- **Tokens.** Your source tokens and your Anthropic key or Claude Code token, in your account's own encrypted file. A Claude Code token is handed to a Claude Code process that runs for your brief alone, through its environment, never written to disk.

Every one of those files is encrypted at rest with AES-256-GCM under a key derived per account from the server's master key. On a hosted server the master key lives in the process environment, not on the disk beside the data.

## What goes to Claude

Two or three calls a morning. A small model reads every new excerpt, three hundred characters each, and sorts them; if any to-do from an earlier morning is undated, the same model is asked once whether it still stands. Then the writing model sees at most thirty excerpts, ranked, plus your calendar for today and tomorrow, your last three briefs' to-dos, and the names of the places it worked out you own. When you connect a source it also asks the small model to pick your name, role and timezone out of your Slack and GitHub profiles. None of them ever sees a channel list, a member list, or anything from a room you ticked off.

On your own machine with a Claude subscription that goes through the local Claude Code command line with session persistence off, so nothing is written to its logs. With an API key it goes to `api.anthropic.com` over TLS. The only other outbound calls are to the sources you connected, any custom URL you added yourself, and the Cleveland Museum of Art's open-access API for the day's painting.

## What it cannot promise

Whoever runs the server can read the key out of the process, because the process needs it to read your Slack and call Claude at seven in the morning while you are asleep. A hosted Pomona is a trust in whoever hosts it. If that is not a trust you want to make, run it on your own machine: it is one binary, and the setup is the same.

## Leaving

Settings has three buttons: sign out this browser, sign out every other browser, and delete the account, which removes every file above and every device token at once. Revoking the Slack token from Slack's side works too.
