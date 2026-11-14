'use strict';

// Mess Auction House. Everything people wrote in the groups is untrusted, so
// it only ever reaches the page through textContent.


// ---------- state ----------

const saved = (() => { try { return JSON.parse(localStorage.getItem('mah') || '{}'); } catch { return {}; } })();
const S = Object.assign({ tab: 'kadamba', mode: 'sell', sort: 'recent', recentDir: 'desc', priceDir: 'asc', diet: 'all', meal: 'all', sold: false, win: 30, via: 'whatsapp' }, saved, { page: 0, q: '' });
const persist = () => {
  const { tab, mode, sort, recentDir, priceDir, diet, meal, sold, win, via } = S;
  localStorage.setItem('mah', JSON.stringify({ tab, mode, sort, recentDir, priceDir, diet, meal, sold, win, via }));
};

let D = null;          // last /api/state
let skew = 0;          // server clock minus ours, seconds
let lastOK = 0;
let failed = false;
const seen = new Set();
let popIds = new Set();
let firstLoad = true;
let L = { s: 72, gap: 6, cols: 9, rows: 5 };
let byId = new Map();
let pageItems = [];
let pages = 1;

const now = () => Date.now() / 1000 + skew;
const tz = () => (D && D.tz) || 'Asia/Kolkata';
const hm = (u) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz() }).format(u * 1000);
const ymd = (u) => new Intl.DateTimeFormat('en-CA', { timeZone: tz() }).format(u * 1000);
function dayWord(date) {
  const n = now();
  if (date === ymd(n)) return 'today';
  if (date === ymd(n + 86400)) return 'tomorrow';
  if (date === ymd(n - 86400)) return 'yesterday';
  return date;
}
function dur(sec) {
  const m = Math.max(1, Math.round(sec / 60));
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`;
}
function ago(u) {
  const s = now() - u;
  return s < 60 ? 'just now' : `${dur(s)} ago`;
}
// A buy order that went through was bought, not sold.
const doneWord = (l) => (l.kind === 'buy' ? 'Bought' : 'Sold');
const doneColor = (l) => (l.kind === 'buy' ? 'cc' : 'ca');
const modeDone = () => (S.mode === 'buy' ? 'Bought' : 'Sold');
const vegOnly = (m) => !!(D && D.messes.find((x) => x.name === m && x.veg_only));
const window_ = (meal) => (D ? D.today.find((s) => s.meal === meal) : null);
const shortGroup = (g) => g.replace(/^mess\s+/i, '').replace(/\s*@\s*/, ' @ ');

// ---------- data ----------

// The server pushes a "change" event on api/events whenever a post, edit,
// delete or reaction lands, and the page fetches the board then. While that
// stream is up, a slow refresh only catches meals ending. If it drops, poll
// every 5s while a meal is being served and every 30s otherwise, cutting the
// slow wait short so polling speeds up as soon as the next meal starts.
const FAST = 5, SLOW = 30, IDLE = 60;
let pollTimer = 0;
let streaming = false;
let loading = false;
let again = false;
function pollEvery() {
  if (streaming) return IDLE;
  if (!D) return FAST;
  const n = now();
  if (D.today.some((s) => s.start <= n && n < s.end)) return FAST;
  const next = D.today.filter((s) => s.start > n).map((s) => s.start - n);
  return Math.max(FAST, Math.min(SLOW, ...next));
}
function schedule() {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(load, pollEvery() * 1000);
}

async function load() {
  if (loading) { again = true; return; }
  loading = true;
  schedule();
  try {
    const r = await fetch('api/state', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    if (!r.ok) throw new Error(r.status);
    const d = await r.json();
    skew = d.now - Date.now() / 1000;
    popIds = new Set();
    for (const l of d.listings) {
      if (!firstLoad && !seen.has(l.id)) popIds.add(l.id);
      seen.add(l.id);
    }
    D = d;
    byId = new Map(d.listings.map((l) => [l.id, l]));
    firstLoad = false;
    failed = false;
    lastOK = Date.now();
    if (!D.messes.some((m) => m.name === S.tab) && S.tab !== 'all') S.tab = D.messes[0] ? D.messes[0].name : 'all';
    render();
  } catch (e) {
    failed = true;
    tickLive();
  } finally {
    loading = false;
    if (again) { again = false; load(); }
  }
}

function connect() {
  if (typeof EventSource === 'undefined') return;
  const es = new EventSource('api/events');
  es.addEventListener('open', () => {
    streaming = true;
    if (lastOK) load(); // catch up on anything missed while disconnected
    tickLive();
  });
  es.addEventListener('change', () => {
    if (document.visibilityState === 'visible') load();
  });
  es.addEventListener('error', () => {
    // The browser reconnects on its own; poll until it does.
    streaming = false;
    schedule();
    tickLive();
  });
}

function inMode(l) {
  return S.mode === 'sell' ? l.kind !== 'buy' : l.kind === 'buy';
}

// matches reports whether l shows on the given tab under the current filters.
function matches(l, tab) {
  if (!inMode(l)) return false;
  if (tab !== 'all' && l.mess !== tab) return false;
  if (!S.sold && l.status !== 'available') return false;
  if (S.meal !== 'all' && l.meal !== S.meal) return false;
  if (S.diet !== 'all' && (tab === 'all' || !vegOnly(tab)) && (l.diet || '') !== S.diet) return false;
  if (S.q) {
    const hay = [l.text, l.seller.name, l.seller.phone, l.mess, l.meal, ...(l.groups || [])].join(' ').toLowerCase();
    if (!S.q.toLowerCase().split(/\s+/).every((w) => hay.includes(w))) return false;
  }
  return true;
}

function sorted(list) {
  const rank = (l) => (l.status === 'available' ? 0 : 1);
  const recent = (a, b) => b.last_seen_at - a.last_seen_at;
  const cmp = S.sort === 'price'
    ? (a, b) => {
      const pa = a.price == null ? null : a.price, pb = b.price == null ? null : b.price;
      if (pa == null && pb == null) return recent(a, b);
      if (pa == null) return 1;
      if (pb == null) return -1;
      return (S.priceDir === 'asc' ? pa - pb : pb - pa) || recent(a, b);
    }
    : (a, b) => (S.recentDir === 'desc' ? recent(a, b) : -recent(a, b));
  return list.slice().sort((a, b) => rank(a) - rank(b) || cmp(a, b));
}

// ---------- layout ----------

function layout() {
  // clientWidth is the layout viewport. innerWidth on Android Chrome follows
  // the visual viewport, which widens when anything overflows; sizing the grid
  // from it then makes the overflow permanent.
  const w = document.documentElement.clientWidth || window.innerWidth;
  let s, gap, cols;
  if (w < 640) {
    // Phones: a fixed column count, with slots stretched to fill the width.
    // Budget = app padding 20 + chest padding 20 + chest border 2, then the tab
    // column (one slot plus a double gap) beside the grid.
    gap = 5;
    cols = w < 360 ? 4 : 5;
    s = Math.max(44, Math.min(72, Math.floor((w - 42 - (cols + 1) * gap) / (cols + 1))));
  } else {
    s = w < 1000 ? 64 : 72;
    gap = 6;
    const avail = Math.min(w, 1280) - 74 - s - gap * 2;
    cols = Math.max(4, Math.min(12, Math.floor((avail + gap) / (s + gap))));
  }
  const rows = cols <= 6 ? 7 : 5;
  L = { s, gap, cols, rows };
  const st = document.documentElement.style;
  st.setProperty('--s', s + 'px');
  st.setProperty('--gap', gap + 'px');
  st.setProperty('--cols', cols);
  st.setProperty('--rows', rows);
}

// ---------- render ----------

function render() {
  if (!D) return;
  const accent = messColor(S.tab);
  document.documentElement.style.setProperty('--accent', accent);
  renderDay();
  $('noGroups').hidden = !D.no_groups;
  renderSales();
  if (!D.nudge) { N = null; nudgeAt = 0; }
  if (N || !D.nudge) renderNudge();
  if (D.nudge) loadNudge();
  renderTabs();
  renderGrid();
  renderControls();
  renderStats();
  keepTip();
  tickLive();
}

function renderDay() {
  const n = now();
  const parts = new Intl.DateTimeFormat('en-GB', { hour: 'numeric', minute: 'numeric', hour12: false, timeZone: tz() }).formatToParts(n * 1000);
  const get = (t) => +parts.find((p) => p.type === t).value;
  const midnight = n - (get('hour') * 3600 + get('minute') * 60) - (n % 60);
  const t0 = midnight + 6 * 3600, t1 = midnight + 24 * 3600;
  const pct = (u) => `${Math.max(0, Math.min(100, ((u - t0) / (t1 - t0)) * 100))}%`;

  const track = $('track');
  track.replaceChildren();
  for (const s of D.today) {
    const seg = el('div', 'seg');
    if (n > s.end) seg.classList.add('past');
    if (n >= s.start && n <= s.end) seg.classList.add('now');
    seg.style.setProperty('left', pct(s.start));
    seg.style.setProperty('width', `calc(${pct(s.end)} - ${pct(s.start)})`);
    track.append(seg);
    const g = el('span', 'seg-glyph' + (seg.classList.contains('now') ? ' now' : ''));
    g.style.setProperty('left', `calc((${pct(s.start)} + ${pct(s.end)}) / 2)`);
    const mine = (D.mine || []).find((x) => x.meal === s.meal);
    if (mine) {
      // A meal you sold or armed today shows a coin and its price instead.
      g.classList.add('coin', mine.state);
      g.append(svgEl(icon('coin')), el('b', '', rupees(mine.amount)));
      const what = { armed: `waiting for ${rupees(mine.amount)}`, handoff: `handed to you, offer asked ${rupees(mine.amount)}` }[mine.state] || `sold for ${rupees(mine.amount)}`;
      g.title = `${cap(s.meal)} ${hm(s.start)}-${hm(s.end)}: ${what}`;
    } else {
      g.append(svgEl(icon(s.meal)));
      g.title = `${cap(s.meal)} ${hm(s.start)}-${hm(s.end)}`;
    }
    track.append(g);
  }
  const mark = el('div', 'now-mark');
  mark.style.setProperty('left', pct(n));
  track.append(mark);

  const lab = $('dayLabel');
  lab.replaceChildren();
  if (D.current) {
    lab.append(el('b', '', cap(D.current.meal)), ' is being served. ',
      el('span', 'gold', `${dur(D.current.end - n)} left`), `, until ${hm(D.current.end)}`);
  } else if (D.next) {
    lab.append('Next up: ', el('b', '', cap(D.next.meal)), ` at ${hm(D.next.start)} ${dayWord(D.next.date) === 'today' ? '' : dayWord(D.next.date)}`,
      el('span', 'gold', ` (in ${dur(D.next.start - n)})`));
  }
}

function renderTabs() {
  const tabs = $('tabs');
  tabs.replaceChildren();
  const names = [...D.messes.map((m) => m.name), 'all'];
  names.forEach((name, i) => {
    const b = el('button', 'slot tab' + (S.tab === name ? ' on' : ''));
    b.style.setProperty('--m', messColor(name));
    b.append(svgEl(messIcon(name)));
    const count = D.listings.filter((l) => matches(l, name) && l.status === 'available').length;
    b.append(el('span', 'badge' + (count ? '' : ' zero'), String(count)));
    b.setAttribute('aria-label', `${cap(name)}: ${count} listings`);
    b.addEventListener('click', () => setTab(name));
    tip(b, () => tabTip(name, count, i));
    tabs.append(b);
  });
  for (let i = names.length; i < L.rows; i++) tabs.append(el('div', 'slot'));
}

function tabTip(name, count, i) {
  const what = S.mode === 'sell' ? 'for sale' : 'wanted';
  const lines = [
    [{ t: name === 'all' ? 'All messes' : cap(name), c: 'nm', color: messColor(name) }],
    [{ t: (MESS[name] || {}).blurb || '', c: 'c8' }],
    0,
    [{ t: 'Listings: ', c: 'c7' }, { t: `${count} ${what}`, c: 'cf' }],
  ];
  if (name !== 'all') lines.push([{ t: 'Serves: ', c: 'c7' }, vegOnly(name) ? { t: 'veg only', c: 'ca' } : { t: 'veg and non-veg', c: 'cf' }]);
  lines.push(0, [{ t: S.tab === name ? 'Currently browsing' : 'Click to browse!', c: 'ce' }]);
  lines.push({ rar: `KEY ${i + 1}`, c: 'c8' });
  return lines;
}

function renderGrid() {
  const grid = $('grid');
  const list = sorted(D.listings.filter((l) => matches(l, S.tab)));
  const per = L.cols * L.rows;
  pages = Math.max(1, Math.ceil(list.length / per));
  S.page = Math.min(S.page, pages - 1);
  pageItems = list.slice(S.page * per, (S.page + 1) * per);

  const nodes = pageItems.map(slotNode);
  while (nodes.length < per) nodes.push(el('div', 'slot'));
  grid.replaceChildren(...nodes);

  if (!list.length) {
    const box = el('div', 'empty');
    const inner = el('div');
    inner.append(el('b', '', S.q ? 'NO MATCHES' : 'NOTHING HERE'), emptyText());
    box.append(inner);
    grid.append(box);
  }

  const avail = list.filter((l) => l.status === 'available').length;
  const tabName = S.tab === 'all' ? 'All messes' : cap(S.tab);
  $('headIcon').replaceChildren(svgEl(messIcon(S.tab)));
  $('headName').textContent = tabName;
  const bits = [`${avail} ${S.mode === 'sell' ? 'for sale' : 'wanted'}`];
  if (S.meal !== 'all') bits.push(S.meal);
  if (S.diet !== 'all' && (S.tab === 'all' || !vegOnly(S.tab))) bits.push(DIET_LABEL[S.diet].toLowerCase());
  if (S.q) bits.push(`matching "${S.q}"`);
  $('headMeta').textContent = bits.join(' · ');
  $('pgText').textContent = `${S.page + 1}/${pages}`;
  $('pgPrev').disabled = S.page === 0;
  $('pgNext').disabled = S.page >= pages - 1;
  $('sub').textContent = S.mode === 'sell' ? 'Buy it now' : 'Buy orders';
}

function emptyText() {
  const where = S.tab === 'all' ? 'any mess' : cap(S.tab);
  const meal = S.meal === 'all' ? '' : ` ${S.meal}`;
  return S.mode === 'sell' ? `No${meal} listings for ${where} right now.` : `Nobody is asking for${meal} ${where} right now.`;
}

const FRESH = 120; // seconds a new post shines
// One glint cycle: a sweep across the slot, then a rest. Every fresh slot
// runs on the same wall clock, so they all sweep together and a re-render
// picks the animation up mid-cycle instead of restarting it.
const GLINT_MS = 3000;
const GLINT_SWEEP_MS = 2200;

function glintNode(expiresIn) {
  const g = el('span', 'glint');
  g.style.animationDelay = `${-(Date.now() % GLINT_MS)}ms`;
  // Switch off on the dot, but never cut a sweep in half: if time runs out
  // mid-sweep, wait for it to finish.
  const phase = (Date.now() + expiresIn) % GLINT_MS;
  const wait = expiresIn + (phase < GLINT_SWEEP_MS ? GLINT_SWEEP_MS - phase : 0);
  setTimeout(() => g.remove(), wait);
  return g;
}

function slotNode(l) {
  const b = el('button', 'slot item');
  b.dataset.id = l.id;
  b.style.setProperty('--m', messColor(l.mess));
  if (l.status === 'sold' || l.status === 'closed') b.classList.add(l.status);
  // Shine for 2 minutes after the latest post.
  const fresh = FRESH - (now() - l.last_seen_at);
  if (fresh > 0 && l.status === 'available') b.append(glintNode(fresh * 1000));
  if (popIds.has(l.id)) b.classList.add('pop');
  // Your own sell task's listing.
  if (D.sales && D.sales.some((v) => v.listings.includes(l.id))) b.classList.add('mine');

  if (!vegOnly(l.mess) || S.tab === 'all') {
    const d = el('span', 'diet');
    d.append(svgEl(dietMark(l.diet)));
    b.append(d);
  }
  if (ART[l.meal]) {
    const m = el('span', 'mealg');
    m.append(svgEl(icon(l.meal)));
    b.append(m);
  }
  b.append(priceNode(l));
  if (S.tab === 'all') {
    const m = el('span', 'messg');
    m.append(svgEl(messIcon(l.mess)));
    b.append(m);
  }
  if (l.quantity > 1) b.append(el('span', 'qty', String(l.quantity)));
  if (l.other_ticks && l.status === 'available') b.append(el('span', 'ticked'));

  const bar = el('span', 'bar');
  const fill = el('i');
  // Full until serving starts, empty when it ends, so every listing for the
  // same meal shows the same amount. Meals on other days have the same
  // window length, so today's schedule gives the start.
  const w = window_(l.meal);
  const total = w ? w.end - w.start : Math.max(1, l.meal_ends_at - l.first_seen_at);
  const frac = Math.max(0, Math.min(1, (l.meal_ends_at - now()) / total));
  fill.style.setProperty('transform', `scaleX(${frac})`);
  fill.style.setProperty('background', `hsl(${Math.round(frac * 120)} 75% 52%)`);
  bar.append(fill);
  b.append(bar);

  if (l.status === 'sold') b.append(el('span', l.kind === 'buy' ? 'stamp long bought' : 'stamp', doneWord(l).toUpperCase()));
  if (l.status === 'closed') b.append(el('span', 'stamp', 'CLOSED'));
  b.setAttribute('aria-label', `${cap(l.mess)} ${l.meal}, ${priceText(l)}, ${l.seller.name || l.seller.phone || 'unknown seller'}`);
  return b;
}

function priceText(l) {
  if (l.kind === 'exchange' && l.price == null) return `swap for ${(l.wants || []).map(cap).join(' or ') || 'another mess'}`;
  if (l.price == null) return 'price not stated';
  if (l.price === 0) return 'free';
  return `₹${l.price}`;
}

function priceNode(l) {
  const p = el('span', 'price');
  if (l.kind === 'exchange' && l.price == null) {
    p.classList.add('swap');
    const want = (l.wants || [])[0];
    if (S.tab === 'all') {
      // the corner already shows the offered mess, so read it as "→ wanted"
      p.classList.add('compact');
      p.append(el('span', 'arrow', '⇄'));
      if (want) p.append(svgEl(messIcon(want)));
    } else {
      if (want) p.append(svgEl(messIcon(want)));
      p.append(el('span', '', 'SWAP'));
    }
    if ((l.wants || []).length > 1) p.append(el('span', 'more', `+${l.wants.length - 1}`));
  } else if (l.price == null) {
    p.classList.add('unknown');
    p.textContent = '?';
  } else if (l.price === 0) {
    p.classList.add('free');
    p.textContent = 'FREE';
  } else {
    if (l.price >= 100) p.classList.add('wide');
    p.append(el('span', 'r', '₹'), String(l.price));
  }
  return p;
}

// ---------- listing tooltip ----------

function title(l) {
  const meal = cap(l.meal);
  if (l.kind === 'exchange') return `${cap(l.mess)} ${meal} ⇄ ${(l.wants || []).map(cap).join(' / ') || '?'}`;
  return `${cap(l.mess)} ${meal}`;
}

function rarity(l) {
  const parts = [];
  if (l.kind === 'exchange') parts.push('EXCHANGE');
  if (l.kind === 'buy') parts.push('BUY ORDER');
  if (l.diet) parts.push(DIET_LABEL[l.diet].toUpperCase());
  parts.push(l.meal.toUpperCase());
  return { rar: parts.join(' '), c: DIET_COLOR[l.diet] || 'c7' };
}

function itemTip(l) {
  const w = window_(l.meal);
  const n = now();
  const who = l.kind === 'buy' ? 'Buyer' : 'Seller';
  const lines = [
    [{ t: title(l), c: 'nm', color: messColor(l.mess) }],
    [{ t: `${cap(dayWord(l.meal_date))}${w ? `, ${hm(w.start)}-${hm(w.end)}` : ''}`, c: 'c8' }],
    0,
    [{ t: clip(l.text, 160), c: 'lore' }],
    0,
    [{ t: `${who}: `, c: 'c7' }, { t: l.seller.name || 'unknown', c: l.seller.name ? 'cf' : 'c8' }, l.seller.saved ? { t: ' (saved)', c: 'c8' } : null],
    [{ t: 'Phone: ', c: 'c7' }, l.seller.phone ? { t: l.seller.phone, c: 'cb' } : { t: 'hidden by WhatsApp', c: 'c8' }],
  ];
  const gs = l.groups || [];
  if (gs.length === 1) lines.push([{ t: 'Group: ', c: 'c7' }, { t: shortGroup(gs[0]), c: 'cf' }]);
  else {
    lines.push([{ t: `Groups (${gs.length}):`, c: 'c7' }]);
    for (const g of gs) lines.push([{ t: '  ' + shortGroup(g), c: 'cf' }]);
  }
  lines.push([{ t: 'Posted: ', c: 'c7' }, { t: `${ago(l.first_seen_at)}, ${hm(l.first_seen_at)}`, c: 'cf' }]);
  if (l.last_seen_at - l.first_seen_at > 120) lines.push([{ t: 'Reposted: ', c: 'c7' }, { t: ago(l.last_seen_at), c: 'cf' }]);
  if (l.quantity > 1) lines.push([{ t: 'Quantity: ', c: 'c7' }, { t: `${l.quantity} registrations`, c: 'cf' }]);
  if (l.other_ticks && l.status === 'available') {
    lines.push([{ t: `${l.other_ticks} other ${l.other_ticks === 1 ? 'person' : 'people'} ticked this. It may be gone.`, c: 'c6' }]);
  }
  lines.push(0);
  if (l.kind === 'exchange') {
    lines.push([{ t: 'Wants: ', c: 'c7' }, { t: (l.wants || []).map(cap).join(' or ') || 'not said', c: 'cd' }]);
  }
  if (l.kind !== 'exchange' || l.price != null) {
    const label = l.kind === 'buy' ? 'Offering: ' : 'Buy it now: ';
    const val = l.price == null ? { t: 'ask', c: 'c8' } : l.price === 0 ? { t: 'FREE', c: 'ca' } : { t: `₹${l.price}`, c: 'c6' };
    lines.push([{ t: label, c: 'c7' }, val]);
  }
  if (l.status === 'sold') lines.push([{ t: 'Status: ', c: 'c7' }, { t: doneWord(l), c: doneColor(l) }]);
  else if (l.status === 'closed') lines.push([{ t: 'Status: ', c: 'c7' }, { t: 'Withdrawn by seller', c: 'cc' }]);
  else lines.push([{ t: 'Ends in: ', c: 'c7' }, { t: l.meal_ends_at > n ? dur(l.meal_ends_at - n) : 'ended', c: 'cf' }]);
  lines.push(0, [{ t: 'Click to inspect!', c: 'ce' }], rarity(l));
  return lines;
}

const clip = (s, n) => (s.length > n ? s.slice(0, n - 1).trimEnd() + '…' : s);

const grid = $('grid');
grid.addEventListener('pointerover', (e) => {
  if (e.pointerType !== 'mouse') return;
  const b = e.target.closest('.item');
  if (!b) {
    // over an empty cell or a gap: drop a listing's tooltip
    if (tipFor && (!tipFor.isConnected || grid.contains(tipFor))) hideTip();
    return;
  }
  if (b === tipFor) return;
  const l = byId.get(+b.dataset.id);
  if (l) showTip(b, () => itemTip(l), e.clientX, e.clientY);
});

// keepTip runs after a re-render replaces the hovered node. If the same
// listing, or the same stats cell (matched by data-tk), is still under the
// mouse, the tooltip moves to its new node with fresh data; anything else
// closes it.
function keepTip() {
  if (!tipFor || tipFor.isConnected) return;
  const { id, tk } = tipFor.dataset;
  let nb = null, fn = null;
  if (id) {
    nb = grid.querySelector(`.item[data-id="${id}"]`);
    const l = nb && byId.get(+id);
    fn = l && (() => itemTip(l));
  } else if (tk) {
    nb = document.querySelector(`[data-tk="${tk}"]`);
    fn = nb && nb._tip;
  }
  const r = nb && nb.getBoundingClientRect();
  // :hover lags until the mouse moves, so test the last mouse position
  if (fn && lastMouse.x >= r.left && lastMouse.x < r.right && lastMouse.y >= r.top && lastMouse.y < r.bottom) {
    tipFor = nb;
    tipFn = fn;
    fillTip(tipFn());
  } else {
    hideTip();
  }
}
grid.addEventListener('pointermove', (e) => { if (tipFor && grid.contains(tipFor)) placeTip(e.clientX, e.clientY); });
grid.addEventListener('pointerleave', hideTip);
grid.addEventListener('click', (e) => {
  const b = e.target.closest('.item');
  if (!b) return;
  const l = byId.get(+b.dataset.id);
  if (l) openListing(l);
});

// ---------- controls ----------

function renderControls() {
  const row = $('ctlRow');
  row.replaceChildren();
  const add = (node) => { row.append(node); return node; };

  const prev = add(ctl('prev', 'PREV', S.page > 0 ? 'set' : '', () => turn(-1), () => [
    [{ t: 'Previous page', c: 'nm ca' }], [{ t: `Page ${S.page + 1} of ${pages}`, c: 'c7' }]]));
  prev.classList.add('page');
  prev.disabled = S.page === 0;
  add(el('span', 'spacer'));

  add(ctl('clock', S.sort === 'recent' ? (S.recentDir === 'desc' ? 'NEWEST' : 'OLDEST') : 'RECENT', S.sort === 'recent' ? 'on' : '', sortRecent, () => [
    [{ t: 'Sort by recency', c: 'nm ca' }],
    [{ t: 'Uses the latest repost of each listing.', c: 'c8' }],
    0,
    opt('Newest first', S.sort === 'recent' && S.recentDir === 'desc'),
    opt('Oldest first', S.sort === 'recent' && S.recentDir === 'asc'),
    0,
    [{ t: S.sort === 'recent' ? 'Click to flip!' : 'Click to sort by recency!', c: 'ce' }],
  ]));

  add(ctl('coin', S.sort === 'price' ? (S.priceDir === 'asc' ? 'LOWEST' : 'HIGHEST') : 'PRICE', S.sort === 'price' ? 'on' : '', sortPrice, () => [
    [{ t: 'Sort by price', c: 'nm c6' }],
    [{ t: 'Listings without a price go last.', c: 'c8' }],
    0,
    opt('Lowest price', S.sort === 'price' && S.priceDir === 'asc'),
    opt('Highest price', S.sort === 'price' && S.priceDir === 'desc'),
    0,
    [{ t: S.sort === 'price' ? 'Click to flip!' : 'Click to sort by price!', c: 'ce' }],
  ]));

  const dietOff = S.tab !== 'all' && vegOnly(S.tab);
  const diet = add(ctl(null, dietOff ? 'VEG ONLY' : DIET_LABEL[S.diet].toUpperCase(), S.diet !== 'all' && !dietOff ? 'on' : '', cycleDiet, () => dietOff
    ? [[{ t: 'Diet filter', c: 'nm ca' }], [{ t: `${cap(S.tab)} serves only veg, so there is nothing to filter.`, c: 'c7' }]]
    : [
      [{ t: 'Diet filter', c: 'nm ca' }],
      [{ t: 'Kadamba and Bakul serve both.', c: 'c8' }],
      0,
      ...DIETS.map((d) => opt(DIET_LABEL[d], S.diet === d, DIET_COLOR[d])),
      0,
      [{ t: 'Click to cycle! Right click goes back.', c: 'ce' }],
    ], dietMark(dietOff ? 'veg' : S.diet === 'all' ? '' : S.diet)));
  diet.disabled = dietOff;

  add(ctl(null, S.meal === 'all' ? 'ALL MEALS' : S.meal.toUpperCase(), S.meal !== 'all' ? 'on' : '', cycleMeal, () => [
    [{ t: 'Meal filter', c: 'nm ce' }],
    0,
    opt('All meals', S.meal === 'all'),
    ...MEALS.map((m) => {
      const w = window_(m);
      return opt(`${cap(m)}${w ? `  ${hm(w.start)}-${hm(w.end)}` : ''}`, S.meal === m);
    }),
    0,
    [{ t: 'Click to cycle! Right click goes back.', c: 'ce' }],
  ], S.meal === 'all' ? mealsAllIcon() : icon(S.meal)));

  const done = modeDone();
  add(ctl('check', `${done.toUpperCase()}: ${S.sold ? 'ON' : 'OFF'}`, S.sold ? 'on' : '', toggleSold, () => [
    [{ t: `Show ${done.toLowerCase()} and withdrawn`, c: 'nm ca' }],
    [{ t: `${done} means the ${S.mode === 'buy' ? 'buyer' : 'seller'} ticked it or said so.`, c: 'c8' }],
    [{ t: 'Deals agreed in DMs stay invisible.', c: 'c8' }],
    0,
    opt('Hide them', !S.sold),
    opt('Show them, greyed out', S.sold),
    0,
    [{ t: 'Click to toggle!', c: 'ce' }],
  ]));

  add(ctl(S.mode === 'sell' ? 'ingot' : 'paper', S.mode === 'sell' ? 'SELLING' : 'WANTED', 'set', toggleMode, () => [
    [{ t: 'Market side', c: 'nm c6' }],
    0,
    opt('Selling: buy it now', S.mode === 'sell'),
    opt('Wanted: buy orders', S.mode === 'buy'),
    0,
    [{ t: 'Click to switch!', c: 'ce' }],
  ]));

  add(el('span', 'spacer'));
  const next = add(ctl('next', 'NEXT', S.page < pages - 1 ? 'set' : '', () => turn(1), () => [
    [{ t: 'Next page', c: 'nm ca' }], [{ t: `Page ${S.page + 1} of ${pages}`, c: 'c7' }]]));
  next.classList.add('page');
  next.disabled = S.page >= pages - 1;

  const left = $('ctlLeft');
  if (!left.firstChild) {
    const help = el('button', 'slot ctl help');
    help.append(el('span', 'q', '?'), el('span', 'lbl', 'KEYS'));
    help.addEventListener('click', () => toast('Keys: 1-5 messes, arrows pages, R P D M S W, / search'));
    tip(help, () => [
      [{ t: 'Mess Auction House', c: 'nm c6' }],
      [{ t: 'Merged from your 4 mess groups.', c: 'c8' }],
      0,
      [{ t: '1-5 ', c: 'ce' }, { t: 'switch mess', c: 'c7' }],
      [{ t: '← → ', c: 'ce' }, { t: 'pages', c: 'c7' }],
      [{ t: 'R / P ', c: 'ce' }, { t: 'sort by recency / price', c: 'c7' }],
      [{ t: 'D / M ', c: 'ce' }, { t: 'diet / meal filter, Shift goes back', c: 'c7' }],
      [{ t: 'S ', c: 'ce' }, { t: `show ${modeDone().toLowerCase()}`, c: 'c7' }],
      [{ t: 'W ', c: 'ce' }, { t: 'selling or wanted', c: 'c7' }],
      [{ t: '/ ', c: 'ce' }, { t: 'search', c: 'c7' }],
      0,
      [{ t: 'Slot corners', c: 'c7' }],
      [{ t: '  diet mark, meal, quantity', c: 'c8' }],
      [{ t: '  bar: time left until the meal ends', c: 'c8' }],
      [{ t: '  shine: posted in the last 2 minutes', c: 'cd' }],
      [{ t: '  amber dot: someone else ticked it', c: 'c6' }],
    ]);
    left.append(help);
  }
}

function opt(text, on, color) {
  return [{ t: on ? '▶ ' : '   ', c: 'cb' }, { t: text, c: on ? (color || 'cb') : 'c8' }];
}

function ctl(art, label, cls, onClick, tipFn, markup) {
  const b = el('button', 'slot ctl' + (cls ? ' ' + cls : ''));
  b.append(svgEl(markup || icon(art)), el('span', label.length > 9 ? 'lbl tight' : 'lbl', label));
  // Left click steps forward, right click steps back. Two-way buttons do the
  // same thing either way.
  const act = (dir) => {
    const at = [...b.parentNode.children].indexOf(b);
    onClick(dir);
    if (tipFor === b) {
      // keep the tooltip on the same button after re-render
      requestAnimationFrame(() => {
        const nb = $('ctlRow').children[at];
        if (nb) showTip(nb, tipFn, lastMouse.x, lastMouse.y);
      });
    }
  };
  b.addEventListener('click', () => act(1));
  b.addEventListener('contextmenu', (e) => { e.preventDefault(); if (!b.disabled) act(-1); });
  b.setAttribute('aria-label', label);
  tip(b, tipFn);
  return b;
}

// ---------- market stats ----------

// The server sends the last two hours of posts and sales. Rates use the
// chosen window; the strip always shows the full two hours in 5-minute
// buckets, on one scale shared by every mess so the cards compare.
const SPAN = 120, STEP = 5, WINS = [15, 30, 60, 120];
const winLabel = (m) => (m < 60 ? `${m}M` : `${m / 60}H`);
const winWords = (m) => (m < 60 ? `${m} min` : m === 60 ? 'hour' : `${m / 60} hours`);

function median(xs) {
  if (!xs.length) return null;
  const v = xs.slice().sort((a, b) => a - b), h = v.length >> 1;
  return v.length % 2 ? v[h] : (v[h - 1] + v[h]) / 2;
}

// rate formats a per-minute rate: 0, 0.03, 0.5, 2.4, 12.
function rate(v) {
  if (!v) return '0';
  if (v < 1) return String(+v.toFixed(2));
  return v < 10 ? v.toFixed(1) : String(Math.round(v));
}
const pctText = (v) => (v == null ? '-' : `${Math.round(v * 100)}%`);

// Messes that serve both get a veg card and a non-veg card. Egg counts as
// non-veg. Posts that don't say which stay out of both cards but count
// toward the market as a whole.
const marketDiet = (d) => (d === 'egg' ? 'nonveg' : d || '');
const inMarket = (x, k) => !k || (x.mess === k.mess && (!k.diet || marketDiet(x.diet) === k.diet));
const marketKey = (k) => `${k.mess}:${k.diet || 'all'}`;
const marketName = (k) => cap(k.mess) + (k.diet ? ` ${DIET_LABEL[k.diet].toLowerCase()}` : '');

function markets() {
  return D.messes.flatMap((m) => (m.veg_only
    ? [{ mess: m.name, diet: null }]
    : [{ mess: m.name, diet: 'veg' }, { mess: m.name, diet: 'nonveg' }]));
}

// stats sums one market (or every mess, for null) over the last `win`
// minutes. Messages are posts selling or swapping a meal; buy orders count
// as wanted.
function stats(k, win) {
  const from = now() - win * 60;
  const M = D.market;
  const posts = M.posts.filter((p) => p.t > from && inMarket(p, k));
  const sales = M.sales.filter((x) => x.t > from && inMarket(x, k));
  const offers = posts.filter((p) => p.kind !== 'buy');
  const asks = [...new Map(offers.filter((p) => p.kind === 'sell' && p.price != null).map((p) => [p.id, p.price])).values()];
  return {
    msgs: offers.length,
    sales: sales.length,
    wanted: posts.length - offers.length,
    demand: offers.length ? sales.length / offers.length : null,
    ask: median(asks),
    asks: asks.length,
    sellsIn: median(sales.map((x) => x.t - x.first)),
  };
}

// verdict compares a mess's demand with the whole market's, so it reads
// right at quiet and busy hours alike.
function verdict(s, all) {
  if (s.msgs < 6 || !all.demand) return { k: 'quiet', w: 'QUIET', why: 'Too few messages to call it.' };
  const r = s.demand / all.demand;
  if (r >= 1.3) return { k: 'hot', w: 'HOT', why: `Sells ${r.toFixed(1)}x as often as the market. Sellers can hold their price.` };
  if (r <= 0.7) return { k: 'slow', w: 'SLOW', why: `Sells ${r.toFixed(1)}x as often as the market. Buyers can haggle.` };
  return { k: 'steady', w: 'STEADY', why: 'Selling about as well as the market.' };
}

function buckets(k, start) {
  const out = [];
  for (let i = 0; i < SPAN / STEP; i++) out.push({ t0: start + i * STEP * 60, msgs: 0, sales: 0 });
  const at = (t) => out[Math.floor((t - start) / (STEP * 60))];
  for (const p of D.market.posts) if (inMarket(p, k) && p.kind !== 'buy' && at(p.t)) at(p.t).msgs++;
  for (const x of D.market.sales) if (inMarket(x, k) && at(x.t)) at(x.t).sales++;
  return out;
}

// untagged counts posts in a two-diet mess that didn't say veg or non-veg.
function untagged(mess, win) {
  const from = now() - win * 60;
  return D.market.posts.filter((p) => p.t > from && p.mess === mess && p.kind !== 'buy' && !marketDiet(p.diet)).length;
}

function renderStats() {
  if (!D || !D.market) return;
  const n = now();
  const win = WINS.includes(S.win) ? S.win : 30;
  const all = stats(null, win);
  const meta = $('pulseMeta');
  meta.textContent = `All messes: ${rate(all.msgs / win)} msgs/min · ${rate(all.sales / win)} sales/min · ${pctText(all.demand)} demand`;
  meta.title = meta.textContent;

  const wins = $('wins');
  wins.replaceChildren(...WINS.map((m) => {
    const b = el('button', 'win' + (m === win ? ' on' : ''), winLabel(m));
    b.setAttribute('aria-pressed', String(m === win));
    b.setAttribute('aria-label', `Last ${winWords(m)}`);
    b.addEventListener('click', () => update(() => { S.win = m; }));
    return b;
  }));

  // Buckets line up with the clock; the last one is still filling.
  const end = Math.ceil(n / (STEP * 60)) * STEP * 60;
  const start = end - SPAN * 60;
  const ks = markets().filter((k) => stats(k, win).msgs > 0);
  const series = new Map(ks.map((k) => [marketKey(k), buckets(k, start)]));
  const top = Math.max(1, ...[...series.values()].flat().map((b) => Math.max(b.msgs, b.sales)));

  $('cards').replaceChildren(...ks.map((k) => {
    const m = k.mess, key = marketKey(k);
    const s = stats(k, win);
    const v = verdict(s, all);
    const on = S.tab === m && (!k.diet || S.diet === 'all' || S.diet === k.diet);
    const card = el('div', 'card' + (on ? ' on' : ''));
    card.style.setProperty('--m', messColor(m));
    card.addEventListener('click', () => (k.diet ? update(() => { S.tab = m; S.diet = k.diet; S.page = 0; }) : setTab(m)));

    const head = el('div', 'card-head');
    const name = el('span', 'card-name', cap(m));
    if (k.diet) {
      const d = el('span', 'card-diet ' + k.diet);
      d.append(svgEl(dietMark(k.diet)), document.createTextNode(DIET_LABEL[k.diet].toUpperCase()));
      name.append(d);
    }
    head.append(svgEl(messIcon(m)), name, el('span', 'verdict ' + v.k, v.w));
    card.append(head);

    const kpis = el('div', 'kpis');
    kpis.dataset.tk = `kpi:${key}`;
    const kpi = (val, label) => {
      const k = el('div', 'kpi');
      k.append(el('b', val === '-' ? 'none' : '', val), el('span', '', label));
      return k;
    };
    kpis.append(kpi(rate(s.msgs / win), 'MSGS/MIN'), kpi(rate(s.sales / win), 'SALES/MIN'), kpi(pctText(s.demand), 'DEMAND'));
    tip(kpis, () => statsTip(k, s, v, win));
    card.append(kpis);

    const spark = el('div', 'spark');
    spark.setAttribute('role', 'img');
    spark.setAttribute('aria-label', `${marketName(k)}: messages and sales per 5 minutes over the last 2 hours`);
    const band = el('span', 'band');
    band.style.setProperty('left', `${Math.max(0, ((n - win * 60 - start) / (end - start)) * 100)}%`);
    spark.append(band);
    for (const b of series.get(key)) {
      const col = el('span', 'bk');
      col.dataset.tk = `bk:${key}:${b.t0}`;
      const bar = (cls, v) => {
        const i = el('i', cls);
        i.style.setProperty('height', v ? `max(2px, ${(v / top) * 100}%)` : '0');
        return i;
      };
      col.append(bar('m', b.msgs), bar('s', b.sales));
      tip(col, () => bucketTip(k, b));
      spark.append(col);
    }
    card.append(spark);
    const axis = el('div', 'axis');
    axis.append(el('span', '', hm(start)), el('span', '', hm(start + SPAN * 30)), el('span', '', 'now'));
    card.append(axis);

    const extras = el('div', 'extras');
    const x = (label, val) => {
      const e = el('div', 'x');
      e.append(el('em', '', label), el('b', val === '-' ? 'none' : '', val));
      return e;
    };
    extras.append(
      x('ASK', s.ask == null ? '-' : s.ask === 0 ? 'FREE' : `₹${Math.round(s.ask)}`),
      x('SELLS IN', s.sellsIn == null ? '-' : dur(s.sellsIn)),
      x('WANTED', `${rate(s.wanted / win)}/min`),
    );
    card.append(extras);
    return card;
  }));
}

function statsTip(k, s, v, win) {
  const m = k.mess;
  const n = (k, one, many) => `${k} ${k === 1 ? one : many}`;
  const loose = k.diet ? untagged(m, win) : 0;
  const browsing = S.tab === m && (!k.diet || S.diet === k.diet);
  return [
    [{ t: `${marketName(k)} market`, c: 'nm', color: messColor(m) }],
    [{ t: `Last ${winWords(win)}`, c: 'c8' }],
    0,
    [{ t: 'Messages: ', c: 'c7' }, { t: `${s.msgs} (${rate(s.msgs / win)}/min)`, c: 'cf' }],
    [{ t: '  posts selling or swapping it, every copy', c: 'c8' }],
    [{ t: '  in every group, not replies', c: 'c8' }],
    ...(k.diet === 'nonveg' ? [[{ t: '  egg counts as non-veg', c: 'c8' }]] : []),
    ...(loose ? [[{ t: `  ${n(loose, 'post', 'posts')} for ${cap(m)} didn't say veg or non-veg`, c: 'c8' }]] : []),
    [{ t: 'Sales: ', c: 'c7' }, { t: `${s.sales} (${rate(s.sales / win)}/min)`, c: 'cf' }],
    [{ t: '  the seller ticked it or said sold', c: 'c8' }],
    [{ t: 'Demand: ', c: 'c7' }, { t: s.demand == null ? 'no messages' : `${pctText(s.demand)} sales per message`, c: 'cf' }],
    0,
    [{ t: 'Wanted: ', c: 'c7' }, { t: n(s.wanted, 'buy order', 'buy orders'), c: 'cf' }],
    [{ t: 'Ask: ', c: 'c7' }, s.ask == null ? { t: 'no prices stated', c: 'c8' } : { t: `₹${Math.round(s.ask)} median of ${n(s.asks, 'priced listing', 'priced listings')}`, c: 'c6' }],
    [{ t: 'Sells in: ', c: 'c7' }, s.sellsIn == null ? { t: 'no sales', c: 'c8' } : { t: `${dur(s.sellsIn)} median, post to tick`, c: 'cf' }],
    0,
    [{ t: v.why, c: v.k === 'hot' ? 'c6' : 'c7' }],
    0,
    [{ t: browsing ? 'Currently browsing' : 'Click to browse!', c: 'ce' }],
  ];
}

