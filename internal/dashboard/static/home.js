'use strict';

// The home page is a column of widgets the user picks and orders. Edit
// mode, opened from Settings › You (./#edit-home), turns every widget into a tile with a grip and a ✕, and opens a tray of
// the widgets not on the page, each with a preview drawn from made-up data
// (widget-demo.js). Drag a tile to reorder it, drag a tray card onto the
// page to add it, drag a tile onto the tray to take it off. Tapping + or ✕
// does the same without dragging. The layout is kept in this browser.
//
// Today and Scheduled are sale.js's cards, Alerts is attention.js's banner,
// and the rest come from widgets.js. The market board and its pulse used to
// be widgets too; they live on the Buy tab (buy.html) now, and a saved
// layout that names them just skips them.

const HOME_KEY = 'mah-home';
const HOME_DEFAULT = ['alerts', 'today', 'ledger', 'earnings'];

// Each widget: its name, what it shows (for the tray), its icon, and how
// it draws. Widgets with `live` draw from the data the page loads; their
// `preview` draws the same thing from the demo data.
const WIDGETS = {
  alerts: { name: 'Alerts', blurb: 'Problems that need you: refunds, failed posts, rules that hit a snag.', art: 'check' },
  today: { name: 'Today', blurb: 'Each of today\'s meals: selling, scheduled, eaten, or a button to sell it.', art: 'clock' },
  scheduled: { name: 'Scheduled', blurb: 'Sales waiting for their meal, with when they go up and at what price.', art: 'clock' },
  ledger: { name: 'Sales ledger', blurb: 'This week\'s meals and what became of them, and what you saved this month.', art: 'ingot' },
  earnings: { name: 'Earnings', blurb: '₹ per day over four weeks, per mess and diet.', art: 'chart' },
  regulars: { name: 'Regulars', blurb: 'Who keeps buying from you, and what they usually take.', art: 'paper' },
  prices: { name: 'Price check', blurb: 'Your sale prices against the market\'s asks.', art: 'chart' },
  rules: { name: 'Recurring rules', blurb: 'Rules that schedule your meals for sale every week.', art: 'clock' },
  friends: { name: 'Friends\' meals', blurb: 'Meals you sell for friends, kept out of your totals.', art: 'coin' },
};

let homeOrder = (() => {
  try {
    const v = JSON.parse(localStorage.getItem(HOME_KEY));
    if (Array.isArray(v)) return v.filter((id, i) => WIDGETS[id] && v.indexOf(id) === i);
  } catch {}
  return HOME_DEFAULT.slice();
})();
let homeEditing = false;
let homeRules = null;      // last api/recurring-rules, or null
let homeRulesAt = 0;
let homeAlerts = 0;        // how many alerts attention.js shows
const homeSecs = new Map(); // id -> the widget's section

const homeHas = (id) => homeOrder.includes(id);
function homeSave() { try { localStorage.setItem(HOME_KEY, JSON.stringify(homeOrder)); } catch {} }

// ---------- building widgets ----------

// wChest is a widget's panel: a header with its icon, name and a meta
// line, and a body. Returns the parts.
function wChest(id, cls) {
  const w = WIDGETS[id];
  const box = el('section', 'chest w-chest' + (cls ? ' ' + cls : ''));
  box.setAttribute('aria-label', w.name);
  const head = el('div', 'chest-head');
  const ic = el('span', 'head-icon');
  ic.append(svgEl(icon(w.art)));
  const meta = el('span', 'head-meta');
  head.append(ic, el('span', 'head-name', w.name), meta);
  const body = el('div', 'w-body');
  box.append(head, body);
  return { box, head, meta, body };
}

