'use strict';

// The sales page: what became of every meal you registered for, who bought
// them and for how much. Buyer names are WhatsApp push names, so they only
// reach the page through textContent.

let D = null;           // last /api/sales
let skew = 0;           // server clock minus ours, seconds
let lastOK = 0;
let failed = false;
let streaming = false;
let page = 0;           // ledger week: 0 ends today, 1 the week before, ...
let pickedCell = '';    // "date|meal" in the ledger inspector
let pickedBuyer = '';   // buyer key in the regulars inspector

const now = () => Date.now() / 1000 + skew;
const tz = () => (D && D.tz) || 'Asia/Kolkata';
const hm = (u) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz() }).format(u * 1000);

// Dates are YYYY-MM-DD in the mess's timezone. Noon UTC keeps them on the
// same calendar day whatever the browser's zone.
const dayOf = (date) => new Date(date + 'T12:00:00Z');
const fmt = (date, o) => new Intl.DateTimeFormat('en-GB', { ...o, timeZone: 'UTC' }).format(dayOf(date));
const fmtDay = (date) => fmt(date, { weekday: 'short', day: 'numeric', month: 'short' });
const fmtShort = (date) => fmt(date, { day: 'numeric', month: 'short' });
const addDays = (date, n) => { const d = dayOf(date); d.setUTCDate(d.getUTCDate() + n); return d.toISOString().slice(0, 10); };
const dayWord = (date) => (date === D.today ? 'today' : date === addDays(D.today, -1) ? 'yesterday' : fmtDay(date));