function bucketTip(k, b) {
  const t1 = b.t0 + STEP * 60;
  return [
    [{ t: `${marketName(k)}, ${hm(b.t0)}-${t1 > now() ? 'now' : hm(t1)}`, c: 'nm', color: messColor(k.mess) }],
    0,
    [{ t: 'Messages: ', c: 'c7' }, { t: String(b.msgs), c: 'cf' }],
    [{ t: 'Sales: ', c: 'c7' }, { t: String(b.sales), c: 'cf' }],
  ];
}

// ---------- actions ----------

function update(fn) {
  fn();
  persist();
  hideTip();
  render();
}
const setTab = (t) => update(() => { S.tab = t; S.page = 0; });
const turn = (d) => update(() => { S.page = Math.max(0, Math.min(pages - 1, S.page + d)); });
const sortRecent = () => update(() => {
  if (S.sort === 'recent') S.recentDir = S.recentDir === 'desc' ? 'asc' : 'desc';
  S.sort = 'recent'; S.page = 0;
});
const sortPrice = () => update(() => {
  if (S.sort === 'price') S.priceDir = S.priceDir === 'asc' ? 'desc' : 'asc';
  S.sort = 'price'; S.page = 0;
});
const step = (list, cur, dir) => list[(list.indexOf(cur) + dir + list.length) % list.length];
const cycleDiet = (dir = 1) => update(() => { S.diet = step(DIETS, S.diet, dir); S.page = 0; });
const cycleMeal = (dir = 1) => update(() => {
  S.meal = step(['all', ...MEALS], S.meal, dir); S.page = 0;
});
const toggleSold = () => update(() => { S.sold = !S.sold; S.page = 0; });
const toggleMode = () => update(() => { S.mode = S.mode === 'sell' ? 'buy' : 'sell'; S.page = 0; });

