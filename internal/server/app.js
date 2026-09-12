"use strict";
const $ = (sel, root) => (root || document).querySelector(sel);
const fmt = (n) => Number(n).toLocaleString("en-US");
// The one definition of "narrow": the stylesheet's breakpoint, so the
// layout and the behaviour that depends on it can never disagree.
const NARROW = matchMedia("(max-width: 720px)");

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

const KIND_LABELS = { photo: "photo", video: "video", gif: "GIF" };
const KIND_TABS = [
  ["all", "All"],
  ["photo", "Photos"],
  ["video", "Videos"],
];
const VIEW_TITLES = {
  timeline: "Timeline",
  activity: "Activity",
  settings: "Settings",
};
const SORTS = [
  "newest",
  "oldest",
  "added",
  "name",
  "largest",
  "longest",
  "shortest",
];

/* ---- State ---- */
const state = {
  view: "grid", // grid | timeline | activity | settings
  root: "", // one configured folder's resolved path, or "" for all of them
  q: "",
  kind: "all",
  dir: "", // a folder (relative to its root) and everything under it
  month: "",
  sort: "newest",
  page: 1,
  total: 0,
  counts: null,
  items: [],
};
let fetchController = null;
let statsCache = null;
let statusCache = null;
let foldersCache = null;

function withRoot(url) {
  if (!state.root) return url;
  return (
    url + (url.includes("?") ? "&" : "?") + "root=" + encodeURIComponent(state.root)
  );
}

async function loadStatus(force) {
  if (statusCache && !force) return statusCache;
  const res = await fetch("/api/status");
  if (!res.ok) throw new Error("status request failed");
  statusCache = await res.json();
  return statusCache;
}

const roots = () => (statusCache && statusCache.roots) || [];

async function ensureStats() {
  if (statsCache) return statsCache;
  const res = await fetch(withRoot("/api/stats"));
  if (!res.ok) throw new Error("stats request failed");
  statsCache = await res.json();
  return statsCache;
}

async function ensureFolders() {
  if (foldersCache) return foldersCache;
  const res = await fetch("/api/folders");
  if (!res.ok) throw new Error("folders request failed");
  foldersCache = (await res.json()).items;
  return foldersCache;
}

/* ---- Sidebar: the folder tree ---- */
// Which nodes are unfolded, by "root\ndir". Roots start open; whatever
// leads to the folder on screen is opened as it is selected.
const expanded = new Set();
let treeTouched = false;
const nodeKey = (root, dir) => root + "\n" + dir;

// Builds one tree per root out of the flat folder counts.
function buildTree(folders) {
  const byRoot = new Map();
  for (const r of roots()) {
    byRoot.set(r.path, {
      root: r.path, dir: "", name: r.name, count: 0, ok: r.ok, children: new Map(),
    });
  }
  for (const f of folders) {
    let node = byRoot.get(f.root);
    if (!node) {
      node = { root: f.root, dir: "", name: f.root.split("/").pop(), count: 0, ok: true, children: new Map() };
      byRoot.set(f.root, node);
    }
    if (f.dir === "") {
      node.count = f.count;
      continue;
    }
    let cur = node;
    let path = "";
    for (const part of f.dir.split("/")) {
      path = path ? path + "/" + part : part;
      let child = cur.children.get(part);
      if (!child) {
        child = { root: f.root, dir: path, name: part, count: 0, ok: true, children: new Map() };
        cur.children.set(part, child);
      }
      cur = child;
    }
    cur.count = f.count;
  }
  return [...byRoot.values()];
}

function isCurrent(node) {
  return state.view === "grid" && state.root === node.root && state.dir === node.dir;
}

function renderNode(node, depth) {
  const key = nodeKey(node.root, node.dir);
  const wrap = el("div", "node" + (depth === 0 ? " root" : ""));
  wrap.style.setProperty("--depth", String(depth));
  const hasKids = node.children.size > 0;
  if (hasKids && expanded.has(key)) wrap.classList.add("open");

  const label = el("button", "nlabel" + (node.ok ? "" : " offline"));
  if (isCurrent(node)) label.setAttribute("aria-current", "true");
  const caret = el("span", "caret" + (hasKids ? "" : " leaf"));
  if (hasKids) {
    caret.addEventListener("click", (e) => {
      e.stopPropagation();
      if (expanded.has(key)) expanded.delete(key);
      else expanded.add(key);
      wrap.classList.toggle("open", expanded.has(key));
    });
  }
  label.append(caret, el("span", "nname", node.name));
  if (node.count > 0) label.append(el("span", "n", fmt(node.count)));
  label.title = node.ok ? node.dir || node.root : "Not reachable";
  label.addEventListener("click", () => selectFolder(node.root, node.dir));
  wrap.append(label);

  if (hasKids) {
    const kids = el("div", "children");
    const sorted = [...node.children.values()].sort((a, b) =>
      a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }),
    );
    for (const child of sorted) kids.append(renderNode(child, depth + 1));
    wrap.append(kids);
  }
  return wrap;
}

async function renderTree() {
  const tree = $("#tree");
  let folders;
  try {
    folders = await ensureFolders();
  } catch {
    tree.textContent = "";
    tree.append(el("div", "spnote", "Could not load folders."));
    return;
  }
  const list = roots();
  if (!treeTouched) {
    for (const r of list) expanded.add(nodeKey(r.path, ""));
    treeTouched = true;
  }
  // The folder on screen must be visible, whatever was folded before.
  if (state.root) {
    expanded.add(nodeKey(state.root, ""));
    let path = "";
    for (const part of state.dir ? state.dir.split("/") : []) {
      path = path ? path + "/" + part : part;
      expanded.add(nodeKey(state.root, path));
    }
  }

  tree.textContent = "";
  const everything = el("div", "node");
  const all = el("button", "nlabel");
  if (state.view === "grid" && !state.root && !state.dir) all.setAttribute("aria-current", "true");
  all.append(el("span", "caret leaf"), el("span", "nname", "Everything"));
  const total = folders.filter((f) => f.dir === "").reduce((n, f) => n + f.count, 0);
  if (total > 0) all.append(el("span", "n", fmt(total)));
  all.addEventListener("click", () => selectFolder("", ""));
  everything.append(all);
  tree.append(everything);
  tree.append(el("div", "treehead", list.length === 1 ? "Folder" : "Folders"));
  for (const node of buildTree(folders)) tree.append(renderNode(node, 0));
  if (!list.length) tree.append(el("div", "spnote", "No folders yet — add one in Settings."));
}

function selectFolder(root, dir) {
  state.root = root;
  state.dir = dir;
  state.month = "";
  closeSide();
  if (state.view === "grid") resetAndLoad();
  else setView("grid");
  scrollTo({ top: 0 });
}

