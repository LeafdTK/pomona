import { sourceMark, paintBlossoms, paintLaurels } from "./icons.js";
import { listBriefs, getBrief, generate, briefProgress, plate, setDone, remember, mute, noteAttention, getConfig, status, inExtension } from "../lib/api.js";

const CLAUDE_CHAT = "https://claude.ai/new";

const $ = (id) => document.getElementById(id);
const clone = (id) => document.getElementById(id).content.firstElementChild.cloneNode(true);

/** Text only, always. Nothing the model writes is ever parsed as markup. */
function setText(el, value) {
  if (el) el.textContent = value ?? "";
}

/**
 * Model-authored prose, with dashes normalised.
 *
 * The system prompt forbids em and en dashes, but a rule you can enforce is
 * better than a rule you ask for: a dash between clauses becomes a comma, and a
 * dash used as a range stays a range.
 */
function prose(text) {
  return String(text ?? "")
    // Dash between clauses: a comma does the same work.
    .replace(/\s+[\u2014\u2013]\s+/g, ", ")
    // Dash closed up between two things: that's a range, keep it a range.
    .replace(/(?<=\S)[\u2014\u2013](?=\S)/g, "-")
    .replace(/[\u2014\u2013]/g, ", ")
    .replace(/,\s*,/g, ",")
    .replace(/\s+,/g, ",");
}

/** setText for anything Claude wrote. */
const setProse = (el, value) => setText(el, prose(value));

function isSafeUrl(url) {
  try {
    return ["http:", "https:"].includes(new URL(url).protocol);
  } catch {
    return false;
  }
}

const chatLink = (prompt) => (prompt ? `${CLAUDE_CHAT}?${new URLSearchParams({ q: prompt })}` : "");

// ── State ───────────────────────────────────────────────

let briefs = [];
let current = null;
let done = new Set();

// ── Boot ────────────────────────────────────────────────

async function boot() {
  paintBlossoms();
  paintLaurels();
  wireChrome();

  const state = await status();
  if (!state.reachable || state.locked || !state.paired) {
    return showBlank(explain(state));
  }

  // A write already under way, started here or by the morning: show it
  // being written rather than a blank page that says nothing is.
  const running = await briefProgress().catch(() => null);
  if (running?.stage && running.kind !== "refresh" && !["done", "failed"].includes(running.stage)) {
    openPress();
    return;
  }

  try {
    briefs = await listBriefs();
  } catch (error) {
    return showBlank(error.message);
  }

  const wanted = new URLSearchParams(location.search).get("id");
  const summary = (wanted && briefs.find((b) => b.id === wanted)) || briefs[0];
  if (!summary) {
    // Nothing written, and the last attempt failed: say why, in the
    // server's words, rather than a blank page that invites another try.
    if (running?.stage === "failed" && running.kind !== "refresh" && running.error) {
      showBlank();
      const blankStatus = $("blankStatus");
      blankStatus.classList.add("is-error");
      setText(blankStatus, `The last attempt failed: ${running.error}`);
      $("blankGenerate").textContent = "Try again";
      return;
    }
    return showBlank();
  }

  current = await getBrief(summary.id);
  await render(current);
}

/** Turn the server's state into the one sentence that tells you what to do. */
function explain(state) {
  // Served by the server to a browser that is not signed in: the setup page
  // is the front door, not a dead end that names the settings. Whatever the
  // server thinks about where the browser is.
  if (!inExtension() && state.reachable && !state.paired && !state.locked && !state.needsSetup) {
    location.replace("/welcome");
    return "";
  }
  if (!state.reachable) return `${state.error} Start it with: pomona`;
  if (state.needsSetup) return "Your server needs a passphrase. Open Sources and settings to choose one.";
  if (state.locked) return "Your server is locked. Open Sources and settings to unlock it.";
  if (!state.paired) return "This browser isn't paired with your server yet. Open Sources and settings to pair.";
  return "";
}

function wireChrome() {
  // Arriving from the last step of onboarding.
  if (new URLSearchParams(location.search).get("write") === "1") {
    history.replaceState(null, "", location.pathname);
    regenerate();
  }

  $("regenerate").addEventListener("click", regenerate);
  $("blankGenerate").addEventListener("click", regenerate);
  $("blankSetup").addEventListener("click", () => {
    location.href = inExtension() ? chrome.runtime.getURL("src/welcome/welcome.html") : "/welcome";
  });
  for (const id of ["openOptions", "blankOptions"]) {
    $(id).addEventListener("click", () => chrome.runtime.openOptionsPage());
  }
  $("prevBrief").addEventListener("click", () => step(1));
  $("nextBrief").addEventListener("click", () => step(-1));
}

