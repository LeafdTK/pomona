import { paintBlossoms } from "../brief/icons.js";
import * as api from "../lib/api.js";

const $ = (id) => document.getElementById(id);
const setText = (el, value) => {
  if (el) el.textContent = value ?? "";
};

async function boot() {
  paintBlossoms();

  $("settings").addEventListener("click", () => chrome.runtime.openOptionsPage());

  const state = await api.status();
  if (!state.reachable || state.needsSetup || state.locked || !state.paired) {
    setText($("line"), needs(state));
    $("primary").textContent = "Set it up";
    $("primary").addEventListener("click", () => chrome.tabs.create({ url: chrome.runtime.getURL("src/welcome/welcome.html") }));
    $("write").hidden = true;
    return;
  }

  let latest = null;
  try {
    latest = await api.getBrief("latest");
  } catch {
    /* nothing written yet */
  }

  if (!latest) {
    setText($("line"), "Nothing written yet. The first brief takes a few minutes.");
    $("primary").textContent = "Write today's brief";
    $("primary").addEventListener("click", write);
    $("write").hidden = true;
    return;
  }

  const when = new Date(latest.createdAt);
  setText($("when"), when.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }));

  const today = new Date().toDateString() === when.toDateString();
  setText(
    $("line"),
    today
      ? (latest.data?.header?.greeting ?? "Today's brief is ready.").slice(0, 150)
      : `The most recent brief is from ${when.toLocaleDateString(undefined, { weekday: "long" })}.`,
  );

  $("primary").addEventListener("click", async () => {
    await chrome.tabs.create({ url: chrome.runtime.getURL(`src/brief/brief.html?id=${latest.id}`) });
    window.close();
  });
  $("write").addEventListener("click", write);

  const sources = $("sources");
  for (const source of latest.sources ?? []) {
    const li = document.createElement("li");
    li.className = source.ok ? "" : "is-down";
    setText(li, source.ok ? `${source.name} · ${source.count}` : `${source.name} · down`);
    if (!source.ok && source.error) li.title = source.error;
    sources.append(li);
  }
}

function needs(state) {
  if (!state.reachable) return "Can't reach your Pomona server. Set one up, or start yours with: pomona";
  if (state.needsSetup) return "Your server needs a passphrase before it can store anything.";
  if (state.locked) return "Your server is locked.";
  return "This browser isn't paired with your server yet.";
}

async function write() {
  const buttons = [$("primary"), $("write")];
  for (const button of buttons) button.disabled = true;
  setText($("line"), "Reading your sources…");
  $("line").classList.remove("is-error");
  try {
    const brief = await api.generate();
    await chrome.tabs.create({ url: chrome.runtime.getURL(`src/brief/brief.html?id=${brief.id}`) });
    window.close();
  } catch (error) {
    setText($("line"), error.message);
    $("line").classList.add("is-error");
    for (const button of buttons) button.disabled = false;
  }
}

boot();