/* ---- Sidebar: links and status ---- */
function setupAlert() {
  const list = roots();
  if (!list.length) return "Add a folder";
  if (list.some((r) => !r.ok)) return "A folder is offline";
  return "";
}

function renderSideLinks() {
  const box = $("#sidelinks");
  box.textContent = "";
  const alert = setupAlert();
  for (const [view, label] of Object.entries(VIEW_TITLES)) {
    const b = el("button", "", label);
    if (state.view === view) b.setAttribute("aria-current", "true");
    if (view === "settings" && alert) b.prepend(el("span", "alert"));
    b.addEventListener("click", () => {
      closeSide();
      setView(view);
    });
    box.append(b);
  }
}

let lastStats = null;
async function loadStats() {
  lastStats = await ensureStats();
  renderSideStatus();
}
function renderSideStatus() {
  const box = $("#sidestatus");
  box.textContent = "";
  if (lastStats) {
    const t = lastStats.totals;
    box.append(fmt(t.files) + (t.files === 1 ? " file" : " files") + " · " + fmtBytes(t.bytes));
  }
  const alert = setupAlert();
  if (alert) {
    const chip = el("button", "warn", alert);
    chip.addEventListener("click", () => {
      closeSide();
      setView("settings");
    });
    box.append(el("br"), chip);
  }
  renderSideLinks();
}

/* ---- Drawer (narrow screens) ---- */
function openSide() {
  document.body.classList.add("sideopen");
  $("#backdrop").hidden = false;
}
function closeSide() {
  document.body.classList.remove("sideopen");
  $("#backdrop").hidden = true;
}
$("#menubtn").addEventListener("click", openSide);
$("#sideclose").addEventListener("click", closeSide);
$("#backdrop").addEventListener("click", closeSide);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && document.body.classList.contains("sideopen")) closeSide();
});

/* ---- Breadcrumbs ---- */
function renderCrumbs() {
  const box = $("#crumbs");
  box.textContent = "";
  const crumb = (label, onClick) => {
    const b = el("button", "", label);
    b.addEventListener("click", onClick);
    if (box.children.length) box.append(el("span", "sep", "›"));
    box.append(b);
  };
  crumb("Everything", () => selectFolder("", ""));
  if (state.view !== "grid") {
    crumb(VIEW_TITLES[state.view], () => {});
    return;
  }
  if (state.root) {
    const r = roots().find((x) => x.path === state.root);
    crumb(r ? r.name : state.root.split("/").pop(), () => selectFolder(state.root, ""));
    let path = "";
    for (const part of state.dir ? state.dir.split("/") : []) {
      const here = path ? path + "/" + part : part;
      path = here;
      crumb(part, () => selectFolder(state.root, here));
    }
  }
  if (state.q) crumb("“" + state.q + "”", () => {});
}

/* ---- Tabs with live counts ---- */
function renderTabs() {
  const tabs = $("#tabs");
  tabs.textContent = "";
  for (const [key, label] of KIND_TABS) {
    const b = el("button");
    b.setAttribute("aria-pressed", String(state.kind === key));
    b.append(label);
    if (state.counts && state.counts[key] != null)
      b.append(el("span", "n", fmt(state.counts[key])));
    b.addEventListener("click", () => {
      if (state.kind === key) return;
      state.kind = key;
      resetAndLoad();
    });
    tabs.append(b);
  }
}

/* ---- Icons / avatar ---- */
const svgNS = "http://www.w3.org/2000/svg";
function icon(kind) {
  const s = document.createElementNS(svgNS, "svg");
  s.setAttribute("viewBox", "0 0 12 12");
  const p = document.createElementNS(svgNS, "path");
  if (kind === "play") p.setAttribute("d", "M2.5 1.5 L10.5 6 L2.5 10.5 Z");
  s.append(p);
  return s;
}

function hueFor(key) {
  let h = 0;
  for (const c of key.toLowerCase()) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}
function avatarChar(str) {
  const graphemes =
    typeof Intl !== "undefined" && Intl.Segmenter
      ? Array.from(new Intl.Segmenter().segment(str), (s) => s.segment)
      : Array.from(str);
  const letter = graphemes.find((g) => /\p{L}|\p{N}/u.test(g));
  return (letter || graphemes[0] || "?").toUpperCase();
}
// Folders stand in for authors: the colour keys off the full folder path
// so two "Season 1" folders under different shows still tell apart.
const folderKey = (t) => t.root + "/" + (t.dir || "");
function avatarEl(t) {
  const a = el("span", "avatar");
  a.style.background = `hsl(${hueFor(folderKey(t))} 45% 45%)`;
  a.textContent = avatarChar(t.folder || "?");
  return a;
}

const fmtDate = (d) =>
  new Date(d).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
const shortDate = (d) =>
  new Date(d).toLocaleDateString(undefined, { month: "short", day: "numeric" });
const fmtBytes = (n) => {
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let u = -1;
  do {
    n /= 1024;
    u++;
  } while (n >= 1024 && u < units.length - 1);
  return n.toFixed(u >= 2 ? 1 : 0) + " " + units[u];
};
function fmtDuration(ms) {
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (n) => String(n).padStart(2, "0");
  return h ? h + ":" + pad(m) + ":" + pad(s) : m + ":" + pad(s);
}

/* ---- Media helpers ---- */
const mediaSrc = (m) => "/media/" + m.id + "/" + encodeURIComponent(m.name);
// One small frame per file, rendered by the local app: the only preview a
// phone can count on for a video, and far lighter than a NAS full of
// full-size photos.
// The query names the file's date and size: ids get reused after a
// re-index, and the browser would otherwise keep showing last week's frame
// for a different file.
const thumbSrc = (m) => "/thumb/" + m.id + "?v=" + Date.parse(m.mod_time) + "-" + m.size;
// Anything a browser cannot play as it is comes remuxed through ffmpeg.
const streamSrc = (m, t) => "/stream/" + m.id + (t > 0 ? "?t=" + t.toFixed(1) : "");
// iOS Safari draws nothing for a preload="metadata" video until it plays;
// asking for the frame a tenth of a second in gives it something to paint.
const videoSrc = (m) => mediaSrc(m) + "#t=0.1";
const isMotion = (m) => m.kind === "video";
const resLabel = (m) => {
  const h = Math.min(m.width || 0, m.height || 0) || m.height || 0;
  if (h >= 2000) return "4K";
  if (h >= 1000) return "1080p";
  if (h >= 700) return "720p";
  return "";
};

function badgeEl(t) {
  const wrap = el("div", "badges");
  if (t.kind === "video") {
    const b = el("span", "badge");
    b.append(icon("play"));
    if (t.duration_ms > 0) b.append(" " + fmtDuration(t.duration_ms));
    wrap.append(b);
    const res = resLabel(t);
    if (res) wrap.append(el("span", "badge right", res));
  } else if (t.kind === "gif") {
    wrap.append(el("span", "badge", "GIF"));
  }
  return wrap;
}

