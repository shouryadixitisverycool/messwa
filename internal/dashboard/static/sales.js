'use strict';

// The sales page: what became of every meal you registered for, who bought
// them and for how much. Buyer names are WhatsApp push names, so they only
// reach the page through textContent.

let D = null;           // last /api/sales
let skew = 0;           // server clock minus ours, seconds
let lastOK = 0;
let failed = false;
let streaming = false;

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
const dayWord = (date) => (date === D.today ? 'today' : date === addDays(D.today, -1) ? 'yesterday' : date === addDays(D.today, 1) ? 'tomorrow' : fmtDay(date));

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
// saleWhat names what a sale sold: "dinner", or "dinner egg" for an extra.
const saleWhat = (s) => (s.extra ? `${s.meal} ${s.extra.toLowerCase()}` : s.meal);

// extrasCount adds up a meal's registered extras (Kadamba's dinner eggs):
// how many the portal lists and how many are still free to sell.
function extrasCount(c) {
  let total = 0, left = 0, slots = 0;
  for (const x of c.extras || []) { total += x.total || 0; left += x.available || 0; slots += (x.slots || []).length; }
  return { total, left, slots };
}

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
    if (typeof calXPageLoaded === 'function') calXPageLoaded();
    skew = D.now - Date.now() / 1000;
    failed = false;
    lastOK = Date.now();
    keepScroll(render);
    openFromHash();
  } catch (e) {
    failed = true;
  }
  tickLive();
}

// The board's "sell it" link lands here as #sell=date|meal, the attention
// banner's as #attention-meal=date|meal, and its extras link as
// #extras=date|meal. Each shows that meal's week with the meal open in the
// panel; #sell on a running or finished sale also opens its sell sheet, and
// #sell=date|dinner#e1 opens that extra's sheet.
function openFromHash() {
  const m = /^#(sell|attention-meal|extras)=(.+)$/.exec(location.hash);
  if (!m) return;
  history.replaceState(null, '', location.pathname + location.search);
  const [date, slotName = ''] = decodeURIComponent(m[2]).split('|');
  const meal = slotName.split('#')[0];
  const hit = calFind(`${date}|${meal}`);
  if (!hit) return;
  calGoto(date, meal);
  const c = hit[1];
  if (slotName !== meal) {
    const x = SELL.findExtra(c, slotName);
    if (x && D.owner) openSell(date, SELL.extraCell(date, c, ...x));
    return;
  }
  if (m[1] === 'extras') { const sec = document.querySelector('#insp .cal-xsec'); if (sec) sec.scrollIntoView({ block: 'center' }); return; }
  if (m[1] === 'sell' && !calPlannable(c) && canSell(c)) openSell(date, c);
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
    const here = document.querySelector('.shell-here');
    if (here) here.textContent = 'My meals';
    document.title = 'My meals · messwa';
  }
  renderMoney();
  renderCalendar();
  if (typeof refreshRecurringRulesView === 'function') refreshRecurringRulesView();
  // A re-render replaces whatever the tooltip was attached to.
  if (tipFor && !tipFor.isConnected) hideTip();
}

// ---------- the jar's tooltip ----------

function savedTip() {
  const M = D.month;
  const monthName = fmt(M.start, { month: 'long' });
  return [
    [{ t: `${monthName} so far`, c: 'nm' }],
    D.owner ? [{ t: 'Earned ', c: 'c7' }, { t: rupees(M.earned), c: 'ca' }, { t: ` from ${M.sold} sold`, c: 'c8' }] : null,
    D.owner && M.extras_sold ? [{ t: 'Extras ', c: 'c7' }, { t: rupees(M.extras_earned || 0), c: 'ca' }, { t: ` from ${M.extras_sold} sold, included above`, c: 'c8' }] : null,
    M.wasted ? [{ t: 'Left on the table ', c: 'c7' }, { t: rupees(Math.round(M.potential)), c: 'c6' }, { t: ` from ${M.wasted} wasted`, c: 'c8' }] : null,
    M.fee > 0 ? [{ t: 'Mess bill ', c: 'c7' }, { t: rupees(M.fee), c: 'cf' }, { t: M.bill ? '' : ' from config.toml', c: 'c8' }] : null,
    ...(M.bill ? [[{ t: 'Availed ', c: 'c7' }, { t: rupees(M.bill.availed), c: 'cf' }, { t: ' + projected ', c: 'c7' }, { t: rupees(M.bill.projected), c: 'cf' }]] : []),
  ];
}

