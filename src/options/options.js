import { sourceMark, paintBlossoms } from "../brief/icons.js";
import * as api from "../lib/api.js";

const $ = (id) => document.getElementById(id);
const clone = (id) => document.getElementById(id).content.firstElementChild.cloneNode(true);
const setText = (el, value) => {
  if (el) el.textContent = value ?? "";
};

let config = null;
let sources = [];
let server = { slack: {} };
let saveTimer = null;

// ── Boot ────────────────────────────────────────────────

async function boot() {
  paintBlossoms();

  const { url } = await api.connection();
  $("serverUrl").value = url;
  $("serverUrl").addEventListener("change", async () => {
    await api.setConnection({ url: $("serverUrl").value.trim() });
    refresh();
  });

  $("recheck").addEventListener("click", refresh);
  $("createVault").addEventListener("click", createVault);
  $("unlock").addEventListener("click", unlock);
  $("pair").addEventListener("click", () => pair());

  // The server prints a link with the code in it, so you can click instead of
  // retyping. The code is single use and dies in two minutes either way.
  const offered = new URLSearchParams(location.search).get("code");
  if (offered) {
    $("pairCode").value = offered;
    history.replaceState(null, "", location.pathname);
  }
  $("signout").addEventListener("click", async () => {
    await api.logout();
    flash("Signed out");
    refresh();
  });
  $("signoutOthers").addEventListener("click", async () => {
    const { signedOut } = await api.signOutOtherDevices();
    flash(signedOut ? `Signed out ${signedOut} other browser${signedOut === 1 ? "" : "s"}` : "No others to sign out");
    refresh();
  });
  $("signin").addEventListener("click", () => enter(api.login));
  $("createAccount").addEventListener("click", () => enter(api.signup));
  $("forgetAll").addEventListener("click", async () => {
    await api.forgetEverything();
    flash("Deleted");
    refresh();
  });
  $("lockServer").addEventListener("click", async () => {
    await api.lock().catch(() => {});
    flash("Locked");
    refresh();
  });
  $("toBrief").addEventListener("click", () => chrome.tabs.create({ url: chrome.runtime.getURL("src/brief/brief.html") }));
  $("generateNow").addEventListener("click", generateNow);
  $("addCustom").addEventListener("click", () => {
    config.custom.push({ id: crypto.randomUUID(), name: "", url: "", method: "GET", headers: "", body: "", itemsPath: "", enabled: true });
    renderCustom();
    save();
  });

  await refresh();
}

/** One place decides what the page is allowed to show. */
async function refresh() {
  const state = await api.status();
  const note = $("connectionNote");

  const show = (setup, unlockIt, pairIt, paired, signin = false) => {
    $("setupPane").hidden = !setup;
    $("unlockPane").hidden = !unlockIt;
    $("pairPane").hidden = !pairIt;
    $("signinPane").hidden = !signin;
    $("pairedPane").hidden = !paired;
    for (const card of document.querySelectorAll("[data-needs-pairing]")) card.hidden = !paired;
    $("generateNow").hidden = !paired;
  };

  if (!state.reachable) {
    setText(note, `${state.error} Start it in a terminal with: pomona`);
    return show(false, false, false, false);
  }
  if (state.needsSetup) {
    setText(note, "Found your server. Choose a passphrase to unlock it with.");
    return show(true, false, false, false);
  }
  if (state.locked) {
    setText(note, state.passphrase
      ? "Found your server. It's locked, so nothing can be read or written until you unlock it."
      : "Found your server. It's locked. There's no passphrase on it, so this just picks the key back up.");
    show(false, true, false, false);
    // Nothing to type when there's no passphrase; don't pretend otherwise.
    $("passphrase").parentElement.hidden = !state.passphrase;
    $("unlock").textContent = state.passphrase ? "Unlock" : "Unlock the server";
    return;
  }
  if (!state.paired) {
    const many = state.accounts > 1;
    setText(note, many
      ? "This server has more than one account, so it needs to know which is yours."
      : state.local
        ? "Found your server but couldn't connect to it. Try Check again."
        : "Sign in, or use the pairing code from the server's terminal.");
    setText($("signinNote"), state.accounts === 0
      ? "No accounts yet. Pick an email and password and this one is yours."
      : "Sign in with the email and password you chose.");
    show(false, false, !state.local && !many, false, many || !state.local);

    const offered = $("pairCode").value.trim();
    if (offered.length === 6) await pair(offered);
    return;
  }

  const who = await api.account().catch(() => null);
  // Locking is only meaningful when there's a passphrase to unlock with.
  $("lockServer").hidden = !state.passphrase;
  setText(note, `${who ? `Signed in as ${who.user.name}. ` : ""}${state.claudeCLI ? "Claude Code is installed, so your subscription can write the brief." : "The claude CLI isn't on the server's PATH, so use an API key."}`);
  show(false, false, false, true);

  await loadEverything();
}