/* ---- Masonry columns ---- */
let cols = [];
let cardNodes = [];
function colCount() {
  if (NARROW.matches) return 2;
  const grid = $("#grid");
  const gap = parseFloat(getComputedStyle(grid).columnGap) || 12;
  return Math.max(1, Math.min(5, Math.floor((grid.clientWidth + gap) / (230 + gap))));
}
// Cards on their way out must stop loading: a browser keeps fetching a
// removed image, and sixty of them queue ahead of the next listing.
function abandonCards() {
  for (const img of $("#grid").querySelectorAll("img")) img.src = "";
  for (const v of $("#grid").querySelectorAll("video")) {
    v.removeAttribute("src");
    v.removeAttribute("poster");
    v.load();
  }
}
function setupColumns() {
  const grid = $("#grid");
  abandonCards();
  grid.textContent = "";
  cols = [];
  for (let i = 0; i < colCount(); i++) {
    const c = el("div", "gcol");
    grid.append(c);
    cols.push(c);
  }
}
function placeCard(node) {
  let best = cols[0];
  for (const c of cols) if (c.offsetHeight < best.offsetHeight) best = c;
  best.append(node);
}
let resizeTimer = null;
addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    if (!cardNodes.length || cols.length === colCount()) return;
    setupColumns();
    cardNodes.forEach(placeCard);
  }, 150);
});

/* ---- Cards ---- */
function card(t) {
  const c = el("button", "mcard");
  // Look the index up at click time — deletions splice state.items.
  c.addEventListener("click", () => openLb(state.items.indexOf(t)));

  const thumb = el("div", "thumb");
  // Reserve the real aspect ratio up front so masonry placement and
  // infinite-scroll measurements are correct before the file loads.
  if (t.width && t.height) thumb.style.aspectRatio = t.width + " / " + t.height;
  else if (t.kind === "video") thumb.style.aspectRatio = "16 / 9";

  const noPreview = () => {
    thumb.textContent = "";
    thumb.classList.add("pending");
    thumb.append(KIND_LABELS[t.kind] + " · no preview");
    thumb.append(badgeEl(t), caption(t));
  };
  if (t.kind === "video" && t.direct) {
    const v = document.createElement("video");
    // A phone needs the first-frame trick to paint anything; a desktop
    // browser keeps the poster until hover plays, and a film's opening
    // frame is black anyway.
    v.src = NARROW.matches ? videoSrc(t) : mediaSrc(t);
    v.poster = thumbSrc(t);
    v.muted = true;
    v.loop = true;
    v.playsInline = true;
    // The poster carries the card at rest, so a phone never fetches the
    // video itself; a mouse still gets its hover preview.
    v.preload = NARROW.matches ? "none" : "metadata";
    c.addEventListener("mouseenter", () => {
      v.play().catch(() => {});
    });
    c.addEventListener("mouseleave", () => {
      v.pause();
    });
    thumb.append(v);
  } else if (t.kind === "gif") {
    const img = document.createElement("img");
    img.src = mediaSrc(t);
    img.loading = "lazy";
    img.alt = "";
    thumb.append(img);
  } else {
    const img = document.createElement("img");
    img.src = thumbSrc(t);
    img.loading = "lazy";
    img.alt = "";
    // Without ffmpeg there is no thumbnail; a photo can still show itself.
    img.addEventListener("error", () => {
      if (t.kind === "photo" && !img.dataset.fallback) {
        img.dataset.fallback = "1";
        img.src = mediaSrc(t);
      } else noPreview();
    });
    thumb.append(img);
  }
  thumb.append(badgeEl(t), caption(t));
  c.append(thumb);
  return c;
}

// The caption rides over the foot of the picture: folder and date always,
// the file name on hover.
function caption(t) {
  const cap = el("div", "cap");
  cap.append(el("div", "capt", t.title));
  const m = el("div", "capm");
  m.append(avatarEl(t), el("span", "folder", t.folder), el("span", "when", shortDate(t.mod_time)));
  cap.append(m);
  return cap;
}

/* ---- Fetch + render ---- */
function params() {
  const p = new URLSearchParams();
  if (state.q) p.set("q", state.q);
  if (state.dir) p.set("dir", state.dir);
  if (state.month) p.set("month", state.month);
  if (state.kind !== "all") p.set("kind", state.kind);
  if (state.sort !== "newest") p.set("sort", state.sort);
  if (state.root) p.set("root", state.root);
  p.set("page", String(state.page));
  return p;
}

async function fetchInto(url, emptyMessage) {
  const grid = $("#grid");
  if (fetchController) fetchController.abort();
  fetchController = new AbortController();
  try {
    const res = await fetch(url, { signal: fetchController.signal });
    if (!res.ok) throw new Error("request failed");
    return await res.json();
  } catch (err) {
    if (err.name === "AbortError") return null;
    grid.classList.remove("loading");
    grid.textContent = "";
    grid.append(el("div", "empty", emptyMessage));
    $("#more").hidden = true;
    return null;
  }
}

function appendCards(items) {
  items.forEach((t) => {
    const node = card(t);
    cardNodes.push(node);
    placeCard(node);
  });
}

function updateCount() {
  $("#count").textContent = fmt(state.total) + (state.total === 1 ? " file" : " files");
}

async function loadPage(append) {
  const grid = $("#grid");
  if (!append) grid.classList.add("loading");

  const data = await fetchInto(
    "/api/items?" + params(),
    "Could not load the library — is Shelf running?",
  );
  if (!data) return;

  state.total = data.total;
  state.counts = data.counts || null;
  if (!append) {
    state.items = [];
    cardNodes = [];
    setupColumns();
  }
  state.items.push(...data.items);
  appendCards(data.items);

  if (!state.items.length) {
    grid.textContent = "";
    grid.append(
      el(
        "div",
        "empty",
        roots().length ? "Nothing here." : "No folders yet — add one in Settings.",
      ),
    );
  }
  updateCount();
  $("#more").hidden = state.items.length >= data.total;
  renderTabs();
  updateChip();
  grid.classList.remove("loading");
  maybeLoadMore();
}

