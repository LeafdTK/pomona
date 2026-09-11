/**
 * Makes the Slack app Pomona needs.
 *
 *   node dev/slack-app.mjs [tier]           print a link that pre-fills Slack's form
 *   node dev/slack-app.mjs [tier] --create  create it outright, via the manifest API
 *
 * tier is one of: public (default), dms, private, all. It decides the scopes
 * the app asks for, and it should match what you chose in Pomona's Slack
 * source. Narrower is better: you can always make another app.
 *
 * The manifest API needs an app configuration token. `slack login` mints one
 * and leaves it in ~/.slack/credentials.json, so --create reads it from there;
 * pass a token as the argument instead if you have one from the browser.
 *
 * Pomona reads Slack with exactly one scope. `search.messages` is user-token
 * only, which is why this is a user scope and not a bot one, and it is the
 * narrowest thing that can answer "what did I miss".
 */

/**
 * The user scopes each tier needs. These mirror ScopesFor() in
 * server/source_slack.go, which is the source of truth: reading histories
 * honours the narrow scopes, and search:read is asked for on the widest tier
 * only, because search.messages refuses anything narrower.
 */
const BASE = ["users:read", "channels:read", "channels:history"];
const DMS = ["im:read", "im:history", "mpim:read", "mpim:history"];
const PRIVATE = ["groups:read", "groups:history"];
const TIERS = {
  public: { scopes: BASE, says: "public channels" },
  dms: { scopes: [...BASE, ...DMS], says: "public channels and DMs" },
  private: { scopes: [...BASE, ...PRIVATE], says: "public and private channels" },
  all: { scopes: [...BASE, ...DMS, ...PRIVATE, "search:read"], says: "everything you can see" },
};

// Where Slack may send people back. Local always; a hosted server too, if
// named: `node dev/slack-app.mjs all --host pomona.leafd.dev`.
const hostArg = process.argv.indexOf("--host");
const hosted = hostArg > 0 ? process.argv[hostArg + 1] : "";

const tier = process.argv.slice(2).find((a) => a in TIERS) ?? "public";
const { scopes, says } = TIERS[tier];

const manifest = {
  display_information: {
    name: "Pomona",
    description: `Reads your own mentions in ${says}, so your morning brief knows what you missed.`,
    background_color: "#b0394a",
  },
  oauth_config: {
    // Registered up front so there's nothing to add by hand afterwards.
    redirect_urls: [
      "http://127.0.0.1:7777/api/slack/callback",
      "http://localhost:7777/api/slack/callback",
      ...(hosted ? [`https://${hosted}/api/slack/callback`] : []),
    ],
    scopes: { user: scopes },
  },
  settings: {
    // Leave this false. Slack will not let you turn it back off, and an
    // org-deploy app cannot be installed to a single workspace.
    org_deploy_enabled: false,
    socket_mode_enabled: false,
    token_rotation_enabled: false,
  },
};

const token = await resolveToken(process.argv.slice(2).find((a) => a === "--create" || a.startsWith("xoxe.")));

/** Prefer an explicit token, then whatever `slack login` left behind. */
async function resolveToken(given) {
  if (given && given.startsWith("xoxe.")) return given;
  if (given !== "--create") return "";

  const { readFile } = await import("node:fs/promises");
  const { homedir } = await import("node:os");
  const { join } = await import("node:path");

  let credentials;
  try {
    credentials = JSON.parse(await readFile(join(homedir(), ".slack", "credentials.json"), "utf8"));
  } catch {
    throw new Error("No Slack CLI credentials found. Run `slack login` first, or pass a config token.");
  }

  // Keyed by team or enterprise id; take the freshest one that hasn't expired.
  const usable = Object.values(credentials)
    .filter((entry) => entry?.token?.startsWith("xoxe.") && (!entry.exp || entry.exp * 1000 > Date.now()))
    .sort((a, b) => (b.exp ?? 0) - (a.exp ?? 0));

  if (!usable.length) throw new Error("Your Slack CLI token has expired. Run `slack login` again.");
  console.log(`Using the Slack CLI login for ${usable[0].team_domain}.slack.com`);
  return usable[0].token;
}

if (!token) {
  const url = "https://api.slack.com/apps?new_app=1&manifest_json=" + encodeURIComponent(JSON.stringify(manifest));
  console.log(`\nTier: ${tier} (${says})\n`);
  console.log("Manifest:\n");
  console.log(JSON.stringify(manifest, null, 2));
  console.log("\nOpen this to create the app with the manifest already filled in:\n");
  console.log(url);
  console.log("\nThen: Create → Install to Workspace → Allow.");
  console.log("Copy the User OAuth Token (xoxp-…) into Pomona's Slack source.\n");
  console.log("To do it without the browser instead, grab a configuration token from");
  console.log("api.slack.com/apps (Your App Configuration Tokens) and re-run:");
  console.log("  node dev/slack-app.mjs xoxe.xoxp-…\n");
  process.exit(0);
}

const res = await fetch("https://slack.com/api/apps.manifest.create", {
  method: "POST",
  headers: { "content-type": "application/json; charset=utf-8", authorization: `Bearer ${token}` },
  body: JSON.stringify({ manifest }),
});
const body = await res.json();

if (!body.ok) {
  console.error(`\nSlack said no: ${body.error}`);
  if (body.errors) console.error(JSON.stringify(body.errors, null, 2));
  if (body.error === "invalid_auth" || body.error === "not_authed") {
    console.error("\nThat needs a configuration token (xoxe.xoxp-…), not a bot or user token.");
    console.error("api.slack.com/apps → Your App Configuration Tokens → Generate.");
  }
  process.exit(1);
}

console.log(`\nCreated "${manifest.display_information.name}" (${body.app_id}) for ${says}.`);
console.log("\nInstall it and copy the User OAuth Token from here:");
console.log(`  https://api.slack.com/apps/${body.app_id}/install-on-team\n`);