$('pgPrev').addEventListener('click', () => turn(-1));
$('pgNext').addEventListener('click', () => turn(1));

const q = $('q');
q.addEventListener('input', () => { S.q = q.value.trim(); S.page = 0; render(); });

addEventListener('keydown', (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (!$('modal').hidden) { if (e.key === 'Escape') closeModal(); return; }
  if (document.activeElement === q) {
    if (e.key === 'Escape') { q.value = ''; S.q = ''; q.blur(); render(); }
    return;
  }
  const k = e.key.toLowerCase();
  const names = D ? [...D.messes.map((m) => m.name), 'all'] : [];
  if (/^[1-9]$/.test(k) && names[+k - 1]) setTab(names[+k - 1]);
  else if (k === 'arrowleft') turn(-1);
  else if (k === 'arrowright') turn(1);
  else if (k === 'r') sortRecent();
  else if (k === 'p') sortPrice();
  else if (k === 'd' && !(S.tab !== 'all' && vegOnly(S.tab))) cycleDiet(e.shiftKey ? -1 : 1);
  else if (k === 'm') cycleMeal(e.shiftKey ? -1 : 1);
  else if (k === 's') toggleSold();
  else if (k === 'w') toggleMode();
  else if (k === '/') { e.preventDefault(); q.focus(); }
  else return;
});