// ---------- ledger ----------

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
    n.append(span('messg', messIcon(c.mess)), p, el('div', 'glint'), el('span', 'live-dot'));
    if (t.autopilot) n.append(el('span', 'auto-tag', '✈'));
  } else if (c.state === 'scheduled') {
    const sc = c.schedule;
    const p = el('div', 'price range');
    p.append(el('span', 'r', '₹'), sc.min === sc.max ? String(sc.min) : `${sc.min}-${sc.max}`);
    if (c.diet) n.append(span('diet', dietMark(c.diet)));
    // No mess icon: the tag's time needs the room.
    n.append(p, el('span', 'sell-tag', hm(sc.at)));
    if (sc.autopilot) n.append(el('span', 'auto-tag', '✈'));
  } else if (c.state === 'unknown') {
    n.append(el('div', 'ghost', '?'));
  } else {
    const g = el('div', 'ghost');
    g.append(svgEl(messIcon(c.mess)));
    n.append(g);
    if (c.sellable) n.append(el('span', 'sell-tag', 'SELL'));
  }
  if (c.extras && c.extras.length) n.append(extrasBadge(c));
  if (canSell(c)) {
    n.classList.add('sellable');
    n.tabIndex = 0;
    n.setAttribute('role', 'button');
    n.setAttribute('aria-label', c.task ? `Manage the sale of ${c.meal}: mark sold, edit prices or stop`
      : SELL.isSold(c) ? `Sold ${c.meal}. Open to delete the sale`
      : c.schedule ? `Manage the scheduled sale of ${c.meal}, going up at ${hm(c.schedule.at)}` : `Sell ${c.meal}`);
  }
  return n;
}

// extrasBadge is the egg tab on a meal's bottom-right corner: how many
// extras are registered, or left/registered once some are sold or up for
// sale. It sits half outside the slot, so it never covers a stamp, the
// price or the SELL tag.
function extrasBadge(c) {
  const { total, left, slots } = extrasCount(c);
  const n = total || slots;
  const b = el('span', 'xbadge' + (left ? '' : ' none'));
  b.append(svgEl(extraIcon()), el('b', '', left && left < n ? `${left}/${n}` : String(n)));
  return b;
}

// extrasLines describe a meal's extras for its tooltip.
function extrasLines(c) {
  const out = [];
  for (const x of c.extras || []) {
    const bits = [];
    if (x.available) bits.push({ t: ` · ${x.available} to sell`, c: 'c6' });
    if (x.selling) bits.push({ t: ` · ${x.selling} selling`, c: 'c6' });
    if (x.scheduled) bits.push({ t: ` · ${x.scheduled} scheduled`, c: 'ce' });
    if (x.sold) bits.push({ t: ` · ${x.sold} sold`, c: 'ca' });
    if (x.availed) bits.push({ t: ` · ${x.availed} eaten`, c: 'c8' });
    out.push([{ t: `${x.name} `, c: 'c7' }, { t: `${x.total} registered`, c: 'cf' }, ...bits]);
    if (x.earned) out.push([{ t: `${x.name} sales `, c: 'c7' }, { t: rupees(x.earned), c: 'c6' }]);
  }
  if (c.extras_err) out.push([{ t: `Couldn't check extras: ${c.extras_err}`, c: 'c8' }]);
  return out;
}

