import { paintBlossoms } from "../brief/icons.js";
import {
  getConfig, putConfig, getSources, slackConnect, githubConnect, status, health,
  connection, setConnection, inExtension, allowHost, linkViaBrowser, linkFinish,
  requestEmailCode, verifyEmailCode, slackSignInURL, slackChannels, getMutes, mute, unmute,
  LOCAL_URL, HOSTED_URL,
} from "../lib/api.js";

const $ = (id) => document.getElementById(id);
const setText = (el, value) => { if (el) el.textContent = value ?? ""; };

/**
 * Onboarding, as one object turning over.
 *
 * The same page runs in three places and knows which:
 *
 *   extension  opened by the extension after install; begins by choosing a
 *              server, signs into it through a popup, then carries on here.
 *   link       the page that popup shows, served by the server; only signs
 *              in, then hands a one-time code back to the extension.
 *   served     opened straight from the server; does the whole thing.
 *
 * Every step lives in the same shell. Moving between them measures the shell
 * before and after, then eases its height between the two while the old step
 * blurs out and the new one blurs in.
 */

const TIERS = [
  { value: "public", name: "Public channels only", reads: "Channels you are in that anyone can join, and your own name. Nothing private, no DMs." },
  { value: "public+dms", name: "Public channels and DMs", reads: "The above, plus your direct messages and group DMs." },
  { value: "public+private", name: "Public and private channels", reads: "The above, plus private channels you are in. No DMs." },
  { value: "all", name: "Everything I can see", reads: "Public, private and DMs, plus Slack search, which is what lets it find a mention of you in any of three hundred rooms in one call." },
];

let mode = "served";
let steps = [];
let at = 0;
let config = null;
let state = null;   // what the server said about itself
let tier = "public";
let link = null;    // { state, redirect_uri } when the extension started this
let rooms = [];     // Slack channels, for the never-read step