async function loadEverything() {
  [config, sources, server] = await Promise.all([api.getConfig(), api.getSources(), api.getServerSettings()]);

  bind("name", "profile.name");
  bind("role", "profile.role");
  bind("focus", "profile.focus");
  bind("model", "claude.model");
  bind("apiKey", "claude.apiKey");
  bind("scheduleTime", "schedule.time");
  bindCheck("scheduleEnabled", "schedule.enabled");
  bindCheck("weekdaysOnly", "schedule.weekdaysOnly");
  bindNumber("lookback", "lookbackHours");

  // After the bindings exist, so the guess lands in bound fields and saves
  // through the same path as typing does.
  await fillInWhatWeKnow();

  const mode = $("claudeMode");
  mode.value = config.claude.mode || "subscription";
  $("apiKeyField").hidden = mode.value !== "apikey";
  mode.onchange = () => {
    config.claude.mode = mode.value;
    $("apiKeyField").hidden = mode.value !== "apikey";
    save({ now: true });
  };

  // The browser knows the timezone. Only fill a blank, and save it: this used
  // to overwrite whatever was set and then never write it back.
  if (!config.profile.timezone) {
    config.profile.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    save({ now: true });
  }

  $("redirectURI").value = server.slack.redirectURI ?? "";
  $("slackClientId").value = server.slack.clientId ?? "";
  // When .env owns these, show them and get out of the way.
  for (const id of ["slackClientId", "slackClientSecret"]) $(id).disabled = server.slack.fromEnv;
  $("saveSlackApp").hidden = server.slack.fromEnv;
  if (server.slack.fromEnv) {
    $("slackClientSecret").placeholder = "set in .env";
    setResult($("slackAppResult"), "Set in .env on the server.", "ok");
  }
  $("saveSlackApp").onclick = async () => {
    try {
      await api.putServerSettings({
        slack: { clientId: $("slackClientId").value.trim(), clientSecret: $("slackClientSecret").value.trim() },
      });
      $("slackClientSecret").value = "";
      setResult($("slackAppResult"), "Saved. Everyone here can connect Slack now.", "ok");
      refresh();
    } catch (error) {
      setResult($("slackAppResult"), error.message, "error");
    }
  };

  renderSources();
  renderCustom();
  renderMemory();
  renderMutes();
  renderOwned();

  $("readAgain").onclick = async () => {
    const button = $("readAgain");
    button.disabled = true;
    button.textContent = "Reading…";
    try {
      const got = await api.refresh();
      flash(`${got.fresh} new, ${got.stored} kept, ${Math.round(got.took / 1e9)}s`);
      renderOwned();
    } catch (error) {
      flash(error.message);
    } finally {
      button.disabled = false;
      button.textContent = "Read sources now";
    }
  };

  // Coming back from Slack's consent screen.
  const connected = new URLSearchParams(location.search).get("connected");
  if (connected) {
    flash(connected);
    history.replaceState(null, "", location.pathname);
  }
}

// ── Getting in ──────────────────────────────────────────

async function createVault() {
  try {
    await api.createVault($("newPassphrase").value);
    $("newPassphrase").value = "";
    setResult($("connectionResult"), "Vault created", "ok");
    refresh();
  } catch (error) {
    setResult($("connectionResult"), error.message, "error");
  }
}

async function unlock() {
  try {
    await api.unlock($("passphrase").value);
    $("passphrase").value = "";
    setResult($("connectionResult"), "Unlocked", "ok");
    refresh();
  } catch (error) {
    setResult($("connectionResult"), error.message, "error");
  }
}

async function pair(code) {
  try {
    await api.pair((code ?? $("pairCode").value).trim(), navigator.userAgent.includes("Chrome") ? "Chrome" : "A browser");
    $("pairCode").value = "";
    setResult($("connectionResult"), "Paired", "ok");
    refresh();
  } catch (error) {
    setResult($("connectionResult"), error.message, "error");
  }
}

async function enter(method) {
  try {
    const user = await method({ email: $("email").value.trim(), password: $("password").value });
    $("password").value = "";
    setResult($("connectionResult"), `Signed in as ${user.name}`, "ok");
    refresh();
  } catch (error) {
    setResult($("connectionResult"), error.message, "error");
  }
}