function dur(sec) {
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return `${sec}s`;
  const m = Math.round(sec / 60);
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)}h ${m % 60}m`;
}
const fmtPhone = (p) => (/^\+91\d{10}$/.test(p) ? `+91 ${p.slice(3, 8)} ${p.slice(8)}` : p);
const messName = (m) => (m ? cap(m) : 'Unknown mess');
const isSold = (s) => s.state === 'sold' || s.state === 'sending';
const sum = (xs) => xs.reduce((a, b) => a + b, 0);
const signed = (v) => `${v > 0 ? '+' : v < 0 ? '−' : '±'}${rupees(Math.abs(Math.round(v)))}`;
function median(xs) {
  if (!xs.length) return null;
  const v = xs.slice().sort((a, b) => a - b), h = v.length >> 1;
  return v.length % 2 ? v[h] : (v[h - 1] + v[h]) / 2;
}
const meals = () => D.windows.map((w) => w.meal);
// priceDiet is the diet a meal's price counts under. Non-veg goes for two
// or three times veg, so prices are never pooled across diets. A meal with
// no diet on record counts as veg, as the server does for listings.
const priceDiet = (d) => (d === 'nonveg' || d === 'egg' ? d : 'veg');
// marketFor is the market spread for meal in diet, or for every diet when
// that diet has no asks.
const marketFor = (meal, diet) => D.market.find((x) => x.meal === meal && x.diet === priceDiet(diet))
  || D.market.find((x) => x.meal === meal && !x.diet);
const recentSales = () => D.sales.filter((s) => isSold(s) && s.date >= D.recent && s.date <= D.today);

function span(cls, markup) {
  const s = el('span', cls);
  s.append(svgEl(markup));
  return s;
}

// ---------- loading ----------

async function load() {
  try {
    const r = await fetch('api/sales', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    if (!r.ok) throw new Error(r.status);
    D = await r.json();
    skew = D.now - Date.now() / 1000;
    failed = false;
    lastOK = Date.now();
    render();
  } catch (e) {
    failed = true;
  }
  tickLive();
}

function connect() {
  if (typeof EventSource === 'undefined') return;
  const es = new EventSource('api/events');
  es.addEventListener('open', () => { streaming = true; tickLive(); });
  es.addEventListener('change', () => { if (document.visibilityState === 'visible') load(); });
  es.addEventListener('error', () => { streaming = false; tickLive(); });
}

function tickLive() {
  const s = Math.round((Date.now() - lastOK) / 1000);
  $('live').classList.toggle('off', failed || !lastOK);
  $('liveText').textContent = !lastOK ? 'connecting' : failed ? `offline, last update ${s}s ago` : streaming ? 'live' : `updated ${s}s ago`;
}

function render() {
  const n = $('notice');
  n.hidden = !D.portal;
  if (D.portal) {
    n.replaceChildren(el('b', '', 'Registrations unavailable. '),
      D.owner
        ? `The portal said: ${D.portal}. The ledger shows sales only, so eaten and wasted meals are missing.`
        : `The portal said: ${D.portal}.`);
  }
  // Sales belong to the linked WhatsApp account. Members see their own
  // registrations only.
  if (!D.owner) {
    $('title').textContent = 'My meals';
    $('sub').textContent = 'Your registrations: eaten, wasted and still open';
    document.title = 'My meals · Mess Auction House';
  }
  for (const id of ['regSec', 'pcSec', 'earnSec']) $(id).hidden = !D.owner;
  for (const e of document.querySelectorAll('.own')) e.hidden = !D.owner;
  renderSaved();
  renderLedger();
  if (D.owner) {
    renderFriends();
    renderRegulars();
    renderPrices();
    renderEarnings();
  }
  // A re-render replaces whatever the tooltip was attached to.
  if (tipFor && !tipFor.isConnected) hideTip();
}

// ---------- saved this month ----------

function renderSaved() {
  const M = D.month;
  const monthName = fmt(M.start, { month: 'long' });
  const scale = M.fee > 0 ? M.fee : M.earned + M.potential;
  const ew = scale ? Math.min(1, M.earned / scale) : 0;
  const cw = scale ? Math.min(1 - ew, M.potential / scale) : 0;
  $('xpEarned').style.setProperty('width', `${ew * 100}%`);
  const could = $('xpCould');
  could.style.setProperty('left', `${ew * 100}%`);
  could.style.setProperty('width', `${cw * 100}%`);

  if (!D.owner) {
    // Members don't sell through messwa, so this is only what went to waste.
    $('savedK').textContent = `Wasted in ${monthName}`;
    $('savedV').replaceChildren(rupees(Math.round(M.potential)));
    const row = $('savedRow');
    const left = el('span');
    left.append(el('b', '', `${M.wasted} meal${M.wasted === 1 ? '' : 's'}`), ' ended unsold and uneaten');
    const right = el('span');
    if (M.wasted) right.append(`worth about ${rupees(Math.round(M.potential))} at the median ask`);
    if (M.unpriced) right.append(` (${M.unpriced} with no asks to go on)`);
    row.replaceChildren(left, right);
    return;
  }
  $('savedK').textContent = `Saved in ${monthName}`;
  const v = $('savedV');
  v.replaceChildren(rupees(M.earned));
  if (M.fee > 0) v.append(el('small', '', `${Math.round((M.earned / M.fee) * 100)}% of ${rupees(M.fee)}`));

  const row = $('savedRow');
  const left = el('span');
  left.append(el('b', '', `${M.sold} meal${M.sold === 1 ? '' : 's'}`), ' sold');
  if (M.fee > 0) left.append(` against a ${rupees(M.fee)} mess bill`);
  else if (M.bill) left.append(el('span', 'dim', '. The portal shows no mess bill this month'));
  else left.append(el('span', 'dim', '. Couldn\'t read your mess bill from the portal to measure it against'));
  const right = el('span');
  if (M.wasted) {
    right.append(el('span', 'g', `+${rupees(Math.round(M.potential))}`),
      ` more if the ${M.wasted} wasted meal${M.wasted === 1 ? '' : 's'} had sold at the median ask`);
    if (M.unpriced) right.append(` (${M.unpriced} with no asks to go on)`);
  } else if (!D.portal) {
    right.append(el('span', 'x', 'Nothing wasted this month'));
  }
  row.replaceChildren(left, right);
}

function savedTip() {
  const M = D.month;
  const monthName = fmt(M.start, { month: 'long' });
  return [
    [{ t: `${monthName} so far`, c: 'nm' }],
    D.owner ? [{ t: 'Earned ', c: 'c7' }, { t: rupees(M.earned), c: 'ca' }, { t: ` from ${M.sold} sold`, c: 'c8' }] : null,
    M.wasted ? [{ t: 'Left on the table ', c: 'c7' }, { t: rupees(Math.round(M.potential)), c: 'c6' }, { t: ` from ${M.wasted} wasted`, c: 'c8' }] : null,
    M.fee > 0 ? [{ t: 'Mess bill ', c: 'c7' }, { t: rupees(M.fee), c: 'cf' }, { t: M.bill ? '' : ' from config.toml', c: 'c8' }] : null,
    ...(M.bill ? [[{ t: 'Availed ', c: 'c7' }, { t: rupees(M.bill.availed), c: 'cf' }, { t: ' + projected ', c: 'c7' }, { t: rupees(M.bill.projected), c: 'cf' }]] : []),
  ];
}

// ---------- ledger ----------

const pages = () => Math.max(1, Math.ceil(D.days.length / 7));
const weekDates = () => {
  const end = addDays(D.today, -7 * page);
  return Array.from({ length: 7 }, (_, i) => addDays(end, i - 6));
};
const dayMap = () => new Map(D.days.map((d) => [d.date, d]));

function renderLedger() {
  page = Math.min(page, pages() - 1);
  const dates = weekDates();
  const days = dayMap();
  const g = $('lgrid');
  g.replaceChildren(el('div'));
  const cells = [];
  for (const date of dates) {
    const h = el('div', 'dh' + (date === D.today ? ' today' : ''), date === D.today ? 'TODAY' : `${fmt(date, { weekday: 'short' }).toUpperCase()} ${dayOf(date).getUTCDate()}`);
    const total = sum(D.sales.filter((s) => isSold(s) && s.date === date).map((s) => s.amount));
    h.append(days.has(date) ? el('span', total ? '' : 'none', total ? rupees(total) : '-') : el('span', 'none', ' '));
    g.append(h);
  }
  // Skip a meal row when nothing in it was registered this week.
  const cellAt = (date, meal) => { const day = days.get(date); return day && day.cells.find((x) => x.meal === meal); };
  const shown = meals().filter((meal) => dates.some((date) => { const c = cellAt(date, meal); return c && c.state !== 'none'; }));
  for (const meal of shown) {
    const mh = el('div', 'mh');
    mh.append(svgEl(icon(meal)));
    mh.title = cap(meal);
    g.append(mh);
    for (const date of dates) {
      const c = cellAt(date, meal);
      if (!c) { g.append(el('div', 'slot out')); continue; }
      if (c.state === 'none') { g.append(el('div', 'slot')); continue; }
      cells.push([date, c]);
      g.append(cellNode(date, c));
    }
  }

  // Keep the inspected slot if it's still on this page; otherwise show the
  // latest sale, or the latest anything.
  const key = ([d, c]) => `${d}|${c.meal}`;
  if (!cells.some((x) => key(x) === pickedCell)) {
    const latest = cells.filter(([, c]) => c.sale != null).pop() || cells[cells.length - 1];
    pickedCell = latest ? key(latest) : '';
  }
  ledgerCells = new Map(cells.map((x) => [key(x), x]));
  pickCell(pickedCell);

  const first = days.has(dates[0]) ? dates[0] : D.days[0].date;
  const soldHere = cells.filter(([, c]) => c.state === 'sold' || c.state === 'sending');
  const earned = sum(soldHere.map(([, c]) => D.sales[c.sale].amount));
  const wasted = cells.filter(([, c]) => c.state === 'wasted');
  const lost = sum(wasted.map(([, c]) => c.ask || 0));
  const meta = $('ledMeta');
  const span = `${fmtDay(first)} - ${dates[6] === D.today ? 'today' : fmtDay(dates[6])}`;
  if (D.owner) {
    meta.replaceChildren(`${span} · `, el('b', '', String(soldHere.length)), ' sold · ', el('span', 'gold', rupees(earned)));
  } else {
    $('ledName').textContent = 'Meal ledger';
    meta.replaceChildren(span);
  }
  if (wasted.length) meta.append(' · ', el('span', 'red', `${wasted.length} wasted`), lost ? ` (${rupees(Math.round(lost))} at median)` : '');
  $('pgPrev').disabled = page >= pages() - 1;
  $('pgNext').disabled = page === 0;
}

let ledgerCells = new Map();
function pickCell(k) {
  pickedCell = k;
  for (const n of $('lgrid').querySelectorAll('.cell')) n.classList.toggle('active', n.dataset.k === k);
  const hit = ledgerCells.get(k);
  fillLines($('insp'), hit ? cellLines(...hit) : [[{ t: 'Nothing registered or sold this week.', c: 'c8' }]]);
}

function cellNode(date, c) {
  const n = el('div', `slot item cell is-${c.state}`);
  n.dataset.k = `${date}|${c.meal}`;
  n.style.setProperty('--m', messColor(c.mess));
  const s = c.sale != null ? D.sales[c.sale] : null;
  if (c.state === 'sold' || c.state === 'sending' || c.state === 'armed' || c.state === 'handoff') {
    if (c.diet) n.append(span('diet', dietMark(c.diet)));
    n.append(span('face', face(s.buyer)), span('messg', messIcon(c.mess)));
    const v = s.amount;
    const p = el('div', 'price' + (v >= 100 || !Number.isInteger(v) ? ' wide' : ''));
    p.append(el('span', 'r', '₹'), Number.isInteger(v) ? String(v) : v.toFixed(2));
    n.append(p);
    if (c.state === 'sending') n.append(el('span', 'ticked'));
    if (c.state === 'handoff') n.append(el('span', 'stamp long', 'HANDOFF'));
    if (c.state === 'armed') {
      n.append(el('div', 'glint'));
      const bar = el('div', 'bar'), i = el('i');
      i.style.setProperty('width', `${Math.max(0, Math.min(1, (c.end - now()) / (c.end - c.start))) * 100}%`);
      i.style.setProperty('background', messColor(c.mess));
      bar.append(i);
      n.append(bar);
    }
  } else if (c.state === 'selling') {
    const t = c.task;
    const p = el('div', 'price range');
    p.append(el('span', 'r', '₹'), t.min === t.max ? String(t.min) : `${t.min}-${t.max}`);
    if (c.diet) n.append(span('diet', dietMark(c.diet)));
    n.append(span('messg', messIcon(c.mess)), p, el('div', 'glint'), el('span', 'sell-tag', 'SELLING'));
  } else if (c.state === 'unknown') {
    n.append(el('div', 'ghost', '?'));
  } else {
    const g = el('div', 'ghost');
    g.append(svgEl(messIcon(c.mess)));
    n.append(g);
    if (c.state === 'ate') n.append(el('span', 'stamp', 'ATE'));
    if (c.state === 'wasted') n.append(el('span', 'stamp', 'WASTED'));
    if (c.sellable) n.append(el('span', 'sell-tag', 'SELL'));
  }
  if (canSell(c)) {
    n.classList.add('sellable');
    n.tabIndex = 0;
    n.setAttribute('role', 'button');
    n.setAttribute('aria-label', c.task ? `Manage the sale of ${c.meal}: mark sold, edit prices or stop` : `Sell ${c.meal}`);
  }
  return n;
}

function cellLines(date, c) {
  const s = c.sale != null ? D.sales[c.sale] : null;
  const diet = c.diet ? ` · ${DIET_LABEL[c.diet].toUpperCase()}` : '';
  const who = (x) => ({ t: x.name || fmtPhone(x.phone) || 'Unknown buyer', c: 'cf' });
  const lines = [
    [{ t: `${messName(c.mess)} ${c.meal}`, c: 'nm', color: messColor(c.mess) }],
    [{ t: `${fmtDay(date)}, ${hm(c.start)}-${hm(c.end)}`, c: 'c8' }],
    null,
  ];
  const askLine = (label) => (c.ask != null ? [{ t: label, c: 'c7' }, { t: rupees(Math.round(c.ask)), c: 'c6' }] : null);
  if (c.state === 'sold' || c.state === 'sending') {
    lines.push([{ t: 'Buyer ', c: 'c7' }, who(s)]);
    if (s.phone && s.name) lines.push([{ t: 'Phone ', c: 'c7' }, { t: fmtPhone(s.phone), c: 'c8' }]);
    lines.push([{ t: 'Paid ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, s.paid_at ? { t: ` at ${hm(s.paid_at)}`, c: 'c8' } : null]);
    if (s.paid_by === 'image') lines.push([{ t: 'By image, not verified. ', c: 'c6' }, { t: 'Check your UPI app.', c: 'c8' }]);
    if (['other', 'manual'].includes(s.paid_by)) lines.push([{ t: 'Paid outside WhatsApp, recorded by hand.', c: 'c8' }]);
    if (s.sent_at && s.paid_at) lines.push([{ t: 'Delivered ', c: 'c7' }, { t: `${dur(s.sent_at - s.paid_at)} after payment`, c: 'ca' }]);
    if (s.ask != null) lines.push([{ t: 'Median ask ', c: 'c7' }, { t: rupees(Math.round(s.ask)), c: 'cf' }, { t: ` (you ${signed(s.amount - s.ask)})`, c: 'c8' }]);
    if (c.state === 'sending') {
      lines.push(null, [{ t: 'Paid, but the QR send was interrupted. Check the chat before doing anything else.', c: 'c6' }]);
      lines.push({ rar: 'PAID · SEND UNCERTAIN', c: 'c6' });
    } else {
      lines.push({ rar: 'SOLD' + (s.paid_by === 'image' ? ' · UNVERIFIED' : '') + diet, c: 'ca' });
    }
  } else if (c.state === 'armed') {
    lines.push([{ t: 'Armed for ', c: 'c7' }, who(s)]);
    lines.push([{ t: 'Waiting for ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ' on WhatsApp Pay', c: 'c8' }]);
    if (s.offer_at) lines.push([{ t: 'Offer sent ', c: 'c7' }, { t: hm(s.offer_at), c: 'c8' }]);
    lines.push([{ t: 'Window closes ', c: 'c7' }, { t: hm(c.end), c: 'ce' }, { t: ` (in ${dur(c.end - now())})`, c: 'c8' }]);
    if (c.task) lines.push(null, [{ t: 'Click to mark it sold, edit the prices or stop selling.', c: 'lore' }]);
    lines.push({ rar: 'ARMED' + diet, c: 'ce' });
  } else if (c.state === 'handoff') {
    lines.push([{ t: 'Handed to you ', c: 'c7' }, { t: hm(s.armed_at), c: 'c8' }, { t: ' when ', c: 'c7' }, who(s), { t: ' replied', c: 'c7' }]);
    if (s.phone && s.name) lines.push([{ t: 'Phone ', c: 'c7' }, { t: fmtPhone(s.phone), c: 'c8' }]);
    lines.push([{ t: 'Offer asked ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }]);
    lines.push(null, [{ t: 'The bot sent this buyer nothing. Finish with deliver-qr -peer, or free the meal with release-handoff.', c: 'c8' }]);
    lines.push({ rar: 'HANDED OFF' + diet, c: 'c6' });
  } else if (c.state === 'ate') {
    lines.push([{ t: 'You ate this one.', c: 'c7' }], { rar: 'EATEN' + diet, c: 'c7' });
  } else if (c.state === 'wasted') {
    lines.push([{ t: 'Registered, never sold, never eaten.', c: 'cc' }]);
    if (s) lines.push([{ t: `Your offer of ${rupees(s.amount)} to ${s.name || fmtPhone(s.phone) || 'the buyer'} lapsed.`, c: 'c8' }]);
    if (c.ask != null) lines.push([{ t: `Would have fetched about ${rupees(Math.round(c.ask))} at the median ask.`, c: 'lore' }]);
    lines.push({ rar: 'WASTED' + diet, c: 'cc' });
  } else if (c.state === 'selling') {
    const t = c.task;
    lines.push([{ t: 'Selling now for ', c: 'c7' }, { t: t.min === t.max ? rupees(t.min) : `${rupees(t.min)}-${rupees(t.max)}`, c: 'c6' }]);
    const a = askLine('Median ask ');
    if (a) lines.push(a);
    lines.push([{ t: 'Window closes ', c: 'c7' }, { t: hm(c.end), c: 'ce' }, { t: ` (in ${dur(c.end - now())})`, c: 'c8' }]);
    lines.push(null, [{ t: 'Click to mark it sold, edit the prices or stop selling.', c: 'lore' }]);
    lines.push({ rar: 'SELLING' + diet, c: 'c6' });
  } else if (c.state === 'open') {
    lines.push([{ t: now() >= c.start ? 'Being served now. Still yours to sell.' : 'Registered. Not sold yet.', c: 'c7' }]);
    const a = askLine('Median ask ');
    if (a) lines.push(a);
    if (canSell(c)) lines.push(null, [{ t: 'Click to put it up for sale.', c: 'lore' }]);
    lines.push({ rar: 'REGISTERED' + diet, c: 'c8' });
  } else {
    lines.push([{ t: "The portal couldn't be read, so this could be eaten, wasted or not registered.", c: 'c8' }]);
    if (s) lines.push([{ t: `Your offer of ${rupees(s.amount)} lapsed.`, c: 'c8' }]);
    lines.push({ rar: 'UNKNOWN', c: 'c8' });
  }
  return lines;
}

$('lgrid').addEventListener('pointerover', (e) => {
  const n = e.target.closest('.cell');
  if (n && n.dataset.k !== pickedCell) pickCell(n.dataset.k);
});
const sellTarget = (e) => {
  const n = e.target.closest('.cell.sellable');
  return n && ledgerCells.get(n.dataset.k);
};
$('lgrid').addEventListener('click', (e) => {
  const hit = sellTarget(e);
  if (hit) openSell(...hit);
});
$('lgrid').addEventListener('keydown', (e) => {
  const hit = (e.key === 'Enter' || e.key === ' ') && sellTarget(e);
  if (hit) { e.preventDefault(); openSell(...hit); }
});

// ---------- selling from the ledger ----------

// canSell: the meal being served now or the next one, while it's still
// open, or any meal with a task running (to mark sold, reprice or stop it).
const canSell = (c) => D.owner && (c.task != null || (c.state === 'open' && c.sellable));

const modal = $('modal');
const sheet = $('sheet');
let sellOpen = '';   // "date|meal" the sell menu is for
function closeModal() { modal.hidden = true; sheet.replaceChildren(); sellOpen = ''; }
modal.addEventListener('click', (e) => { if (e.target === modal) closeModal(); });

// sellBounds is the slider's scale: wide enough for the median ask with
// room to haggle above it.
function sellBounds(c) {
  const top = Math.max(150, c.ask ? Math.ceil((c.ask * 2.5) / 50) * 50 : 0, c.task ? Math.ceil(c.task.max / 50) * 50 : 0);
  return { lo: 10, hi: top };
}

// sellDefaults opens a new sale a little under the median ask and asks a
// quarter above it, or the running task's range.
function sellDefaults(c, b) {
  if (c.task) return [c.task.min, c.task.max];
  if (c.ask == null) return [40, 80];
  const r5 = (v) => Math.max(b.lo, Math.min(b.hi, Math.round(v / 5) * 5));
  return [r5(c.ask * 0.85), r5(c.ask * 1.25)];
}

// Buyer states as the sell menu words them.
const BUYER_WORD = {
  queued: 'waiting', negotiating: 'haggling', agreed: 'agreed, waiting to pay', offered: 'asked to pay', passed: 'passed',
  timed_out: 'timed out', bought: 'bought it', skipped: 'skipped', told_sold: 'told sold', parked: 'needs you', handed_off: 'handed to you',
};
const priceText = (t) => (t.min === t.max ? rupees(t.min) : `${rupees(t.min)}-${rupees(t.max)}`);
const buyerName = (b) => b.name || fmtPhone(b.phone) || b.key;

// openSell opens the sell menu for a ledger cell. A meal with no task goes
// straight to the price range; a running task opens on its overview, with
// views to mark it sold or edit its prices.
function openSell(date, c, view = 'menu') {
  hideTip();
  sellOpen = `${date}|${c.meal}`;
  const t = c.task;
  const head = el('div', 'sell-head');
  const h = el('h2', '', `${t ? 'Selling' : 'Sell'} ${messName(c.mess)} ${c.meal}${c.friend != null ? ` for ${c.friend || 'a friend'}` : ''}`);
  h.id = 'sellTitle';
  h.style.setProperty('color', messColor(c.mess));
  const live = now() >= c.start;
  head.append(h, el('div', 'sub', `${fmtDay(date)}, ${hm(c.start)}-${hm(c.end)} · ${live ? `ends in ${dur(c.end - now())}` : `starts in ${dur(c.start - now())}`}`));
  sheet.setAttribute('aria-labelledby', 'sellTitle');
  sheet.replaceChildren(head);
  if (!t) priceView(date, c);
  else if (view === 'prices') priceView(date, c);
  else if (view === 'sold') soldView(date, c);
  else menuView(date, c);
  modal.hidden = false;
  const first = sheet.querySelector('[data-focus]') || sheet.querySelector('input, select, button');
  if (first) first.focus();
}

// sellParts are the pieces every view shares: the error line and a row of
// buttons, and run, which sends one action and closes on success.
function sellParts() {
  const error = el('p', 'sell-err');
  error.setAttribute('role', 'alert');
  if (D.sell_off) error.textContent = D.sell_off;
  const acts = el('div', 'sell-acts');
  const btn = (label, cls, fn) => {
    const x = el('button', 'sell-btn ' + cls, label);
    x.type = 'button';
    x.addEventListener('click', fn);
    if (D.sell_off && (cls === 'gold' || cls === 'red')) x.disabled = true;
    return x;
  };
  const run = async (body, done) => {
    const btns = [...sheet.querySelectorAll('button')];
    for (const k of btns) k.disabled = true;
    error.textContent = '';
    try {
      await send('POST', 'api/sell-action', body);
      closeModal();
      toast(done);
      load();
    } catch (err) {
      error.textContent = err.message;
      for (const k of btns) k.disabled = false;
    }
  };
  return { error, acts, btn, run };
}

function menuView(date, c) {
  const t = c.task;
  const { error, acts, btn, run } = sellParts();
  const sum = el('div', 'sell-sum');
  const stat = (k, v, cls) => { const d = el('div', 'st'); d.append(el('span', '', k), el('b', cls || '', v)); return d; };
  const talking = t.buyers.filter((b) => ['queued', 'negotiating', 'agreed', 'offered', 'parked'].includes(b.state));
  sum.append(stat('PRICE', priceText(t), 'gold'), stat('BUYERS', `${talking.length} active`), stat('ENDS', hm(c.end)));

  const list = el('ul', 'sell-buyers');
  list.setAttribute('aria-label', 'Buyers');
  if (!t.buyers.length) list.append(el('li', 'none', 'No replies yet.'));
  for (const b of t.buyers) {
    const li = el('li');
    li.append(el('span', 'who', buyerName(b)), el('span', 'what', (b.muted ? 'muted · ' : '') + (BUYER_WORD[b.state] || b.state)),
      el('span', 'amt', b.price ? rupees(b.price) : ''));
    list.append(li);
  }

  const tools = el('div', 'sell-tools');
  const tool = (glyph, label, hint, cls, fn) => {
    const x = btn('', 'sell-tool ' + cls, fn);
    x.append(el('b', '', glyph), el('span', '', label), el('small', '', hint));
    return x;
  };
  const sold = tool('✓', 'MARK SOLD', 'You sold it yourself', 'grn', () => openSell(date, c, 'sold'));
  sold.dataset.focus = '1';
  const stop = tool('✕', 'STOP SELLING', 'Deletes the offer posts', 'red', () => {
    if (stop.dataset.sure) return run({ action: 'cancel', id: t.id }, 'Stopped. The offer posts are being deleted.');
    stop.dataset.sure = '1';
    stop.querySelector('span').textContent = 'SURE? STOP';
    stop.querySelector('small').textContent = 'Click again to stop';
  });
  tools.append(sold, tool('₹', 'EDIT PRICES', priceText(t), 'gold-o', () => openSell(date, c, 'prices')), stop);
  // Marking sold and editing prices need sell tasks on, like stopping.
  if (D.sell_off) for (const x of tools.children) x.disabled = true;

  acts.append(btn('CLOSE', '', closeModal));
  sheet.append(sum, list, tools, error, acts);
}

// priceSlider is the two-handle price range for a cell-like c (ask,
// task). It returns its nodes and a getter for [lowest, opening].
function priceSlider(c) {
  const t = c.task;
  const b = sellBounds(c);
  let [lo, hi] = sellDefaults(c, b);
  // A running task can't switch between haggling and a fixed price, so a
  // fixed one moves both handles together and a haggling one keeps them
  // at least a rupee apart.
  const fixed = t && t.min === t.max;
  const gap = t && !fixed ? 1 : 0;

  const vals = el('div', 'sell-vals');
  const vMin = el('b'), vMax = el('b');
  const lMin = el('div', 'sv'), lMax = el('div', 'sv');
  lMin.append(el('span', '', fixed ? 'PRICE' : 'LOWEST'), vMin);
  lMax.append(el('span', '', 'OPENS AT'), vMax);
  vals.append(lMin);
  if (!fixed) vals.append(lMax);

  const dual = el('div', 'dual');
  const fill = el('i', 'fill');
  const track = el('div', 'track');
  track.append(fill);
  if (c.ask != null) {
    const m = el('i', 'med');
    m.style.setProperty('left', `${((c.ask - b.lo) / (b.hi - b.lo)) * 100}%`);
    track.append(m);
  }
  const range = (v, label) => {
    const r = el('input');
    Object.assign(r, { type: 'range', min: b.lo, max: b.hi, step: 1, value: v });
    r.setAttribute('aria-label', label);
    return r;
  };
  const rMin = range(lo, fixed ? 'Price' : 'Lowest price'), rMax = range(hi, 'Opening price');
  rMin.dataset.focus = '1';
  dual.append(track, rMin);
  if (!fixed) dual.append(rMax);

  const axis = el('div', 'sell-axis');
  axis.append(el('span', '', rupees(b.lo)), c.ask != null ? el('span', 'med-l', `median ${rupees(Math.round(c.ask))}`) : el('span'), el('span', '', rupees(b.hi)));

  const note = el('p', 'sell-note');
  note.setAttribute('aria-live', 'polite');
  const draw = () => {
    vMin.textContent = rupees(lo);
    vMax.textContent = rupees(hi);
    rMin.setAttribute('aria-valuetext', rupees(lo));
    rMax.setAttribute('aria-valuetext', rupees(hi));
    fill.style.setProperty('left', `${((lo - b.lo) / (b.hi - b.lo)) * 100}%`);
    fill.style.setProperty('right', `${100 - ((hi - b.lo) / (b.hi - b.lo)) * 100}%`);
    note.textContent = lo === hi
      ? `Posts the meal at ${rupees(lo)} and sells to the first buyer who pays it.`
      : `Opens at ${rupees(hi)} and haggles down toward the median, reaching ${rupees(lo)} only as the meal nears its end. Never goes below ${rupees(lo)}.`;
    if (t) note.textContent += ' Prices already quoted or agreed stand.';
  };
  // The thumb that last moved sits on top, so two thumbs at the same spot
  // can still be pulled apart.
  rMin.addEventListener('input', () => {
    if (fixed) hi = lo = +rMin.value;
    else { lo = Math.min(+rMin.value, hi - gap); rMin.value = lo; }
    rMin.style.zIndex = 3; rMax.style.zIndex = 2; draw();
  });
  rMax.addEventListener('input', () => { hi = Math.max(+rMax.value, lo + gap); rMax.value = hi; rMax.style.zIndex = 3; rMin.style.zIndex = 2; draw(); });
  draw();
  return { nodes: [vals, dual, axis, note], get: () => [lo, hi] };
}

function priceView(date, c) {
  const t = c.task;
  const slider = priceSlider(c);
  const { error, acts, btn, run } = sellParts();
  if (t) {
    acts.append(btn('BACK', '', () => openSell(date, c)),
      btn('SAVE PRICES', 'gold', () => { const [min, max] = slider.get(); run({ action: 'prices', id: t.id, min, max }, 'Prices updated'); }));
  } else {
    acts.append(btn('CANCEL', '', closeModal),
      btn('CONFIRM SALE', 'gold', () => { const [min, max] = slider.get(); run({ action: 'start', date, meal: c.meal, min, max }, `Selling your ${c.meal}`); }));
  }
  sheet.append(...slider.nodes, error, acts);
}

function soldView(date, c) {
  const t = c.task;
  const { error, acts, btn, run } = sellParts();
  const form = el('form', 'sell-form');
  const field = (label, input) => { const l = el('label', '', label); l.append(input); form.append(l); return l; };
  const input = (type, attrs = {}) => { const i = el('input'); Object.assign(i, { type, ...attrs }); return i; };

  const pick = el('select');
  pick.dataset.focus = '1';
  for (const b of t.buyers) pick.append(new Option(`${buyerName(b)}${b.price ? ` · ${rupees(b.price)}` : ''}`, b.key));
  pick.append(new Option('Someone else', ''));
  // Default to the buyer furthest along.
  const order = ['bought', 'offered', 'agreed', 'handed_off', 'parked', 'negotiating', 'queued'];
  const rank = (x) => { const i = order.indexOf(x.state); return i < 0 ? order.length : i; };
  const best = t.buyers.slice().sort((x, y) => rank(x) - rank(y))[0];
  pick.value = best ? best.key : '';
  field('Buyer', pick);
  const phone = input('tel', { placeholder: '+91 98765 43210', autocomplete: 'off' });
  const name = input('text', { autocomplete: 'off' });
  const other = [field('Their phone, with country code', phone), field('Or their name', name)];
  const price = input('number', { min: 1, max: 10000, step: 1, required: true });
  field('Sold for (₹)', price);
  const note = input('text', { placeholder: 'e.g. paid cash', autocomplete: 'off' });
  field('Note (optional)', note);

  const sync = () => {
    const b = t.buyers.find((x) => x.key === pick.value);
    for (const l of other) l.hidden = !!b;
    price.value = b && b.price ? Math.round(b.price) : price.value || Math.round(t.max);
  };
  pick.addEventListener('change', sync);
  sync();

  form.append(el('p', 'sell-note', 'Only confirm once the meal is sold and paid for. The bot stops selling, records the sale, ticks the offer posts and tells other buyers it\'s sold. It sends your buyer nothing.'));
  const submit = btn('CONFIRM SOLD', 'gold', () => {});
  submit.type = 'submit';
  acts.append(btn('BACK', '', () => openSell(date, c)), submit);
  form.append(error, acts);
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const body = { action: 'sold', id: t.id, price: Number(price.value), note: note.value.trim() };
    if (pick.value) body.buyer = pick.value;
    else { body.buyer = phone.value.replace(/[^\d+]/g, ''); body.name = name.value.trim(); }
    if (!body.buyer && !body.name) { error.textContent = 'Enter their phone number or name.'; return; }
    if (!(body.price >= 1)) { error.textContent = 'Enter the price it sold for.'; return; }
    run(body, 'Marked sold');
  });
  sheet.append(form);
}

// ---------- friends' meals ----------

// Friends' meals are sold through your WhatsApp from their QR. They're
// listed here and nowhere else on the page, so they never count as yours.

const FRIEND_LOOK = {
  selling: ['SELLING', 'c6'], armed: ['ARMED', 'ce'], sending: ['PAID · SENDING', 'c6'], sold: ['SOLD', 'ca'],
  handoff: ['HANDED OFF', 'c6'], ended: ['ENDED UNSOLD', 'c8'],
};
const friendCell = (f) => ({ meal: f.meal, mess: f.mess, diet: f.diet, start: f.start, end: f.end, ask: f.ask, task: f.task, friend: f.friend || '' });

function renderFriends() {
  const box = $('friends');
  box.replaceChildren();
  const fs = D.friends.slice().reverse();
  const live = fs.filter((f) => f.task).length, sold = fs.filter((f) => f.state === 'sold');
  $('frMeta').replaceChildren(el('b', '', String(live)), ' selling · ', el('b', '', String(sold.length)),
    ` sold for ${rupees(sum(sold.map((f) => f.amount)))} · not in your totals`);
  if (!fs.length) {
    box.append(el('div', 'empty-note', 'Selling a meal for a friend? Add it with their QR. It posts and haggles like yours, and its money stays out of your numbers.'));
    return;
  }
  for (const f of fs) {
    const row = el(f.task ? 'button' : 'div', 'friend' + (f.task ? ' live' : ''));
    row.style.setProperty('--m', messColor(f.mess));
    if (f.task) {
      row.type = 'button';
      row.setAttribute('aria-label', `Manage the sale of ${f.friend || 'a friend'}'s ${f.meal}: mark sold, edit prices or stop`);
      row.addEventListener('click', () => openSell(f.date, friendCell(f)));
    }
    const ic = el('span', 'fr-ic');
    ic.append(svgEl(messIcon(f.mess)));
    const what = el('div', 'fr-what');
    const nm = el('b', '', `${messName(f.mess)}${f.diet ? ` ${DIET_LABEL[f.diet].toLowerCase()}` : ''} ${f.meal}`);
    nm.style.setProperty('color', messColor(f.mess));
    what.append(nm, el('span', '', `${f.friend ? `for ${f.friend} · ` : ''}${dayWord(f.date)}${f.start ? `, ${hm(f.start)}-${hm(f.end)}` : ''}${f.extras && f.extras.length ? ` · + ${f.extras.join(', ')}` : ''}`));
    const [word, cls] = FRIEND_LOOK[f.state] || [f.state.toUpperCase(), 'c8'];
    const right = el('div', 'fr-right');
    const price = f.task ? priceText(f.task) : f.amount ? rupees(f.amount) : '';
    right.append(el('span', 'pill ' + cls, word), el('span', 'fr-amt', price));
    if (f.buyer && (f.state === 'sold' || f.state === 'armed' || f.state === 'sending' || f.state === 'handoff')) right.append(el('span', 'fr-buyer', fmtPhone(f.buyer)));
    row.append(ic, what, right);
    box.append(row);
  }
}