function step(offset) {
  const index = briefs.findIndex((b) => b.id === current.id) + offset;
  if (index < 0 || index >= briefs.length) return;
  location.search = `?id=${briefs[index].id}`;
}

function showBlank(message = "") {
  $("blank").hidden = false;
  $("page").hidden = true;
  offerSetup();
  if (message) {
    setText($("blankBody"), message);
    $("blankGenerate").hidden = /pair|locked|vault|reach/i.test(message);
  }
}

/**
 * On a machine with nothing connected, "Write today's brief" can only fail.
 * Offer the way in instead, and put it first.
 */
async function offerSetup() {
  const config = await getConfig().catch(() => null);
  if (!config) return;
  const connected = Object.values(config.sources ?? {}).some((s) => s?.enabled === "true");
  const fresh = !connected && !config.profile?.name;

  $("blankSetup").hidden = !fresh;
  $("blankSetup").classList.toggle("chip--action", fresh);
  $("blankGenerate").classList.toggle("chip--action", !fresh);
}

async function regenerate() {
  const press = openPress();
  try {
    await generate(); // starts the job; the press follows it to the end
  } catch (error) {
    press.fail(error.message);
  }
}

// ── The press ───────────────────────────────────────────
//
// Writing a brief takes minutes, nearly all of it spent reading Slack a
// channel at a time. Rather than spin, the wait shows the day's plate being
// revealed, and says which source it is still waiting on. The plate is chosen
// from the date alone, so it can be fetched before the brief exists and is the
// same painting the finished brief will carry.

// How far the reveal has got by the time each stage is reached. Nothing
// reaches 1 until the brief is genuinely in hand: a wipe that completes and
// then sits there is a worse lie than no wipe at all.
const REVEAL = { reading: 0.62, sorting: 0.7, writing: 0.9, setting: 0.96 };