async function generateNow() {
  const button = $("generateNow");
  button.disabled = true;
  button.textContent = "Writing…";
  try {
    await save({ now: true });
    const brief = await api.generate();
    await chrome.tabs.create({ url: chrome.runtime.getURL(`src/brief/brief.html?id=${brief.id}`) });
    button.textContent = "Write one now";
  } catch (error) {
    button.textContent = "Failed, try again";
    flash(error.message);
  } finally {
    button.disabled = false;
  }
}

// ── Config plumbing ─────────────────────────────────────

const read = (path) => path.split(".").reduce((acc, key) => acc?.[key], config);

function write(path, value) {
  const keys = path.split(".");
  let cursor = config;
  while (keys.length > 1) cursor = cursor[keys.shift()];
  cursor[keys[0]] = value;
}

/**
 * Ask the connected sources who this is, and fill in whatever is still blank.
 *
 * A Slack token knows your name, your job title and your timezone. Presenting
 * an empty form to somebody who just handed all that over is a form pretending
 * to be a conversation. Only blanks are touched: a guess never overwrites
 * something you typed, however confident it is.
 */
async function fillInWhatWeKnow() {
  const note = $("guessNote");
  const empty = ["name", "role", "timezone"].filter((id) => !read(`profile.${id}`));
  if (!empty.length) return;

  let guess;
  try {
    guess = await api.guessProfile();
  } catch {
    return; // a guess is a courtesy, and a failed one says nothing
  }

  const filled = [];
  for (const id of empty) {
    if (!guess[id]) continue;
    write(`profile.${id}`, guess[id]);
    if ($(id)) $(id).value = guess[id]; // timezone has no field of its own
    filled.push(id);
  }
  if (!filled.length) return;

  save({ now: true });
  setText(note, `Read from ${list(guess.from)}. Change anything that's wrong.`);
  note.hidden = false;
}

