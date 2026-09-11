/**
 * Source marks.
 *
 * The model only ever supplies a *key*; the markup lives here, so nothing it
 * writes can reach innerHTML. Unknown keys fall back to the generic link mark.
 *
 * Slack and GitHub are their owners' trademarks, drawn here to identify the
 * source. Everything else is a neutral glyph in currentColor, which inherits
 * the page's ink in both light and dark.
 */

const SLACK = `<svg viewBox="0 0 32 32" aria-hidden="true">
  <g fill="#36C5F0"><rect x="8.43" y="0" width="6.72" height="6.72" rx="3.36"/><rect x="0" y="8.43" width="15.15" height="6.72" rx="3.36"/></g>
  <g fill="#E01E5A"><rect x="0" y="16.85" width="6.72" height="6.72" rx="3.36"/><rect x="8.43" y="16.85" width="6.72" height="15.15" rx="3.36"/></g>
  <g fill="#2EB67D"><rect x="25.28" y="8.43" width="6.72" height="6.72" rx="3.36"/><rect x="16.85" y="0" width="6.72" height="15.15" rx="3.36"/></g>
  <g fill="#ECB22E"><rect x="25.28" y="25.28" width="6.72" height="6.72" rx="3.36"/><rect x="16.85" y="16.85" width="15.15" height="6.72" rx="3.36"/></g>
</svg>`;

const GITHUB = `<svg viewBox="0 0 32 32" aria-hidden="true"><path fill="currentColor" d="M12.7945 0.252261C8.54358 1.22515 5.05925 3.64306 2.66203 7.30569C-1.53311 13.7582 -0.696867 22.414 4.65507 27.9366C6.25786 29.5962 8.69689 31.1986 10.5366 31.8138C11.2195 32.0428 11.3032 32.0428 11.6098 31.8281C11.9443 31.5992 11.9582 31.542 11.9582 29.8967V28.2084L10.7317 28.28C9.2962 28.3515 8.18121 28.094 7.51222 27.5074C7.27528 27.3071 6.81535 26.6203 6.49479 25.9908C5.96518 24.9321 5.57493 24.4457 4.48782 23.4585C4.12545 23.1294 4.11151 23.1151 4.36238 22.9291C4.96169 22.4713 6.17424 22.9148 6.94079 23.8591C8.27877 25.533 8.79445 25.9765 9.56101 26.1482C10.2439 26.2913 10.8711 26.234 11.8189 25.9479C11.9443 25.905 12.1115 25.6045 12.1952 25.2612C12.2788 24.9321 12.5018 24.4313 12.683 24.1595L13.0175 23.6588L11.8885 23.4298C9.51919 22.9291 8.05578 22.0564 7.0941 20.5684C6.20211 19.1949 5.90943 18.0074 5.89549 15.7898C5.89549 13.701 6.11849 12.8426 6.99654 11.6694C7.38678 11.1543 7.42859 10.9969 7.3171 10.6965C7.10804 10.1814 7.16379 8.00674 7.38678 7.34861C7.56797 6.86217 7.66553 6.77632 8.04184 6.7334C8.66902 6.66187 9.83975 7.07677 11.0244 7.76352L12.0419 8.36442L12.9338 8.14981C14.2021 7.84936 18.007 7.86367 19.1499 8.14981L20.014 8.37872L20.8224 7.87797C21.8955 7.21984 23.2614 6.70479 23.9443 6.70479C24.4879 6.70479 24.4879 6.71909 24.7109 7.44876C24.9478 8.29288 24.9896 9.82375 24.7806 10.5534C24.6691 10.9826 24.6969 11.1114 25.0036 11.5263C26.5227 13.5865 26.69 17.0059 25.4077 19.8101C24.5436 21.6558 22.7178 22.9434 20.2649 23.4155L19.0941 23.6445L19.5262 24.4313L19.9722 25.2325L20.014 28.423L20.0698 31.6135L20.4182 31.8425C20.7387 32.0571 20.8084 32.0571 21.4774 31.8138C22.5924 31.4275 24.5018 30.3688 25.5471 29.5533C31.7632 24.7747 33.7701 16.2047 30.3276 9.07977C28.3067 4.88778 24.4879 1.66867 20.1116 0.466868C18.2022 -0.0481892 14.6063 -0.162646 12.7945 0.252261Z"/></svg>`;

const CALENDAR = `<svg viewBox="0 0 32 32" fill="none" aria-hidden="true" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
  <rect x="3" y="6" width="26" height="23" rx="4"/><path d="M3 13h26"/><path d="M10 3v6M22 3v6"/>
  <path d="M10 20h4M19 20h3" stroke-width="2.6"/>
</svg>`;