// ---------- detail view ----------

const modal = $('modal');
const sheet = $('sheet');
modal.addEventListener('click', (e) => { if (e.target === modal) closeModal(); });
function closeModal() { modal.hidden = true; sheet.replaceChildren(); sheet.className = 'sheet'; saleClosed(); }

function digits(phone) { return (phone || '').replace(/\D/g, ''); }

function openListing(l) {
  hideTip();
  sheet.replaceChildren();
  const head = el('div', 'sheet-head');
  const big = slotNode(l);
  big.classList.remove('pop', 'item');
  big.disabled = true;
  head.append(big);
  const info = el('div');
  const h = el('h2', '', title(l));
  h.style.setProperty('color', messColor(l.mess));
  const w = window_(l.meal);
  info.append(h, el('div', 'sub', `${cap(dayWord(l.meal_date))}${w ? `, ${hm(w.start)}-${hm(w.end)}` : ''} · ${priceText(l)}`));
  const r = rarity(l);
  const pill = el('span', 'pill ' + (l.status === 'available' ? r.c : l.status === 'sold' ? doneColor(l) : 'c8'),
    l.status === 'available' ? r.rar : l.status === 'sold' ? doneWord(l).toUpperCase() : 'WITHDRAWN');
  info.append(pill);
  head.append(info);
  sheet.append(head);

  const facts = el('dl', 'facts');
  const fact = (k, v, cls) => { facts.append(el('dt', '', k)); facts.append(el('dd', cls || '', v)); };
  fact(l.kind === 'buy' ? 'Buyer' : 'Seller', l.seller.name || 'unknown');
  fact('Phone', l.seller.phone || 'hidden by WhatsApp in this group', l.seller.phone ? 'cb' : 'c8');
  if (l.kind === 'exchange') fact('Wants', (l.wants || []).map(cap).join(' or ') || 'not said', 'cd');
  fact('Diet', l.diet ? DIET_LABEL[l.diet] : 'not stated', DIET_COLOR[l.diet] || 'c8');
  if (l.quantity > 1) fact('Quantity', `${l.quantity} registrations`);
  fact('Posted', `${ago(l.first_seen_at)} (${hm(l.first_seen_at)})`);
  if (l.last_seen_at - l.first_seen_at > 120) fact('Last repost', `${ago(l.last_seen_at)} (${hm(l.last_seen_at)})`);
  fact('Meal ends', l.meal_ends_at > now() ? `in ${dur(l.meal_ends_at - now())} (${hm(l.meal_ends_at)})` : 'ended');
  if (l.other_ticks) fact('Others ticked', `${l.other_ticks}. It may already be gone.`, 'c6');
  if (l.meal_source === 'time') fact('Meal from', 'when it was posted. The post did not name a meal.', 'c8');
  sheet.append(facts);

  const copies = el('div', 'copies');
  copies.append(el('h3', '', 'POSTED IN'));
  for (const g of l.groups || []) {
    const c = el('div', 'copy');
    const meta = el('div', 'copy-meta');
    meta.append(el('b', '', shortGroup(g)));
    c.append(meta, el('div', 'copy-text', l.text));
    copies.append(c);
  }
  sheet.append(copies);
  // In Beeper mode the server also sends a beeper:// link per copy, which
  // opens that exact message in its group.
  const inBeeper = messagingApp() === 'beeper';
  fetch(`api/listing?id=${encodeURIComponent(l.id)}${inBeeper ? '&beeper=1' : ''}`, { cache: 'no-store' })
    .then((r) => (r.ok ? r.json() : null))
    .then((d) => {
      if (!d || !d.copies || modal.hidden) return;
      copies.replaceChildren(el('h3', '', `POSTED ${d.copies.length} ${d.copies.length === 1 ? 'TIME' : 'TIMES'}`));
      d.copies.forEach((cp, i) => {
        const link = inBeeper && !cp.deleted && d.beeper ? d.beeper[i] : '';
        const c = el(link ? 'a' : 'div', 'copy' + (cp.deleted ? ' deleted' : '') + (link ? ' linked' : ''));
        if (link) {
          c.href = link;
          c.title = 'Open this message in Beeper';
        }
        const meta = el('div', 'copy-meta');
        meta.append(el('b', '', shortGroup(cp.group)), el('span', '', cp.time.slice(11, 16)));
        if (cp.edited) meta.append(el('span', '', 'edited'));
        if (cp.deleted) meta.append(el('span', '', 'deleted'));
        if (link) {
          const go = el('span', 'copy-go');
          go.append(svgEl(beeperIcon), 'Open in Beeper');
          meta.append(go);
        }
        c.append(meta, el('div', 'copy-text', cp.text));
        copies.append(c);
      });
    })
    .catch(() => {});

  const ph = digits(l.seller.phone);
  const app = messagingApp();
  const actions = el('div', 'actions');
  const msg = el('a', 'btn primary');
  msg.append(svgEl(app === 'beeper' ? beeperIcon : waIcon), l.kind === 'buy' ? 'Message buyer' : 'Message seller');
  if (ph) {
    const text = encodeURIComponent(greeting(l));
    if (app === 'beeper') {
      msg.href = `beeper://compose/whatsapp/phone/+${ph}?text=${text}`;
    } else {
      msg.href = `https://wa.me/${ph}?text=${text}`;
      msg.target = '_blank';
      msg.rel = 'noopener noreferrer';
    }
  } else {
    msg.setAttribute('aria-disabled', 'true');
  }
  actions.append(msg);
  if (ph) {
    const copy = el('button', 'btn', 'Copy number');
    copy.addEventListener('click', () => {
      navigator.clipboard?.writeText(l.seller.phone).then(() => toast('Number copied'), () => toast(l.seller.phone));
    });
    actions.append(copy);
  }
  const close = el('button', 'btn', 'Close');
  close.addEventListener('click', closeModal);
  actions.append(close);
  sheet.append(actions);
  sheet.append(el('div', 'note', !ph
    ? 'WhatsApp hides this member\'s number. Reply to their post in the group instead.'
    : `The message button opens ${app === 'beeper' ? 'Beeper' : 'WhatsApp'} with a draft. Nothing is sent until you send it.`));
  modal.hidden = false;
}