function cellLines(date, c, opts = {}) {
  const s = c.sale != null ? D.sales[c.sale] : null;
  // The row already says which meal it is, so the title names the diet,
  // and the meal moves down to the time line.
  const who = (x) => ({ t: x.name || fmtPhone(x.phone) || 'Unknown buyer', c: 'cf' });
  const lines = [
    [{ t: `${messName(c.mess)} ${c.diet ? DIET_LABEL[c.diet].toLowerCase() : c.meal}`, c: 'nm', color: messColor(c.mess) }],
    [{ t: `${fmtDay(date)}, ${c.meal} ${hm(c.start)}-${hm(c.end)}`, c: 'c8' }],
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
      if (canSell(c)) lines.push(null, [{ t: 'Click to delete the sale if it fell through.', c: 'lore' }]);
      lines.push({ rar: 'SOLD' + (s.paid_by === 'image' ? ' · UNVERIFIED' : ''), c: 'ca' });
    }
  } else if (c.state === 'armed') {
    lines.push([{ t: 'Armed for ', c: 'c7' }, who(s)]);
    lines.push([{ t: 'Waiting for ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ' on WhatsApp Pay', c: 'c8' }]);
    if (s.offer_at) lines.push([{ t: 'Offer sent ', c: 'c7' }, { t: hm(s.offer_at), c: 'c8' }]);
    lines.push([{ t: 'Window closes ', c: 'c7' }, { t: hm(c.end), c: 'ce' }, { t: ` (in ${dur(c.end - now())})`, c: 'c8' }]);
    if (c.task) lines.push(null, [{ t: 'Click to mark it sold, edit the prices or stop selling.', c: 'lore' }]);
    lines.push({ rar: 'ARMED', c: 'ce' });
  } else if (c.state === 'handoff') {
    lines.push([{ t: 'Handed to you ', c: 'c7' }, { t: hm(s.armed_at), c: 'c8' }, { t: ' when ', c: 'c7' }, who(s), { t: ' replied', c: 'c7' }]);
    if (s.phone && s.name) lines.push([{ t: 'Phone ', c: 'c7' }, { t: fmtPhone(s.phone), c: 'c8' }]);
    lines.push([{ t: 'Offer asked ', c: 'c7' }, { t: rupees(s.amount), c: 'c6' }]);
    lines.push(null, [{ t: 'The bot sent this buyer nothing. Finish with deliver-qr -peer, or free the meal with release-handoff.', c: 'c8' }]);
    lines.push({ rar: 'HANDED OFF', c: 'c6' });
  } else if (c.state === 'ate') {
    lines.push([{ t: 'You ate this one.', c: 'c7' }], { rar: 'EATEN', c: 'c7' });
  } else if (c.state === 'wasted') {
    lines.push([{ t: 'Registered, never sold, never eaten.', c: 'cc' }]);
    if (s) lines.push([{ t: `Your offer of ${rupees(s.amount)} to ${s.name || fmtPhone(s.phone) || 'the buyer'} lapsed.`, c: 'c8' }]);
    if (c.ask != null) lines.push([{ t: `Would have fetched about ${rupees(Math.round(c.ask))} at the median ask.`, c: 'lore' }]);
    lines.push({ rar: 'WASTED', c: 'cc' });
  } else if (c.state === 'selling') {
    const t = c.task;
    lines.push([{ t: 'Selling now for ', c: 'c7' }, { t: t.min === t.max ? rupees(t.min) : `${rupees(t.min)}-${rupees(t.max)}`, c: 'c6' }]);
    const a = askLine('Median ask ');
    if (a) lines.push(a);
    lines.push([{ t: 'Window closes ', c: 'c7' }, { t: hm(c.end), c: 'ce' }, { t: ` (in ${dur(c.end - now())})`, c: 'c8' }]);
    lines.push(null, [{ t: 'Click to mark it sold, edit the prices or stop selling.', c: 'lore' }]);
    lines.push({ rar: 'SELLING', c: 'c6' });
  } else if (c.state === 'scheduled') {
    const sc = c.schedule;
    lines.push([{ t: 'Goes up at ', c: 'c7' }, { t: hm(sc.at), c: 'ce' }, { t: ` (in ${dur(sc.at - now())})`, c: 'c8' }]);
    lines.push([{ t: 'Price ', c: 'c7' }, { t: priceText(sc), c: 'c6' }]);
    const a = askLine('Median ask ');
    if (a) lines.push(a);
    lines.push([{ t: 'Sells till ', c: 'c7' }, { t: hm(c.end), c: 'cf' }]);
    if (sc.autopilot) lines.push([{ t: 'Autopilot ', c: 'c7' }, { t: 'on, never waits on you', c: 'cb' }]);
    lines.push(null, [{ t: 'Click to start it now, turn autopilot on or off, edit the prices or unschedule it.', c: 'lore' }]);
    lines.push({ rar: 'SCHEDULED', c: 'ce' });
  } else if (c.state === 'open') {
    lines.push([{ t: now() >= c.start ? 'Being served now. Still yours to sell.' : 'Registered. Not sold yet.', c: 'c7' }]);
    const at = canSell(c) && listAt(c);
    if (at) lines.push([{ t: 'Sell it and it goes up at ', c: 'c7' }, { t: hm(at), c: 'ce' }, { t: `, ${listWhen(c, at)}.`, c: 'c8' }]);
    const a = askLine('Median ask ');
    if (a) lines.push(a);
    if (canSell(c)) lines.push(null, [{ t: 'Click to put it up for sale.', c: 'lore' }]);
    lines.push({ rar: 'REGISTERED', c: 'c8' });
  } else if (c.state === 'none') {
    lines.push([{ t: 'Not registered for this meal.', c: 'c8' }]);
    lines.push({ rar: 'NOT REGISTERED', c: 'c8' });
  } else {
    lines.push([{ t: "The portal couldn't be read, so this could be eaten, wasted or not registered.", c: 'c8' }]);
    if (s) lines.push([{ t: `Your offer of ${rupees(s.amount)} lapsed.`, c: 'c8' }]);
    lines.push({ rar: 'UNKNOWN', c: 'c8' });
  }
  // Extras go after what became of the meal, before the click hint.
  const xs = opts.extras === false ? [] : extrasLines(c);
  if (xs.length) {
    const lore = lines.findIndex((l) => Array.isArray(l) && l[0] && l[0].c === 'lore');
    lines.splice(lore > 0 && lines[lore - 1] === null ? lore - 1 : lines.length - 1, 0, null, ...xs);
  }
  return lines;
}

// ---------- selling from the ledger ----------

// canSell: a meal the sell sheet can open: an open one being served now or
// next, a schedule, a running task (to mark sold, reprice or stop it), or a
// sold meal (to delete the sale).
const canSell = (c) => D.owner && (c.task != null || c.schedule != null || (c.state === 'open' && c.sellable) || SELL.isSold(c));

const modal = $('modal');
const sheet = $('sheet');
function closeModal() { modal.hidden = true; sheet.replaceChildren(); }
modal.addEventListener('click', (e) => { if (e.target === modal) closeModal(); });

// The sell sheet lives in sell.js; the board opens the same one.
const SELL = sellSheet({ data: () => D, now, close: closeModal, toast, reload: () => { if (typeof calXReset === 'function') calXReset(); return load(); } });
// ledger.js prices meals with the same slider and defaults.
const { priceSlider, priceText, listAt, listWhen, sellBounds, sellDefaults, extraCell } = SELL;
const openSell = (date, c, view) => SELL.open(date, c, view);

// ---------- friends' meals ----------

// Friends' meals are sold through your WhatsApp from their QR. They're
// listed here and nowhere else on the page, so they never count as yours.

const { friendCell } = SELL;

function openFriendForm() {
  hideTip();
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
      const whose = friend.value.trim() ? `${friend.value.trim()}'s` : "your friend's";
      toast(data.at ? `Scheduled. ${cap(whose)} ${w.meal} goes up at ${hm(data.at)}` : `Selling ${whose} ${w.meal}`);
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

function buyerLines(b) {
  const t = tierOf(b.n);
  const usual = new Map();
  for (const s of b.sales) {
    const k = `${messName(s.mess)} ${saleWhat(s)}`;
    usual.set(k, (usual.get(k) || 0) + 1);
  }
  const fav = [...usual.entries()].sort((x, y) => y[1] - x[1])[0];
  const waits = b.sales.filter((s) => s.offer_at && s.paid_at && s.paid_at >= s.offer_at).map((s) => s.paid_at - s.offer_at);
  const first = b.sales[0].date, last = b.sales[b.sales.length - 1].date;
  return [
    [{ t: b.name || fmtPhone(b.phone) || 'Unknown buyer', c: 'nm', color: t.color }],
    b.name && b.phone ? [{ t: fmtPhone(b.phone), c: 'c8' }] : null,
    null,
    [{ t: 'Bought ', c: 'c7' }, { t: String(b.n), c: 'cf' }, ...(() => { const x = b.sales.filter((s) => s.extra).length; return x ? [{ t: `, ${x} of them extra${x === 1 ? '' : 's'}`, c: 'c8' }] : []; })()],
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
  // Extras sell for a fraction of a meal and have no market median, so the
  // price check is meals only.
  const mine = recentSales().filter((s) => !s.extra);
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
  const top = narrow ? 8 : 22, rowH = narrow ? 50 : 40, H = top + rows.length * rowH + (narrow ? 10 : 16);
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
    const ly = top + i * rowH + (narrow ? 8 : 6), y = narrow ? ly + 24 : ly;
    svg += (ART[meal] ? icon(meal) : '').replace('<svg ', `<svg x="2" y="${ly - 6}" width="12" height="12" `)
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

// ---------- boot ----------

// The phone layout draws at the box's pixel width, so redraw when the box
// resizes, not only when it crosses into or out of the narrow layout.
// Only width counts: the recap track's height animates as slides change.
let recapWidth = 0;
new ResizeObserver(() => {
  const w = $('recap').clientWidth;
  if (w === recapWidth) return;
  recapWidth = w;
  if (!D || !D.owner || !$('pplot')) return;
  if (pricesNarrow || pricesTooNarrow()) renderPrices();
  recapDot();
  if (tipFor && !tipFor.isConnected) hideTip();
}).observe($('recap'));
addEventListener('keydown', (e) => {
  if (!modal.hidden) { if (e.key === 'Escape') closeModal(); return; }
  if (!D || e.metaKey || e.ctrlKey || e.altKey) return;
  if (document.body.classList.contains('shell-menu-open')) return;
  if (e.target.matches('input, select, textarea, [contenteditable="true"]') || e.target.closest('#insp, #recap')) return;
  if (e.key === 'ArrowLeft') $('pgPrev').click();
  if (e.key === 'ArrowRight') $('pgNext').click();
});
load();
connect();
setInterval(tickLive, 1000);
// Armed bars drain and meals end on their own, so refetch now and then.
setInterval(() => { if (document.visibilityState === 'visible') load(); }, 60000);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') load(); });

addEventListener('hashchange', () => { if (D) openFromHash(); });