function openFriendForm() {
  hideTip();
  sellOpen = 'friend';
  const head = el('div', 'sell-head');
  const h = el('h2', '', "Sell a friend's meal");
  h.id = 'sellTitle';
  head.append(h, el('div', 'sub', 'Posted and haggled like yours; the buyer gets their QR. The money stays out of your totals.'));
  sheet.setAttribute('aria-labelledby', 'sellTitle');

  const form = el('form', 'sell-form');
  const field = (label, node) => { const l = el('label', '', label); l.append(node); form.append(l); return l; };
  const friend = el('input');
  Object.assign(friend, { type: 'text', maxLength: 60, autocomplete: 'off', placeholder: 'e.g. Ravi' });
  field("Friend's name (optional)", friend);

  const slots = D.friend_slots;
  const meal = el('select');
  slots.forEach((w, i) => meal.append(new Option(`${w.date === D.today ? 'Today' : fmtDay(w.date)} ${w.meal} · ${hm(w.start)}-${hm(w.end)}${now() >= w.start ? ' · serving now' : ''}`, String(i))));
  field('Meal', meal);

  // Radios styled as chips, so arrow keys and screen readers work.
  const chips = (legend, name, opts) => {
    const fs = el('fieldset', 'chips');
    fs.append(el('legend', '', legend));
    for (const [value, label, color] of opts) {
      const l = el('label', 'chip'), r = el('input');
      Object.assign(r, { type: 'radio', name, value });
      if (color) l.style.setProperty('--m', color);
      l.append(r, el('span', '', label));
      fs.append(l);
    }
    form.append(fs);
    return fs;
  };
  const messes = chips('Mess', 'fmess', D.messes.map((m) => [m.name, messName(m.name), messColor(m.name)]));
  const diets = chips('Diet', 'fdiet', [['veg', 'Veg'], ['egg', 'Egg'], ['nonveg', 'Non-veg']]);
  const picked = (fs) => (fs.querySelector('input:checked') || {}).value || '';

  const priceBox = el('div', 'fr-price');
  form.append(priceBox);
  let slider;
  const drawPrice = () => {
    const w = slots[+meal.value];
    const m = w && marketFor(w.meal, picked(diets));
    slider = priceSlider({ ask: m ? m.median : null });
    priceBox.replaceChildren(el('span', 'fr-k', 'Price'), ...slider.nodes);
  };
  meal.addEventListener('change', drawPrice);
  diets.addEventListener('change', drawPrice);
  drawPrice();

  const extras = el('input');
  Object.assign(extras, { type: 'text', autocomplete: 'off', placeholder: 'e.g. 2 omelette, lassi' });
  field('Extras they registered (optional, comma separated)', extras);

  // The QR: a file picker that also takes a dropped image.
  const file = el('input');
  Object.assign(file, { type: 'file', accept: 'image/png,image/jpeg' });
  const drop = el('label', 'qr-drop');
  const preview = el('img');
  preview.alt = '';
  preview.hidden = true;
  const dropText = el('span', '', 'Attach their meal QR (PNG or JPEG, up to 5 MB). Click or drop the image here.');
  drop.append(file, preview, dropText);
  form.append(drop);
  let qr = null;
  const takeFile = (f) => {
    if (!f) return;
    if (!/^image\/(png|jpeg)$/.test(f.type)) { error.textContent = 'The QR must be a PNG or JPEG image.'; return; }
    if (f.size > 5 * 1024 * 1024) { error.textContent = 'The QR image is over 5 MB.'; return; }
    error.textContent = '';
    qr = f;
    dropText.textContent = `${f.name} · ${Math.ceil(f.size / 1024)} KB. Click to change.`;
    // The page's CSP allows data: images, not blob: ones.
    const rd = new FileReader();
    rd.onload = () => { preview.src = rd.result; preview.hidden = false; preview.alt = "Your friend's QR"; };
    rd.readAsDataURL(f);
  };
  file.addEventListener('change', () => takeFile(file.files[0]));
  drop.addEventListener('dragover', (e) => { e.preventDefault(); drop.classList.add('over'); });
  drop.addEventListener('dragleave', () => drop.classList.remove('over'));
  drop.addEventListener('drop', (e) => { e.preventDefault(); drop.classList.remove('over'); takeFile(e.dataTransfer.files[0]); });

  const error = el('p', 'sell-err');
  error.setAttribute('role', 'alert');
  if (D.sell_off) error.textContent = D.sell_off;
  if (!slots.length) error.textContent = 'No meal left to sell today.';

  // A veg-only mess has no non-veg to sell.
  const syncDiet = () => {
    const m = D.messes.find((x) => x.name === picked(messes));
    const vegOnly = !!(m && m.veg_only);
    for (const r of diets.querySelectorAll('input:not([value="veg"])')) r.disabled = vegOnly;
    if (vegOnly && !diets.querySelector('input[value="veg"]').checked) {
      diets.querySelector('input[value="veg"]').checked = true;
      drawPrice();
    }
  };
  messes.addEventListener('change', syncDiet);

  const acts = el('div', 'sell-acts');
  const cancel = el('button', 'sell-btn', 'CANCEL');
  cancel.type = 'button';
  cancel.addEventListener('click', closeModal);
  const submit = el('button', 'sell-btn gold', 'CONFIRM SALE');
  submit.type = 'submit';
  submit.disabled = !!D.sell_off || !slots.length;
  acts.append(cancel, submit);
  form.append(error, acts);

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const w = slots[+meal.value];
    const mess = picked(messes), diet = picked(diets);
    if (!mess) { error.textContent = 'Pick their mess.'; messes.querySelector('input').focus(); return; }
    if (!diet) { error.textContent = 'Pick veg, egg or non-veg.'; diets.querySelector('input').focus(); return; }
    if (!qr) { error.textContent = "Attach your friend's QR."; file.focus(); return; }
    const [min, max] = slider.get();
    const body = new FormData();
    for (const [k, v] of Object.entries({ date: w.date, meal: w.meal, mess, diet, friend: friend.value.trim(), extras: extras.value, min, max })) body.append(k, String(v));
    body.append('qr', qr);
    submit.disabled = cancel.disabled = true;
    error.textContent = '';
    try {
      const m = await me();
      const r = await fetch('api/friend-sale', { method: 'POST', cache: 'no-store', headers: { 'X-CSRF-Token': m.csrf }, body });
      if (r.status === 401) return toLogin();
      const data = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(data.error || `HTTP ${r.status}`);
      closeModal();
      toast(`Selling ${friend.value.trim() ? `${friend.value.trim()}'s` : "your friend's"} ${w.meal}`);
      load();
    } catch (err) {
      error.textContent = err.message;
      submit.disabled = cancel.disabled = false;
    }
  });

  sheet.replaceChildren(head, form);
  modal.hidden = false;
  friend.focus();
}