/* ---- Infinite loading ---- */
let loadingMore = false;
let loadGen = 0;
function sentinelNear() {
  const s = $("#more");
  return !s.hidden && s.getBoundingClientRect().top < innerHeight + 600;
}
async function loadMoreItems() {
  if (loadingMore || $("#more").hidden) return 0;
  const gen = loadGen;
  loadingMore = true;
  state.page++;
  const before = state.items.length;
  await loadPage(true);
  loadingMore = false;
  return gen === loadGen ? state.items.length - before : 0;
}
async function maybeLoadMore() {
  if (!sentinelNear()) return;
  const gen = loadGen;
  if ((await loadMoreItems()) && gen === loadGen) maybeLoadMore();
}
new IntersectionObserver(
  (entries) => {
    if (entries.some((entry) => entry.isIntersecting)) maybeLoadMore();
  },
  { rootMargin: "600px 0px" },
).observe($("#more"));

let lastScrollY = scrollY;
addEventListener(
  "scroll",
  () => {
    const y = scrollY;
    if (Math.abs(y - lastScrollY) < 6) return;
    document.body.classList.toggle("scrolldown", y > lastScrollY && y > 80);
    lastScrollY = y;
  },
  { passive: true },
);

function syncURL() {
  const p = new URLSearchParams();
  if (state.view !== "grid") p.set("view", state.view);
  if (state.q) p.set("q", state.q);
  if (state.kind !== "all") p.set("kind", state.kind);
  if (state.dir) p.set("dir", state.dir);
  if (state.month) p.set("month", state.month);
  if (state.sort !== "newest") p.set("sort", state.sort);
  if (state.root) p.set("root", state.root);
  const qs = p.toString();
  history.replaceState(null, "", qs ? "?" + qs : location.pathname);
}

function resetAndLoad() {
  loadGen++;
  state.page = 1;
  $("#refresh").hidden = true;
  syncURL();
  renderCrumbs();
  renderTree();
  loadPage(false);
}

const monthName = (ym) => {
  const [y, mo] = ym.split("-").map(Number);
  return new Date(y, mo - 1, 1).toLocaleDateString(undefined, {
    month: "short",
    year: "numeric",
  });
};

function updateChip() {
  const mchip = $("#mchip");
  mchip.hidden = !state.month;
  if (state.month) $("span", mchip).textContent = monthName(state.month);
}

/* ---- Views ---- */
function applyView() {
  const inGrid = state.view === "grid";
  for (const sel of [".bar", "#grid"]) $(sel).hidden = !inGrid;
  $("#sort").parentElement.hidden = !inGrid;
  if (!inGrid) {
    $("#more").hidden = true;
    $("#mchip").hidden = true;
  }
  $("#subpage").hidden = inGrid;
}

function setView(view) {
  state.view = view;
  syncURL();
  applyView();
  renderSideLinks();
  if (view === "grid") resetAndLoad();
  else {
    renderCrumbs();
    renderTree();
    renderSubpage();
  }
}

function clearFilters() {
  state.q = "";
  $("#q").value = "";
  state.kind = "all";
  state.dir = "";
  state.root = "";
  state.month = "";
}

function goHome() {
  clearFilters();
  state.sort = "newest";
  $("#sort").value = "newest";
  closeSide();
  setView("grid");
}

async function renderSubpage() {
  const sp = $("#subpage");
  sp.textContent = "";
  const head = el("div", "sphead");
  head.append(el("h2", "sptitle", VIEW_TITLES[state.view] || ""));
  sp.append(head);
  const body = el("div", "spbody");
  sp.append(body);
  body.append(el("div", "spnote", "Loading…"));
  try {
    if (state.view === "timeline") await renderTimeline(body);
    else if (state.view === "activity") await renderActivity(body);
    else if (state.view === "settings") await renderSettings(body);
  } catch {
    body.textContent = "";
    body.append(el("div", "spnote", "Could not load — is Shelf running?"));
  }
}

function statRow(cls, cells, count, max, onClick) {
  const row = el("button", "srow " + cls);
  for (const cell of cells) row.append(cell);
  const track = el("span", "strack");
  const bar = el("span", "sbar");
  bar.style.width = (100 * count) / max + "%";
  track.append(bar);
  row.append(track, el("span", "scount", fmt(count)));
  row.addEventListener("click", onClick);
  return row;
}

async function renderTimeline(body) {
  const stats = await ensureStats();
  body.textContent = "";
  const monthly = (stats.monthly || []).filter((m) => m.count > 0).reverse();
  if (!monthly.length) {
    body.append(el("div", "spnote", "Nothing indexed yet."));
    return;
  }
  body.append(
    el("p", "spsummary", "By file date" + (state.root ? ", within the selected folder" : "") + "."),
  );
  const max = Math.max(1, ...monthly.map((m) => m.count));
  for (const m of monthly) {
    body.append(
      statRow("plain", [el("span", "slabel", monthName(m.month))], m.count, max, () => {
        state.month = m.month;
        setView("grid");
      }),
    );
  }
}

/* ---- Activity: live tail of the app's log ---- */
let logTimer = null;
let logLastId = 0;
function logRow(entry) {
  const row = el("div", "logrow " + entry.level);
  const marks = { info: "•", warn: "!", error: "✗" };
  row.append(
    el("span", "ltime", new Date(entry.time).toLocaleTimeString(undefined, { hour12: false })),
    el("span", "lmark", marks[entry.level] || "•"),
    el("span", "lmsg", entry.msg),
  );
  return row;
}
async function renderActivity(body) {
  body.classList.add("wide");
  body.textContent = "";
  const list = el("div", "loglist");
  body.append(list);
  logLastId = 0;
  clearInterval(logTimer);
  const poll = async () => {
    if (state.view !== "activity") {
      clearInterval(logTimer);
      logTimer = null;
      return;
    }
    let data;
    try {
      const res = await fetch("/api/logs?after=" + logLastId);
      if (!res.ok) throw new Error();
      data = await res.json();
    } catch {
      return;
    }
    if (!data.items.length) {
      if (!list.children.length) list.append(el("div", "spnote", "No activity yet."));
      return;
    }
    const note = list.querySelector(".spnote");
    if (note) note.remove();
    const stick = list.scrollHeight - list.scrollTop - list.clientHeight < 40;
    for (const entry of data.items) list.append(logRow(entry));
    while (list.children.length > 500) list.firstChild.remove();
    logLastId = data.last_id;
    if (stick) list.scrollTop = list.scrollHeight;
  };
  await poll();
  list.scrollTop = list.scrollHeight;
  logTimer = setInterval(poll, 2000);
}

/* ---- Settings: the folders Shelf indexes ---- */
async function saveSettings(patch) {
  let res;
  try {
    res = await fetch("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch),
    });
  } catch {
    res = null;
  }
  if (!res || !res.ok) {
    alert("Could not save — is Shelf running?");
    return false;
  }
  statusCache = await loadStatus(true);
  statsCache = null;
  foldersCache = null;
  renderTree();
  loadStats().catch(() => {});
  if (state.view !== "grid") renderSubpage();
  return true;
}

