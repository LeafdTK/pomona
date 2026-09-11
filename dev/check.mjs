/**
 * Preflight. Catches the things that make an unpacked extension fail quietly:
 * a manifest pointing at a file that moved, an import that doesn't resolve, a
 * chrome API used without the permission that unlocks it.
 *
 *   node dev/check.mjs
 */
import { readFileSync, existsSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve, relative } from "node:path";

const root = process.cwd();
const problems = [];
const note = (message) => problems.push(message);

// ── Manifest ────────────────────────────────────────────
const manifest = JSON.parse(readFileSync("manifest.json", "utf8"));

const declared = [
  manifest.background?.service_worker,
  manifest.options_page,
  manifest.action?.default_popup,
  ...Object.values(manifest.icons ?? {}),
  ...Object.values(manifest.action?.default_icon ?? {}),
].filter(Boolean);

for (const path of new Set(declared)) {
  if (!existsSync(path)) note(`manifest points at a missing file: ${path}`);
}

if (manifest.background && manifest.background.type !== "module") {
  note("background.type must be \"module\" for the service worker's ES imports to load");
}

// ── Every import resolves ───────────────────────────────
function walk(dir) {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    return statSync(path).isDirectory() ? walk(path) : [path];
  });
}

const scripts = walk("src").filter((path) => path.endsWith(".js"));
for (const file of scripts) {
  const source = readFileSync(file, "utf8");
  for (const match of source.matchAll(/^\s*(?:import|export)[^'"]*?from\s+['"]([^'"]+)['"]/gm)) {
    const spec = match[1];
    if (!spec.startsWith(".")) {
      note(`${file} imports a bare specifier "${spec}" — extensions have no bundler to resolve it`);
      continue;
    }
    if (!existsSync(resolve(dirname(file), spec))) {
      note(`${file} imports ${spec}, which doesn't exist`);
    }
  }
}

// ── HTML asset references ───────────────────────────────
for (const file of walk("src").filter((path) => path.endsWith(".html"))) {
  const source = readFileSync(file, "utf8");
  for (const match of source.matchAll(/(?:href|src)="([^"#][^"]*)"/g)) {
    if (!existsSync(resolve(dirname(file), match[1]))) note(`${file} references missing ${match[1]}`);
  }
  if (/<script(?![^>]*type="module")[^>]*\ssrc=/.test(source)) {
    note(`${file} has a non-module <script src>, which can't use import`);
  }
}

// ── CSS @import ─────────────────────────────────────────
for (const file of walk("src").filter((path) => path.endsWith(".css"))) {
  for (const match of readFileSync(file, "utf8").matchAll(/@import\s+url\(["']([^"']+)["']\)/g)) {
    if (!existsSync(resolve(dirname(file), match[1]))) note(`${file} @imports missing ${match[1]}`);
  }
}

// ── Permissions cover the APIs actually called ──────────
const allSource = scripts.map((file) => readFileSync(file, "utf8")).join("\n");
const permissions = new Set(manifest.permissions ?? []);
for (const [api, needed] of [
  ["chrome.storage.", "storage"],
  ["chrome.alarms.", "alarms"],
  ["chrome.notifications.", "notifications"],
  ["chrome.tabs.", "tabs"],
]) {
  // tabs.create/query/update on your own pages need no "tabs" permission.
  if (allSource.includes(api) && !permissions.has(needed) && needed !== "tabs") {
    note(`code calls ${api}* but "${needed}" isn't in manifest.permissions`);
  }
}

const hosts = manifest.host_permissions ?? [];
const fetched = [...allSource.matchAll(/https:\/\/([a-z0-9.-]+\.[a-z]{2,})/g)].map((m) => m[1]);
for (const host of new Set(fetched)) {
  const covered = hosts.some((pattern) => new RegExp(`^${pattern.replace(/[.]/g, "\\.").replace(/\*/g, ".*")}$`).test(`https://${host}/`));
  const optional = (manifest.optional_host_permissions ?? []).length > 0;
  if (!covered && !optional) note(`code fetches ${host} with no host_permission for it`);
}

// ── Report ──────────────────────────────────────────────
if (problems.length) {
  console.log(`${problems.length} problem(s):\n`);
  for (const problem of problems) console.log(`  ${problem}`);
  process.exit(1);
}
console.log(`ok: manifest, ${scripts.length} scripts, imports, assets and permissions all check out`);
console.log(`\nLoad unpacked from:\n  ${root}`);