// The widget sections, made once.
function homeSection(id) {
  if (homeSecs.has(id)) return homeSecs.get(id);
  const sec = el('div', 'widget');
  sec.dataset.w = id;
  const bar = el('div', 'w-edit');
  const grip = el('span', 'w-grip', '⠿');
  grip.setAttribute('aria-hidden', 'true');
  const name = el('span', 'w-name', WIDGETS[id].name);
  const x = el('button', 'w-x', '✕');
  x.type = 'button';
  x.title = `Take ${WIDGETS[id].name} off the page`;
  x.setAttribute('aria-label', x.title);
  x.addEventListener('click', () => homeRemove(id));
  // Keyboard reordering: arrows on the bar move the widget.
  const up = el('button', 'w-mv', '↑'), down = el('button', 'w-mv', '↓');
  up.type = down.type = 'button';
  up.setAttribute('aria-label', `Move ${WIDGETS[id].name} up`);
  down.setAttribute('aria-label', `Move ${WIDGETS[id].name} down`);
  up.addEventListener('click', () => homeMove(id, -1));
  down.addEventListener('click', () => homeMove(id, 1));
  bar.append(grip, name, up, down, x);
  bar.addEventListener('pointerdown', (e) => {
    if (e.target.closest('button')) return;
    dragStart(e, id, 'page', sec);
  });
  const content = el('div', 'w-content');
  sec.append(bar, content);

  let parts = null;
  if (id === 'alerts') {
    const host = el('div', 'w-alerts');
    host.id = 'attnHost';
    content.append(host, el('div', 'w-quiet-note', 'Nothing needs you right now. Alerts show here when something does.'));
  } else {
    parts = wChest(id);
    content.append(parts.box);
    if (id === 'today' || id === 'scheduled') {
      parts.body.classList.add('sales-box');
      parts.body.id = id === 'today' ? 'todayBox' : 'schedBox';
      const more = el('a', 'w-link head-link', id === 'today' ? 'My sales ›' : 'All in My sales ›');
      more.href = 'sales.html';
      parts.head.append(more);
    }
  }
  sec._parts = parts;
  homeSecs.set(id, sec);
  return sec;
}

// homeDraw fills the widgets that draw from api/sales or rules.
function homeDraw(id) {
  const sec = homeSecs.get(id);
  if (!sec || !sec._parts) return;
  const { body, meta } = sec._parts;
  const n = typeof N !== 'undefined' ? N : null;
  const need = ['ledger', 'earnings', 'regulars', 'prices', 'friends'];
  if (need.includes(id) && !n) {
    if (!body.firstChild) body.append(el('div', 'empty-note', 'Loading your sales'));
    return;
  }
  switch (id) {
    case 'ledger': HW.ledger(body, n); meta.textContent = `week of ${new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(new Date(n.today + 'T12:00:00Z'))}`; break;
    case 'earnings': HW.earnings(body, n); meta.textContent = HW.earnMeta(n); break;
    case 'regulars': HW.regulars(body, n); meta.textContent = HW.regMeta(n); break;
    case 'prices': HW.prices(body, n); meta.textContent = 'last 4 weeks'; break;
    case 'friends': HW.friends(body, n); meta.textContent = HW.friendMeta(n); break;
    case 'rules':
      if (homeRules) { HW.rules(body, homeRules); meta.textContent = HW.ruleMeta(homeRules); }
      else if (!body.firstChild) body.append(el('div', 'empty-note', 'Loading your rules'));
      break;
    case 'today': meta.textContent = todayMeta(); break;
    case 'scheduled': {
      const k = ((typeof D !== 'undefined' && D && D.scheduled) || []).length;
      meta.textContent = k ? `${k} waiting` : '';
      break;
    }
  }
}

function todayMeta() {
  if (typeof D === 'undefined' || !D) return '';
  const date = ymd(now());
  const sold = ((typeof N !== 'undefined' && N && N.sales) || []).filter((s) => s.date === date && (s.state === 'sold' || s.state === 'sending'));
  const words = new Intl.DateTimeFormat('en-GB', { weekday: 'long', day: 'numeric', month: 'short', timeZone: tz() }).format(now() * 1000);
  return sold.length ? `${words} · ${rupees(sold.reduce((a, s) => a + s.amount, 0))} earned` : words;
}

async function homeLoadRules(force) {
  if (!homeHas('rules') && !homeEditing) return;
  if (!force && Date.now() - homeRulesAt < 60000) return;
  homeRulesAt = Date.now();
  try {
    const r = await fetch('api/recurring-rules', { cache: 'no-store' });
    if (!r.ok) throw new Error(r.status);
    homeRules = await r.json();
  } catch { if (!homeRules) homeRules = []; }
  homeDraw('rules');
}

// ---------- hooks app.js calls ----------

// homeRender runs after every board render.
function homeRender() {
  homeDraw('today');
  homeDraw('scheduled');
  homeLoadRules();
}

// homeSalesLoaded runs when app.js has a fresh api/sales.
function homeSalesLoaded() {
  for (const id of ['ledger', 'earnings', 'regulars', 'prices', 'friends', 'today']) if (homeHas(id)) homeDraw(id);
}