async function boot() {
  paintBlossoms();

  const query = new URLSearchParams(location.search);
  if (inExtension()) mode = "extension";
  else if (location.pathname === "/link" || query.get("link") === "1") {
    mode = "link";
    link = { state: query.get("state") ?? "", redirect_uri: query.get("redirect_uri") ?? "" };
    if (!link.state || !link.redirect_uri) mode = "served";
  }
  if (mode !== "extension") $("privacyLink").href = "/privacy";

  // Coming back from Slack with a fresh token in the fragment: keep it, and
  // never let it sit in the address bar.
  const fragment = new URLSearchParams(location.hash.replace(/^#/, ""));
  if (fragment.get("token")) {
    await setConnection({ token: fragment.get("token") });
    history.replaceState(null, "", location.pathname + location.search);
  }
  const connected = query.get("connected");

  state = await status().catch(() => null);
  if (state?.paired) config = await getConfig().catch(() => null);
  tier = config?.sources?.slack?.access || "public";

  // Where to start: after a Slack return, past the sign-in; otherwise at
  // the top, unless this person has plainly been here before.
  steps = plan();
  if (connected) {
    if (mode === "link") return finishLink();
    at = Math.max(steps.indexOf("never"), 0);
  } else if (config?.schedule?.set && mode !== "link") {
    at = steps.indexOf("never");
  }
  // ?step=read opens one step directly: for looking at it, and for the
  // screenshots in the readme.
  if (query.get("step") && document.getElementById(`step-${query.get("step")}`)) {
    at = Math.max(steps.indexOf(query.get("step")), 0);
    show(query.get("step"));
    return;
  }
  show(steps[at]);
  if (connected) note("signinStatus", connected) || note("whereStatus", connected);
}

/**
 * The steps this run needs. Recomputed after anything that changes the
 * answer: signing in, connecting Slack.
 */
function plan() {
  if (mode === "link") return ["read", "signin"];
  const out = [];
  if (mode === "extension") out.push("where");
  const paired = Boolean(state?.paired && config);
  const slackOn = config?.sources?.slack?.enabled === "true" && Boolean(config?.sources?.slack?.token);
  if (!paired) {
    // Served, off this machine: sign in here. In the extension the sign-in
    // happens inside the popup, so nothing to add.
    if (mode === "served" && !state?.local) out.push("read", "signin");
  } else if (!slackOn) {
    out.push("read", "sources");
  }
  out.push("never", "claude", "when", "done");
  return out;
}

// ── Moving between steps ────────────────────────────────

const still = matchMedia("(prefers-reduced-motion: reduce)").matches;

async function show(name, direction = 1) {
  const pane = $("pane");
  const shell = $("shell");
  const leaving = pane.firstElementChild;

  const from = shell.getBoundingClientRect().height;
  shell.style.setProperty("--shell-h", `${from}px`);

  const entering = document.getElementById(`step-${name}`).content.firstElementChild.cloneNode(true);
  entering.classList.add("is-entering");
  if (direction < 0) entering.style.transform = "translateY(-14px) scale(0.985)";

  if (leaving) leaving.classList.add("is-leaving");
  pane.append(entering);

  // Not awaited: a step that loads something (the channel list) must be
  // visible while it loads, not blurred at zero opacity until it is done.
  void wire(name, entering);
  marks();

  shell.style.removeProperty("--shell-h");
  const to = shell.offsetHeight;
  shell.style.setProperty("--shell-h", `${from}px`);
  void shell.offsetHeight;
  shell.style.setProperty("--shell-h", `${to}px`);

  entering.classList.remove("is-entering");
  entering.style.transform = "";

  await wait(still ? 0 : 480);
  leaving?.remove();
  shell.style.removeProperty("--shell-h");
}

const wait = (ms) => new Promise((go) => setTimeout(go, ms));

async function go(by) {
  const current = steps[at];
  steps = plan();
  let index = steps.indexOf(current);
  if (index < 0) index = Math.min(at, steps.length - 1);
  const next = Math.min(Math.max(index + by, 0), steps.length - 1);
  if (steps[next] === current) return;
  at = next;
  show(steps[at], by);
}

function marks() {
  const bar = $("progress");
  bar.replaceChildren(
    ...steps.map((_, i) => {
      const mark = document.createElement("span");
      if (i === at) mark.className = "is-here";
      else if (i < at) mark.className = "is-past";
      return mark;
    }),
  );
}

/** A status line on the current step, if it has one. Returns whether it did. */
function note(id, text, error = false) {
  const el = $(id);
  if (!el) return false;
  setText(el, text);
  el.classList.toggle("is-error", error);
  return true;
}

// ── What each step does ─────────────────────────────────

async function wire(name, step) {
  step.querySelector("[data-next]")?.addEventListener("click", () => save(name).then((ok) => ok !== false && go(1)));
  step.querySelector("[data-back]")?.addEventListener("click", () => go(-1));
  const back = step.querySelector("[data-back]");
  if (back && at === 0) back.hidden = true;
  if (back && at > 0) back.hidden = false;

  if (name === "where") wireWhere(step);
  if (name === "read") wireRead(step);
  if (name === "signin") wireSignIn(step);
  if (name === "sources") await renderPicks();
  if (name === "never") await wireNever(step);
  if (name === "claude") wireClaude(step);
  if (name === "when") {
    $("time").value = config?.schedule?.time || "07:00";
    $("weekdays").checked = config?.schedule?.weekdaysOnly ?? true;
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    setText($("whenBody"), `It starts reading twenty minutes before this, ${zone.replace(/_/g, " ")} time, so the page is finished when you open the browser, whether that is at seven or at quarter past nine.`);
  }
  if (name === "done") finish(step);
}

/** The reader's zone, from the only clock that is certainly theirs. */
async function keepZone() {
  if (!config?.profile) return;
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (!zone || config.profile.timezone === zone) return;
  if (config.profile.timezone && !config.profile.inferred?.timezone) return; // they typed one
  config.profile = { ...config.profile, timezone: zone };
  await putConfig(config).catch(() => {});
}

async function save(name) {
  if (name === "read") {
    tier = step$("tiers")?.querySelector("input:checked")?.value || tier;
    if (config) {
      config.sources.slack = { ...(config.sources.slack ?? {}), access: tier };
      await putConfig(config).catch(() => {});
    }
    return true;
  }
  if (name === "never") return saveNever();
  if (name === "claude") return saveClaude();
  if (name === "when" && config) {
    await keepZone();
    config.schedule = {
      ...config.schedule, enabled: true,
      time: $("time").value || "07:00", weekdaysOnly: $("weekdays").checked,
    };
    await putConfig(config).catch(() => {});
    if (mode === "extension") chrome.runtime.sendMessage({ type: "reschedule" }).catch(() => {});
    return true;
  }
  return true;
}

const step$ = (id) => document.getElementById(id);

// ── Where: which server (extension only) ────────────────

async function wireWhere(step) {
  const host = $("servers");
  const choices = [];
  const local = await fetch(`${LOCAL_URL}/api/health`).then((r) => r.ok).catch(() => false);
  if (local) choices.push({ url: LOCAL_URL, name: "This computer", blurb: "A Pomona server is running here. Nothing leaves this machine." });
  choices.push({ url: HOSTED_URL, name: "pomona.leafd.dev", blurb: "Hosted, always on, so the brief is ready even when this computer is asleep. Sign in with Slack or an emailed code." });
  choices.push({ url: "", name: "My own server", blurb: "Somewhere you run it yourself." });

  let chosen = choices[0];
  const paint = () => {
    host.replaceChildren();
    for (const c of choices) {
      const li = document.getElementById("tpl-pick").content.firstElementChild.cloneNode(true);
      li.classList.toggle("is-connected", c === chosen);
      setText(li.querySelector(".pick__name"), c.name);
      setText(li.querySelector(".pick__blurb"), c.blurb);
      setText(li.querySelector(".pick__state"), c === chosen ? "chosen" : "");
      li.querySelector(".pick__button").addEventListener("click", () => { chosen = c; paint(); });
      host.append(li);
    }
    $("customServer").hidden = chosen.url !== "";
  };
  paint();

  step.querySelector("#connectServer").addEventListener("click", async (event) => {
    const button = event.currentTarget;
    const url = (chosen.url || $("serverUrl").value.trim()).replace(/\/$/, "");
    if (!/^https?:\/\//.test(url)) return note("whereStatus", "That needs to start with https://", true);
    button.disabled = true;
    note("whereStatus", "Connecting…");
    try {
      if (!(await allowHost(url))) throw new Error("Chrome needs permission to talk to that address.");
      await setConnection({ url, token: "" });
      const info = await health();
      if (info.local && info.accounts <= 1) {
        state = await status(); // adopts itself on loopback
        if (!state.paired) throw new Error("Couldn't sign into the local server.");
      } else {
        await linkViaBrowser(url);
        state = await status();
      }
      config = await getConfig();
      tier = config?.sources?.slack?.access || tier;
      await keepZone();
      note("whereStatus", "");
      go(1);
    } catch (error) {
      note("whereStatus", error.message, true);
      button.disabled = false;
    }
  });
}

// ── Read: the Slack tier ────────────────────────────────

function wireRead(step) {
  const host = $("tiers");
  host.replaceChildren();
  for (const t of TIERS) {
    const li = document.getElementById("tpl-tier").content.firstElementChild.cloneNode(true);
    const radio = li.querySelector(".tier__radio");
    radio.value = t.value;
    radio.checked = t.value === tier;
    setText(li.querySelector(".tier__name"), t.name);
    setText(li.querySelector(".tier__reads"), t.reads);
    radio.addEventListener("change", () => { tier = t.value; });
    host.append(li);
  }
  if (mode === "link") step.querySelector("[data-back]").hidden = true;
}

// ── Sign in: Slack, or an emailed code ──────────────────

function wireSignIn(step) {
  const doors = state?.auth ?? {};
  $("slackDoor").hidden = !doors.slack;
  $("emailDoor").hidden = !doors.email;
  if (!doors.slack && !doors.email) {
    note("signinStatus", "This server has no way to sign anyone in yet: it needs a Slack app or a mail sender set up.", true);
  }
  if (doors.slack && !doors.email) setText($("signinBody"), "No password to invent. Slack signs you in and connects itself in the same click, with the permissions you just chose.");

  $("slackDoor").addEventListener("click", async () => {
    const next = mode === "link" ? location.pathname + location.search : "/welcome";
    location.href = await slackSignInURL(tier, next);
  });

  $("sendCode").addEventListener("click", async (event) => {
    const email = $("email").value.trim();
    if (!email.includes("@")) return note("signinStatus", "That needs an email address.", true);
    event.currentTarget.disabled = true;
    try {
      await requestEmailCode(email);
      note("signinStatus", `Sent. Check ${email}.`);
      $("codeDoor").hidden = false;
      $("code").focus();
    } catch (error) {
      note("signinStatus", error.message, true);
    } finally {
      event.currentTarget.disabled = false;
    }
  });

  $("verifyCode").addEventListener("click", async (event) => {
    event.currentTarget.disabled = true;
    try {
      await verifyEmailCode($("email").value.trim(), $("code").value.trim());
      state = await status();
      config = await getConfig();
      // The tier chosen a step ago waits on the account for when Slack is connected.
      config.sources.slack = { ...(config.sources.slack ?? {}), access: tier };
      await putConfig(config).catch(() => {});
      await keepZone();
      if (mode === "link") return finishLink();
      go(1);
    } catch (error) {
      note("signinStatus", error.message, true);
      event.currentTarget.disabled = false;
    }
  });
  $("code").addEventListener("keydown", (e) => { if (e.key === "Enter") $("verifyCode").click(); });
  $("email").addEventListener("keydown", (e) => { if (e.key === "Enter") $("sendCode").click(); });
}

/** Link mode's last act: hand the extension its code and get out of the way. */
async function finishLink() {
  try {
    const { url } = await linkFinish(link.state, link.redirect_uri);
    location.replace(url);
  } catch (error) {
    show("signin");
    note("signinStatus", error.message, true);
  }
}

// ── Sources ─────────────────────────────────────────────

async function renderPicks() {
  const host = $("picks");
  const [sources, fresh] = await Promise.all([getSources().catch(() => []), getConfig().catch(() => null)]);
  if (fresh) config = fresh;
  host.replaceChildren();

  for (const source of sources) {
    if (source.id === "custom") continue;
    const li = document.getElementById("tpl-pick").content.firstElementChild.cloneNode(true);
    const connected = config?.sources?.[source.id]?.enabled === "true";
    li.classList.toggle("is-connected", connected);
    setText(li.querySelector(".pick__name"), source.name);
    setText(li.querySelector(".pick__blurb"), source.blurb ?? "");
    setText(li.querySelector(".pick__state"), connected ? "on" : "connect");
    li.querySelector(".pick__button").addEventListener("click", () => connect(source));
    host.append(li);
  }
}

async function connect(source) {
  if (source.id === "slack") {
    // The tier decides what Slack asks you to approve, so it is saved first.
    if (config) {
      config.sources.slack = { ...(config.sources.slack ?? {}), access: tier };
      await putConfig(config).catch(() => {});
    }
    try {
      const { url } = await slackConnect();
      if (mode === "extension") {
        // Slack must land on the server's page, not this one: hand over.
        const { url: server } = await connection();
        location.href = `${server}/welcome`;
        await wait(50);
      }
      location.href = url;
    } catch (error) {
      note("pickNote", error.message, true);
    }
    return;
  }
  if (source.id === "github" && state?.gh) {
    try {
      const { login } = await githubConnect();
      note("pickNote", `Connected GitHub as ${login}.`);
      await renderPicks();
    } catch (error) {
      note("pickNote", error.message, true);
    }
    return;
  }
  location.href = settingsURL(source.id);
}

function settingsURL(hash = "") {
  const suffix = hash ? `#${encodeURIComponent(hash)}` : "";
  if (mode === "extension") return chrome.runtime.getURL(`src/options/options.html${suffix}`);
  return `/settings${suffix}`;
}

// ── Never read these ────────────────────────────────────

async function wireNever(step) {
  const host = $("rooms");
  const slackOn = config?.sources?.slack?.token;
  if (!slackOn) {
    setText($("neverBody"), "Once Slack is connected, this is where you tick the rooms Pomona must never fetch. You can do that in settings whenever you like.");
    return;
  }
  note("neverBody", "Loading your channels…");
  let muted = [];
  try {
    [rooms, muted] = await Promise.all([slackChannels(), getMutes().catch(() => [])]);
  } catch (error) {
    setText($("neverBody"), `Couldn't list your channels: ${error.message}`);
    return;
  }
  const mutedSet = new Set(muted.map((m) => m.replace(/^#/, "").toLowerCase()));
  setText($("neverBody"), `Tick a room and Pomona never fetches it: not summarised, not stored, not sent to Claude. ${rooms.length} channels; find one by name.`);
  $("roomSearch").hidden = false;

  const paint = (filter = "") => {
    host.replaceChildren();
    const shown = rooms.filter((r) => r.name.includes(filter.toLowerCase())).slice(0, 60);
    for (const r of shown) {
      const li = document.getElementById("tpl-room").content.firstElementChild.cloneNode(true);
      const box = li.querySelector(".room__check");
      box.checked = mutedSet.has(r.name.toLowerCase());
      box.dataset.name = r.name;
      box.addEventListener("change", () => {
        if (box.checked) mutedSet.add(r.name.toLowerCase());
        else mutedSet.delete(r.name.toLowerCase());
      });
      setText(li.querySelector(".room__name"), `#${r.name}${r.private ? " (private)" : ""}`);
      host.append(li);
    }
  };
  paint();
  $("roomSearch").addEventListener("input", () => paint($("roomSearch").value.trim()));
  step.dataset.muted = "live";
  wireNever.mutedSet = mutedSet;
  wireNever.before = new Set(muted.map((m) => m.replace(/^#/, "").toLowerCase()));
}

async function saveNever() {
  const now = wireNever.mutedSet;
  const before = wireNever.before;
  if (!now || !before) return true;
  const jobs = [];
  for (const name of now) if (!before.has(name)) jobs.push(mute("#" + name));
  for (const name of before) if (!now.has(name)) jobs.push(unmute("#" + name));
  await Promise.all(jobs).catch(() => {});
  wireNever.before = new Set(now);
  return true;
}

// ── Claude ──────────────────────────────────────────────

function wireClaude() {
  const cli = Boolean(state?.claudeCLI);
  const hosted = Boolean(state?.hosted);
  if (cli && !hosted) {
    setText($("claudeBody"), "Claude Code is installed on this machine, so your subscription writes the brief. Nothing to paste. If you would rather use an API key, that lives in settings.");
    return;
  }
  setText($("claudeBody"), hosted
    ? "A hosted server can't use your Claude subscription: that only works where Claude Code is signed in, and this machine isn't yours. So it uses an API key of your own, and this is the one thing in setup you have to paste."
    : "The claude CLI isn't on this server, so it needs an API key from console.anthropic.com.");
  $("claudeFields").hidden = false;
  $("apiKey").value = config?.claude?.apiKey ?? "";
}

async function saveClaude() {
  if ($("claudeFields").hidden || !config) return true;
  const key = $("apiKey").value.trim();
  if (!key) {
    note("claudeStatus", "Without a key there is nothing to write the brief with.", true);
    return false;
  }
  config.claude = { ...config.claude, mode: "apikey", apiKey: key };
  await putConfig(config).catch((error) => note("claudeStatus", error.message, true));
  return true;
}

// ── The last step ───────────────────────────────────────

function finish(step) {
  const name = config?.profile?.name?.trim();
  if (name) setText(step.querySelector("#doneTitle"), `That's everything, ${name}.`);
  $("focus").value = config?.profile?.focus ?? "";

  const keepFocus = async () => {
    if (!config) return;
    config.profile = { ...config.profile, focus: $("focus").value.trim() };
    await putConfig(config).catch(() => {});
  };
  const briefURL = (write) => mode === "extension"
    ? chrome.runtime.getURL(`src/brief/brief.html${write ? "?write=1" : ""}`)
    : `/${write ? "?write=1" : ""}`;

  step.querySelector("#later").addEventListener("click", async () => { await keepFocus(); location.href = briefURL(false); });
  step.querySelector("#writeNow").addEventListener("click", async () => { await keepFocus(); location.href = briefURL(true); });
}

boot().catch((error) => {
  steps = steps.length ? steps : ["where"];
  show(steps[0]);
  note("whereStatus", error.message, true) || note("status", error.message, true);
});