function openPress() {
  $("page").hidden = true;
  $("blank").hidden = true;
  $("press").hidden = false;

  const startedAt = Date.now();
  const plateEl = $("pressPlate");
  const noteEl = $("pressNote");

  // The masthead and the title are the finished page's, filled in now, so the
  // brief completes in place rather than replacing something.
  const today = new Date();
  setText($("pressDay"), `${today.toLocaleDateString("en-US", { weekday: "long" })} Brief`);
  setText(
    $("pressDate"),
    today.toLocaleDateString("en-US", { day: "2-digit", month: "short", year: "numeric" }).toUpperCase(),
  );
  let reached = 0;
  let stopped = false;

  const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let shown = 0;
  let tweening = 0;

  // Ease toward the target rather than jumping. Both the clip and the line of
  // light read the same number, so they can never disagree about where the
  // boundary is.
  const reveal = (to) => {
    reached = Math.max(reached, Math.min(to, 1)); // a reveal never un-reveals
    if (still) return paint(reached);

    const from = shown;
    const distance = reached - from;
    if (distance <= 0.0005) return paint(reached);
    const startedTween = performance.now();
    cancelAnimationFrame(tweening);

    const frame = (now) => {
      const t = Math.min((now - startedTween) / 1400, 1);
      paint(from + distance * (1 - Math.pow(1 - t, 3)));
      if (t < 1) tweening = requestAnimationFrame(frame);
    };
    tweening = requestAnimationFrame(frame);
  };

  function paint(value) {
    shown = value;
    plateEl.style.setProperty("--reveal", value.toFixed(4));
  }

  showPlate();
  const ticking = setInterval(tick, 1000);
  const polling = setInterval(poll, 2000);
  // A five minute wait is exactly when someone switches tabs, and a hidden tab
  // gets no animation frames: the reveal freezes wherever it was. Catch it up
  // the moment they look again rather than making them wait for the next poll.
  const woke = () => {
    if (document.visibilityState === "visible" && !stopped) reveal(reached);
  };
  document.addEventListener("visibilitychange", woke);
  poll();

  async function showPlate() {
    try {
      const art = await plate();
      if (stopped || !art?.image || !isSafeUrl(art.image)) return;
      $("pressImg").src = art.image;
      $("pressImg").alt = art.caption ?? "";
      $("pressGhost").src = art.image;
      setText($("pressCaption"), art.caption ?? "");
      setText($("pressCaption"), art.caption ?? "");
    } catch {
      // No plate is a fine reason to show no plate.
    }
  }

  function tick() {
    const seconds = Math.round((Date.now() - startedAt) / 1000);
    setText($("pressClock"), `${elapsed(seconds)} elapsed. A first read of Slack takes a few minutes.`);
  }

  async function poll() {
    if (stopped) return;
    let state;
    try {
      state = await briefProgress();
    } catch {
      return; // a dropped poll is not worth saying anything about
    }
    if (stopped || !state?.stage) return;

    if (state.stage === "done") {
      await finish();
      location.search = state.briefId ? `?id=${state.briefId}` : "";
      return;
    }
    if (state.stage === "failed") {
      fail(state.error || "The brief could not be written.");
      return;
    }

    if (state.note) setText(noteEl, state.note);
    drawSteps(state.steps ?? []);

    const ceiling = REVEAL[state.stage] ?? 0.5;
    const floor = state.stage === "reading" ? REVEAL.reading * settled(state.steps) : 0;
    // Creep toward the ceiling while a stage runs, so a long Slack sweep still
    // looks alive without ever claiming to be further along than it is.
    reveal(Math.max(floor, ceiling * ease((Date.now() - startedAt) / 1000)));
  }

  // How much of the reading is done, by source.
  function settled(steps = []) {
    if (!steps.length) return 0;
    return steps.filter((s) => s.done).length / steps.length;
  }

  // Approaches 1 without arriving: 90 seconds gets about halfway.
  function ease(seconds) {
    return 1 - Math.exp(-seconds / 130);
  }

  function drawSteps(steps) {
    const list = $("pressSteps");
    if (list.childElementCount !== steps.length) {
      list.replaceChildren(...steps.map(() => clone("tpl-press-step")));
    }
    steps.forEach((step, i) => {
      const el = list.children[i];
      if (!el) return;
      el.classList.toggle("is-done", Boolean(step.done) && !step.error);
      el.classList.toggle("is-failed", Boolean(step.error));
      setText(el.querySelector(".step__name"), step.name);
      setText(
        el.querySelector(".step__count"),
        step.error ? "unavailable" : step.done ? `${step.count}` : "reading",
      );
    });
  }

  // Stop asking the server anything. The reveal may still be finishing.
  function hush() {
    stopped = true;
    clearInterval(ticking);
    clearInterval(polling);
    document.removeEventListener("visibilitychange", woke);
  }

  // The last of the plate only comes off once the brief is really written.
  async function finish() {
    hush();
    reveal(1);
    plateEl.classList.add("press__plate--whole");
    $("pressCaption").classList.add("is-visible");
    await new Promise((done) => setTimeout(done, 1100));
  }

  function fail(message) {
    hush();
    cancelAnimationFrame(tweening);
    $("press").hidden = true;
    showBlank(message);
    const blankStatus = $("blankStatus");
    blankStatus.classList.add("is-error");
    setText(blankStatus, message);
    $("blankGenerate").textContent = "Try again";
  }

  return { finish, fail };
}

