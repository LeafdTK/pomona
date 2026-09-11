/**
 * Serves the repo so the previews can load as ES modules, which `file://`
 * refuses to do, and lands you on the brief instead of a directory listing.
 *
 *   node dev/serve.mjs          → http://localhost:8000/dev/preview.html
 *   node dev/serve.mjs 8080     → same, on another port
 */
import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { extname, join, normalize, resolve } from "node:path";
import { spawn } from "node:child_process";

const root = resolve(process.cwd());
const port = Number(process.argv[2]) || 8000;
const HOME = "/dev/preview.html";

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".ics": "text/calendar; charset=utf-8",
};

const server = createServer(async (req, res) => {
  const path = decodeURIComponent(new URL(req.url, "http://localhost").pathname);

  if (path === "/") {
    res.writeHead(302, { Location: HOME });
    return res.end();
  }

  // Keep the server inside the repo, path traversal and all.
  const file = join(root, normalize(path));
  if (!file.startsWith(root)) {
    res.writeHead(403);
    return res.end("Nope");
  }

  try {
    if ((await stat(file)).isDirectory()) {
      res.writeHead(404, { "content-type": "text/plain" });
      return res.end(`No index here. Try ${HOME}`);
    }
    const body = await readFile(file);
    res.writeHead(200, {
      "content-type": TYPES[extname(file)] ?? "application/octet-stream",
      // Previews are edited constantly; a cached one is a confusing one.
      "cache-control": "no-store",
    });
    res.end(body);
  } catch {
    res.writeHead(404, { "content-type": "text/plain" });
    res.end(`Not found: ${path}\n\nTry ${HOME} or /dev/options.html`);
  }
});

server.listen(port, () => {
  const base = `http://localhost:${port}`;
  console.log(`\n  the brief    ${base}${HOME}`);
  console.log(`  the options  ${base}/dev/options.html\n`);
  console.log("  ctrl-c to stop\n");
  if (process.platform === "darwin" && !process.env.NO_OPEN) {
    spawn("open", [`${base}${HOME}`], { stdio: "ignore", detached: true }).unref();
  }
});
