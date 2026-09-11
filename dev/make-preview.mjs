/**
 * Renders the extension's pages outside Chrome, pointed at a real Pomona
 * server, so the UI can be iterated on without reloading the extension.
 *
 *   go build -o /tmp/pomona ./server && /tmp/pomona &
 *   node dev/make-preview.mjs && node dev/serve.mjs
 *
 * The only thing stubbed is chrome.* itself: the pages fetch the same HTTP API
 * the real extension does.
 */
import { readFileSync, writeFileSync, existsSync } from "node:fs";

const server = process.env.POMONA_URL ?? "http://127.0.0.1:7777";
const token = existsSync("dev/.token") ? readFileSync("dev/.token", "utf8").trim() : "";

const stub = `<script>
  const store = { server: { url: ${JSON.stringify(server)}, token: ${JSON.stringify(token)} } };
  window.chrome = {
    storage: { local: {
      get: (key) => Promise.resolve(typeof key === "string" ? { [key]: store[key] } : store),
      set: (patch) => (Object.assign(store, patch), Promise.resolve()),
    } },
    runtime: {
      getURL: (path) => "/" + path,
      openOptionsPage: () => { location.href = "/dev/options.html"; },
      sendMessage: () => Promise.resolve({ ok: true }),
    },
    tabs: { create: ({ url }) => (location.href = url, Promise.resolve({ id: 1 })), query: () => Promise.resolve([]), update: () => Promise.resolve() },
    notifications: { create: () => Promise.resolve() },
    alarms: { create: () => Promise.resolve() },
  };
</script>`;

const build = (from, to, rewrites) => {
  let html = readFileSync(from, "utf8");
  for (const [a, b] of rewrites) html = html.replace(a, b);
  writeFileSync(to, html.replace("</head>", `${stub}\n</head>`));
  console.log("wrote", to);
};

build("src/brief/brief.html", "dev/preview.html", [
  ['href="brief.css"', 'href="../src/brief/brief.css"'],
  ['src="brief.js"', 'src="../src/brief/brief.js"'],
]);
build("src/options/options.html", "dev/options.html", [
  ['href="options.css"', 'href="../src/options/options.css"'],
  ['src="options.js"', 'src="../src/options/options.js"'],
]);
build("src/popup/popup.html", "dev/popup.html", [
  ['href="popup.css"', 'href="../src/popup/popup.css"'],
  ['src="popup.js"', 'src="../src/popup/popup.js"'],
]);
