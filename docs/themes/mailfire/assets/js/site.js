// Mailfire documentation site: theme toggle, code copy buttons, table of
// contents scroll-spy, and client-side search. No dependencies beyond the
// vendored UIkit; no network requests except the site's own search index.

const root = document.documentElement;

// ---- Icons that UIkit's icon set does not have -------------------------

if (window.UIkit) {
  window.UIkit.icon.add({
    "mf-sun": '<svg width="20" height="20" viewBox="0 0 20 20"><circle fill="none" stroke="#000" stroke-width="1.1" cx="10" cy="10" r="3.6"/><g fill="none" stroke="#000" stroke-width="1.1" stroke-linecap="round"><path d="M10 1.8v2M10 16.2v2M1.8 10h2M16.2 10h2M4.2 4.2l1.4 1.4M14.4 14.4l1.4 1.4M4.2 15.8l1.4-1.4M14.4 5.6l1.4-1.4"/></g></svg>',
    "mf-moon": '<svg width="20" height="20" viewBox="0 0 20 20"><path fill="none" stroke="#000" stroke-width="1.1" stroke-linejoin="round" d="M16.5 12.6A7 7 0 0 1 7.4 3.5a7 7 0 1 0 9.1 9.1z"/></svg>',
  });
}

// ---- Theme toggle -------------------------------------------------------

function setTheme(theme) {
  root.setAttribute("data-theme", theme);
  try { localStorage.setItem("mf-theme", theme); } catch (e) { /* private mode */ }
}

document.querySelectorAll(".mf-theme-toggle").forEach((button) => {
  button.addEventListener("click", () => {
    setTheme(root.getAttribute("data-theme") === "dark" ? "light" : "dark");
  });
});

// Follow the operating system until the reader chooses explicitly.
if (window.matchMedia) {
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", (event) => {
    let saved = null;
    try { saved = localStorage.getItem("mf-theme"); } catch (e) { /* private mode */ }
    if (!saved) root.setAttribute("data-theme", event.matches ? "dark" : "light");
  });
}

// ---- Copy buttons -------------------------------------------------------

document.querySelectorAll(".highlight").forEach((block) => {
  const code = block.querySelector("code");
  if (!code || !navigator.clipboard) return;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "mf-copy";
  button.setAttribute("aria-label", "Copy code to clipboard");
  button.textContent = "Copy";
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(code.innerText.replace(/\n$/, ""));
      button.textContent = "Copied";
      button.classList.add("mf-copied");
    } catch (e) {
      button.textContent = "Press Ctrl+C";
    }
    setTimeout(() => {
      button.textContent = "Copy";
      button.classList.remove("mf-copied");
    }, 1800);
  });
  block.appendChild(button);
});

// ---- Table of contents scroll-spy ----------------------------------------

const tocLinks = Array.from(document.querySelectorAll("#TableOfContents a"));
if (tocLinks.length && "IntersectionObserver" in window) {
  const byId = new Map(tocLinks.map((a) => [decodeURIComponent(a.hash.slice(1)), a]));
  const headings = Array.from(byId.keys()).map((id) => document.getElementById(id)).filter(Boolean);
  const visible = new Set();
  const update = () => {
    const current = headings.find((h) => visible.has(h)) ||
      headings.filter((h) => h.getBoundingClientRect().top < 120).pop();
    tocLinks.forEach((a) => a.classList.remove("mf-active"));
    if (current) byId.get(current.id).classList.add("mf-active");
  };
  const observer = new IntersectionObserver((entries) => {
    entries.forEach((e) => (e.isIntersecting ? visible.add(e.target) : visible.delete(e.target)));
    update();
  }, { rootMargin: "-64px 0px -65% 0px" });
  headings.forEach((h) => observer.observe(h));
}

// ---- Search -------------------------------------------------------------

const input = document.getElementById("mf-search-input");
const results = document.getElementById("mf-search-results");
const script = document.querySelector("script[data-search-index]");
let index = null;
let loading = null;
let selected = -1;

function loadIndex() {
  if (!loading) {
    loading = fetch(script.dataset.searchIndex)
      .then((r) => (r.ok ? r.json() : []))
      .then((entries) => {
        index = entries.map((e) => ({
          ...e,
          titleLc: e.title.toLowerCase(),
          textLc: (e.description + " " + e.text).toLowerCase(),
        }));
      })
      .catch(() => { index = []; });
  }
  return loading;
}

