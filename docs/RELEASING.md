# Releasing

The version lives in `VERSION` and nowhere else by hand. `manifest.json` and `server/writer.go` are copies of it, written by the bump.

## A release

1. Add a `## x.y.z` section to `CHANGELOG.md`.
2. `node dev/release.mjs bump x.y.z` writes the version into the three files, commits `vx.y.z`, and tags it.
3. `git push origin master vx.y.z`.

That push does two things on its own:

- **The server.** Orchard watches `master` and rebuilds the image from the `Dockerfile` on every push that touches `server/`, `src/`, `assets.go`, `go.mod`, `manifest.json`, `icons/` or the Dockerfile. pomona.leafd.dev is on the new build a few minutes later. The data volume and the vault key survive the restart, so nobody signs in again.
- **The extension and binaries.** The `release` workflow builds `pomona-{darwin,linux}-{arm64,amd64}`, zips the extension from a clean list (`manifest.json`, `src/`, `icons/`, `LICENSE`, `PRIVACY.md`), and attaches everything to a GitHub Release with that version's changelog section as the notes.

People on the extension load the new zip unpacked over the old one. Chrome keeps the extension id, so the server still knows the browser.

## Checks before tagging

- `go test ./server/` and `node dev/check.mjs` are what CI runs on every push; run them first.
- `node dev/e2e.mjs --no-claude` walks a real server through pairing, both sign-in doors, linking, deletion, restart and hosted mode. `node dev/e2e.mjs` also writes a real brief with your Claude.
- If the Slack scopes changed, the Slack app needs reinstalling: `node dev/slack-app.mjs all --host pomona.leafd.dev` prints the manifest.

## Rolling back

Orchard keeps the previous image; `rollback_deployment` in the Orchard UI or MCP puts it back. For the extension, the previous release zip is still on GitHub.
