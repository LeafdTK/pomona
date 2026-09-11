import { paintBlossoms } from "../brief/icons.js";
import { getConfig, putConfig, getSources, slackConnect, githubConnect, status } from "../lib/api.js";

const $ = (id) => document.getElementById(id);
const setText = (el, value) => { if (el) el.textContent = value ?? ""; };

/**
 * Onboarding, as one object turning over.
 *
 * Every step lives in the same shell. Moving between them measures the shell
 * before and after, then eases its height between the two while the old step
 * blurs out and the new one blurs in. Nothing is ever replaced on screen, which
 * is the whole difference between this and four pages taking turns.
 */

const STEPS = ["welcome", "you", "sources", "when", "done"];
let at = 0;
let config = null;
let state = null; // what the server said about itself: reachable, gh on PATH, and so on

async function boot() {
  paintBlossoms();

  state = await status().catch(() => null);
  if (!state?.reachable) {
    show("welcome");
    return;
  }
  config = await getConfig().catch(() => null);

  // Somebody who already set this up does not need to be walked through it.
  if (config?.profile?.name) at = STEPS.indexOf("sources");
  show(STEPS[at]);
}

// ── Moving between steps ────────────────────────────────

const still = matchMedia("(prefers-reduced-motion: reduce)").matches;

async function show(name, direction = 1) {
  const pane = $("pane");
  const shell = $("shell");
  const leaving = pane.firstElementChild;

  const from = shell.getBoundingClientRect().height;
  shell.style.setProperty("--shell-h", `${from}px`);

  const entering = document
    .getElementById(`step-${name}`)
    .content.firstElementChild.cloneNode(true);
  entering.classList.add("is-entering");
  if (direction < 0) entering.style.transform = "translateY(-14px) scale(0.985)";

  // The outgoing step comes out of the flow first, so the shell measures the
  // new content on its own rather than the two of them stacked.
  if (leaving) leaving.classList.add("is-leaving");
  pane.append(entering);

  wire(name, entering);
  marks();

  // Measure the new height, put the old one back, then animate between them.
  //
  // Reading offsetHeight forces layout there and then, which matters: an
  // animation frame is the obvious way to do this and never arrives in a
  // background tab, so onboarding opened in one would sit at a blurred blank
  // step forever waiting for a frame nobody was going to draw.
  shell.style.removeProperty("--shell-h");
  const to = shell.offsetHeight;
  shell.style.setProperty("--shell-h", `${from}px`);
  void shell.offsetHeight; // flush, so the browser has something to ease from
  shell.style.setProperty("--shell-h", `${to}px`);

  entering.classList.remove("is-entering");
  entering.style.transform = "";

  await wait(still ? 0 : 480);
  leaving?.remove();
  // Back to auto, so a step that grows later (an error line, a connected
  // source) is not trapped at the height it happened to open with.
  shell.style.removeProperty("--shell-h");
}

const wait = (ms) => new Promise((go) => setTimeout(go, ms));

function go(by) {
  const next = Math.min(Math.max(at + by, 0), STEPS.length - 1);
  if (next === at) return;
  at = next;
  show(STEPS[at], by);
}

function marks() {
  const bar = $("progress");
  bar.replaceChildren(
    ...STEPS.map((_, i) => {
      const mark = document.createElement("span");
      if (i === at) mark.className = "is-here";
      else if (i < at) mark.className = "is-past";
      return mark;
    }),
  );
}

// ── What each step does ─────────────────────────────────

function wire(name, step) {
  step.querySelector("[data-next]")?.addEventListener("click", () => save(name).then(() => go(1)));
  step.querySelector("[data-back]")?.addEventListener("click", () => go(-1));

  if (name === "you") {
    $("name").value = config?.profile?.name ?? "";
    $("role").value = config?.profile?.role ?? "";
    $("focus").value = config?.profile?.focus ?? "";
    $("name").focus();
  }

  if (name === "sources") renderPicks();

  if (name === "when") {
    $("time").value = config?.schedule?.time || "07:30";
    $("weekdays").checked = config?.schedule?.weekdaysOnly ?? true;
  }

  if (name === "done") finish(step);
}

async function save(name) {
  if (!config) return;

  if (name === "you") {
    config.profile = {
      ...config.profile,
      name: $("name").value.trim(),
      role: $("role").value.trim(),
      focus: $("focus").value.trim(),
    };
  }
  if (name === "when") {
    config.schedule = {
      ...config.schedule,
      enabled: true,
      time: $("time").value || "07:30",
      weekdaysOnly: $("weekdays").checked,
    };
  }
  if (name === "you" || name === "when") {
    await putConfig(config).catch(() => {});
  }
}

// ── Sources ─────────────────────────────────────────────

async function renderPicks() {
  const host = $("picks");
  const [sources, fresh] = await Promise.all([
    getSources().catch(() => []),
    getConfig().catch(() => null),
  ]);
  if (fresh) config = fresh; // coming back from a consent screen
  host.replaceChildren();

  for (const source of sources) {
    if (source.id === "custom") continue;
    const li = document.getElementById("tpl-pick").content.firstElementChild.cloneNode(true);
    // Whether a source is on lives in the config, not in its description.
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
  // Slack is one hop. GitHub is one click when gh is signed in on this
  // machine. Everything else needs a token pasted, which is a settings job,
  // and pretending otherwise wastes a click.
  if (source.id === "slack") {
    const { url } = await slackConnect().catch(() => ({}));
    if (url) location.href = url;
    return;
  }
  if (source.id === "github" && state?.gh) {
    try {
      const { login } = await githubConnect();
      note(`Connected GitHub as ${login}.`);
      config = await getConfig().catch(() => config);
      await renderPicks();
    } catch (error) {
      note(error.message);
      location.href = "/settings#github";
    }
    return;
  }
  location.href = "/settings#" + encodeURIComponent(source.id);
}

/** A line under the picks, if the step has one; silence if not. */
function note(text) {
  const el = document.querySelector(".pick__note") || $("pickNote");
  if (el) setText(el, text);
}

// ── The last step ───────────────────────────────────────

function finish(step) {
  const name = config?.profile?.name?.trim();
  if (name) setText(step.querySelector("#doneTitle"), `That's everything, ${name}.`);

  step.querySelector("#later").addEventListener("click", () => { location.href = "/"; });
  step.querySelector("#writeNow").addEventListener("click", () => {
    // The brief page owns the waiting: it already shows the plate developing
    // while the sources come in, and rebuilding that here would be a worse
    // copy of it.
    location.href = "/?write=1";
  });
}

boot().catch((error) => {
  show("welcome");
  setText($("status"), error.message);
});