// ---------- settings ----------

// Beeper's links only work in Beeper Desktop, so phones and tablets always
// get WhatsApp whatever the setting says.
function touchOnly() {
  return matchMedia('(hover: none) and (pointer: coarse)').matches;
}
function messagingApp() {
  return S.via === 'beeper' && !touchOnly() ? 'beeper' : 'whatsapp';
}
function paintSettingsIcon() {
  $('settingsIcon').replaceChildren(svgEl(messagingApp() === 'beeper' ? beeperIcon : waIcon));
}

// The settings page changes the messaging app in another tab; follow it.
addEventListener('storage', (e) => {
  if (e.key !== 'mah') return;
  try { S.via = (JSON.parse(e.newValue || '{}').via) || 'whatsapp'; } catch { return; }
  paintSettingsIcon();
});


function greeting(l) {
  const meal = `${cap(l.mess)} ${l.meal}`;
  if (l.kind === 'buy') return `Hi, I saw you're looking for ${meal}. I have one, still need it?`;
  if (l.kind === 'exchange') return `Hi, about your ${meal} exchange, is it still available?`;
  return `Hi, is your ${meal} still available?`;
}

let toastTimer = 0;
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 2200);
}

// ---------- sell your next meal ----------

// The nudge above the board points at your next registered meal nobody is
// selling yet: the one being served now or the next one, the same meals
// the sales ledger offers to sell. It reads api/sales, which asks the
// portal, so it refreshes every few minutes rather than with the board.
let N = null;            // last /api/sales, or null
let nudgeAt = 0;
const NUDGE_EVERY = 3 * 60 * 1000;
const skipped = (() => { try { return new Set(JSON.parse(sessionStorage.getItem('nudge-skip') || '[]')); } catch { return new Set(); } })();

