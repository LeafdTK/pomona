/**
 * The extension's only background job now: notice when the server has written
 * a new brief, and put it in front of you.
 *
 * The server owns the sources, the schedule and the writing. This watches.
 */
import { status, getBrief, connection } from "./lib/api.js";

const WATCH_ALARM = "watch-for-brief";
const BRIEF_URL = "src/brief/brief.html";

chrome.runtime.onInstalled.addListener(async (details) => {
  await ensureWatching();
  if (details.reason === "install") await chrome.runtime.openOptionsPage();
});

chrome.runtime.onStartup.addListener(ensureWatching);

async function ensureWatching() {
  await chrome.alarms.create(WATCH_ALARM, { periodInMinutes: 5 });
}

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name === WATCH_ALARM) await checkForNewBrief();
});

/**
 * Poll rather than push: a local server can't wake a service worker, and once
 * every five minutes is plenty for something that happens once a morning.
 */
async function checkForNewBrief() {
  const state = await status();
  if (!state.reachable || state.locked || !state.paired) return;

  let brief;
  try {
    brief = await getBrief("latest");
  } catch {
    return; // nothing written yet
  }

  const { announced } = await chrome.storage.local.get("announced");
  if (!brief?.id || announced === brief.id) return;
  await chrome.storage.local.set({ announced: brief.id });

  // Don't announce a brief that was already there when we started watching.
  if (announced === undefined) return;

  const day = new Date(brief.createdAt).toLocaleDateString("en-US", { weekday: "long" });
  await chrome.notifications.create(`brief-${brief.id}`, {
    type: "basic",
    iconUrl: chrome.runtime.getURL("icons/128.png"),
    title: `The ${day} Brief`,
    message: brief.data?.header?.greeting?.slice(0, 180) ?? "Your brief is ready.",
  });
  await openBrief(brief.id, false);
}

chrome.notifications.onClicked.addListener(async (id) => {
  if (id.startsWith("brief-")) await openBrief(id.slice("brief-".length));
});

async function openBrief(id, active = true) {
  const url = chrome.runtime.getURL(`${BRIEF_URL}${id ? `?id=${id}` : ""}`);
  const existing = await chrome.tabs.query({ url: chrome.runtime.getURL(`${BRIEF_URL}*`) });
  if (existing.length) {
    await chrome.tabs.update(existing[0].id, { url, active });
    return existing[0].id;
  }
  const tab = await chrome.tabs.create({ url, active });
  return tab.id;
}

// The options page asks for this when the server address changes.
chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message?.type !== "check") return false;
  checkForNewBrief()
    .then(() => sendResponse({ ok: true }))
    .catch((error) => sendResponse({ ok: false, error: error.message }));
  return true;
});