async function renderSettings(body) {
  const status = await loadStatus(true);
  body.textContent = "";

  const panel = el("div", "panel");
  panel.append(el("h3", "", "Folders"));
  panel.append(
    el(
      "p",
      "",
      "Each folder is indexed with everything under it. A path on this machine, or an smb://host/share URL — a share is mounted through Finder when it is not already.",
    ),
  );
  const list = el("div");
  const specs = status.roots.map((r) => r.spec);
  for (const r of status.roots) {
    const row = el("div", "rootrow");
    row.append(el("span", "dot" + (r.ok ? " ok" : " warn")));
    const p = el("span", "rpath", r.spec);
    if (r.path && r.path !== r.spec) p.append(el("small", "", r.path + (r.ok ? "" : " — not reachable")));
    else if (!r.ok) p.append(el("small", "", "not reachable"));
    row.append(p);
    const rm = el("button", "", "Remove");
    rm.addEventListener("click", () => saveSettings({ roots: specs.filter((s) => s !== r.spec) }));
    row.append(rm);
    list.append(row);
  }
  if (!status.roots.length) list.append(el("p", "", "No folders yet."));
  panel.append(list);

  const field = el("div", "field");
  const input = el("input");
  input.type = "text";
  input.placeholder = "/Volumes/Media or smb://nas/share";
  const add = () => {
    const spec = input.value.trim();
    if (!spec || specs.includes(spec)) return;
    saveSettings({ roots: specs.concat(spec) });
  };
  const addBtn = el("button", "btn", "Add folder");
  addBtn.addEventListener("click", add);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") add();
  });
  field.append(input, addBtn);
  panel.append(field);
  body.append(panel);

  const ign = el("div", "panel");
  ign.append(el("h3", "", "Ignored folders"));
  ign.append(
    el(
      "p",
      "",
      "Skipped when indexing, with everything inside them. A bare name (Trash) matches that folder anywhere; a path (Shows/Extras) matches that folder under any root.",
    ),
  );
  const ignored = status.ignore || [];
  for (const pattern of ignored) {
    const row = el("div", "rootrow");
    row.append(el("span", "rpath", pattern));
    const rm = el("button", "", "Remove");
    rm.addEventListener("click", () => saveSettings({ ignore: ignored.filter((s) => s !== pattern) }));
    row.append(rm);
    ign.append(row);
  }
  if (!ignored.length) ign.append(el("p", "", "Nothing ignored."));
  const ifield = el("div", "field");
  const iinput = el("input");
  iinput.type = "text";
  iinput.placeholder = "Trash or Shows/Extras";
  const addIgnore = () => {
    const pattern = iinput.value.trim().replace(/^\/+|\/+$/g, "");
    if (!pattern || ignored.includes(pattern)) return;
    saveSettings({ ignore: ignored.concat(pattern) });
  };
  const iadd = el("button", "btn quiet", "Ignore folder");
  iadd.addEventListener("click", addIgnore);
  iinput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") addIgnore();
  });
  ifield.append(iinput, iadd);
  ign.append(ifield);
  body.append(ign);

  const scan = el("div", "panel");
  scan.append(el("h3", "", "Index"));
  const when = status.last_scan && !status.last_scan.startsWith("0001")
    ? "Last scanned " + new Date(status.last_scan).toLocaleString()
    : "Not scanned yet";
  scan.append(
    el(
      "p",
      "",
      when + ". Folders are re-scanned every few minutes; files that changed get their metadata read again.",
    ),
  );
  const rescan = el("button", "btn quiet", status.scanning ? "Scanning…" : "Rescan now");
  rescan.disabled = status.scanning;
  rescan.addEventListener("click", async () => {
    rescan.disabled = true;
    rescan.textContent = "Scanning…";
    try {
      await fetch("/api/rescan", { method: "POST" });
    } catch {}
    pollStatus();
  });
  scan.append(rescan);
  body.append(scan);

  if (!status.ffmpeg) {
    const warn = el("div", "panel");
    warn.append(el("h3", "", "ffmpeg not found"));
    warn.append(
      el(
        "p",
        "",
        "Install ffmpeg (brew install ffmpeg) for thumbnails and to play mkv and other formats browsers cannot open on their own.",
      ),
    );
    body.append(warn);
  }
}

/* ---- Lightbox ---- */
let lbIndex = 0;
function openLb(i) {
  lbIndex = i;
  resetFeed();
  const asFeed = NARROW.matches;
  $("#lb").classList.toggle("feed", asFeed);
  $("#lb").classList.remove("chrome");
  $("#lb").classList.add("open");
  document.body.style.overflow = "hidden";
  if (asFeed) openFeed(i);
  else fillLb();
}
function closeLb() {
  $("#lb").classList.remove("open");
  document.body.style.overflow = "";
  $("#lbmedia").textContent = ""; // stop any playing video
  resetFeed();
}
NARROW.addEventListener("change", () => {
  if ($("#lb").classList.contains("open")) openLb(lbIndex);
});

/* ---- Mobile feed ----
   One pane per file, snapped to the viewport, so the browser owns the
   scrolling physics. Panes carry only media: the name, details and actions
   stay in the single .lb-side, overlaid and refilled as the showing pane
   changes. Only the three panes around the reader hold real media. */
let feedPanes = [];
let feedObserver = null;
let feedLive = false;
const filledPanes = new Set();

function resetFeed() {
  cancelTap();
  unbindControls();
  userPaused = false;
  $("#lb").classList.remove("hasvideo", "paused", "streamed");
  if (feedObserver) feedObserver.disconnect();
  feedObserver = null;
  feedLive = false;
  feedPanes = [];
  filledPanes.clear();
  const box = $("#lbfeed");
  box.textContent = "";
  box.style.scrollSnapType = "";
  box.hidden = true;
}

function openFeed(i) {
  const box = $("#lbfeed");
  box.hidden = false;
  feedObserver = new IntersectionObserver(onFeedPane, { root: box, threshold: 0.6 });
  appendPanes(state.items.length);
  fillLbSide();
  windowPanes(i);
  box.scrollTop = i * box.clientHeight;
  feedLive = true;
  for (const pane of feedPanes) feedObserver.observe(pane);
  extendFeed();
}

function appendPanes(count) {
  const box = $("#lbfeed");
  for (let k = 0; k < count; k++) {
    const j = feedPanes.length;
    const pane = el("div", "lb-pane");
    pane.dataset.i = String(j);
    pane.addEventListener("click", onPaneTap);
    feedPanes.push(pane);
    box.append(pane);
    if (feedLive && feedObserver) feedObserver.observe(pane);
  }
}

function onFeedPane(entries) {
  if (!feedLive) return;
  for (const e of entries) {
    if (!e.isIntersecting) continue;
    const i = Number(e.target.dataset.i);
    if (i === lbIndex) continue;
    lbIndex = i;
    fillLbSide();
    windowPanes(i);
    extendFeed();
  }
}