async function loadNudge(force) {
  if (!force && Date.now() - nudgeAt < NUDGE_EVERY) return;
  nudgeAt = Date.now();
  try {
    const r = await fetch('api/sales', { cache: 'no-store' });
    N = r.ok ? await r.json() : null;
  } catch { N = null; }
  renderNudge();
}

function nextSellable() {
  if (!D.nudge || !N || !N.owner || N.sell_off) return null;
  const n = now();
  const out = [];
  for (const d of N.days) for (const c of d.cells) {
    if (c.state === 'open' && c.sellable && c.end > n && !skipped.has(`${d.date}|${c.meal}`)) out.push({ date: d.date, c });
  }
  out.sort((a, b) => a.c.start - b.c.start);
  return out[0] || null;
}

// Lines for the nudge. The same meal keeps the same line across refreshes.
function nudgeLine(c, key) {
  const ask = c.ask ? rupees(Math.round(c.ask / 5) * 5) : '';
  const lines = [
    ask ? `You don't wanna waste ${ask}.` : `You don't wanna waste the money.`,
    `Not hungry? Someone else is.`,
    ask ? `Someone out there would pay about ${ask} for it.` : `Someone out there would pay for it.`,
    `An uneaten ${c.meal} is money in the bin.`,
    `The mess already has your money. Get some of it back.`,
    `Skipping it? Get paid instead.`,
    `Your ${c.meal} won't sell itself. Fine, we'll do it.`,
  ];
  let h = 0;
  for (const ch of key) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return lines[h % lines.length];
}

