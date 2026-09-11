/**
 * The whole thing, end to end, against a real server on a scratch port:
 * vault, pairing, config, a source, and (unless you pass --no-claude) a real
 * brief written by your own Claude.
 *
 *   node dev/e2e.mjs [--no-claude]
 */
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync, rmSync, readdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const PORT = 7791;
const CAL_PORT = 7792;
const API = `http://127.0.0.1:${PORT}/api`;
const PASSPHRASE = "an end to end passphrase";
const withClaude = !process.argv.includes("--no-claude");

const MAIL_PORT = 7793;
const dataDir = mkdtempSync(join(tmpdir(), "pomona-e2e-"));
const binary = join(dataDir, "pomona");
let server, calendar, failures = 0;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function check(label, passed, detail = "") {
  console.log(`  ${passed ? "ok  " : "FAIL"} ${label}${detail ? `  ${detail}` : ""}`);
  if (!passed) failures++;
}

async function call(path, { method = "GET", body, token } = {}) {
  const res = await fetch(API + path, {
    method,
    headers: { ...(body ? { "content-type": "application/json" } : {}), ...(token ? { authorization: `Bearer ${token}` } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, body: await res.json().catch(() => ({})) };
}

try {
  console.log("building…");
  execFileSync("go", ["build", "-o", binary, "./server"], { stdio: "inherit" });

  // A calendar to read, so the ICS path is exercised too.
  const cal = mkdtempSync(join(tmpdir(), "pomona-cal-"));
  const day = new Date().toISOString().slice(0, 10).replace(/-/g, "");
  writeFileSync(join(cal, "c.ics"), [
    "BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:1", "SUMMARY:1:1 with Zach",
    "DESCRIPTION:Builder redesign", `DTSTART;TZID=America/Chicago:${day}T140000`,
    `DTEND;TZID=America/Chicago:${day}T143000`, "ATTENDEE;CN=Zach Latta:mailto:z@x.com",
    "END:VEVENT", "END:VCALENDAR",
  ].join("\r\n"));
  calendar = spawn("python3", ["-m", "http.server", String(CAL_PORT)], { cwd: cal, stdio: "ignore" });

  // A stand-in for Resend: remembers the last code it was asked to send.
  const { createServer } = await import("node:http");
  let lastMail = null;
  const mailer = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      lastMail = JSON.parse(body);
      res.writeHead(200, { "content-type": "application/json" });
      res.end('{"id":"fake"}');
    });
  });
  await new Promise((r) => mailer.listen(MAIL_PORT, "127.0.0.1", r));
  const codeFrom = () => lastMail?.text?.match(/\b(\d{6})\b/)?.[1];

  const log = [];
  const env = { ...process.env, POMONA_RESEND_KEY: "re_fake", POMONA_RESEND_BASE: `http://127.0.0.1:${MAIL_PORT}` };
  server = spawn(binary, ["--addr", `127.0.0.1:${PORT}`, "--data", dataDir], { env });
  server.stdout.on("data", (c) => log.push(c.toString()));
  server.stderr.on("data", (c) => log.push(c.toString()));
  await sleep(1200);

  console.log("\nout of the box");
  const health = (await call("/health")).body;
  check("ready with no setup", health.locked === false && health.needsSetup === false);
  check("knows it's local", health.local === true);
  check("config still refused without a token", (await call("/config")).status === 401);
  check("no pairing code printed for a local server", !log.join("").includes("Pairing code"));
  check("says it is not hosted", health.hosted === false);
  check("knows its doors", health.auth?.email === true && typeof health.auth?.slack === "boolean");

  console.log("\nadopting this machine");
  const token = (await call("/pair/auto", { method: "POST", body: { name: "e2e" } })).body.token;
  check("a local client adopts itself", token?.length === 64);
  check("a made-up token is refused", (await call("/config", { token: "f".repeat(64) })).status === 401);
  check("the adopted token works", (await call("/config", { token })).status === 200);

  console.log("\nconfig and sources");
  const config = (await call("/config", { token })).body;
  config.profile = { name: "Sebastian", role: "Infra", focus: "Shipping Orchard v2.26.", timezone: "America/Chicago" };
  config.sources = { calendar: { enabled: "true", urls: `http://127.0.0.1:${CAL_PORT}/c.ics` } };
  config.claude = { mode: "subscription", apiKey: "", model: "claude-opus-5", effort: "high" };
  check("config saved", (await call("/config", { method: "PUT", body: config, token })).body.ok === true);
  const test = await call("/sources/test", { method: "POST", body: { id: "calendar" }, token });
  check("calendar source reads the ics", test.body.count === 1, test.body.sample ?? test.body.error ?? "");

  if (withClaude) {
    console.log("\nwriting a brief (this calls Claude for real)");
    const started = Date.now();
    const kicked = await call("/briefs", { method: "POST", token });
    check("the write starts and answers at once", kicked.status === 202 && kicked.body.running === true);
    let progress;
    do {
      await sleep(2000);
      progress = (await call("/briefs/progress", { token })).body;
    } while (progress?.stage && !["done", "failed"].includes(progress.stage) && Date.now() - started < 15 * 60_000);
    const { status, body } = progress?.stage === "done"
      ? await call(`/briefs/${progress.briefId}`, { token })
      : { status: 500, body: { error: progress?.error ?? "no progress" } };
    if (status !== 200) {
      check("brief written", false, body.error ?? String(status));
    } else {
      const data = body.data;
      check("brief written", true, `${body.model} in ${((Date.now() - started) / 1000).toFixed(1)}s`);
      check("greeting is present", Boolean(data.header?.greeting), (data.header?.greeting ?? "").slice(0, 70));
      check("meeting time survived the round trip",
        [...(data.your_day?.morning ?? []), ...(data.your_day?.afternoon ?? [])].some((m) => m.time?.includes("2:00")));
      check("no em dashes", !/[—–]/.test(JSON.stringify(data)));
      check("every schema key present",
        ["header", "push_forward", "top_todos", "new_updates", "your_day", "looking_ahead"]
          .every((k) => k in data));
    }
  }

  console.log("\nencryption at rest");
  // Everything under the data directory, whatever the layout.
  const walk = (dir) =>
    readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
      e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)],
    );
  const files = walk(dataDir).filter((f) => !f.endsWith("pomona"));
  const onDisk = Buffer.concat(files.map((f) => readFileSync(f))).toString("latin1");
  check(`something was actually written (${files.length} files)`, files.length > 2);
  for (const secret of ["Sebastian", "Orchard", "Zach", "127.0.0.1", "alice's private focus", "alice@example.com"]) {
    check(`"${secret}" is not readable on disk`, !onDisk.includes(secret));
  }

  console.log("\nmultiple people, one server");
  const alice = await call("/signup", { method: "POST", body: { email: "alice@example.com", name: "Alice", password: "alice's password" } });
  const bob = await call("/signup", { method: "POST", body: { email: "bob@example.com", name: "Bob", password: "bob's password" } });
  check("two accounts created", alice.body.token && bob.body.token && alice.body.token !== bob.body.token);
  check("duplicate email refused", (await call("/signup", { method: "POST", body: { email: "ALICE@example.com", password: "whatever12" } })).status === 400);
  check("wrong password refused", (await call("/login", { method: "POST", body: { email: "alice@example.com", password: "nope" } })).status === 401);
  check("right password works", (await call("/login", { method: "POST", body: { email: "alice@example.com", password: "alice's password" } })).body.token?.length === 64);

  // Alice writes something; Bob must not see it.
  const aliceCfg = (await call("/config", { token: alice.body.token })).body;
  aliceCfg.profile = { name: "Alice", role: "", focus: "alice's private focus", timezone: "UTC" };
  await call("/config", { method: "PUT", body: aliceCfg, token: alice.body.token });
  const bobSees = (await call("/config", { token: bob.body.token })).body;
  check("Bob cannot see Alice's profile", bobSees.profile.name === "" && bobSees.profile.focus === "");
  check("Alice still sees her own", (await call("/config", { token: alice.body.token })).body.profile.name === "Alice");
  check("Bob sees none of Alice's briefs", (await call("/briefs", { token: bob.body.token })).body.length === 0);
  // Without Claude no brief was written, so "still its own" means "still none of Bob's".
  check("the original account's briefs are still its own", (await call("/briefs", { token })).body.length === (withClaude ? 1 : 0));
  check("auto-adopt refuses once there are several accounts",
    (await call("/pair/auto", { method: "POST", body: { name: "x" } })).status === 409);

  console.log("\npairing another browser with a code");
  const { code: pairCode } = (await call("/pair/code", { method: "POST", token: bob.body.token })).body;
  check("a signed-in account mints a six digit code", /^\d{6}$/.test(pairCode ?? ""));
  check("a wrong code is refused", (await call("/pair", { method: "POST", body: { code: "000000", name: "tv" } })).status === 401);
  const paired = (await call("/pair", { method: "POST", body: { code: pairCode, name: "tv" } })).body.token;
  check("the right code pairs a browser into that account", paired?.length === 64);
  check("and it is Bob's account, not anyone else's",
    (await call("/config", { token: paired })).body.profile.name === "" &&
    (await call("/briefs", { token: paired })).status === 200);
  check("a code works once", (await call("/pair", { method: "POST", body: { code: pairCode, name: "tv2" } })).status === 401);

  console.log("\nsigning in with an emailed code");
  check("a bad address is refused", (await call("/auth/email", { method: "POST", body: { email: "nope" } })).status === 400);
  check("a code is sent", (await call("/auth/email", { method: "POST", body: { email: "Carol@Example.com" } })).body.sent === true);
  check("it went to the address, lower-cased", lastMail?.to?.[0] === "carol@example.com");
  check("a second ask straight away is rate limited", (await call("/auth/email", { method: "POST", body: { email: "carol@example.com" } })).status === 429);
  check("a wrong code is refused", (await call("/auth/email/verify", { method: "POST", body: { email: "carol@example.com", code: "000000" } })).status === 401);
  const carol = await call("/auth/email/verify", { method: "POST", body: { email: "carol@example.com", code: codeFrom() } });
  check("the right code makes the account and signs in", carol.body.token?.length === 64 && carol.body.user?.email === "carol@example.com");
  check("the code is spent", (await call("/auth/email/verify", { method: "POST", body: { email: "carol@example.com", code: codeFrom() } })).status === 401);
  check("carol has no password path", (await call("/login", { method: "POST", body: { email: "carol@example.com", password: "" } })).status === 401);

  console.log("\nlinking an extension");
  const state = "0123456789abcdef0123456789abcdef";
  const redirect = "https://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org/link";
  check("a stranger's redirect is refused",
    (await call("/link", { method: "POST", body: { state, redirect_uri: "https://evil.example/" }, token: carol.body.token })).status === 400);
  const linked = (await call("/link", { method: "POST", body: { state, redirect_uri: redirect }, token: carol.body.token })).body;
  const landed = new URL(linked.url ?? "https://x/");
  check("the page is sent back to the extension with a code", landed.origin === "https://abcdefghijklmnopabcdefghijklmnop.chromiumapp.org" && landed.searchParams.get("state") === state && Boolean(landed.searchParams.get("code")));
  const ext = await call("/pair/exchange", { method: "POST", body: { state, code: landed.searchParams.get("code"), name: "ext" } });
  check("the extension gets its own token for carol's account", ext.body.token?.length === 64 && ext.body.user?.email === "carol@example.com");
  check("the link code is spent",
    (await call("/pair/exchange", { method: "POST", body: { state, code: landed.searchParams.get("code"), name: "ext" } })).status === 401);
  // A code presented with the wrong state is refused, and burnt: whoever saw
  // it does not get a second guess at the state.
  const again = new URL((await call("/link", { method: "POST", body: { state, redirect_uri: redirect }, token: carol.body.token })).body.url);
  check("the wrong state cannot redeem a code",
    (await call("/pair/exchange", { method: "POST", body: { state: "other", code: again.searchParams.get("code"), name: "ext" } })).status === 401);
  check("and that burns it",
    (await call("/pair/exchange", { method: "POST", body: { state, code: again.searchParams.get("code"), name: "ext" } })).status === 401);

  console.log("\nleaving");
  check("delete needs to be signed in", (await call("/account/delete", { method: "POST" })).status === 401);
  check("carol deletes her account", (await call("/account/delete", { method: "POST", token: carol.body.token })).body.deleted === true);
  check("every one of her tokens is dead", (await call("/config", { token: ext.body.token })).status === 401);
  check("bob is untouched", (await call("/config", { token: bob.body.token })).status === 200);

  // Signing one device out mustn't touch another.
  await call("/logout", { method: "POST", token: alice.body.token });
  check("logout revokes that token", (await call("/config", { token: alice.body.token })).status === 401);
  check("and leaves other accounts alone", (await call("/config", { token: bob.body.token })).status === 200);

  console.log("\nthe web UI");
  const home = await fetch(`http://127.0.0.1:${PORT}/`);
  const homeHtml = await home.text();
  check("serves the brief page", home.status === 200 && homeHtml.includes("Tuesday Brief") === false && homeHtml.includes("brief.css"));
  check("rewrites asset paths", homeHtml.includes('href="/src/brief/brief.css"'));
  check("injects the chrome shim", homeHtml.includes("window.chrome"));
  check("serves its own css", (await fetch(`http://127.0.0.1:${PORT}/src/tokens.css`)).status === 200);
  check("settings page served", (await fetch(`http://127.0.0.1:${PORT}/settings`)).status === 200);
  check("welcome and link pages served", (await fetch(`http://127.0.0.1:${PORT}/welcome`)).status === 200 && (await fetch(`http://127.0.0.1:${PORT}/link`)).status === 200);
  const privacy = await fetch(`http://127.0.0.1:${PORT}/privacy`);
  check("privacy page served with headers", privacy.status === 200 && privacy.headers.get("content-security-policy")?.includes("default-src 'self'") && privacy.headers.get("x-frame-options") === "DENY");

  console.log("\nrestart");
  server.kill();
  await sleep(600);
  server = spawn(binary, ["--addr", `127.0.0.1:${PORT}`, "--data", dataDir], { stdio: "ignore", env });
  await sleep(1400);
  check("comes back ready, no unlocking", (await call("/health")).body.locked === false);
  check("the adopted browser is still known", (await call("/config", { token })).status === 200);
  check("yesterday's brief is still readable", (await call("/briefs", { token })).status === 200);

  console.log("\npassphrase mode (opt in)");
  server.kill();
  await sleep(400);
  const strictDir = mkdtempSync(join(tmpdir(), "pomona-strict-"));
  server = spawn(binary, ["--addr", `127.0.0.1:${PORT}`, "--data", strictDir, "--passphrase"], { stdio: "ignore" });
  await sleep(1300);
  check("starts locked and asks for setup", (await call("/health")).body.needsSetup === true);
  check("short passphrase rejected", (await call("/setup", { method: "POST", body: { passphrase: "abc" } })).status === 400);
  check("passphrase accepted", (await call("/setup", { method: "POST", body: { passphrase: PASSPHRASE } })).body.ok === true);
  const strictToken = (await call("/pair/auto", { method: "POST", body: { name: "e2e" } })).body.token;
  check("still adopts locally once unlocked", strictToken?.length === 64);
  await call("/lock", { method: "POST", token: strictToken });
  check("locking shuts the door", (await call("/config", { token: strictToken })).status === 423);
  check("wrong passphrase refused", (await call("/unlock", { method: "POST", body: { passphrase: "nope" } })).status === 401);
  check("right passphrase opens it", (await call("/unlock", { method: "POST", body: { passphrase: PASSPHRASE } })).body.ok === true);
  rmSync(strictDir, { recursive: true, force: true });

  console.log("\nhosted mode");
  server.kill();
  await sleep(400);
  const hostedDir = mkdtempSync(join(tmpdir(), "pomona-hosted-"));
  const vaultKey = Buffer.from(Array.from({ length: 32 }, (_, i) => i)).toString("base64");
  server = spawn(binary, ["--addr", `0.0.0.0:${PORT}`, "--data", hostedDir], { stdio: "ignore", env: { ...env, POMONA_VAULT_KEY: vaultKey } });
  await sleep(1300);
  const hostedHealth = (await call("/health")).body;
  check("knows it is hosted", hostedHealth.hosted === true && hostedHealth.locked === false);
  check("no key file on the volume: the key came from the environment", !readdirSync(hostedDir).includes("vault.json.key"));
  check("HSTS only behind TLS", !(await fetch(`http://127.0.0.1:${PORT}/`)).headers.get("strict-transport-security"));
  check("HSTS behind a TLS proxy", Boolean((await fetch(`http://127.0.0.1:${PORT}/`, { headers: { "x-forwarded-proto": "https" } })).headers.get("strict-transport-security")));
  const ownOrigin = await fetch(`http://127.0.0.1:${PORT}/api/config`, { method: "OPTIONS", headers: { origin: `http://127.0.0.1:${PORT}`, "access-control-request-method": "PUT" } });
  check("the server's own origin passes CORS", ownOrigin.headers.get("access-control-allow-origin") === `http://127.0.0.1:${PORT}`);
  rmSync(hostedDir, { recursive: true, force: true });
} finally {
  server?.kill();
  calendar?.kill();
  rmSync(dataDir, { recursive: true, force: true });
}

console.log(failures ? `\n${failures} failure(s)` : "\neverything passed");
process.exit(failures ? 1 : 0);