async function extendFeed() {
  while (feedLive && lbIndex >= feedPanes.length - 3) {
    if (feedPanes.length >= state.items.length) {
      const added = await loadMoreItems();
      if (!added || !feedLive) return;
    }
    growFeed();
  }
}

// Appending to a mandatory-snap scroller makes iOS Safari re-snap to the
// FIRST pane; lift the snapping around the append and hold the offset.
function growFeed() {
  const box = $("#lbfeed");
  const at = box.scrollTop;
  box.style.scrollSnapType = "none";
  appendPanes(state.items.length - feedPanes.length);
  box.scrollTop = at;
  requestAnimationFrame(() => {
    box.scrollTop = at;
    box.style.scrollSnapType = "";
  });
}

function windowPanes(i) {
  for (const j of [...filledPanes]) if (Math.abs(j - i) > 1) clearPane(j);
  for (const j of [i - 1, i, i + 1]) fillPane(j);
  pausePane(i - 1);
  pausePane(i + 1);
  playPane(i);
  bindControls();
  cancelTap();
}

function fillPane(j) {
  const pane = feedPanes[j];
  const t = state.items[j];
  if (!pane || !t || filledPanes.has(j)) return;
  filledPanes.add(j);
  const slide = el("div", "lb-slide");
  slide.append(lbMediaEl(t, true));
  pane.append(slide);
}

function clearPane(j) {
  const pane = feedPanes[j];
  if (!pane) return;
  filledPanes.delete(j);
  pane.textContent = ""; // drops the <video>, stopping its download
}

function paneVideo(j) {
  const pane = feedPanes[j];
  return pane ? pane.querySelector("video") : null;
}

let feedMuted = false;
let userPaused = false;

function playPane(j) {
  const v = paneVideo(j);
  if (!v) return;
  userPaused = false;
  if (feedMuted) v.muted = true;
  playFeedVideo(v, j);
}

function playFeedVideo(v, j) {
  v.play().catch((err) => {
    if (!feedLive || j !== lbIndex || userPaused || !v.paused) return;
    if (paneVideo(j) !== v) return;
    if (err && err.name === "NotAllowedError") {
      if (!v.muted) {
        v.muted = true;
        v.dataset.mutedByPolicy = "1";
      }
      v.play().catch(() => {});
    } else {
      setTimeout(() => {
        if (feedLive && j === lbIndex && !userPaused && v.paused) playFeedVideo(v, j);
      }, 150);
    }
  });
}

function pausePane(j) {
  const v = paneVideo(j);
  if (v) v.pause();
}

/* ---- Gesture priming (iOS only unlocks sound from a user gesture) ---- */
function primePane(j) {
  const v = paneVideo(j);
  if (!v || v.dataset.primed) return;
  v.dataset.primed = "1";
  const keepMuted = v.muted;
  v.muted = true;
  const p = v.play();
  v.pause();
  if (p) p.catch(() => {});
  v.muted = keepMuted;
}

let gestureResumeAt = 0;

function onFeedGesture() {
  if (!feedLive) return;
  const v = ctlVideo;
  if (v && !userPaused) {
    if (v.dataset.mutedByPolicy && !feedMuted) {
      delete v.dataset.mutedByPolicy;
      v.muted = false;
    }
    if (v.paused) {
      gestureResumeAt = performance.now();
      v.play().catch(() => {});
    }
  }
  primePane(lbIndex - 1);
  primePane(lbIndex + 1);
}
$("#lbfeed").addEventListener("touchend", onFeedGesture, { passive: true });
$("#lbfeed").addEventListener("pointerup", onFeedGesture);

function toggleChrome() {
  $("#lb").classList.toggle("chrome");
  bindControls();
}

/* ---- Tap and double tap ---- */
const DOUBLE_TAP_MS = 260;
const SEEK_STEP = 10;
let tapTimer = null;
let jumpTimer = null;

function cancelTap() {
  clearTimeout(tapTimer);
  tapTimer = null;
}

function onPaneTap(e) {
  if (!ctlVideo) return toggleChrome();
  if (tapTimer) {
    cancelTap();
    seekBy(e.clientX < innerWidth / 2 ? -SEEK_STEP : SEEK_STEP);
    return;
  }
  const revived = performance.now() - gestureResumeAt < 500;
  const centered =
    Math.abs(e.clientX - innerWidth / 2) < innerWidth / 4 &&
    Math.abs(e.clientY - innerHeight / 2) < innerHeight / 4;
  tapTimer = setTimeout(() => {
    tapTimer = null;
    if (!centered) toggleChrome();
    else if (!revived) togglePlay();
  }, DOUBLE_TAP_MS);
}

function togglePlay() {
  const v = ctlVideo;
  if (!v) return;
  userPaused = !v.paused;
  if (v.paused) v.play().catch(() => {});
  else v.pause();
}

/* ---- Video position, for files and for streams alike ----
   A remuxed stream has no duration the browser knows and cannot be
   seeked in; the page knows both from the index, and seeks by reopening
   the stream further in and remembering the offset. */
const vidDur = (v) =>
  v.dataset.stream ? Number(v.dataset.duration) / 1000 : v.duration;
const vidAt = (v) => (Number(v.dataset.offset) || 0) + v.currentTime;
function vidSeek(v, secs) {
  const dur = vidDur(v);
  if (!Number.isFinite(dur) || dur <= 0) return;
  secs = Math.min(Math.max(0, secs), Math.max(0, dur - 1));
  if (!v.dataset.stream) {
    v.currentTime = secs;
    return;
  }
  const wasPaused = v.paused;
  v.dataset.offset = String(secs);
  v.src = streamSrc({ id: Number(v.dataset.id) }, secs);
  if (!wasPaused || !userPaused) v.play().catch(() => {});
}

function seekBy(step) {
  const v = ctlVideo;
  if (!v) return;
  vidSeek(v, vidAt(v) + step);
  const jump = $("#lbjump");
  jump.className = "lb-jump on " + (step < 0 ? "back" : "fwd");
  jump.textContent = (step < 0 ? "« " : "") + Math.abs(step) + "s" + (step < 0 ? "" : " »");
  clearTimeout(jumpTimer);
  jumpTimer = setTimeout(() => jump.classList.remove("on"), 400);
}

/* ---- Video controls: one set, re-pointed at whichever video shows ---- */
let ctlVideo = null;
const CTL_EVENTS = ["timeupdate", "play", "pause", "loadedmetadata", "volumechange", "ended"];