function renderNudge() {
  const box = $('nudge');
  const hit = nextSellable();
  box.hidden = !hit;
  if (!hit) { box.replaceChildren(); return; }
  const { date, c } = hit;
  const key = `${date}|${c.meal}`;
  const n = now();
  box.style.setProperty('--mess', messColor(c.mess));

  const ic = el('span', 'nudge-icon');
  ic.append(svgEl(messIcon(c.mess)));

  const text = el('div', 'nudge-text');
  text.append(el('b', 'nudge-line', nudgeLine(c, key)));
  const what = el('span', 'nudge-what');
  const name = el('span', 'nudge-meal', `${c.mess ? cap(c.mess) : 'Your'} ${c.meal}`);
  what.append(name, ` ${dayWord(date)} ${hm(c.start)}-${hm(c.end)} · ${n >= c.start ? `serving now, ${dur(c.end - n)} left` : `starts in ${dur(c.start - n)}`}`);
  text.append(what);

  const go = el('a', 'nudge-go', 'SELL IT');
  go.href = `sales.html#sell=${encodeURIComponent(key)}`;

  const x = el('button', 'nudge-x', '×');
  x.type = 'button';
  x.setAttribute('aria-label', `Not this ${c.meal}`);
  x.title = `Not this ${c.meal}`;
  x.addEventListener('click', () => {
    skipped.add(key);
    try { sessionStorage.setItem('nudge-skip', JSON.stringify([...skipped])); } catch {}
    renderNudge();
  });

  box.replaceChildren(ic, text, go, x);
}