function elapsed(seconds) {
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, "0")}s`;
}

/**
 * Tell the server what the reader did, so places they never touch can sink and
 * places they act on stay. Failures are swallowed: this is bookkeeping about
 * attention, and it is never worth interrupting somebody's morning over.
 */
function keep(origin, signal) {
  if (origin) noteAttention(origin, signal).catch(() => {});
}

/** Opening the source is interest, even when nothing gets ticked. */
function watchOpening(link, origin) {
  if (!link || !origin) return;
  link.addEventListener("click", () => keep(origin, "acted"), { once: true });
}

/**
 * Burying a place answers a room rather than an item: #money-laundering is a
 * real channel doing real things, none of them yours, and no amount of judging
 * each message gets that right.
 */
function watchBurying(button, origin, row) {
  if (!button) return;
  if (!origin) return button.remove();
  setText(button, `Bury ${origin}`);
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      await mute(origin);
      keep(origin, "rejected"); // a mute is the loudest "not this" there is
      row.classList.add("is-dismissed");
      setText(button, `${origin} buried`);
    } catch {
      button.disabled = false;
      setText(button, "Try again");
    }
  });
}

// ── Render ──────────────────────────────────────────────

async function render(brief) {
  const data = brief.data ?? {};
  const when = new Date(data.header?.date_time ?? brief.createdAt ?? Date.now());

  document.title = `The ${when.toLocaleDateString("en-US", { weekday: "long" })} Brief`;
  setText($("titleDay"), `${when.toLocaleDateString("en-US", { weekday: "long" })} Brief`);
  setText(
    $("mastheadDay"),
    when.toLocaleDateString("en-US", { day: "2-digit", month: "short", year: "numeric" }).toUpperCase(),
  );
  setText($("mastheadTime"), when.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit" }));
  setProse($("greeting"), data.header?.greeting);

  const index = briefs.findIndex((b) => b.id === brief.id);
  $("prevBrief").disabled = index >= briefs.length - 1;
  $("nextBrief").disabled = index <= 0;

  applyPlate(brief);

  done = new Set(brief.done ?? []);

  const content = $("content");
  content.replaceChildren();
  let movement = 0;
  const next = () => roman(++movement);

  if (data.push_forward?.title) content.append(pushSection(next(), data.push_forward));
  if (data.top_todos?.length) content.append(todoSection(next(), data.top_todos, brief));
  if (data.new_updates?.length) content.append(updateSection(next(), data.new_updates));

  const meetings = [...(data.your_day?.morning ?? []), ...(data.your_day?.afternoon ?? [])];
  if (meetings.length) content.append(scheduleSection(next(), meetings));
  if (data.looking_ahead?.blurb) content.append(aheadSection(next(), data.looking_ahead.blurb));

  renderColophon(brief);

  $("page").hidden = false;
  $("blank").hidden = true;
  revealOnScroll();
}

function applyPlate(brief) {
  const painting = brief.painting;
  const caption = painting?.caption || brief.data?.painting?.caption || "";
  const img = $("plateImg");
  const captionEl = $("plateCaption");

  if (!painting?.image || !isSafeUrl(painting.image)) {
    $("plate").hidden = true;
    return;
  }

  const loader = new Image();
  loader.addEventListener("load", () => {
    img.src = painting.image;
    requestAnimationFrame(() => img.classList.add("is-loaded"));
    setText(captionEl, caption);
    captionEl.classList.add("is-visible");
  });
  loader.addEventListener("error", () => {
    $("plate").hidden = true;
  });
  loader.src = painting.image;
}

function section(index, title, count = "") {
  const el = clone("tpl-section");
  setText(el.querySelector(".movement__index"), index);
  setText(el.querySelector(".movement__title"), title);
  setText(el.querySelector(".movement__count"), count);
  return el;
}

// ── Push forward ────────────────────────────────────────

function pushSection(index, push) {
  const el = section(index, "Push your work forward");
  const card = clone("tpl-push");
  setProse(card.querySelector(".push__title"), push.title);
  setProse(card.querySelector(".push__text"), push.body);

  const seal = card.querySelector(".push__seal");
  const href = chatLink(push.cta_prompt);
  if (isSafeUrl(href)) {
    seal.href = href;
    seal.setAttribute("aria-label", "Start this in Claude");
    seal.title = push.cta_prompt;
  } else {
    seal.remove();
  }

  el.querySelector(".movement__body").append(card);

  // Dia's trick, and the best token spend in the whole system: when the
  // push is a message to send, the brief does the first third of it.
  if (push.draft?.trim()) {
    const draft = clone("tpl-draft");
    setText(draft.querySelector(".draft__text"), push.draft.trim());
    const copy = draft.querySelector(".draft__copy");
    copy.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(push.draft.trim());
        setText(copy, "Copied");
      } catch {
        setText(copy, "Select and copy");
      }
    });
    el.querySelector(".movement__body").append(draft);
  }
  return el;
}

// ── To-dos ──────────────────────────────────────────────

function todoSection(index, todos, brief) {
  const el = section(index, "Top to-dos", `${todos.length} open`);
  const body = el.querySelector(".movement__body");
  const allClear = clone("tpl-allclear");

  todos.forEach((todo, i) => {
    const row = clone("tpl-todo");
    const label = row.querySelector(".todo__label");
    if (todo.label) {
      setText(label, todo.label);
      if (todo.label_style === "active") label.classList.add("todo__label--active");
    } else {
      label.remove();
    }

    setProse(row.querySelector(".todo__title"), todo.title);
    setProse(row.querySelector(".todo__body"), todo.body);

    const source = row.querySelector(".todo__source");
    const mark = sourceMark(todo.source_icon_key);
    if (isSafeUrl(todo.source_url)) {
      source.href = todo.source_url;
      source.setAttribute("aria-label", `Open in ${todo.source_name || "source"}`);
      setText(source.querySelector(".todo__source-name"), todo.source_name || "Open");
      if (mark) source.prepend(mark);
    } else {
      source.remove();
    }

    const box = row.querySelector("input");
    box.checked = done.has(i);
    row.classList.toggle("is-done", box.checked);
    box.addEventListener("change", async () => {
      row.classList.toggle("is-done", box.checked);
      box.checked ? done.add(i) : done.delete(i);
      await setDone(brief.id, [...done]).catch(() => {});
      if (box.checked) keep(todo.origin, "acted");
      syncAllClear(allClear, todos.length);
    });

    // Ticking a to-do says you did it. This says it was never yours, which is a
    // different thing: it covers work that was never theirs, work already done,
    // and work the brief simply got wrong, without making them say which. It
    // goes to memory, so it holds tomorrow as well as today.
    watchOpening(row.querySelector(".todo__source"), todo.origin);
    watchBurying(row.querySelector(".todo__bury"), todo.origin, row);

    const dismiss = row.querySelector(".todo__dismiss");
    dismiss.addEventListener("click", async () => {
      dismiss.disabled = true;
      const note = `They said this did not belong in their brief: ${prose(todo.title)}. Whatever the reason, it was wrong to raise, so do not raise it or anything like it again.`;
      try {
        await remember(note);
        keep(todo.origin, "rejected");
        row.classList.add("is-dismissed");
        setText(dismiss, "Won't come back");
      } catch {
        dismiss.disabled = false;
        setText(dismiss, "Try again");
      }
    });

    body.append(row);
  });

  body.append(allClear);
  syncAllClear(allClear, todos.length, { silent: true });
  return el;
}

function syncAllClear(el, total, { silent = false } = {}) {
  const complete = done.size >= total && total > 0;
  if (complete === !el.hidden) return;
  el.hidden = !complete;
  if (complete && !silent) {
    // Re-trigger the stamp animation on the transition into "done".
    const stamp = el.querySelector(".allclear__stamp");
    stamp.style.animation = "none";
    void stamp.offsetWidth;
    stamp.style.animation = "";
  }
}

// ── Updates ─────────────────────────────────────────────

function updateSection(index, updates) {
  const el = section(index, "New updates", `${updates.length} to know`);
  const body = el.querySelector(".movement__body");

  updates.forEach((update, i) => {
    const row = clone("tpl-update");
    setText(row.querySelector(".update__index"), roman(i + 1));

    const title = row.querySelector(".update__title");
    if (isSafeUrl(update.source_url)) {
      const link = document.createElement("a");
      link.href = update.source_url;
      link.target = "_blank";
      link.rel = "noopener";
      setProse(link, update.title);
      title.append(link);
    } else {
      title.append(document.createTextNode(prose(update.title)));
    }

    if (update.label) {
      const tag = document.createElement("span");
      tag.className = update.label_style === "active" ? "tag tag--active" : "tag";
      setText(tag, update.label);
      title.append(tag);
    }

    setProse(row.querySelector(".update__body"), update.body);

    const bullets = row.querySelector(".update__bullets");
    for (const bullet of update.bullets ?? []) {
      const li = document.createElement("li");
      setProse(li, bullet);
      bullets.append(li);
    }
    if (!bullets.children.length) bullets.remove();

    stamp(row, update);
    body.append(row);
  });

  return el;
}

/**
 * The line under an update: when and where it was said, and the one place it
 * points at. Details that read as clutter inside a sentence but as useful
 * chrome once they are set apart from it.
 */
function stamp(row, update) {
  const meta = row.querySelector(".update__meta");
  // An update that points somewhere is a thing with a place and a time, not a
  // paragraph: give it the card and let the destination be the object.
  if (isSafeUrl(update.link_url)) row.classList.add("update--card");
  const when = row.querySelector(".update__when");
  const link = row.querySelector(".update__link");

  const said = [
    update.when ? `Posted ${prose(update.when)}` : "",
    update.where ? `in ${prose(update.where)}` : "",
  ].filter(Boolean).join(" ");
  if (said) setText(when, said);
  else when.remove();

  if (isSafeUrl(update.link_url)) {
    link.href = update.link_url;
    setText(link, plainly(update.link_url));
  } else {
    link.remove();
  }

  // The server stitches origin on from the item it matched; placeOf is only
  // for briefs written before it did.
  silencer(row.querySelector(".update__mute"), update.origin || placeOf(update));

  if (!meta.children.length) meta.remove();
}

/**
 * Where an update came from, in the form the server mutes: a channel, or a
 * repository. Some updates come from nowhere in particular, and those simply
 * cannot be silenced.
 */
function placeOf(update) {
  if (update.where?.startsWith("#")) return update.where;
  try {
    const { hostname, pathname } = new URL(update.source_url);
    if (!hostname.endsWith("github.com")) return "";
    const [owner, repo] = pathname.replace(/^\//, "").split("/");
    return owner && repo ? `${owner}/${repo}` : "";
  } catch {
    return "";
  }
}

/**
 * Judging each message from a room one at a time never gets a whole room
 * right. This silences the place, before anything is gathered, so it costs
 * nothing to keep muted and no good sentence can argue it back in.
 */
function silencer(button, place) {
  if (!button) return;
  if (!place) {
    button.remove();
    return;
  }
  setText(button, `Nothing more from ${place}`);
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      await mute(place);
      keep(place, "rejected");
      setText(button, `Silenced ${place}`);
      button.classList.add("is-silenced");
    } catch {
      button.disabled = false;
      setText(button, "Try again");
    }
  });
}

/** A url as someone would read it aloud: no scheme, no www, no query. */
function plainly(url) {
  try {
    const { hostname, pathname } = new URL(url);
    return (hostname.replace(/^www\./, "") + pathname).replace(/\/$/, "");
  } catch {
    return url;
  }
}

// ── Your day ────────────────────────────────────────────

function scheduleSection(index, meetings) {
  const el = section(index, "Your day", `${meetings.length} on the books`);
  const card = clone("tpl-schedule");
  const list = card.querySelector(".schedule__list");
  const detail = card.querySelector(".schedule__detail");

  const show = (i) => {
    const meeting = meetings[i];
    detail.classList.add("is-swapping");
    setTimeout(() => {
      setText(detail.querySelector(".schedule__detail-when"), [meeting.time, meeting.end_time].filter(Boolean).join(" – "));
      setProse(detail.querySelector(".schedule__detail-title"), meeting.title);
      setProse(detail.querySelector(".schedule__detail-note"), meeting.note);

      const prep = detail.querySelector(".schedule__prep");
      const href = chatLink(meeting.prep_prompt);
      prep.hidden = !isSafeUrl(href);
      if (!prep.hidden) {
        prep.href = href;
        prep.title = meeting.prep_prompt;
      }
      detail.classList.remove("is-swapping");
    }, 120);

    for (const [j, slot] of [...list.children].entries()) slot.classList.toggle("is-active", i === j);
  };

  const upNext = nextMeetingIndex(meetings);

  meetings.forEach((meeting, i) => {
    const slot = clone("tpl-schedule-row");
    setText(slot.querySelector(".slot__time"), meeting.time);
    setProse(slot.querySelector(".slot__title"), meeting.title);
    slot.querySelector(".slot__next").hidden = i !== upNext;
    slot.addEventListener("mouseenter", () => show(i));
    slot.addEventListener("focus", () => show(i));
    slot.addEventListener("click", () => show(i));
    list.append(slot);
  });

  el.querySelector(".movement__body").append(card);
  show(upNext);
  return el;
}

/** "2:00 PM" → minutes since midnight, or null if it isn't a clock time. */
function minutesOfDay(label) {
  const match = /^(\d{1,2})(?::(\d{2}))?\s*([ap])\.?m\.?$/i.exec(String(label ?? "").trim());
  if (!match) return null;
  const [, hour, minute = "0", half] = match;
  const base = (Number(hour) % 12) * 60 + Number(minute);
  return half.toLowerCase() === "p" ? base + 720 : base;
}

/** The first meeting that hasn't started yet — the one you actually need. */
function nextMeetingIndex(meetings) {
  const now = new Date();
  const nowMinutes = now.getHours() * 60 + now.getMinutes();
  const index = meetings.findIndex((meeting) => {
    const start = minutesOfDay(meeting.time);
    return start !== null && start >= nowMinutes;
  });
  return index === -1 ? 0 : index;
}

/** 1 → I, 4 → IV. Only ever asked for single digits here, but it's four lines. */
function roman(value) {
  const table = [[10, "X"], [9, "IX"], [5, "V"], [4, "IV"], [1, "I"]];
  let out = "";
  for (const [amount, glyph] of table) while (value >= amount) (out += glyph), (value -= amount);
  return out;
}

/** "claude-haiku-4-5" -> "Claude Haiku 4.5" */
function modelName(id = "") {
  const known = { "claude-opus-5": "Claude Opus 5", "claude-sonnet-5": "Claude Sonnet 5", "claude-haiku-4-5": "Claude Haiku 4.5" };
  return known[id] ?? id;
}

// ── Looking ahead ───────────────────────────────────────

function aheadSection(index, blurb) {
  const el = section(index, "Looking ahead");
  const p = document.createElement("p");
  p.className = "ahead";
  setProse(p, blurb);
  el.querySelector(".movement__body").append(p);
  return el;
}

// ── Colophon ────────────────────────────────────────────

function renderColophon(brief) {
  const sources = brief.data?.footer?.sources ?? [];
  const line = $("colophonSources");
  line.replaceChildren();

  // No byline. The sources are worth naming; the author isn't.
  if (sources.length) {
    const lead = document.createElement("span");
    setText(lead, "From");
    line.append(lead);
  }

  sources.forEach((source, i) => {
    const node = isSafeUrl(source.url) ? document.createElement("a") : document.createElement("span");
    if (node.tagName === "A") {
      node.href = source.url;
      node.target = "_blank";
      node.rel = "noopener";
    }
    const mark = sourceMark(source.source_icon_key);
    if (mark) node.append(mark);
    const name = document.createElement("span");
    setText(name, source.name + (i < sources.length - 1 ? "," : ""));
    node.append(name);
    line.append(node);
  });

  // A brief written by the fallback model reads differently. Say so.
  const downgraded = $("colophonModel");
  downgraded.hidden = !brief.downgradedFrom;
  if (brief.downgradedFrom) {
    setText(
      downgraded,
      `${modelName(brief.downgradedFrom)} was unavailable this morning, so this is ${modelName(brief.model)}.`,
    );
  }

  const spent = $("colophonUsage");
  if (spent) {
    const usage = brief.usage ?? [];
    const write = usage.find((u) => u.purpose === "write");
    const triage = usage.filter((u) => u.purpose === "triage" || u.purpose === "carried");
    if (write) {
      const k = (n) => (n >= 1000 ? `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k` : `${n}`);
      const parts = [`${modelName(write.model)}, ${k(write.input)} in / ${k(write.output)} out`];
      if (write.cacheRead) parts.push(`${k(write.cacheRead)} cached`);
      if (triage.length) parts.push(`triage on ${modelName(triage[0].model)}, ${triage.length} call${triage.length === 1 ? "" : "s"}`);
      setText(spent, parts.join(" · "));
      spent.hidden = false;
    } else {
      spent.hidden = true;
    }
  }

  const gaps = (brief.sources ?? []).filter((source) => !source.ok);
  const gapsEl = $("colophonGaps");
  gapsEl.hidden = gaps.length === 0;
  if (gaps.length) {
    setText(
      gapsEl,
      `${gaps.map((s) => s.name).join(" and ")} didn't answer this morning. ` +
        `This brief was written without ${gaps.length > 1 ? "them" : "it"}.`,
    );
  }

  const plate = brief.painting;
  setText(
    $("colophonPlate"),
    plate?.caption ? `Plate: ${plate.caption}${plate.credit ? `. ${plate.credit}` : ""}` : "",
  );
}

// ── Reveal ──────────────────────────────────────────────

function revealOnScroll() {
  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.add("is-visible");
        observer.unobserve(entry.target);
      }
    },
    { rootMargin: "0px 0px -12% 0px" },
  );
  for (const movement of document.querySelectorAll(".movement")) observer.observe(movement);
}

boot().catch((error) => {
  // A blank page tells you nothing. Whatever went wrong, say it.
  showBlank(error.message ?? String(error));
  console.error(error);
});