function currentVideo() {
  if ($("#lb").classList.contains("feed")) return paneVideo(lbIndex);
  return $("#lbmedia video");
}
function unbindControls() {
  if (ctlVideo) for (const name of CTL_EVENTS) ctlVideo.removeEventListener(name, syncControls);
  ctlVideo = null;
}
function bindControls() {
  const v = currentVideo() || null;
  if (v !== ctlVideo) {
    unbindControls();
    ctlVideo = v;
    if (v) for (const name of CTL_EVENTS) v.addEventListener(name, syncControls);
  }
  $("#lb").classList.toggle("streamed", !!(v && v.dataset.stream));
  syncControls();
}

function paintProgress(frac) {
  const pct = 100 * Math.min(1, Math.max(0, frac || 0)) + "%";
  $("#lbfill").style.width = pct;
  $("#lbbarfill").style.width = pct;
}

function syncControls() {
  const v = ctlVideo;
  $("#lb").classList.toggle("hasvideo", !!v);
  $("#lb").classList.toggle("paused", !!v && v.paused && userPaused);
  if (!v || seeking) return;
  const dur = vidDur(v) || 0;
  const at = vidAt(v) || 0;
  paintProgress(dur ? at / dur : 0);
  $("#lbat").textContent = fmtDuration(at * 1000);
  $("#lbdur").textContent = fmtDuration(dur * 1000);
  $("#lbplay").textContent = v.paused ? "▶" : "❚❚";
  $("#lbplay").setAttribute("aria-label", v.paused ? "Play" : "Pause");
  $("#lbmute").classList.toggle("muted", v.muted);
  $("#lbmute").setAttribute("aria-label", v.muted ? "Unmute" : "Mute");
}

let seeking = false;
function seekTo(e, commit) {
  const v = ctlVideo;
  if (!v) return;
  const dur = vidDur(v);
  if (!Number.isFinite(dur) || dur <= 0) return;
  const rail = $("#lbseek").getBoundingClientRect();
  const at = Math.min(1, Math.max(0, (e.clientX - rail.left) / (rail.width || 1)));
  paintProgress(at);
  $("#lbat").textContent = fmtDuration(at * dur * 1000);
  // A file follows the finger as it drags; a stream would reopen ffmpeg
  // on every pixel, so it seeks once the finger lifts.
  if (!v.dataset.stream || commit) vidSeek(v, at * dur);
}
$("#lbseek").addEventListener("pointerdown", (e) => {
  if (!ctlVideo) return;
  e.preventDefault();
  seeking = true;
  $("#lb").classList.add("seeking");
  try {
    $("#lbseek").setPointerCapture(e.pointerId);
  } catch {}
  seekTo(e, false);
});
$("#lbseek").addEventListener("pointermove", (e) => {
  if (seeking) seekTo(e, false);
});
for (const name of ["pointerup", "pointercancel"]) {
  $("#lbseek").addEventListener(name, (e) => {
    if (seeking && name === "pointerup") seekTo(e, true);
    seeking = false;
    $("#lb").classList.remove("seeking");
  });
}
$("#lbplay").addEventListener("click", togglePlay);
$("#lbmute").addEventListener("click", () => {
  if (!ctlVideo) return;
  feedMuted = ctlVideo.muted = !ctlVideo.muted;
});

function lbMediaEl(item, feed) {
  if (item.kind === "video") {
    const v = document.createElement("video");
    v.poster = thumbSrc(item);
    v.preload = "metadata";
    v.playsInline = true;
    v.dataset.id = String(item.id);
    if (item.direct) {
      v.src = videoSrc(item);
      if (feed) v.loop = true;
      else v.controls = true;
    } else {
      v.src = streamSrc(item, 0);
      v.dataset.stream = "1";
      v.dataset.offset = "0";
      v.dataset.duration = String(item.duration_ms || 0);
      v.classList.add("streamed");
      // No native controls on a stream: the panel's own row drives it,
      // and a click on the picture plays or pauses like a player would.
      if (!feed) v.addEventListener("click", togglePlay);
    }
    return v;
  }
  const img = document.createElement("img");
  img.src = mediaSrc(item);
  img.alt = "";
  return img;
}
function playLbVideo(container) {
  const v = container.querySelector("video");
  if (!v) return;
  v.play().catch(() => {
    v.muted = true;
    v.play().catch(() => {});
  });
}
function renderLbMedia(t) {
  const m = $("#lbmedia");
  m.textContent = "";
  m.append(lbMediaEl(t, false));
  bindControls();
  playLbVideo(m);
}

function fillLb() {
  const t = state.items[lbIndex];
  if (!t) return;
  renderLbMedia(t);
  fillLbSide();
}

function detailsFor(t) {
  const parts = [];
  if (t.width && t.height) parts.push(t.width + "×" + t.height);
  if (t.duration_ms > 0) parts.push(fmtDuration(t.duration_ms));
  parts.push(fmtBytes(t.size));
  if (t.vcodec) parts.push(t.vcodec + (t.acodec ? " / " + t.acodec : ""));
  else if (t.ext) parts.push(t.ext.toUpperCase());
  return parts.join(" · ");
}

// Everything about the file that is not its picture. The feed shows the
// same element as an overlay, moving it to whichever pane is on screen.
function fillLbSide() {
  const t = state.items[lbIndex];
  if (!t) return;
  const av = avatarEl(t);
  av.id = "lbavatar";
  $("#lbavatar").replaceWith(av);
  $("#lbname").textContent = t.title;
  $("#lbhandle").textContent = t.dir ? t.dir : t.root_name;
  $("#lbfilter").textContent = "More from " + t.folder;
  const details = $("#lbdetails");
  details.textContent = detailsFor(t) + "\nModified " + fmtDate(t.mod_time);
  if (t.kind === "video" && !t.direct)
    details.textContent += "\nPlayed through ffmpeg";
  $("#lbdate").textContent = t.name + " ↗";
  $("#lbdate").href = mediaSrc(t);
  hideConfirm();
  $("#lbprev").disabled = lbIndex <= 0;
  $("#lbnext").disabled = lbIndex >= state.items.length - 1 && $("#more").hidden;
}
$("#lbclose").addEventListener("click", closeLb);
$("#lb").addEventListener("click", (e) => {
  if (e.target === e.currentTarget) closeLb();
});
$("#lbprev").addEventListener("click", () => {
  if (lbIndex > 0) {
    lbIndex--;
    fillLb();
  }
});
$("#lbnext").addEventListener("click", async () => {
  if (lbIndex >= state.items.length - 1) await loadMoreItems();
  if (lbIndex < state.items.length - 1) {
    lbIndex++;
    fillLb();
  }
});
addEventListener("keydown", (e) => {
  if (!$("#lb").classList.contains("open")) return;
  if (e.key === "Escape") return closeLb();
  if (NARROW.matches) return;
  if (e.key === "ArrowLeft") $("#lbprev").click();
  if (e.key === "ArrowRight") $("#lbnext").click();
  if (e.key === " " && ctlVideo && ctlVideo.dataset.stream) {
    e.preventDefault();
    togglePlay();
  }
});
function filterFolder(t) {
  closeLb();
  selectFolder(t.root, t.dir);
}
$("#lbhandle").addEventListener("click", () => {
  const t = state.items[lbIndex];
  if (t) filterFolder(t);
});
$("#lbfilter").addEventListener("click", () => {
  const t = state.items[lbIndex];
  if (t) filterFolder(t);
});