const escapeHTML = (s) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const escapeRE = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function highlight(text, terms) {
  let html = escapeHTML(text);
  terms.forEach((t) => {
    html = html.replace(new RegExp("(" + escapeRE(escapeHTML(t)) + ")", "ig"), "<mark>$1</mark>");
  });
  return html;
}

function snippet(entry, terms) {
  // Prefer the description; fall back to the body where a term occurs.
  const has = (text) => terms.some((t) => text.toLowerCase().includes(t));
  const source = entry.description && (has(entry.description) || !has(entry.text)) ? entry.description : entry.text;
  if (!source) return "";
  const lc = source.toLowerCase();
  const at = Math.min(...terms.map((t) => { const i = lc.indexOf(t); return i < 0 ? Infinity : i; }));
  const start = Number.isFinite(at) ? Math.max(0, at - 50) : 0;
  return (start > 0 ? "…" : "") + source.slice(start, start + 150).trim() + (source.length > start + 150 ? "…" : "");
}

function score(entry, terms) {
  let total = 0;
  for (const t of terms) {
    const inTitle = entry.titleLc.includes(t);
    const count = entry.textLc.split(t).length - 1;
    if (!inTitle && !count) return 0; // every term must match
    if (entry.titleLc === t) total += 30;
    else if (entry.titleLc.startsWith(t)) total += 16;
    else if (inTitle) total += 10;
    // Pages that are about the term mention it often; grow slowly.
    total += 3 * Math.log2(1 + count);
  }
  return entry.url.includes("#") ? total - 1 : total; // a page outranks its own section
}

function render(query) {
  const terms = query.toLowerCase().split(/\s+/).filter((t) => t.length > 1);
  if (!terms.length) { close(); return; }
  const hits = index
    .map((e) => ({ e, s: score(e, terms) }))
    .filter((h) => h.s > 0)
    .sort((a, b) => b.s - a.s)
    .slice(0, 8);
  selected = hits.length ? 0 : -1;
  results.innerHTML = hits.length
    ? hits.map(({ e }, i) =>
      `<a href="${escapeHTML(e.url)}" role="option" id="mf-hit-${i}" aria-selected="${i === 0}">` +
      (e.section ? `<span class="mf-search-hit-section">${escapeHTML(e.section)}</span>` : "") +
      `<span class="mf-search-hit-title">${highlight(e.title, terms)}</span>` +
      (snippet(e, terms) ? `<span class="mf-search-hit-text">${highlight(snippet(e, terms), terms)}</span>` : "") +
      "</a>").join("")
    : `<p class="mf-search-empty">No results for “${escapeHTML(query)}”.</p>`;
  results.hidden = false;
  input.setAttribute("aria-expanded", "true");
  if (hits.length) input.setAttribute("aria-activedescendant", "mf-hit-0");
}

function close() {
  results.hidden = true;
  results.innerHTML = "";
  selected = -1;
  input.setAttribute("aria-expanded", "false");
  input.removeAttribute("aria-activedescendant");
}

function move(delta) {
  const hits = results.querySelectorAll("a");
  if (!hits.length) return;
  selected = (selected + delta + hits.length) % hits.length;
  hits.forEach((a, i) => a.setAttribute("aria-selected", String(i === selected)));
  input.setAttribute("aria-activedescendant", hits[selected].id);
  hits[selected].scrollIntoView({ block: "nearest" });
}

if (input && results && script) {
  input.addEventListener("focus", loadIndex);
  input.addEventListener("input", async () => {
    await loadIndex();
    render(input.value.trim());
  });
  input.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
    else if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
    else if (event.key === "Enter") {
      const hit = results.querySelectorAll("a")[selected];
      if (hit) window.location.href = hit.href;
    } else if (event.key === "Escape") { close(); input.blur(); }
  });
  document.addEventListener("click", (event) => {
    if (!event.target.closest(".mf-search")) close();
  });
  document.addEventListener("keydown", (event) => {
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName) || document.activeElement.isContentEditable;
    if (event.key === "/" && !typing && !event.metaKey && !event.ctrlKey) {
      event.preventDefault();
      input.focus();
    }
  });
}