// toast is a short note at the bottom of the page.
function toast(text) {
  const t = el('div', 'toast', text);
  document.body.append(t);
  setTimeout(() => t.remove(), 2600);
}

// ---------- regulars ----------

// Loyalty tiers, as item rarities: meals bought from you, all time.
const TIERS = [
  { min: 10, name: 'LEGENDARY', color: '#ffaa1f' },
  { min: 7, name: 'EPIC', color: '#c86bff' },
  { min: 4, name: 'RARE', color: '#6b8cff' },
  { min: 2, name: 'UNCOMMON', color: '#5be37a' },
  { min: 1, name: 'COMMON', color: '#f4f5f8' },
];
const tierOf = (n) => TIERS.find((t) => n >= t.min);

function buyers() {
  const by = new Map();
  for (const s of D.sales) {
    if (!isSold(s)) continue;
    let b = by.get(s.buyer);
    if (!b) by.set(s.buyer, (b = { key: s.buyer, phone: s.phone, name: '', sales: [] }));
    b.sales.push(s);
    if (s.name) b.name = s.name; // oldest first, so the latest name wins
  }
  return [...by.values()]
    .map((b) => ({ ...b, n: b.sales.length, spent: sum(b.sales.map((s) => s.amount)) }))
    .sort((a, b) => b.n - a.n || b.spent - a.spent);
}