// ---------- live status ----------

function tickLive() {
  const live = $('live');
  const s = Math.round((Date.now() - lastOK) / 1000);
  live.classList.toggle('off', failed || !lastOK);
  $('liveText').textContent = !lastOK ? 'connecting' : failed ? `offline, last update ${s}s ago` : streaming || s <= pollEvery() + 2 ? 'live' : `updated ${s}s ago`;
  if (tipFor && tipFn && !tipEl.hidden && document.body.contains(tipFor)) fillTip(tipFn());
}

// ---------- boot ----------

$('logo').append(svgEl(icon('coin')));
$('navIcon').append(svgEl(icon('ingot')));
$('insIcon').append(svgEl(icon('chart')));
$('useIcon').append(svgEl(icon('check')));
paintSettingsIcon();
me().then((m) => {
  userLink(m);
  $('insLink').hidden = m.role !== 'admin';
  $('useLink').hidden = m.role !== 'admin';

}).catch(() => {});
$('pulseIcon').append(svgEl(icon('chart')));
layout();
addEventListener('resize', () => {
  const before = `${L.cols}x${L.rows}x${L.s}`;
  layout();
  if (`${L.cols}x${L.rows}x${L.s}` !== before) render();
});
load();
connect();
setInterval(tickLive, 1000);
setInterval(() => { if (D && document.visibilityState === 'visible') { renderDay(); renderGrid(); renderStats(); keepTip(); } }, 30000);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') load(); });