/* ---- Delete a file ---- */
function showConfirm() {
  if (!state.items[lbIndex]) return;
  $("#lbactions").hidden = true;
  $("#lbconfirm").hidden = false;
}
function hideConfirm() {
  $("#lbconfirm").hidden = true;
  $("#lbactions").hidden = false;
}
async function deleteFile() {
  const t = state.items[lbIndex];
  if (!t) return;
  let res;
  try {
    res = await fetch("/api/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: t.id }),
    });
  } catch {
    res = null;
  }
  if (!res || !res.ok) {
    alert("Could not delete the file — is Shelf running?");
    return;
  }
  const node = cardNodes[lbIndex];
  if (node) node.remove();
  state.items.splice(lbIndex, 1);
  cardNodes.splice(lbIndex, 1);
  state.total = Math.max(0, state.total - 1);
  if (state.counts) {
    for (const key of ["all", t.kind])
      if (state.counts[key] > 0) state.counts[key]--;
  }
  statsCache = null;
  foldersCache = null;
  updateCount();
  renderTabs();
  renderTree();
  closeLb();
}
$("#lbreveal").addEventListener("click", async () => {
  const t = state.items[lbIndex];
  if (!t) return;
  let res;
  try {
    res = await fetch("/api/reveal", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: t.id }),
    });
  } catch {
    res = null;
  }
  if (!res || !res.ok) alert("Could not reveal the file — is Shelf running?");
});
$("#lbremove").addEventListener("click", showConfirm);
$("#lbcancel").addEventListener("click", hideConfirm);
$("#lbdelete").addEventListener("click", deleteFile);

/* ---- Index progress strip, and the library changing underneath ---- */
let statusTimer = null;
let knownVersion = null;
async function pollStatus() {
  let data = null;
  try {
    const res = await fetch("/api/status");
    if (res.ok) data = await res.json();
  } catch {
    data = null;
  }
  if (data) statusCache = data;
  const busy = !!data && data.scanning;
  $("#ixstrip").hidden = !busy;
  if (busy) {
    if (data.phase === "probing" && data.total > 0) {
      const pct = Math.floor((100 * data.done) / data.total);
      $("#ixtext").textContent =
        "Reading metadata — " + fmt(data.done) + " of " + fmt(data.total) + " files";
      $("#ixpct").textContent = pct + "%";
      $("#ixbar").style.width = pct + "%";
    } else if (data.phase === "thumbs" && data.total > 0) {
      const pct = Math.floor((100 * data.done) / data.total);
      $("#ixtext").textContent =
        "Rendering previews — " + fmt(data.done) + " of " + fmt(data.total);
      $("#ixpct").textContent = pct + "%";
      $("#ixbar").style.width = pct + "%";
    } else {
      $("#ixtext").textContent =
        "Listing folders" + (data.done > 0 ? " — " + fmt(data.done) + " files so far" : "…");
      $("#ixpct").textContent = "";
      $("#ixbar").style.width = "0%";
    }
  }
  if (data) {
    if (knownVersion != null && data.version !== knownVersion) {
      statsCache = null;
      foldersCache = null;
      loadStats().catch(() => {});
      renderTree();
      // An empty grid can simply fill itself; one being read gets a
      // button, so nothing jumps under the reader.
      if (state.view === "grid" && !state.items.length) resetAndLoad();
      else if (state.view === "grid") $("#refresh").hidden = false;
      else renderSubpage();
    }
    knownVersion = data.version;
    const rootsNow = JSON.stringify(data.roots.map((r) => [r.path, r.ok]));
    if (rootsNow !== renderedRoots) {
      renderedRoots = rootsNow;
      renderTree();
      renderSideStatus();
      renderCrumbs();
    }
  }
  clearTimeout(statusTimer);
  statusTimer = setTimeout(pollStatus, busy ? 1500 : 15000);
}
let renderedRoots = "";
$("#refresh").addEventListener("click", resetAndLoad);

/* ---- Controls ---- */
$("#home").addEventListener("click", goHome);
let debounceTimer = null;
$("#q").addEventListener("input", (e) => {
  clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    state.q = e.target.value.trim();
    if (state.view !== "grid") setView("grid");
    else resetAndLoad();
  }, 300);
});
$("#q").addEventListener("keydown", (e) => {
  if (e.key === "Enter") closeSide();
});
$("#sort").addEventListener("change", (e) => {
  state.sort = e.target.value;
  resetAndLoad();
});
$("#mchip button").addEventListener("click", () => {
  state.month = "";
  resetAndLoad();
});

/* ---- Boot ---- */
let viewFromURL = false;
(function initFromURL() {
  const p = new URLSearchParams(location.search);
  const view = p.get("view");
  if (Object.keys(VIEW_TITLES).includes(view)) {
    state.view = view;
    viewFromURL = true;
  }
  state.root = (p.get("root") || "").trim();
  state.q = (p.get("q") || "").trim();
  const kind = p.get("kind") || "all";
  if (KIND_TABS.some(([key]) => key === kind)) state.kind = kind;
  state.dir = (p.get("dir") || "").trim().replace(/^\/+|\/+$/g, "");
  if (/^\d{4}-\d{2}$/.test(p.get("month") || "")) state.month = p.get("month");
  const sort = p.get("sort");
  if (SORTS.includes(sort)) state.sort = sort;
  $("#q").value = state.q;
  $("#sort").value = state.sort;
})();

(async function boot() {
  try {
    const status = await loadStatus();
    knownVersion = status.version;
    renderedRoots = JSON.stringify(status.roots.map((r) => [r.path, r.ok]));
    if (state.root && !status.roots.some((r) => r.path === state.root)) {
      state.root = "";
      state.dir = "";
    }
    if (!viewFromURL && !status.roots.length) state.view = "settings";
  } catch {
    // The grid still explains itself without this.
  }
  renderSideLinks();
  loadStats().catch(() => {
    $("#sidestatus").textContent = "Could not load — is Shelf running?";
  });
  applyView();
  if (state.view === "grid") resetAndLoad();
  else {
    renderCrumbs();
    renderTree();
    renderSubpage();
  }
  pollStatus();
})();