function renderRegulars() {
  const bs = buyers();
  const heads = $('heads');
  heads.replaceChildren();
  $('regMeta').replaceChildren(el('b', '', String(bs.length)), ` buyer${bs.length === 1 ? '' : 's'} · `,
    el('b', '', String(bs.filter((b) => b.n > 1).length)), ' came back · all time');
  const body = heads.nextElementSibling;
  body.hidden = !bs.length;
  if (!bs.length) {
    heads.append(el('div', 'empty-note', 'No sales yet. Meals you sell with deliver-qr show up here.'));
    return;
  }
  if (!bs.some((b) => b.key === pickedBuyer)) pickedBuyer = bs[0].key;
  for (const b of bs.slice(0, 40)) {
    const t = tierOf(b.n);
    const n = el('div', 'slot item' + (t.min > 1 ? ' tiered' : '') + (b.key === pickedBuyer ? ' active' : ''));
    n.style.setProperty('--m', t.color);
    n.append(svgEl(face(b.key)), el('span', 'pip'));
    if (b.n > 1) n.append(el('span', 'qty', String(b.n)));
    n.addEventListener('pointerenter', () => {
      if (pickedBuyer === b.key) return;
      pickedBuyer = b.key;
      for (const x of heads.children) x.classList.toggle('active', x === n);
      fillLines($('rinsp'), buyerLines(b));
    });
    heads.append(n);
  }
  fillLines($('rinsp'), buyerLines(bs.find((b) => b.key === pickedBuyer)));

  const tiers = $('tiers');
  tiers.replaceChildren();
  TIERS.slice().reverse().forEach((t, i, all) => {
    const next = all[i + 1];
    const range = !next ? `${t.min}+` : next.min - 1 === t.min ? String(t.min) : `${t.min}-${next.min - 1}`;
    const row = el('div', 'tier', t.name);
    row.style.setProperty('color', t.color);
    row.append(el('span', '', `${range} meal${range === '1' ? '' : 's'}`));
    tiers.append(row);
  });
  tiers.append(el('p', '', 'The number on a head is meals bought from you.'));
}