document.addEventListener('attention', (e) => {
  homeAlerts = e.detail;
  const sec = homeSecs.get('alerts');
  if (sec) sec.classList.toggle('w-empty', !homeAlerts);
});

// ---------- layout ----------

function homeApply() {
  const host = $('widgets');
  // Every widget is made up front.
  for (const id of Object.keys(WIDGETS)) homeSection(id);
  const keep = new Set(homeOrder);
  for (const id of homeOrder) {
    const sec = homeSection(id);
    sec.hidden = false;
    host.append(sec);
  }
  for (const [id, sec] of homeSecs) {
    if (keep.has(id)) continue;
    sec.hidden = true;
    host.append(sec);
  }
  $('homeEmpty').hidden = homeOrder.length > 0;
  for (const id of homeOrder) homeDraw(id);
  if (typeof D !== 'undefined' && D) {
    renderSales();
    renderNudge();
  }
  homeLoadRules();
  if (homeEditing) homeTray();
}

function homeSet(order) {
  homeOrder = order;
  homeSave();
  homeApply();
}
function homeRemove(id) { homeSet(homeOrder.filter((x) => x !== id)); }
function homeAdd(id, at = homeOrder.length) {
  const o = homeOrder.filter((x) => x !== id);
  o.splice(at, 0, id);
  homeSet(o);
  const sec = homeSecs.get(id);
  if (sec) {
    sec.classList.add('w-new');
    setTimeout(() => sec.classList.remove('w-new'), 900);
    sec.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }
}
function homeMove(id, d) {
  const o = homeOrder.slice(), i = o.indexOf(id), j = i + d;
  if (j < 0 || j >= o.length) return;
  [o[i], o[j]] = [o[j], o[i]];
  homeSet(o);
  homeSecs.get(id)?.querySelector(d < 0 ? '.w-mv' : '.w-mv + .w-mv')?.focus();
}

// ---------- edit mode ----------

function homeEdit(on) {
  homeEditing = on;
  document.body.classList.toggle('home-editing', on);
  $('homeBar').hidden = !on;
  $('tray').hidden = !on;
  // Tiles can't be clicked into while editing; the grip and ✕ can.
  for (const sec of homeSecs.values()) {
    const c = sec.querySelector('.w-content');
    if (on) c.setAttribute('inert', ''); else c.removeAttribute('inert');
  }
  if (on) {
    hideTip();
    homeTray();
    homeLoadRules();
  }
}

$('homeDone').addEventListener('click', () => homeEdit(false));
$('homeReset').addEventListener('click', () => homeSet(HOME_DEFAULT.slice()));
$('homeEmptyEdit').addEventListener('click', () => homeEdit(true));
addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && drag) dragEnd(null);
  else if (e.key === 'Escape' && homeEditing && $('modal').hidden) homeEdit(false);
});

// ---------- the tray ----------

// homeTray lists the widgets off the page, each with a preview.
function homeTray() {
  const list = $('trayList');
  const off = Object.keys(WIDGETS).filter((id) => !homeHas(id));
  $('trayMeta').textContent = off.length ? `${off.length} more · drag one onto the page, or tap +` : 'Every widget is on your page';
  const have = new Map([...list.children].map((c) => [c.dataset.w, c]));
  list.replaceChildren(...off.map((id) => have.get(id) || trayCard(id)));
}

function trayCard(id) {
  const w = WIDGETS[id];
  const c = el('div', 'tray-card');
  c.dataset.w = id;
  const frame = el('div', 'tray-prev');
  const zoom = el('div', 'tray-zoom');
  zoom.setAttribute('inert', '');
  zoom.setAttribute('aria-hidden', 'true');
  frame.append(zoom);
  try { homePreview(id, zoom); } catch (err) { zoom.append(el('div', 'empty-note', 'No preview')); }
  const meta = el('div', 'tray-info');
  const name = el('b', '', w.name);
  meta.append(name, el('span', '', w.blurb));
  const add = el('button', 'tray-add', '+');
  add.type = 'button';
  add.title = `Add ${w.name} to the page`;
  add.setAttribute('aria-label', add.title);
  add.addEventListener('click', () => homeAdd(id));
  c.append(frame, meta, add);
  c.addEventListener('pointerdown', (e) => {
    if (e.target.closest('button')) return;
    dragStart(e, id, 'tray', c);
  });
  return c;
}

// ---------- previews ----------