const LINEAR = `<svg viewBox="0 0 32 32" fill="none" aria-hidden="true" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
  <rect x="3" y="3" width="26" height="26" rx="7"/><path d="M9 19.5 19.5 9M13 23 23 13M9.5 12.5 12.5 9.5M19.5 22.5 22.5 19.5"/>
</svg>`;

const NOTES = `<svg viewBox="0 0 32 32" fill="none" aria-hidden="true" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
  <path d="M7 4h14l5 5v19a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1Z"/><path d="M20 4v6h6"/>
  <path d="M11 17h10M11 22h6"/>
</svg>`;

const LINK = `<svg viewBox="0 0 32 32" fill="none" aria-hidden="true" stroke="currentColor" stroke-width="2.4" stroke-linecap="round">
  <path d="M13 19a5 5 0 0 0 7 0l4-4a5 5 0 0 0-7-7l-1.5 1.5"/><path d="M19 13a5 5 0 0 0-7 0l-4 4a5 5 0 0 0 7 7l1.5-1.5"/>
</svg>`;

const MARKS = {
  "provider:slack": SLACK,
  "provider:github": GITHUB,
  "provider:calendar": CALENDAR,
  "provider:gcal": CALENDAR,
  "provider:linear": LINEAR,
  "provider:granola": NOTES,
  "provider:notes": NOTES,
  "provider:link": LINK,
};

/**
 * A laurel sprig, generated rather than drawn by hand so the leaf count and
 * curve stay adjustable. `dir` is 1 for a sprig opening right, -1 for left.
 * The stem runs mostly horizontal — these flank a line of text.
 */
function laurelBranch(dir) {
  const leaves = [];
  const count = 7;
  const stemX = (t) => (2 * (1 - t) * t * 20 + t * t * 36) * dir;
  const stemY = (t) => 2 * (1 - t) * t * -9 + t * t * -6;

  for (let i = 1; i <= count; i++) {
    const t = i / (count + 0.6);
    const x = stemX(t);
    const y = stemY(t);
    // Leaves alternate above and below the stem, shrinking toward the tip.
    const above = i % 2 === 0;
    const tilt = (above ? -38 : 26) * dir;
    const size = 6.2 - i * 0.55;
    const offset = above ? -3.4 : 2.6;
    leaves.push(
      `<ellipse cx="${x.toFixed(1)}" cy="${(y + offset).toFixed(1)}" rx="${size.toFixed(1)}" ` +
        `ry="${(size * 0.4).toFixed(1)}" transform="rotate(${tilt} ${x.toFixed(1)} ${(y + offset).toFixed(1)})"/>`,
    );
  }

  return (
    `<path d="M0 0 Q ${20 * dir} -9 ${36 * dir} -6" fill="none" stroke="currentColor" stroke-width="1.1" ` +
      `stroke-linecap="round"/>` +
    `<g fill="currentColor" opacity="0.85">${leaves.join("")}</g>`
  );
}

const LAUREL = `<svg viewBox="-44 -18 88 28" fill="none" aria-hidden="true">${laurelBranch(1)}${laurelBranch(-1)}</svg>`;

/** Paint every [data-laurel] slot. */
export function paintLaurels(root = document) {
  for (const slot of root.querySelectorAll("[data-laurel]")) slot.innerHTML = LAUREL;
}

/** Orchard's blossom: four rings, drawn in currentColor. */
const BLOSSOM = `<svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><g stroke="currentColor" stroke-width="2.7">
  <circle cx="12" cy="7.5" r="4.05"/><circle cx="16.5" cy="12" r="4.05"/><circle cx="12" cy="16.5" r="4.05"/><circle cx="7.5" cy="12" r="4.05"/>
</g></svg>`;

/** Paint every [data-blossom] slot on the page. */
export function paintBlossoms(root = document) {
  for (const slot of root.querySelectorAll("[data-blossom]")) slot.innerHTML = BLOSSOM;
}

/** A <span> carrying the mark for `key`, or null when there's nothing to show. */
export function sourceMark(key, { title = "" } = {}) {
  const markup = MARKS[key] ?? (key ? LINK : null);
  if (!markup) return null;
  const span = document.createElement("span");
  span.className = "mark";
  span.innerHTML = markup; // constant markup from this module only
  if (title) span.title = title;
  return span;
}

export const hasMark = (key) => Boolean(MARKS[key]);