function buyerLines(b) {
  const t = tierOf(b.n);
  const usual = new Map();
  for (const s of b.sales) {
    const k = `${messName(s.mess)} ${s.meal}`;
    usual.set(k, (usual.get(k) || 0) + 1);
  }
  const fav = [...usual.entries()].sort((x, y) => y[1] - x[1])[0];
  const waits = b.sales.filter((s) => s.offer_at && s.paid_at && s.paid_at >= s.offer_at).map((s) => s.paid_at - s.offer_at);
  const first = b.sales[0].date, last = b.sales[b.sales.length - 1].date;
  return [
    [{ t: b.name || fmtPhone(b.phone) || 'Unknown buyer', c: 'nm', color: t.color }],
    b.name && b.phone ? [{ t: fmtPhone(b.phone), c: 'c8' }] : null,
    null,
    [{ t: 'Meals bought ', c: 'c7' }, { t: String(b.n), c: 'cf' }],
    [{ t: 'Spent ', c: 'c7' }, { t: rupees(b.spent), c: 'c6' }, { t: ` · avg ${rupees(Math.round(b.spent / b.n))}`, c: 'c8' }],
    [{ t: 'Usual ', c: 'c7' }, { t: fav[0], c: 'cf' }],
    [{ t: 'First ', c: 'c7' }, { t: dayWord(first), c: 'c8' }, { t: '   Last ', c: 'c7' }, { t: dayWord(last), c: 'c8' }],
    waits.length ? [{ t: `Pays about ${dur(median(waits))} after your offer.`, c: 'lore' }] : null,
    { rar: `${t.name} BUYER`, color: t.color },
  ];
}

