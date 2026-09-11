/**
 * The extension's one job: have the brief open when you open the browser.
 *
 * The server writes it before the hour you asked for. This watches for it,
 * and puts it in a tab: at that hour if the browser is open, or the moment
 * the browser starts if it was not. Two mornings ago it was only ever a
 * notification; the page you actually wanted was a click away.
 */
import { status, getBrief, getConfig } from "./lib/api.js";

const POLL_ALARM = "watch-for-brief";
const MORNING_ALARM = "morning";
const BRIEF_URL = "src/brief/brief.html";
const WELCOME_URL = "src/welcome/welcome.html";

chrome.runtime.onInstalled.addListener(async (details) => {
  await ensureWatching();
  if (details.reason === "install") {
    await chrome.tabs.create({ url: chrome.runtime.getURL(WELCOME_URL) });
    return;
  }
  await checkNow({ starting: false });
});

chrome.runtime.onStartup.addListener(async () => {
  await ensureWatching();
  // The browser just opened. If this morning's page is written, it goes in
  // front: this is the whole point.
  await checkNow({ starting: true });
});

async function ensureWatching() {
  await chrome.alarms.create(POLL_ALARM, { periodInMinutes: 5 });
  await scheduleMorning();
}

/** One alarm at the ready-by time, refreshed from the server's config daily. */
async function scheduleMorning() {
  let config;
  try {
    config = await getConfig();
  } catch {
    return; // not paired yet; the poll will pick it up
  }
  const when = nextReadyTime(config);
  if (when) await chrome.alarms.create(MORNING_ALARM, { when: when.getTime() });
}

/** The next moment the brief should be ready, in the reader's own timezone. */
export function nextReadyTime(config, now = new Date()) {
  const time = config?.schedule?.time || "07:00";
  const [h, m] = time.split(":").map(Number);
  if (!Number.isFinite(h) || !Number.isFinite(m)) return null;
  const tz = config?.profile?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone;
  // Walk forward from now until the local time reads h:m and, if weekdays
  // only, the day is one. Minute steps are cheap and avoid DST arithmetic.
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: tz, hour12: false, weekday: "short", hour: "2-digit", minute: "2-digit",
  });
  let t = new Date(Math.ceil(now.getTime() / 60000) * 60000 + 60000);
  for (let i = 0; i < 60 * 24 * 8; i++, t = new Date(t.getTime() + 60000)) {
    const bits = Object.fromEntries(parts.formatToParts(t).map((p) => [p.type, p.value]));
    const hour = Number(bits.hour) % 24;
    if (hour !== h || Number(bits.minute) !== m) continue;
    if (config?.schedule?.weekdaysOnly && (bits.weekday === "Sat" || bits.weekday === "Sun")) continue;
    return t;
  }
  return null;
}

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name === POLL_ALARM) await checkNow({ starting: false });
  if (alarm.name === MORNING_ALARM) {
    await checkNow({ starting: false, morning: true });
    await scheduleMorning(); // tomorrow's
  }
});

/**
 * Is there a brief for today that this browser has not shown yet? Then show
 * it. "Today" is judged on the brief's own id, which is the reader's day.
 */
async function checkNow({ starting, morning = false }) {
  const state = await status();
  if (!state.reachable || state.locked || !state.paired) return;

  let brief;
  try {
    brief = await getBrief("latest");
  } catch {
    return; // nothing written yet
  }
  if (!brief?.id) return;

  const { shown = "", prefs = {} } = await chrome.storage.local.get(["shown", "prefs"]);
  if (shown === brief.id) return;

  const config = await getConfig().catch(() => null);
  const today = dayIn(config?.profile?.timezone);
  if (brief.id !== today) return; // an old brief is not this morning's

  await chrome.storage.local.set({ shown: brief.id });

  // In front when the browser has just opened and there is nothing to
  // interrupt; behind the current tab when the person is mid-something.
  const active = starting || (morning && prefs.openTab !== "background");
  if (prefs.openTab !== "never") await openBrief(brief.id, active);
  if (prefs.notify !== false && !active) {
    const day = new Date(brief.createdAt).toLocaleDateString("en-US", { weekday: "long" });
    await chrome.notifications.create(`brief-${brief.id}`, {
      type: "basic",
      iconUrl: chrome.runtime.getURL("icons/128.png"),
      title: `The ${day} Brief`,
      message: brief.data?.header?.greeting?.slice(0, 180) ?? "Your brief is ready.",
    });
  }
}

/** YYYY-MM-DD as the reader's clock reads it. */
export function dayIn(tz, now = new Date()) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: tz || undefined, year: "numeric", month: "2-digit", day: "2-digit",
  }).formatToParts(now);
  const bits = Object.fromEntries(parts.map((p) => [p.type, p.value]));
  return `${bits.year}-${bits.month}-${bits.day}`;
}

chrome.notifications.onClicked.addListener(async (id) => {
  if (id.startsWith("brief-")) await openBrief(id.slice("brief-".length), true);
});

async function openBrief(id, active) {
  const url = chrome.runtime.getURL(`${BRIEF_URL}${id ? `?id=${id}` : ""}`);
  const existing = await chrome.tabs.query({ url: chrome.runtime.getURL(`${BRIEF_URL}*`) });
  if (existing.length) {
    await chrome.tabs.update(existing[0].id, { url, active });
    return existing[0].id;
  }
  const tab = await chrome.tabs.create({ url, active });
  return tab.id;
}

// The options page asks for this when the server address or schedule changes.
chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message?.type === "check") {
    checkNow({ starting: false })
      .then(() => sendResponse({ ok: true }))
      .catch((error) => sendResponse({ ok: false, error: error.message }));
    return true;
  }
  if (message?.type === "reschedule") {
    scheduleMorning()
      .then(() => sendResponse({ ok: true }))
      .catch((error) => sendResponse({ ok: false, error: error.message }));
    return true;
  }
  return false;
});
