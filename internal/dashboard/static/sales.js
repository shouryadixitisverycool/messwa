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
  for (const meal of meals()) {
    const mh = el('div', 'mh');
    mh.append(svgEl(icon(meal)));
    mh.title = cap(meal);
    g.append(mh);
    for (const date of dates) {
      const day = days.get(date);
      const c = day && day.cells.find((x) => x.meal === meal);
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
  } else if (c.state === 'unknown') {
    n.append(el('div', 'ghost', '?'));
  } else {
    const g = el('div', 'ghost');
    g.append(svgEl(messIcon(c.mess)));
    n.append(g);
    if (c.state === 'ate') n.append(el('span', 'stamp', 'ATE'));
    if (c.state === 'wasted') n.append(el('span', 'stamp', 'WASTED'));
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
    if (s.sent_at && s.paid_at) lines.push([{ t: 'Delivered ', c: 'c7' }, { t: `${dur(s.sent_at - s.paid_at)} after payment`, c: 'ca' }]);
    if (s.ask != null) lines.push([{ t: 'Median ask ', c: 'c7' }, { t: rupees(Math.round(s.ask)), c: 'cf' }, { t: ` (you ${signed(s.amount - s.ask)})`, c: 'c8' }]);
    if (c.state === 'sending') {
      lines.push(null, [{ t: 'Paid, but the QR send was interrupted. Check the chat before doing anything else.', c: 'c6' }]);
      lines.push({ rar: 'PAID · SEND UNCERTAIN', c: 'c6' });
    } else {
      lines.push({ rar: 'SOLD' + diet, c: 'ca' });
    }
  } else if (c.state === 'armed') {
    lines.push([{ t: 'Armed for ', c: 'c7' }, who(s)]);
    lines.push([{ t: 'Waiting for ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ' on WhatsApp Pay', c: 'c8' }]);
    if (s.offer_at) lines.push([{ t: 'Offer sent ', c: 'c7' }, { t: hm(s.offer_at), c: 'c8' }]);
    lines.push([{ t: 'Window closes ', c: 'c7' }, { t: hm(c.end), c: 'ce' }, { t: ` (in ${dur(c.end - now())})`, c: 'c8' }]);
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
  } else if (c.state === 'open') {
    lines.push([{ t: now() >= c.start ? 'Being served now. Still yours to sell.' : 'Registered. Not sold yet.', c: 'c7' }]);
    const a = askLine('Median ask ');
    if (a) lines.push(a);
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

function renderPrices() {
  const box = $('pplot');
  box.replaceChildren();
  const mine = recentSales();
  const rows = meals().filter((m) => D.market.some((x) => x.meal === m) || mine.some((s) => s.meal === m));
  if (!rows.length) {
    box.append(el('div', 'empty-note', 'No priced listings or sales in the last 4 weeks.'));
    return;
  }
  const vals = [];
  for (const x of D.market) vals.push(x.q1, x.q3);
  for (const s of mine) vals.push(s.amount);
  let lo = Math.floor(Math.min(...vals) / 10) * 10, hi = Math.ceil(Math.max(...vals) / 10) * 10;
  if (hi - lo < 20) { lo = Math.max(0, lo - 10); hi = lo + 30; }
  const W = 860, x0 = 124, x1 = W - 168, top = 22, rowH = 40, H = top + rows.length * rowH + 16;
  const X = (v) => x0 + ((v - lo) / (hi - lo)) * (x1 - x0);
  const step = [5, 10, 20, 25, 50, 100].find((s) => (hi - lo) / s <= 6) || 100;
  let svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${H}" role="img" aria-label="Your sale prices against the market's asking prices, by meal">`;
  for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) {
    svg += `<line x1="${X(v)}" x2="${X(v)}" y1="4" y2="${H - 22}" stroke="#252c3a" stroke-dasharray="2 4"/>`
      + `<text x="${X(v)}" y="${H - 6}" fill="#586076" font-size="10.5" text-anchor="middle">₹${v}</text>`;
  }
  const verdicts = [];
  rows.forEach((meal, i) => {
    const y = top + i * rowH + 6;
    const m = D.market.find((x) => x.meal === meal);
    svg += icon(meal).replace('<svg ', `<svg x="2" y="${y - 6}" width="12" height="12" `)
      + `<text class="px" x="22" y="${y + 4}" fill="#8b94a7">${meal.toUpperCase()}</text>`;
    if (m) {
      svg += `<rect x="${X(m.q1)}" y="${y - 9}" width="${Math.max(2, X(m.q3) - X(m.q1))}" height="18" rx="4" fill="rgba(232,236,243,.14)"/>`
        + `<line x1="${X(m.median)}" x2="${X(m.median)}" y1="${y - 12}" y2="${y + 12}" stroke="#e8ecf3" stroke-opacity=".8" stroke-width="2" stroke-linecap="round"/>`;
    }
    const ms = mine.filter((s) => s.meal === meal);
    ms.forEach((s, k) => {
      const cx = X(s.amount) + ((k % 3) - 1) * 2, cy = y + (((k * 37) % 13) - 6);
      svg += `<g class="dot" data-i="${D.sales.indexOf(s)}"><circle cx="${cx}" cy="${cy}" r="10" fill="transparent"/><circle cx="${cx}" cy="${cy}" r="4.5" fill="#ffc53d" stroke="#141923" stroke-width="2"/></g>`;
    });
    const diffs = ms.filter((s) => s.ask != null).map((s) => s.amount - s.ask);
    const d = diffs.length ? sum(diffs) / diffs.length : null;
    const word = d == null ? 'NO SALES' : d <= -3 ? 'UNDERCUT' : d >= 3 ? 'PREMIUM' : 'AT MARKET';
    const hot = word === 'UNDERCUT';
    const pw = word.length * 7 + 14;
    svg += `<g transform="translate(${W - 150} ${y - 9})"><rect width="${pw}" height="18" rx="9" fill="none" stroke="${hot ? 'rgba(255,197,61,.55)' : '#252c3a'}"/>`
      + `<text class="px pill" x="${pw / 2}" y="12.5" text-anchor="middle" font-size="8" fill="${hot ? '#ffc53d' : d == null ? '#586076' : '#8b94a7'}">${word}</text></g>`;
    if (d != null) svg += `<text x="${W}" y="${y + 4}" text-anchor="end" fill="#8b94a7" font-size="11">${signed(d)}</text>`;
    verdicts.push({ meal, d, word, m, n: ms.length });
  });
  svg += '</svg>';
  box.append(svgEl(svg));
  for (const g of box.querySelectorAll('.dot')) {
    const s = D.sales[+g.dataset.i];
    tip(g, () => [
      [{ t: `${messName(s.mess)} ${s.meal}`, c: 'nm', color: messColor(s.mess) }],
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
  const names = D.messes.map((m) => m.name);
  if (mine.some((s) => !names.includes(s.mess))) names.push('');
  const per = new Map(names.map((m) => [m, days.map(() => 0)]));
  for (const s of mine) {
    const row = per.get(names.includes(s.mess) ? s.mess : '');
    row[days.indexOf(s.date)] += s.amount;
  }
  const mx = Math.max(1, ...[...per.values()].flat());
  const totals = names.map((m) => sum(per.get(m)));
  const best = Math.max(...totals);
  const top = totals.filter((t) => t === best).length === 1 && best > 0 ? names[totals.indexOf(best)] : null;
  const week = weekDates();
  const w0 = days.indexOf(week[0]), w1 = days.indexOf(week[6]);

  names.forEach((m, mi) => {
    const list = mine.filter((s) => (names.includes(s.mess) ? s.mess : '') === m);
    const c = el('div', 'card');
    c.style.setProperty('--m', messColor(m));
    const head = el('div', 'card-head');
    head.append(svgEl(messIcon(m)));
    const nm = el('span', 'card-name', messName(m));
    nm.style.setProperty('color', messColor(m));
    head.append(nm);
    if (m === top) head.append(el('span', 'verdict hot', 'TOP EARNER'));
    const kpis = el('div', 'kpis');
    const kpi = (v, label) => { const k = el('div', 'kpi'); k.append(el('b', v == null ? 'none' : '', v == null ? '-' : v), el('span', '', label)); return k; };
    const med = median(list.map((s) => s.amount));
    kpis.append(kpi(String(list.length), 'MEALS SOLD'), kpi(rupees(totals[mi]), 'EARNED'), kpi(med == null ? null : rupees(Math.round(med)), 'MEDIAN PRICE'));
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
    per.get(m).forEach((v, i) => {
      const bk = el('div', 'bk');
      if (v) {
        const bar = el('i');
        bar.style.setProperty('height', `${Math.max(6, (v / mx) * 100)}%`);
        bk.append(bar);
      }
      const ds = list.filter((s) => s.date === days[i]);
      tip(bk, () => [
        [{ t: `${messName(m)}, ${fmtDay(days[i])}`, c: 'nm', color: messColor(m) }],
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
me().then((m) => {
  userLink(m);
  $('insLink').hidden = m.role !== 'admin';
}).catch(() => {});
$('ledIcon').append(svgEl(icon('coin')));
$('regIcon').append(svgEl(icon('paper')));
$('pcIcon').append(svgEl(icon('chart')));
$('cardIcon').append(svgEl(icon('chart')));
tip($('xpBar'), () => (D ? savedTip() : []));
$('pgPrev').addEventListener('click', () => { if (D) { page = Math.min(pages() - 1, page + 1); renderLedger(); if (D.owner) renderEarnings(); } });
$('pgNext').addEventListener('click', () => { if (D) { page = Math.max(0, page - 1); renderLedger(); if (D.owner) renderEarnings(); } });
addEventListener('keydown', (e) => {
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