// ---------- price check ----------

// pricesNarrow is whether the price check is too narrow for its desktop
// layout. A narrow chart puts each row's label and verdict on one line with
// the bar under it at full width, drawn at the box's own pixel width.
let pricesNarrow = false;
const pricesTooNarrow = () => $('pplot').clientWidth < 620;

function renderPrices() {
  const box = $('pplot');
  box.replaceChildren();
  pricesNarrow = pricesTooNarrow();
  const narrow = pricesNarrow;
  const mine = recentSales();
  // A row per meal and diet you sold in: the market's asks in that diet
  // against your sales in it.
  const rows = [];
  for (const meal of meals()) {
    for (const diet of ['veg', 'egg', 'nonveg']) {
      const m = D.market.find((x) => x.meal === meal && x.diet === diet);
      const ms = mine.filter((s) => s.meal === meal && priceDiet(s.diet) === diet);
      if (ms.length) rows.push({ meal, diet, m, ms });
    }
  }
  if (!rows.length) {
    box.append(el('div', 'empty-note', 'No sales in the last 4 weeks.'));
    return;
  }
  const vals = [];
  for (const r of rows) {
    if (r.m) vals.push(r.m.q1, r.m.q3);
    for (const s of r.ms) vals.push(s.amount);
  }
  let lo = Math.floor(Math.min(...vals) / 10) * 10, hi = Math.ceil(Math.max(...vals) / 10) * 10;
  if (hi - lo < 20) { lo = Math.max(0, lo - 10); hi = lo + 30; }
  const W = narrow ? Math.max(260, box.clientWidth) : 860;
  const x0 = narrow ? 14 : 136, x1 = narrow ? W - 14 : W - 168;
  const top = narrow ? 8 : 22, rowH = narrow ? 64 : 40, H = top + rows.length * rowH + (narrow ? 10 : 16);
  const X = (v) => x0 + ((v - lo) / (hi - lo)) * (x1 - x0);
  const step = [5, 10, 20, 25, 50, 100].find((s) => (hi - lo) / s <= (narrow ? 4 : 6)) || 100;
  const ticks = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) ticks.push(v);
  let svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${H}" role="img" aria-label="Your sale prices against the market's asking prices, by meal and diet">`;
  for (const v of ticks) {
    if (!narrow) svg += `<line x1="${X(v)}" x2="${X(v)}" y1="4" y2="${H - 22}" stroke="#252c3a" stroke-dasharray="2 4"/>`;
    svg += `<text x="${X(v)}" y="${H - 6}" fill="#586076" font-size="10.5" text-anchor="middle">₹${v}</text>`;
  }
  const verdicts = [];
  rows.forEach(({ meal, diet, m, ms }, i) => {
    // ly is the label line, y the bar's middle. They share a line on desktop.
    const ly = top + i * rowH + (narrow ? 8 : 6), y = narrow ? ly + 28 : ly;
    svg += icon(meal).replace('<svg ', `<svg x="2" y="${ly - 6}" width="12" height="12" `)
      + dietMark(diet).replace('<svg ', `<svg x="18" y="${ly - 6}" width="12" height="12" `)
      + `<text class="px" x="36" y="${ly + 4}" fill="#8b94a7">${meal.toUpperCase()}</text>`;
    if (narrow) {
      for (const v of ticks) svg += `<line x1="${X(v)}" x2="${X(v)}" y1="${y - 14}" y2="${y + 14}" stroke="#252c3a" stroke-dasharray="2 4"/>`;
    }
    if (m) {
      svg += `<rect x="${X(m.q1)}" y="${y - 9}" width="${Math.max(2, X(m.q3) - X(m.q1))}" height="18" rx="4" fill="rgba(232,236,243,.14)"/>`
        + `<line x1="${X(m.median)}" x2="${X(m.median)}" y1="${y - 12}" y2="${y + 12}" stroke="#e8ecf3" stroke-opacity=".8" stroke-width="2" stroke-linecap="round"/>`;
    }
    ms.forEach((s, k) => {
      const cx = X(s.amount) + ((k % 3) - 1) * 2, cy = y + (((k * 37) % 13) - 6);
      svg += `<g class="dot" data-i="${D.sales.indexOf(s)}"><circle cx="${cx}" cy="${cy}" r="10" fill="transparent"/><circle cx="${cx}" cy="${cy}" r="4.5" fill="#ffc53d" stroke="#141923" stroke-width="2"/></g>`;
    });
    const diffs = ms.filter((s) => s.ask != null).map((s) => s.amount - s.ask);
    const d = diffs.length ? sum(diffs) / diffs.length : null;
    const word = d == null ? 'NO ASKS' : d <= -3 ? 'UNDERCUT' : d >= 3 ? 'PREMIUM' : 'AT MARKET';
    const hot = word === 'UNDERCUT';
    const pw = word.length * 7 + 14;
    const px = narrow ? W - (d == null ? 0 : 42) - pw : W - 150;
    svg += `<g transform="translate(${px} ${ly - 9})"><rect width="${pw}" height="18" rx="9" fill="none" stroke="${hot ? 'rgba(255,197,61,.55)' : '#252c3a'}"/>`
      + `<text class="px pill" x="${pw / 2}" y="12.5" text-anchor="middle" font-size="8" fill="${hot ? '#ffc53d' : d == null ? '#586076' : '#8b94a7'}">${word}</text></g>`;
    if (d != null) svg += `<text x="${W}" y="${ly + 4}" text-anchor="end" fill="#8b94a7" font-size="11">${signed(d)}</text>`;
    verdicts.push({ meal, diet, d, word, m, n: ms.length });
  });
  svg += '</svg>';
  box.append(svgEl(svg));
  for (const g of box.querySelectorAll('.dot')) {
    const s = D.sales[+g.dataset.i];
    tip(g, () => [
      [{ t: `${messName(s.mess)} ${DIET_LABEL[priceDiet(s.diet)].toLowerCase()} ${s.meal}`, c: 'nm', color: messColor(s.mess) }],
      [{ t: `${fmtDay(s.date)} · `, c: 'c8' }, { t: s.name || fmtPhone(s.phone) || 'Unknown buyer', c: 'c8' }],
      null,
      [{ t: 'You sold at ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }],
      s.ask != null ? [{ t: 'Median ask that day ', c: 'c7' }, { t: rupees(Math.round(s.ask)), c: 'cf' }] : null,
      s.ask == null ? null : s.amount < s.ask ? [{ t: `Undercut by ${rupees(Math.round(s.ask - s.amount))}`, c: 'ce' }]
        : s.amount > s.ask ? [{ t: `${rupees(Math.round(s.amount - s.ask))} above the market`, c: 'ca' }] : [{ t: 'Right on the median', c: 'ca' }],
    ]);
  }
}

// ---------- earnings per mess ----------

function renderEarnings() {
  const box = $('cards');
  box.replaceChildren();
  const mine = recentSales();
  const days = [];
  for (let d = D.recent; d <= D.today; d = addDays(d, 1)) days.push(d);
  // A card per mess and diet you sold something in.
  const names = D.messes.map((m) => m.name);
  const messOf = (s) => (names.includes(s.mess) ? s.mess : '');
  const keyOf = (mess, diet) => `${mess}|${diet}`;
  const cards = [];
  for (const m of [...D.messes, { name: '' }]) {
    const sold = mine.filter((s) => messOf(s) === m.name);
    for (const diet of ['veg', 'nonveg', 'egg']) {
      if (sold.some((s) => priceDiet(s.diet) === diet)) cards.push({ mess: m.name, diet });
    }
  }
  if (!cards.length) {
    box.append(el('div', 'empty-note', 'No sales in the last 4 weeks.'));
    return;
  }
  const per = new Map(cards.map((k) => [keyOf(k.mess, k.diet), days.map(() => 0)]));
  for (const s of mine) per.get(keyOf(messOf(s), priceDiet(s.diet)))[days.indexOf(s.date)] += s.amount;
  const mx = Math.max(1, ...[...per.values()].flat());
  const totals = cards.map((k) => sum(per.get(keyOf(k.mess, k.diet))));
  const best = Math.max(...totals);
  const top = totals.filter((t) => t === best).length === 1 && best > 0 ? totals.indexOf(best) : -1;
  const week = weekDates();
  const w0 = days.indexOf(week[0]), w1 = days.indexOf(week[6]);

  cards.forEach(({ mess: m, diet }, ci) => {
    const list = mine.filter((s) => messOf(s) === m && priceDiet(s.diet) === diet);
    const c = el('div', 'card');
    c.style.setProperty('--m', messColor(m));
    const head = el('div', 'card-head');
    head.append(svgEl(messIcon(m)));
    const nm = el('span', 'card-name', messName(m));
    nm.style.setProperty('color', messColor(m));
    const dt = span('card-diet', dietMark(diet));
    dt.append(DIET_LABEL[diet]);
    nm.append(dt);
    head.append(nm);
    if (ci === top) head.append(el('span', 'verdict hot', 'TOP EARNER'));
    const kpis = el('div', 'kpis');
    const kpi = (v, label) => { const k = el('div', 'kpi'); k.append(el('b', v == null ? 'none' : '', v == null ? '-' : v), el('span', '', label)); return k; };
    const med = median(list.map((s) => s.amount));
    kpis.append(kpi(String(list.length), 'MEALS SOLD'), kpi(rupees(totals[ci]), 'EARNED'), kpi(med == null ? null : rupees(Math.round(med)), 'MEDIAN PRICE'));
    const spark = el('div', 'spark');
    spark.style.setProperty('--n', days.length);
    if (w1 >= 0 || w0 >= 0) {
      const band = el('div', 'band');
      const a = Math.max(0, w0 < 0 ? 0 : w0), b = w1 < 0 ? days.length - 1 : w1;
      band.style.setProperty('left', `${(a / days.length) * 100}%`);
      band.style.setProperty('width', `${((b - a + 1) / days.length) * 100}%`);
      band.style.setProperty('right', 'auto');
      spark.append(band);
    }
    per.get(keyOf(m, diet)).forEach((v, i) => {
      const bk = el('div', 'bk');
      if (v) {
        const bar = el('i');
        bar.style.setProperty('height', `${Math.max(6, (v / mx) * 100)}%`);
        bk.append(bar);
      }
      const ds = list.filter((s) => s.date === days[i]);
      tip(bk, () => [
        [{ t: `${messName(m)} ${DIET_LABEL[diet].toLowerCase()}, ${fmtDay(days[i])}`, c: 'nm', color: messColor(m) }],
        ...(ds.length ? ds.map((s) => [{ t: `${cap(s.meal)} `, c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ` · ${s.name || fmtPhone(s.phone) || 'Unknown buyer'}`, c: 'c8' }]) : [[{ t: 'No sales', c: 'c8' }]]),
        ds.length > 1 ? [{ t: 'Total ', c: 'c7' }, { t: rupees(v), c: 'cf' }] : null,
      ].filter((x) => x !== null));
      spark.append(bk);
    });
    const axis = el('div', 'axis');
    axis.append(el('span', '', fmtShort(days[0])), el('span', '', fmtShort(days[Math.floor(days.length / 2)])), el('span', '', 'today'));
    c.append(head, kpis, spark, axis);
    box.append(c);
  });
}

// ---------- boot ----------

$('logo').append(svgEl(icon('ingot')));
$('navIcon').append(svgEl(icon('prev')));
$('insIcon').append(svgEl(icon('chart')));
$('useIcon').append(svgEl(icon('check')));
me().then((m) => {
  userLink(m);
  $('insLink').hidden = m.role !== 'admin';
  $('useLink').hidden = m.role !== 'admin';
}).catch(() => {});
$('ledIcon').append(svgEl(icon('coin')));
$('frIcon').append(svgEl(icon('coin')));
$('frAdd').addEventListener('click', () => { if (D) openFriendForm(); });
$('regIcon').append(svgEl(icon('paper')));
$('pcIcon').append(svgEl(icon('chart')));
$('cardIcon').append(svgEl(icon('chart')));
tip($('xpBar'), () => (D ? savedTip() : []));
// The phone layout draws at the box's pixel width, so redraw when the box
// resizes, not only when it crosses into or out of the narrow layout.
new ResizeObserver(() => {
  if (!D || !D.owner || !(pricesNarrow || pricesTooNarrow())) return;
  renderPrices();
  if (tipFor && !tipFor.isConnected) hideTip();
}).observe($('pplot'));
$('pgPrev').addEventListener('click', () => { if (D) { page = Math.min(pages() - 1, page + 1); renderLedger(); if (D.owner) renderEarnings(); } });
$('pgNext').addEventListener('click', () => { if (D) { page = Math.max(0, page - 1); renderLedger(); if (D.owner) renderEarnings(); } });
addEventListener('keydown', (e) => {
  if (!modal.hidden) { if (e.key === 'Escape') closeModal(); return; }
  if (!D || e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.key === 'ArrowLeft') $('pgPrev').click();
  if (e.key === 'ArrowRight') $('pgNext').click();
});
load();
connect();
setInterval(tickLive, 1000);
// Armed bars drain and meals end on their own, so refetch now and then.
setInterval(() => { if (document.visibilityState === 'visible') load(); }, 60000);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') load(); });