/** "Slack", "Slack and GitHub", "Slack, GitHub and Linear". */
function list(names = []) {
  if (names.length < 2) return names[0] ?? "what you connected";
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

function bind(id, path) {
  const el = $(id);
  if (!el) return;
  el.value = read(path) ?? "";
  el.oninput = () => {
    write(path, el.value);
    save();
  };
}

function bindCheck(id, path) {
  const el = $(id);
  el.checked = Boolean(read(path));
  el.onchange = () => {
    write(path, el.checked);
    save({ now: true });
  };
}

function bindNumber(id, path) {
  const el = $(id);
  el.value = String(read(path) ?? "");
  el.onchange = () => {
    write(path, Number(el.value));
    save({ now: true });
  };
}

/** No save button on purpose: the page writes through as you type. */
function save({ now = false } = {}) {
  clearTimeout(saveTimer);
  const run = async () => {
    try {
      await api.putConfig(config);
      flash("Saved");
    } catch (error) {
      flash(error.message);
    }
  };
  return now ? run() : new Promise((resolve) => (saveTimer = setTimeout(() => resolve(run()), 400)));
}

function flash(text) {
  const el = $("saved");
  el.textContent = text;
  el.classList.add("is-visible");
  clearTimeout(flash.timer);
  flash.timer = setTimeout(() => el.classList.remove("is-visible"), 1600);
}

function setResult(el, text, state = "") {
  el.textContent = text;
  el.className = `result${state ? ` is-${state}` : ""}`;
}

// ── Sources ─────────────────────────────────────────────

function renderSources() {
  const host = $("connectors");
  host.replaceChildren();

  for (const source of sources) {
    const settings = (config.sources[source.id] ??= {});
    const row = clone("tpl-connector");

    const mark = sourceMark(source.iconKey);
    if (mark) row.querySelector(".connector__mark").append(mark);
    setText(row.querySelector(".connector__name"), source.name);
    setText(row.querySelector(".connector__blurb"), source.blurb);
    setText(row.querySelector(".connector__help"), source.help);

    const body = row.querySelector(".connector__body");
    const toggle = row.querySelector(".connector__toggle");
    toggle.checked = settings.enabled === "true";
    body.hidden = !toggle.checked;
    toggle.onchange = () => {
      body.hidden = !toggle.checked;
      settings.enabled = String(toggle.checked);
      save({ now: true });
    };

    const fields = row.querySelector(".connector__fields");
    for (const field of source.fields) fields.append(buildField(field, settings));
    if (source.id === "slack") fields.append(buildSlackConnect(settings));

    const result = row.querySelector(".connector__result");
    row.querySelector(".connector__test").onclick = async (event) => {
      event.currentTarget.disabled = true;
      setResult(result, "Checking…");
      try {
        await save({ now: true });
        const { count, sample } = await api.testSource({ id: source.id });
        setResult(result, count ? `${count} item${count === 1 ? "" : "s"}. First: “${sample}”` : "Connected, nothing new right now", "ok");
      } catch (error) {
        const friendly =
          source.id === "slack" && /no token/i.test(error.message)
            ? "Connect Slack first, then Test."
            : error.message;
        setResult(result, friendly, "error");
      } finally {
        event.currentTarget.disabled = false;
      }
    };

    host.append(row);
  }
}

/**
 * Slack is the one source you shouldn't have to paste a token for. If the
 * server has an app configured, this is a button; if not, it says so rather
 * than leaving an inert box on the page.
 */
function buildSlackConnect(settings) {
  const wrap = document.createElement("div");
  wrap.className = "field";

  if (!server.slack.configured) {
    const jump = document.createElement("button");
    jump.type = "button";
    jump.className = "btn";
    jump.textContent = "Set up the Slack app";
    jump.onclick = () => {
      $("connectingApps").scrollIntoView({ behavior: "smooth", block: "center" });
      $("slackClientId").focus();
    };
    const note = document.createElement("span");
    note.className = "field__help";
    note.textContent = "This server has no Slack app yet. Set one up once and this becomes a Connect button, for you and everyone else here.";
    wrap.append(jump, note);
    return wrap;
  }

  const row = document.createElement("div");
  row.className = "connector__actions";

  if (settings.token) {
    const state = document.createElement("span");
    state.className = "result is-ok";
    state.textContent = settings.workspaceName ? `Connected to ${settings.workspaceName}` : "Connected";

    const off = document.createElement("button");
    off.type = "button";
    off.className = "btn btn--quiet";
    off.textContent = "Disconnect";
    off.onclick = async () => {
      await api.slackDisconnect();
      refresh();
    };
    row.append(off, state);
  } else {
    const go = document.createElement("button");
    go.type = "button";
    go.className = "btn btn--primary";
    go.textContent = "Connect Slack";
    go.onclick = async () => {
      try {
        // Save first: the tier you picked decides what Slack asks you to approve.
        await save({ now: true });
        const { url } = await api.slackConnect();
        location.href = url;
      } catch (error) {
        setResult($("slackAppResult"), error.message, "error");
      }
    };
    row.append(go);
  }

  wrap.append(row);
  return wrap;
}

function buildField(field, settings) {
  const label = document.createElement("label");
  label.className = "field";

  const name = document.createElement("span");
  name.className = "field__label";
  name.textContent = field.label;
  label.append(name);

  let input;
  if (field.type === "select") {
    input = document.createElement("select");
    for (const option of field.options ?? []) {
      const el = document.createElement("option");
      el.value = option.value;
      el.textContent = option.label;
      input.append(el);
    }
    input.value = settings[field.key] || field.default || field.options?.[0]?.value || "";
    // A select has no half-typed state, so write it through immediately.
    input.onchange = () => {
      settings[field.key] = input.value;
      save({ now: true });
    };
  } else {
    input = document.createElement(field.type === "textarea" ? "textarea" : "input");
    if (field.type === "textarea") input.rows = 3;
    else input.type = field.type;
    input.placeholder = field.placeholder ?? "";
    input.spellcheck = false;
    input.value = settings[field.key] ?? "";
    input.oninput = () => {
      settings[field.key] = input.value;
      save();
    };
  }
  label.append(input);

  if (field.help) {
    const help = document.createElement("span");
    help.className = "field__help";
    help.textContent = field.help;
    label.append(help);
  }
  return label;
}

// ── Custom sources ──────────────────────────────────────

function renderCustom() {
  const host = $("customList");
  host.replaceChildren();

  for (const source of config.custom) {
    const row = clone("tpl-custom");
    const result = row.querySelector(".custom__result");

    for (const input of row.querySelectorAll("[data-key]")) {
      const key = input.dataset.key;
      input.value = source[key] ?? "";
      input.oninput = () => {
        source[key] = input.value;
        source.enabled = true;
        save();
      };
    }

    row.querySelector(".custom__test").onclick = async (event) => {
      event.currentTarget.disabled = true;
      setResult(result, "Checking…");
      try {
        await save({ now: true });
        const { count, sample } = await api.testSource({ custom: source });
        setResult(result, count ? `${count} item${count === 1 ? "" : "s"}. First: “${sample}”` : "Reached it, nothing to read", "ok");
      } catch (error) {
        setResult(result, error.message, "error");
      } finally {
        event.currentTarget.disabled = false;
      }
    };

    row.querySelector(".custom__remove").onclick = () => {
      config.custom = config.custom.filter((entry) => entry.id !== source.id);
      renderCustom();
      save({ now: true });
    };

    host.append(row);
  }
}

// ── Memory ──────────────────────────────────────────────

async function renderMemory() {
  const notes = await api.getMemory().catch(() => []);
  const host = $("memory");
  host.replaceChildren();
  $("memoryCard").hidden = !notes.length;
  if (!notes.length) return;

  for (const note of notes) {
    const li = document.createElement("li");

    const on = document.createElement("span");
    on.className = "memory__on";
    setText(on, note.on ?? "");

    const text = document.createElement("span");
    text.className = "memory__text";
    setText(text, note.text);

    const forget = document.createElement("button");
    forget.type = "button";
    forget.className = "memory__forget";
    forget.title = "Forget this";
    forget.setAttribute("aria-label", `Forget: ${note.text}`);
    forget.textContent = "×";
    forget.onclick = async () => {
      await api.forget(note.text);
      renderMemory();
      flash("Forgotten");
    };

    li.append(on, text, forget);
    host.append(li);
  }
}

// ── What you own ────────────────────────────────────────

/**
 * Inferred, with reasons, and one click to disagree. The reasons are the
 * point: an inference nobody can read is an inference nobody can correct.
 */
async function renderOwned() {
  const places = await api.ownership().catch(() => []);
  const host = $("owned");
  host.replaceChildren();
  $("ownedCard").hidden = false;

  for (const place of places) {
    const li = document.createElement("li");

    const text = document.createElement("span");
    text.className = "memory__text";
    const why = place.why?.[0] ? ` · ${place.why[0]}` : "";
    setText(text, `${place.origin}${place.pinned ? " (you said so)" : ""}${why}`);

    const undo = document.createElement("button");
    undo.type = "button";
    undo.className = "memory__forget";
    undo.title = "Not mine";
    undo.setAttribute("aria-label", `${place.origin} is not mine`);
    undo.textContent = "×";
    undo.onclick = async () => {
      await api.disown(place.origin);
      renderOwned();
      flash(`${place.origin} is not yours`);
    };

    li.append(text, undo);
    host.append(li);
  }

  $("ownAdd").onclick = async () => {
    const origin = $("ownInput").value.trim();
    if (!origin) return;
    await api.own(origin).catch((error) => flash(error.message));
    $("ownInput").value = "";
    renderOwned();
  };
}

// ── Silenced places ─────────────────────────────────────

async function renderMutes() {
  const places = await api.getMutes().catch(() => []);
  const host = $("mutes");
  host.replaceChildren();
  $("mutesCard").hidden = !places.length;

  for (const place of places) {
    const li = document.createElement("li");

    const text = document.createElement("span");
    text.className = "memory__text";
    setText(text, place);

    const undo = document.createElement("button");
    undo.type = "button";
    undo.className = "memory__forget";
    undo.title = "Start reading this again";
    undo.setAttribute("aria-label", `Unsilence ${place}`);
    undo.textContent = "×";
    undo.onclick = async () => {
      await api.unmute(place);
      renderMutes();
      flash(`Reading ${place} again`);
    };

    li.append(text, undo);
    host.append(li);
  }

  await renderFaded();
}

/**
 * Places that went quiet without anybody asking. Showing this is the price of
 * doing it at all: a brief that decides on its own what you do not care about,
 * with no way to see the decision, is worse than one that shows you too much.
 */
async function renderFaded() {
  const places = await api.fadedPlaces().catch(() => []);
  const host = $("faded");
  host.replaceChildren();
  $("fadedNote").hidden = !places.length;
  if (places.length) $("mutesCard").hidden = false;

  for (const { origin, shown } of places) {
    const li = document.createElement("li");

    const text = document.createElement("span");
    text.className = "memory__text";
    setText(text, `${origin} · shown ${shown} times, never acted on`);

    const undo = document.createElement("button");
    undo.type = "button";
    undo.className = "memory__forget";
    undo.title = "Start reading this again";
    undo.setAttribute("aria-label", `Bring back ${origin}`);
    undo.textContent = "×";
    undo.onclick = async () => {
      await api.revivePlace(origin);
      renderMutes();
      flash(`Reading ${origin} again`);
    };

    li.append(text, undo);
    host.append(li);
  }
}

boot();