let demo = null;
// withDemo runs fn with app.js's board and ledger swapped for the demo
// data, so the real renderers draw the preview.
function withDemo(fn) {
  if (!demo || Math.abs(demo.state.now - now()) > 600) demo = demoData(Math.floor(now()));
  const d0 = D, n0 = N, s0 = { ...S };
  D = demo.state; N = demo.sales;
  Object.assign(S, { tab: 'all', mode: 'sell', sold: false, meal: 'all', diet: 'all', page: 0 });
  try { return fn(); } finally { D = d0; N = n0; Object.assign(S, s0); }
}

function homePreview(id, box) {
  const fill = (fn) => {
    const parts = wChest(id);
    fn(parts.body, parts.meta);
    box.append(parts.box);
  };
  withDemo(() => {
    const n = N;
    switch (id) {
      case 'alerts': box.append(alertsPreview(demo.attention)); break;
      case 'today': fill((b, m) => { b.classList.add('sales-box'); renderToday(b, null); m.textContent = 'Wednesday 18 Nov'; }); break;
      case 'scheduled': fill((b, m) => { b.classList.add('sales-box'); renderScheduled(b); m.textContent = `${D.scheduled.length} waiting`; }); break;
      case 'ledger': fill((b) => HW.ledger(b, n)); break;
      case 'earnings': fill((b, m) => { HW.earnings(b, n); m.textContent = HW.earnMeta(n); }); break;
      case 'regulars': fill((b, m) => { HW.regulars(b, n); m.textContent = HW.regMeta(n); }); break;
      case 'prices': fill((b, m) => { HW.prices(b, n); m.textContent = 'last 4 weeks'; }); break;
      case 'friends': fill((b, m) => { HW.friends(b, n); m.textContent = HW.friendMeta(n); }); break;
      case 'rules': fill((b, m) => { HW.rules(b, demo.rules); m.textContent = HW.ruleMeta(demo.rules); }); break;
    }
  });
}

function alertsPreview(items) {
  const box = el('section', 'attn red');
  const head = el('div', 'attn-head');
  const text = el('div', 'attn-text');
  text.append(el('span', 'attn-line', `${items.length} THINGS NEED YOU`));
  head.append(el('span', 'attn-mark', '!'), text, el('span', 'attn-bell', 'ALERT ME'));
  const list = el('div', 'attn-list');
  for (const it of items) {
    const r = el('div', 'attn-row ' + (it.kind === 'refund' ? 'red' : 'egg'));
    const copy = el('div', 'attn-copy');
    copy.append(el('b', '', it.title), el('p', '', it.detail));
    const acts = el('div', 'attn-acts');
    acts.append(el('span', 'attn-btn go', it.task ? 'OPEN SALE' : 'OPEN RULE'));
    r.append(el('span', 'attn-tag', it.kind === 'refund' ? 'REFUND' : 'RULE'), copy, acts);
    list.append(r);
  }
  box.append(head, list);
  return box;
}

// ---------- dragging ----------

// drag is the drag in progress: the widget, where it came from, the ghost
// following the pointer and the placeholder marking where it would land.
let drag = null;
let dragAuto = 0;

function dragStart(e, id, from, src) {
  if (!homeEditing || e.button > 0) return;
  const x0 = e.clientX, y0 = e.clientY;
  // A mouse drags once it moves a few pixels. A finger on a tray card has
  // to hold still first, so swiping still scrolls the tray; the grip on a
  // page widget drags straight away.
  const touch = e.pointerType !== 'mouse';
  const hold = touch && from === 'tray';
  let started = false, timer = 0;
  const begin = (ev) => {
    started = true;
    clearTimeout(timer);
    const ghost = el('div', 'drag-ghost');
    const gi = el('span');
    gi.append(svgEl(icon(WIDGETS[id].art)));
    ghost.append(gi, WIDGETS[id].name);
    document.body.append(ghost);
    const ph = el('div', 'w-ph');
    const h = from === 'page' ? Math.min(src.getBoundingClientRect().height, 220) : 120;
    ph.style.setProperty('height', `${h}px`);
    if (from === 'page') {
      src.after(ph);
      src.classList.add('w-dragging');
    } else {
      src.classList.add('w-lifted');
    }
    drag = { id, from, src, ghost, ph, x: ev.clientX, y: ev.clientY };
    document.body.classList.add('home-dragging');
    if (navigator.vibrate && touch) navigator.vibrate(10);
    dragMove(ev);
  };
  const move = (ev) => {
    if (!started) {
      const far = Math.hypot(ev.clientX - x0, ev.clientY - y0) > 6;
      if (hold) { if (far) cleanup(); return; }
      if (far || touch) begin(ev);
      return;
    }
    ev.preventDefault();
    dragMove(ev);
  };
  const up = (ev) => {
    cleanup();
    if (started) dragEnd(ev);
  };
  const cleanup = () => {
    clearTimeout(timer);
    removeEventListener('pointermove', move);
    removeEventListener('pointerup', up);
    removeEventListener('pointercancel', cancel);
  };
  const cancel = () => { cleanup(); if (started) dragEnd(null); };
  addEventListener('pointermove', move, { passive: false });
  addEventListener('pointerup', up);
  addEventListener('pointercancel', cancel);
  if (hold) timer = setTimeout(() => begin(e), 280);
  else if (touch) { e.preventDefault(); begin(e); }
}

