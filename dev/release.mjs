/**
 * The release chores, so they cannot drift apart:
 *
 *   node dev/release.mjs bump 0.3.0     write the version everywhere, commit, tag
 *   node dev/release.mjs zip out.zip    the extension, from a clean list
 *   node dev/release.mjs notes 0.3.0    that version's section of CHANGELOG.md
 *
 * The version lives in VERSION, and is copied into manifest.json and
 * server/writer.go by `bump`, which is the only thing that should touch them.
 */
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const [command, arg] = process.argv.slice(2);

const read = (p) => readFileSync(resolve(root, p), "utf8");
const write = (p, s) => writeFileSync(resolve(root, p), s);

if (command === "bump") {
  const version = (arg ?? "").trim();
  if (!/^\d+\.\d+\.\d+$/.test(version)) fail("bump needs a version like 0.3.0");
  write("VERSION", version + "\n");
  write("manifest.json", read("manifest.json").replace(/"version": "[^"]+"/, `"version": "${version}"`));
  write("server/writer.go", read("server/writer.go").replace(/const version = "[^"]+"/, `const version = "${version}"`));
  if (!read("CHANGELOG.md").includes(`## ${version}`)) fail(`CHANGELOG.md has no "## ${version}" section yet`);
  execFileSync("git", ["add", "VERSION", "manifest.json", "server/writer.go", "CHANGELOG.md"], { cwd: root, stdio: "inherit" });
  execFileSync("git", ["commit", "-m", `v${version}`], { cwd: root, stdio: "inherit" });
  execFileSync("git", ["tag", `v${version}`], { cwd: root, stdio: "inherit" });
  console.log(`v${version}: now \`git push origin master v${version}\``);
} else if (command === "zip") {
  if (!arg) fail("zip needs an output path");
  const files = ["manifest.json", "src", "icons", "LICENSE", "PRIVACY.md"].filter((f) => existsSync(resolve(root, f)));
  execFileSync("zip", ["-r", "-q", "-X", resolve(arg), ...files, "-x", "*.DS_Store"], { cwd: root, stdio: "inherit" });
  console.log(`wrote ${arg}`);
} else if (command === "notes") {
  const version = (arg ?? "").trim();
  const log = read("CHANGELOG.md");
  const start = log.indexOf(`## ${version}`);
  if (start < 0) fail(`no "## ${version}" in CHANGELOG.md`);
  const rest = log.slice(start + `## ${version}`.length);
  const end = rest.search(/\n## /);
  process.stdout.write((end < 0 ? rest : rest.slice(0, end)).trim() + "\n");
} else {
  fail("usage: release.mjs bump <version> | zip <out.zip> | notes <version>");
}

function fail(message) {
  console.error(message);
  process.exit(1);
}
