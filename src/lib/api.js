/**
 * The extension's whole relationship with the server.
 *
 * Everything that used to live in here, connectors, scheduling, the Claude
 * call, now lives in the Go server. The extension keeps exactly two things:
 * which server to talk to, and the device token it got when it paired.
 */

const DEFAULT_URL = "http://127.0.0.1:7777";

export async function connection() {
  const { server } = await chrome.storage.local.get("server");
  return { url: DEFAULT_URL, token: "", ...server };
}

export async function setConnection(patch) {
  const next = { ...(await connection()), ...patch };
  await chrome.storage.local.set({ server: next });
  return next;
}

export class ServerError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

/**
 * One request. Throws ServerError with the server's own words, because the
 * server already writes better messages than a status code does.
 */
async function request(path, options = {}) {
  const { method = "GET", body, auth = true, retried = false } = options;
  const { url, token } = await connection();
  if (auth && !token) throw new ServerError("This browser isn't paired with a Pomona server yet.", 401);

  let res;
  try {
    res = await fetch(url.replace(/\/$/, "") + path, {
      method,
      headers: {
        ...(body ? { "content-type": "application/json" } : {}),
        ...(auth && token ? { authorization: `Bearer ${token}` } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ServerError(`Can't reach the Pomona server at ${url}. Is it running?`, 0);
  }

  // A token the server no longer recognises: it was revoked, or the server
  // forgot it making room for other devices. On your own machine that is
  // recoverable without telling you anything, because signing in there needs
  // no secret. Once only, so a server that always says 401 still surfaces it.
  if (res.status === 401 && auth && !retried && (await resignIn())) {
    return request(path, { ...options, retried: true });
  }

  const payload = await res.json().catch(() => ({}));
  if (!res.ok) throw new ServerError(payload.error ?? `The server returned ${res.status}.`, res.status);
  return payload;
}

/**
 * Replace a token the server has rejected, where that can be done without
 * asking anyone. The old token is only dropped once there is a new one to put
 * in its place: throwing away credentials we cannot replace would turn a bad
 * request into a signed out browser.
 */
async function resignIn() {
  let info;
  try {
    info = await health();
  } catch {
    return false;
  }
  if (!info.local || info.locked || info.accounts > 1) return false;
  return adoptIfLocal(info);
}

// ── Getting in ──────────────────────────────────────────

export const health = () => request("/api/health", { auth: false });
export const createVault = (passphrase) => request("/api/setup", { method: "POST", body: { passphrase }, auth: false });
export const unlock = (passphrase) => request("/api/unlock", { method: "POST", body: { passphrase }, auth: false });

/** Trade the six digit code the server is showing for a lasting device token. */
export async function pair(code, name) {
  const { token } = await request("/api/pair", { method: "POST", body: { code, name }, auth: false });
  await setConnection({ token });
  return token;
}

export const unpair = () => setConnection({ token: "" });

export const signup = (body) => auth("/api/signup", body);
export const login = (body) => auth("/api/login", body);
export const account = () => request("/api/account");
export const forgetEverything = () => request("/api/account/forget", { method: "POST" });
/** End every other browser signed into this account. This one keeps working. */
export const signOutOtherDevices = () => request("/api/account/devices/others", { method: "POST" });

export async function logout() {
  await request("/api/logout", { method: "POST" }).catch(() => {});
  await setConnection({ token: "" });
}

async function auth(path, body) {
  const { token, user } = await request(path, { method: "POST", body, auth: false });
  await setConnection({ token });
  return user;
}

/**
 * On your own machine, with one account, there is nothing to prove: a browser
 * here could already read the server's data directory, so it signs itself in
 * and you never see a password. More than one account, or a server somewhere
 * else, and it has to ask.
 */
async function adoptIfLocal(info) {
  if (!info.local || info.locked || info.accounts > 1) return false;
  try {
    const { token } = await request("/api/pair/auto", {
      method: "POST",
      body: { name: navigator.userAgent.includes("Chrome") ? "Chrome" : "A browser" },
      auth: false,
    });
    await setConnection({ token });
    return true;
  } catch {
    return false;
  }
}

// ── Everything else ─────────────────────────────────────

export const getConfig = () => request("/api/config");
export const putConfig = (config) => request("/api/config", { method: "PUT", body: config });
export const getSources = () => request("/api/sources");
export const testSource = (body) => request("/api/sources/test", { method: "POST", body });
export const listBriefs = () => request("/api/briefs");
export const getBrief = (id = "latest") => request(`/api/briefs/${encodeURIComponent(id)}`);
export const generate = () => request("/api/briefs", { method: "POST" });
export const briefProgress = () => request("/api/briefs/progress");
/** Today's artwork, which the server can name before the brief exists. */
export const plate = () => request("/api/plate");
/** Who the connected sources say you are, so setup need not ask. */
export const guessProfile = () => request("/api/profile/guess");

/** Tell the server this place still matters, or that it does not. */
export const noteAttention = (origin, signal) =>
  request("/api/attention", { method: "POST", body: { origin, signal } });
export const fadedPlaces = () => request("/api/attention/faded");
export const revivePlace = (origin) =>
  request("/api/attention/revive", { method: "POST", body: { origin } });

/** Read every source into the store now, without writing a brief. */
export const refresh = () => request("/api/refresh", { method: "POST" });
export const signals = (since) => request(`/api/signals${since ? `?since=${encodeURIComponent(since)}` : ""}`);

/** What the brief thinks you own, with its reasons, and the two ways to disagree. */
export const ownership = () => request("/api/ownership");
export const own = (origin) => request("/api/ownership/own", { method: "POST", body: { origin } });
export const disown = (origin) => request("/api/ownership/disown", { method: "POST", body: { origin } });
export const setDone = (id, done) => request(`/api/briefs/${encodeURIComponent(id)}/done`, { method: "POST", body: { done } });
export const lock = () => request("/api/lock", { method: "POST" });
export const getServerSettings = () => request("/api/server");
export const putServerSettings = (body) => request("/api/server", { method: "PUT", body });
export const slackConnect = () => request("/api/slack/connect", { method: "POST" });
export const slackDisconnect = () => request("/api/slack/disconnect", { method: "POST" });
/** GitHub in one click, by reusing the gh CLI's token on this machine. */
export const githubConnect = () => request("/api/github/connect", { method: "POST" });
export const getMemory = () => request("/api/memory");
export const forget = (text) => request("/api/memory/forget", { method: "POST", body: { text } });
export const getMutes = () => request("/api/mutes");
/** Silence a place: a channel, a repo. Applied before anything is gathered. */
export const mute = (origin) => request("/api/mutes", { method: "POST", body: { origin } });
export const unmute = (origin) => request("/api/mutes/unmute", { method: "POST", body: { origin } });

/** Tell tomorrow's brief something today's got wrong. */
export const remember = (text) => request("/api/memory", { method: "POST", body: { text } });

/**
 * What the UI needs to know before it can show anything: are we pointed at a
 * server, is it awake, is it unlocked, are we paired with it.
 */
export async function status() {
  const { url, token } = await connection();
  let info;
  try {
    info = await health();
  } catch (error) {
    return { url, reachable: false, paired: Boolean(token), error: error.message };
  }

  let paired = Boolean(token);
  if (!paired) paired = await adoptIfLocal(info);

  return { url, reachable: true, ...info, paired };
}