// Holding a finger on a tray card must not scroll the page once it's lifted.
document.addEventListener('touchmove', (e) => { if (drag) e.preventDefault(); }, { passive: false });

function overTray(x, y) {
  const r = $('tray').getBoundingClientRect();
  return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
}

function dragMove(ev) {
  const d = drag;
  d.x = ev.clientX; d.y = ev.clientY;
  d.ghost.style.setProperty('transform', `translate(${d.x + 12}px, ${d.y + 12}px)`);
  const out = overTray(d.x, d.y);
  $('tray').classList.toggle('drop-out', out && d.from === 'page');
  d.ghost.classList.toggle('out', out && d.from === 'page');
  if (out) {
    d.ph.remove();
  } else {
    // Land before the first widget whose middle is below the pointer.
    const host = $('widgets');
    const secs = [...host.children].filter((s) => s.classList.contains('widget') && !s.hidden && s !== d.src);
    const before = secs.find((s) => { const r = s.getBoundingClientRect(); return d.y < r.top + Math.min(r.height, 240) / 2; });
    if (before) { if (d.ph.nextSibling !== before) host.insertBefore(d.ph, before); }
    else {
      const lastSec = secs[secs.length - 1];
      if (lastSec) { if (lastSec.nextSibling !== d.ph) lastSec.after(d.ph); }
      else if (d.ph.parentNode !== host) host.prepend(d.ph);
    }
  }
  // Scroll while held near the top or bottom of the window.
  cancelAnimationFrame(dragAuto);
  const edge = 70, bottom = homeEditing ? $('tray').getBoundingClientRect().top : innerHeight;
  const v = d.y < edge ? -(edge - d.y) / 4 : d.y > bottom - edge && d.y < bottom ? (d.y - (bottom - edge)) / 4 : 0;
  if (v) {
    const step = () => {
      if (!drag) return;
      scrollBy(0, v);
      dragAuto = requestAnimationFrame(step);
    };
    dragAuto = requestAnimationFrame(step);
  }
}

function dragEnd(ev) {
  const d = drag;
  if (!d) return;
  drag = null;
  cancelAnimationFrame(dragAuto);
  document.body.classList.remove('home-dragging');
  $('tray').classList.remove('drop-out');
  d.ghost.remove();
  d.src.classList.remove('w-dragging', 'w-lifted');
  const placed = d.ph.parentNode === $('widgets');
  let order = null;
  if (ev && placed) {
    // The new order is the page's widgets in DOM order, the placeholder
    // standing in for the dragged one.
    order = [];
    for (const n of $('widgets').children) {
      if (n === d.ph) order.push(d.id);
      else if (n.classList.contains('widget') && !n.hidden && n.dataset.w !== d.id) order.push(n.dataset.w);
    }
  } else if (ev && d.from === 'page' && overTray(ev.clientX, ev.clientY)) {
    order = homeOrder.filter((x) => x !== d.id);
  }
  d.ph.remove();
  if (order) {
    const added = d.from === 'tray';
    homeSet(order);
    if (added) {
      const sec = homeSecs.get(d.id);
      sec.classList.add('w-new');
      setTimeout(() => sec.classList.remove('w-new'), 900);
    }
  }
}

// Settings › You links here with #edit-home to open edit mode.
function homeEditFromHash() {
  if (location.hash !== '#edit-home') return;
  history.replaceState(null, '', location.pathname + location.search);
  homeEdit(true);
}
addEventListener('hashchange', homeEditFromHash);

// ---------- boot ----------

homeApply();
homeEditFromHash();
