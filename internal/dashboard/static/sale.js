'use strict';

// The "my sale" chest: your running sell tasks, drawn for a glance. Colour
// and shape carry the state, and words only appear in tooltips and sheets:
//
//   glow       red: a buyer needs you. green: a payment is in flight.
//   clock      minutes until the meal ends.
//   price bar  floor to opening price. Green is what the bot takes now,
//              the arrow its lowest, the gold tick its target; dots are
//              buyers' offers.
//   posts      gold is your post, the bar after it the listings posted
//              since. Red once it's buried; dashed red when it failed.
//              ↻ on a failed post sends it to that group again.
//   buyers     border colour is their state; ! needs you, ? check, the ring
//              their pay window.
//
// Uses app.js's globals (D, now, hm, dur, sheet, modal, closeModal,
// messagingApp, digits, toast, openListing). Buyer names are untrusted and
// only ever reach the page through textContent.

const SVGNS = 'http://www.w3.org/2000/svg';
function sv(tag, attrs, text) {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, v);
  if (text != null) e.textContent = text;
  return e;
}
const C = { red: '#ff6b5b', egg: '#f2b51b', veg: '#34c463', gold: '#ffc53d', blue: '#5aa9ff', dim: '#586076', mut: '#8b94a7', line: '#262d3b' };
const LIVE_PAY = ['armed', 'sending'];
const won = (v) => v.state === 'sold' || (v.pay && v.pay.state === 'sent');
const rup = (x) => '₹' + (Number.isInteger(x) ? x : x.toFixed(2));
// A friend's meal is stored under a slot like "dinner#f1"; show "dinner"
// and whose it is.
const mealOf = (v) => v.base_meal || v.meal.split('#')[0];
const mealLabel = (v) => (v.for_friend ? `${mealOf(v)} · for ${v.friend || 'a friend'}` : mealOf(v));

// ---------- reading a buyer ----------

const has = (b, kind) => b.events.some((e) => e.kind === kind);
const last = (b, kind) => { for (let i = b.events.length - 1; i >= 0; i--) if (b.events[i].kind === kind) return b.events[i]; return null; };

// sentPicture: a picture in their pay window and no QR, which usually means
// they paid some other way.
const sentPicture = (b) => has(b, 'image') && !has(b, 'qr_sent') && b.state !== 'bought';

// Short reasons for a parked buyer, from the note negotiate.go writes.
function reason(b) {
  const n = b.note || '';
  if (b.refund) return ['₹', `REFUND ${rup(b.refund)}`];
  if (/lowest/.test(n)) return ['↓', 'BELOW LOWEST'];
  if (/couldn't read|could not read/.test(n)) return ['?', 'UNREADABLE'];
  if (/other than WhatsApp Pay/.test(n)) return ['₹', 'OTHER PAYMENT'];
  if (/says they paid/.test(n)) return ['💸', 'SAYS PAID'];
  if (/limit of/.test(n)) return ['✉', 'MSG LIMIT'];
  if (/matches no price/.test(n)) return ['≠', 'WRONG AMOUNT'];
  return ['!', 'NEEDS YOU'];
}

const STATE_WORD = {
  queued: 'waiting', negotiating: 'haggling', agreed: 'agreed, waiting to pay', offered: 'asked to pay', passed: 'passed',
  timed_out: 'timed out', bought: 'bought it', skipped: 'skipped', told_sold: 'told sold', parked: 'needs you', handed_off: 'handed to you',
};

// look is how a buyer tile reads: border class, badge, the chip under it.
function look(b, v) {
  if (b.muted) return { cls: 'gone', badge: '🔇', chip: 'MUTED', tone: C.dim };
  const price = b.agreed || b.offer || b.ask;
  if (b.state === 'bought') return { cls: 'won', badge: '✓', chip: rup(b.asked || b.agreed || price), tone: C.veg };
  // Once the task is over a parked buyer no longer needs you, unless they
  // paid and are owed a refund.
  if (b.refund || (b.state === 'parked' && v.state === 'active')) return { cls: 'need', badge: '!', chip: b.offer ? rup(b.offer) : reason(b)[0], tone: C.red };
  if (b.state === 'offered') return { cls: 'pay', ring: true, chip: '', tone: C.veg };
  if (sentPicture(b)) return { cls: 'chk', badge: '?', chip: `🖼${b.agreed || b.asked ? rup(b.agreed || b.asked) : ''}`, tone: C.egg };
  if (b.state === 'agreed') return { cls: 'wait', chip: `⏳${rup(b.agreed)}`, tone: C.gold };
  if (b.state === 'negotiating' || b.state === 'queued') return { cls: 'talk', chip: b.offer ? rup(b.offer) : '…', tone: C.blue };
  if (b.state === 'handed_off') return { cls: 'chk', badge: '→', chip: price ? rup(price) : '', tone: C.egg };
  return { cls: 'gone', chip: '✕', tone: C.dim };
}

function buyerTip(b) {
  const lines = [[{ t: b.name || b.phone || 'Buyer', c: 'nm' }, b.saved ? { t: ' ♥ saved', c: 'c8' } : null],
    [{ t: cap(STATE_WORD[b.state] || b.state), c: 'c7' }]];
  const nums = [];
  if (b.ask) nums.push({ t: `bot ${rup(b.ask)}  `, c: 'ca' });
  if (b.offer) nums.push({ t: `them ${rup(b.offer)}  `, c: 'cc' });
  if (b.agreed) nums.push({ t: `agreed ${rup(b.agreed)}`, c: 'c6' });
  if (nums.length) lines.push(nums);
  if (b.note) lines.push(0, [{ t: b.note, c: 'c8' }]);
  if (sentPicture(b)) lines.push(0, [{ t: 'Sent a picture, not a WhatsApp Pay payment. They may have paid another way.', c: 'c6' }]);
  return lines;
}

// ---------- chest ----------

let saleSheet = null; // {id, key} or {id, meal: true} while a sale sheet is open

function renderSales() {
  const box = $('sales');
  if (!box) return;
  const list = (D && D.sales) || [];
  box.hidden = !list.length;
  box.replaceChildren(...list.map(chest));
  tickSales();
  if (saleSheet && !saleSheet.editing && !modal.hidden) {
    const v = list.find((x) => x.id === saleSheet.id);
    if (v) (saleSheet.meal ? openMealSheet : openBuyerSheet)(v, saleSheet.key, true);
  }
}
function saleClosed() { saleSheet = null; }

function chest(v) {
  const needs = v.buyers.some((b) => ['need', 'chk'].includes(look(b, v).cls));
  // A live pay window outranks the rest: it's over in two minutes.
  const paying = v.state === 'active' && v.buyers.some((b) => b.state === 'offered') || (v.pay && LIVE_PAY.includes(v.pay.state) && v.pay.paid);
  const c = el('section', 'mysale' + (won(v) ? ' sold' : paying ? ' pay' : needs ? ' hot' : v.state !== 'active' ? ' over' : ''));
  c.setAttribute('aria-label', v.for_friend ? `Your sale of ${mealLabel(v)}` : `Your ${mealOf(v)} sale`);

  const top = el('div', 'ms-top');
  const dot = el('span', 'ms-mess');
  dot.style.setProperty('--m', messColor(v.mess));
  const diet = el('span', 'ms-diet');
  diet.append(svgEl(dietMark(v.diet)));
  const day = v.date === ymd(now()) ? '' : ' ' + dayWord(v.date).toUpperCase();
  top.append(dot, diet, el('span', 'ms-meal', mealLabel(v).toUpperCase() + day), historyPips(v));
  c.append(top);

  const hero = el('div', 'ms-hero');
  hero.append(clock(v), rail(v));
  hero.addEventListener('click', () => openMealSheet(v));
  tip(hero, () => heroTip(v));
  c.append(hero);

  const posts = el('div', 'ms-posts');
  for (const g of postGroups(v)) posts.append(groupPill(g, v));
  if (!v.posts.length) posts.append(el('span', 'ms-none', '…'));
  posts.addEventListener('click', () => openMealSheet(v));
  c.append(posts);

  const row = el('div', 'ms-buyers');
  for (const b of v.buyers) row.append(buyerTile(b, v));
  for (let i = v.buyers.length; i < 5; i++) row.append(el('span', 'ms-b empty'));
  c.append(row);
  if (v.state === 'active') c.append(saleTaskActions(v));

  const mk = marketRow(v);
  if (mk) c.append(mk);
  return c;
}

function historyPips(v) {
  const h = el('span', 'ms-hist');
  for (const x of v.history) {
    const p = el('i', 'h-' + x.state + (x.id === v.id ? ' cur' : ''));
    tip(p, () => [[{ t: `#${x.id} `, c: 'nm' }, { t: x.state.replace('_', ' '), c: 'c7' }],
      [{ t: `${hm(x.started)}${x.ended ? '–' + hm(x.ended) : '–'} · ${x.buyers} ${x.buyers === 1 ? 'buyer' : 'buyers'}`, c: 'c8' }]]);
    h.append(p);
  }
  return h;
}

function clock(v) {
  const box = el('div', 'ms-clock');
  const R = 25, L = 2 * Math.PI * R;
  const s = sv('svg', { viewBox: '0 0 58 58' });
  s.append(sv('circle', { cx: 29, cy: 29, r: R, fill: 'none', stroke: C.line, 'stroke-width': 5 }));
  const arc = sv('circle', { cx: 29, cy: 29, r: R, fill: 'none', stroke: C.gold, 'stroke-width': 5, 'stroke-linecap': 'round', 'stroke-dasharray': L.toFixed(1), transform: 'rotate(-90 29 29)' });
  s.append(arc);
  box.append(s);
  const b = el('b');
  if (v.state === 'active') {
    arc.dataset.from = v.started;
    arc.dataset.until = v.ends;
    arc.dataset.len = L;
    b.dataset.until = v.ends;
    b.dataset.fmt = 'mins';
  } else {
    arc.setAttribute('stroke', won(v) ? C.veg : C.dim);
    arc.setAttribute('stroke-dashoffset', 0);
    b.textContent = won(v) ? '✓' : '✕';
    b.classList.add(won(v) ? 'grn' : 'dim');
  }
  box.append(b);
  return box;
}

// rail draws floor to opening price, the bot's lowest, the target and
// everyone's offers. It draws in pixels (times RAIL_K) rather than a fixed
// viewBox, so a wide laptop gets a longer bar, not a 5x zoomed one.
const RAIL_K = 1.2;
const railRO = typeof ResizeObserver === 'function' && new ResizeObserver((es) => {
  for (const e of es) {
    if (!e.target.isConnected) railRO.unobserve(e.target);
    else e.target._draw?.(e.contentRect.width);
  }
});

function rail(v) {
  const wrap = el('div', 'ms-rail');
  let drawn = 0;
  wrap._draw = (px) => {
    const W = Math.max(120, Math.round(px / RAIL_K));
    if (W === drawn) return;
    drawn = W;
    wrap.replaceChildren(railSVG(v, W));
  };
  wrap._draw(220 * RAIL_K);
  if (railRO) railRO.observe(wrap);
  return wrap;
}

function railSVG(v, W) {
  const lo = Math.min(v.min, ...v.buyers.filter((b) => b.offer).map((b) => b.offer)), hi = v.max;
  const x = (p) => 12 + (hi === lo ? 1 : (Math.min(Math.max(p, lo), hi) - lo) / (hi - lo)) * (W - 24);
  const s = sv('svg', { viewBox: `0 0 ${W} 46`, width: '100%', height: Math.round(46 * RAIL_K) });
  s.append(sv('rect', { x: 12, y: 25, width: W - 24, height: 7, rx: 3.5, fill: C.line }));
  if (v.state === 'active' || won(v)) {
    const at = won(v) && v.pay ? v.pay.amount : v.lowest;
    s.append(sv('rect', { x: x(at).toFixed(1), y: 25, width: Math.max(0, W - 12 - x(at)).toFixed(1), height: 7, rx: 3.5, fill: C.veg, 'fill-opacity': 0.55 }));
  }
  if (v.negotiates && v.target > v.min && v.target < v.max) {
    s.append(sv('line', { x1: x(v.target), x2: x(v.target), y1: 21, y2: 36, stroke: C.gold, 'stroke-width': 2 }));
  }
  for (const b of v.buyers) {
    const p = b.agreed || b.offer;
    if (!p) continue;
    const l = look(b, v);
    if (l.cls === 'gone') continue;
    s.append(sv('circle', { cx: x(p).toFixed(1), cy: 28.5, r: 5, fill: l.cls === 'need' ? '#121620' : l.tone, stroke: l.tone, 'stroke-width': 2 }));
  }
  const mark = won(v) && v.pay ? v.pay.amount : v.lowest;
  const xl = x(mark);
  const col = won(v) ? C.veg : v.held ? C.gold : C.veg;
  s.append(sv('path', { d: `M${(xl - 6).toFixed(1)} 13 L${(xl + 6).toFixed(1)} 13 L${xl.toFixed(1)} 21 Z`, fill: col }));
  const anchor = xl < 30 ? 'start' : xl > W - 30 ? 'end' : 'middle';
  s.append(sv('text', { x: xl.toFixed(1), y: 10, 'text-anchor': anchor, class: 'rail-now', fill: col }, (v.held ? '🔒' : '') + rup(mark)));
  s.append(sv('text', { x: 12, y: 45, class: 'rail-end' }, String(lo)));
  s.append(sv('text', { x: W - 12, y: 45, 'text-anchor': 'end', class: 'rail-end' }, String(hi)));
  return s;
}

function heroTip(v) {
  const lines = [[{ t: `${cap(v.mess)} ${mealLabel(v)}`, c: 'nm', color: messColor(v.mess) }, { t: `  #${v.id}`, c: 'c8' }]];
  if (v.state === 'active') lines.push([{ t: `Ends ${hm(v.ends)}, in ${dur(v.ends - now())}`, c: 'c7' }]);
  else lines.push([{ t: `${cap(v.state.replace('_', ' '))}${v.detail ? ': ' + v.detail : ''}`, c: won(v) ? 'ca' : 'c8' }]);
  lines.push(0, [{ t: `Opens ${rup(v.max)}, floor ${rup(v.min)}`, c: 'c7' }]);
  if (v.negotiates) {
    lines.push([{ t: `Aims for ${rup(v.target)}, takes ${rup(v.lowest)} now`, c: 'ca' }]);
    if (v.held) lines.push([{ t: 'Held at the target: two buyers are active.', c: 'c6' }]);
  }
  return lines;
}

// A group's name without the words most groups share: "IIIT Mess Buy
// Sell" -> "Buy Sell".
const FILLER = new Set(['mess', 'iiit', 'iiith', 'the', 'group', 'and', 'of']);
function groupShort(g) {
  const w = g.split(/\s+/).filter((x) => x && !FILLER.has(x.toLowerCase().replace(/[^a-z0-9]/g, '')));
  return w.length ? w.join(' ') : g;
}
const errCode = (e) => ((e || '').match(/\b(\d{3})\b/) || [, 'ERR'])[1];
const BURIED = 15;
const postMark = (p) => (p.state === 'failed' ? '✗' : p.state === 'posted' ? '✓' : '…');

// postGroups gathers the offer's copies by group, oldest copy first, in the
// order the groups first got one. cur is the copy that counts now.
function postGroups(v) {
  const by = new Map();
  for (const p of v.posts) {
    const k = p.jid || p.group;
    if (!by.has(k)) by.set(k, []);
    by.get(k).push(p);
  }
  return [...by.values()].map((copies) => {
    copies.sort((a, b) => a.seq - b.seq || a.at - b.at);
    return { name: copies[0].group, copies, cur: copies[copies.length - 1] };
  });
}

function copyLine(p, i) {
  const what = i === 0 ? 'Posted' : 'Reposted';
  if (p.state === 'failed') return [{ t: `${what} ${hm(p.at)}: failed, ${p.error || 'unknown error'}`, c: 'cc' }];
  if (p.state === 'posting') return [{ t: `${what} ${hm(p.at)}: sending…`, c: 'c6' }];
  return [{ t: `${what} ${hm(p.at)}: ${p.after} ${p.after === 1 ? 'listing' : 'listings'} since`, c: p.after > BURIED ? 'cc' : 'c8' }];
}

// groupPill is one group in the box: how its newest copy is doing, and
// how many copies it got.
function groupPill(g, v) {
  const p = g.cur;
  const n = el('span', 'ms-post ' + p.state + (p.state === 'posted' && p.after > BURIED ? ' buried' : ''));
  n.append(el('b', '', postMark(p)), el('span', 'name', groupShort(g.name)));
  if (g.copies.length > 1) n.append(el('span', 'times', `×${g.copies.length}`));
  if (p.state === 'failed') {
    n.append(el('span', 'err', errCode(p.error)));
    if (p.resend) n.append(resendButton(v, p));
  } else if (p.state === 'posted') {
    const bar = el('span', 'depth');
    const after = el('i', 'after');
    after.style.setProperty('width', `${Math.min(1, p.after / 25) * 100}%`);
    bar.append(el('i', 'me'), after);
    n.append(bar);
  }
  tip(n, () => [[{ t: g.name, c: 'nm' }], ...g.copies.map(copyLine),
    p.resend ? [{ t: '↻ posts it again', c: 'c8' }] : null].filter(Boolean));
  return n;
}

// resendButton posts the offer again in a group whose last copy failed.
// The bot never retries such a group by itself.
function resendButton(v, p) {
  const b = el('button', 'resend', '↻');
  b.type = 'button';
  b.setAttribute('aria-label', `Post again in ${p.group}`);
  b.addEventListener('click', async (e) => {
    e.stopPropagation();
    b.disabled = true;
    b.classList.add('busy');
    try {
      await send('POST', 'api/sell-action', { action: 'resend', id: v.id, group: p.jid });
      toast(`Posted again in ${p.group}`);
    } catch (err) {
      toast(err.message);
    }
    await load();
  });
  return b;
}

function buyerTile(b, v) {
  const l = look(b, v);
  const t = el('button', 'ms-b ' + l.cls);
  t.type = 'button';
  t.append(svgEl(face(b.key)));
  if (l.ring && b.pay_until) {
    const R = 30, L = 2 * Math.PI * R;
    const s = sv('svg', { viewBox: '0 0 66 66', class: 'ring' });
    const arc = sv('circle', { cx: 33, cy: 33, r: R, fill: 'none', stroke: C.veg, 'stroke-width': 3, 'stroke-linecap': 'round', 'stroke-dasharray': L.toFixed(1), transform: 'rotate(-90 33 33)' });
    arc.dataset.from = b.pay_until - 120;
    arc.dataset.until = b.pay_until;
    arc.dataset.len = L;
    s.append(arc);
    t.append(s);
  }
  if (b.saved) t.append(el('span', 'hrt', '♥'));
  if (l.badge) t.append(el('span', 'bd', l.badge));
  const chip = el('span', 'chip', l.chip);
  chip.style.setProperty('color', l.tone);
  if (l.ring && b.pay_until) { chip.dataset.until = b.pay_until; chip.dataset.fmt = 'mmss'; }
  if (chip.textContent || chip.dataset.until) t.append(chip);
  t.setAttribute('aria-label', `${b.name || 'Buyer'}: ${STATE_WORD[b.state] || b.state}`);
  tip(t, () => buyerTip(b));
  t.addEventListener('click', () => openBuyerSheet(v, b.key));
  return t;
}

// The other open listings for the same meal, cheapest first, yours in gold.
function marketRow(v) {
  if (!D || v.state !== 'active') return null;
  const mine = new Set(v.listings);
  const ls = D.listings.filter((l) => l.kind !== 'buy' && l.mess === v.mess && l.meal === mealOf(v) && l.meal_date === v.date
    && l.status === 'available' && (!v.diet || !l.diet || l.diet === v.diet) && (l.price != null || mine.has(l.id)));
  if (ls.length < 2) return null;
  ls.sort((a, b) => (a.price ?? 1e9) - (b.price ?? 1e9));
  const row = el('div', 'ms-market');
  for (const l of ls.slice(0, 8)) {
    const m = el('button', 'ms-l' + (mine.has(l.id) ? ' me' : ''), l.price != null ? String(l.price) : '?');
    m.type = 'button';
    tip(m, () => itemTip(l));
    m.addEventListener('click', () => openListing(l));
    row.append(m);
  }
  return row;
}

// tickSales moves the clocks, pay rings and countdowns between fetches.
function tickSales() {
  const n = now();
  for (const a of document.querySelectorAll('#sales circle[data-until], .sheet circle[data-until]')) {
    const from = +a.dataset.from, until = +a.dataset.until, len = +a.dataset.len;
    const frac = Math.min(1, Math.max(0, (until - n) / Math.max(1, until - from)));
    a.setAttribute('stroke-dashoffset', (len * (1 - frac)).toFixed(1));
  }
  for (const t of document.querySelectorAll('#sales [data-fmt], .sheet [data-fmt]')) {
    const left = Math.max(0, +t.dataset.until - n);
    if (t.dataset.fmt === 'mmss') {
      t.textContent = `${Math.floor(left / 60)}:${String(Math.floor(left % 60)).padStart(2, '0')}`;
    } else {
      const m = Math.ceil(left / 60);
      t.replaceChildren(m >= 100 ? String(Math.round(m / 60)) : String(m), el('small', '', m >= 100 ? 'H' : 'M'));
    }
  }
}
setInterval(() => { if (document.visibilityState === 'visible') tickSales(); }, 1000);

// ---------- sheets ----------

function sheetFor(v, keep) {
  hideTip();
  const scroll = keep ? sheet.scrollTop : 0;
  sheet.replaceChildren();
  sheet.classList.add('sale-sheet');
  modal.hidden = false;
  return () => { sheet.scrollTop = scroll; };
}

function chatLink(b) {
  const ph = digits(b.phone);
  if (!ph) return null;
  return messagingApp() === 'beeper' ? `beeper://compose/whatsapp/phone/+${ph}` : `https://wa.me/${ph}`;
}

function openBuyerSheet(v, key, keep) {
  const b = v.buyers.find((x) => x.key === key);
  if (!b) return;
  saleSheet = { id: v.id, key };
  const restore = sheetFor(v, keep);
  const l = look(b, v);

  const head = el('div', 'sb-head');
  const f = el('span', 'sb-face ' + l.cls);
  f.append(svgEl(face(b.key)));
  const who = el('div', 'sb-who');
  const nm = el('div', 'sb-name', b.name || b.phone || 'Buyer');
  if (b.saved) nm.append(el('span', 'hrt', ' ♥'));
  who.append(nm);
  const chips = el('div', 'sb-chips');
  if (l.cls === 'need') {
    const [ic, word] = reason(b);
    chips.append(el('span', 'sb-chip red', `${ic} ${word}`));
  } else {
    chips.append(el('span', 'sb-chip', (STATE_WORD[b.state] || b.state).toUpperCase()));
  }
  if (sentPicture(b)) chips.append(el('span', 'sb-chip egg', '🖼 PICTURE'));
  who.append(chips);
  head.append(f, who);
  if (b.state === 'offered' && b.pay_until) {
    const cd = el('b', 'sb-count');
    cd.dataset.until = b.pay_until;
    cd.dataset.fmt = 'mmss';
    head.append(cd);
  } else if (b.agreed || b.asked) {
    head.append(el('b', 'sb-price', rup(b.agreed || b.asked)));
  }
  sheet.append(head);

  if (v.negotiates && b.events.some((e) => e.kind === 'read' || e.kind === 'bot_said')) sheet.append(tugOfWar(b, v));
  if (b.events.some((e) => e.kind === 'prompt') || (v.pay && v.pay.buyer === b.key)) sheet.append(payPips(b, v));
  if (b.note) sheet.append(el('div', 'sb-note', b.note));

  const acts = el('div', 'sb-acts');
  const link = chatLink(b);
  const chat = el(link ? 'a' : 'span', 'sb-act grn');
  chat.append(el('b', '', '💬'), el('span', '', 'CHAT'));
  if (link) {
    chat.href = link;
    if (link.startsWith('https:')) { chat.target = '_blank'; chat.rel = 'noopener noreferrer'; }
  } else {
    chat.classList.add('off');
  }
  acts.append(chat);
  if (b.phone) {
    const copy = el('button', 'sb-act');
    copy.append(el('b', '', '📋'), el('span', '', 'NUMBER'));
    copy.addEventListener('click', () => navigator.clipboard?.writeText(b.phone).then(() => toast('Number copied'), () => toast(b.phone)));
    acts.append(copy);
  }
  if (v.state === 'active') {
    acts.append(saleActionButton('✓', 'MARK SOLD', () => saleActionForm(v, 'sold', b)));
    acts.append(saleActionButton(b.muted ? '🔈' : '🔇', b.muted ? 'UNMUTE' : 'MUTE', () => saleActionForm(v, b.muted ? 'unmute' : 'mute', b)));
  }
  const close = el('button', 'sb-act');
  close.append(el('b', '', '✕'), el('span', '', 'CLOSE'));
  close.addEventListener('click', closeModal);
  acts.append(close);
  sheet.append(acts);
  tickSales();
  restore();
}

// READ glyphs: what the bot read, never what they wrote.
const READ = { opener: '?', question: '?', ok: '✓', accept: '✓', lower: '↓', pay_other: '₹?', paid: '💸', pass: '✕', unknown: '…' };

// tugOfWar draws the negotiation round by round: their offer in red, the
// bot's answer in green, and at the bottom where the bot's lowest is now.
function tugOfWar(b, v) {
  const rows = [];
  let row = null;
  for (const e of b.events) {
    if (e.kind === 'read') {
      row = { them: e.text.startsWith('offer') ? e.n : null, glyph: READ[e.text] || '…', at: e.at };
      rows.push(row);
    } else if (e.kind === 'bot_said' || e.kind === 'agreed') {
      if (!row || row.bot != null) { row = { glyph: '?', at: e.at }; rows.push(row); }
      row.bot = e.n;
      row.lock = row.lock || /lowest/.test(e.text || '');
      row.deal = row.deal || e.kind === 'agreed';
    } else if (e.kind === 'parked' && row) {
      row.stop = true;
    }
  }
  const shown = rows.slice(-6);
  const prices = [v.min, v.max, v.lowest, ...shown.flatMap((r) => [r.them, r.bot]).filter((p) => p != null)];
  const lo = Math.floor(Math.min(...prices) / 5) * 5, hi = Math.ceil(Math.max(...prices) / 5) * 5;
  const W = 300, X0 = 24, X1 = 286, H = 22 + shown.length * 30 + 36;
  const x = (p) => X0 + (hi === lo ? 0.5 : (p - lo) / (hi - lo)) * (X1 - X0);
  const s = sv('svg', { viewBox: `0 0 ${W} ${H}`, width: '100%', class: 'tug' });
  const ny = 12 + shown.length * 30 + 4;
  s.append(sv('rect', { x: x(v.lowest), y: 4, width: X1 - x(v.lowest), height: ny + 6, fill: C.veg, 'fill-opacity': 0.06 }));
  s.append(sv('line', { x1: x(v.min), x2: x(v.min), y1: 4, y2: ny + 10, stroke: C.red, 'stroke-opacity': 0.5, 'stroke-dasharray': '3 3' }));
  if (v.target > v.min && v.target < v.max) s.append(sv('line', { x1: x(v.target), x2: x(v.target), y1: 4, y2: ny + 10, stroke: C.gold, 'stroke-opacity': 0.5, 'stroke-dasharray': '3 3' }));
  const pill = (cx, y, text, fg, bg, w = 30) => {
    s.append(sv('rect', { x: cx - w / 2, y: y - 9, width: w, height: 18, rx: 5, fill: bg, stroke: fg }));
    s.append(sv('text', { x: cx, y: y + 3.5, 'text-anchor': 'middle', fill: fg, class: 'tug-n' }, text));
  };
  shown.forEach((r, i) => {
    const y = 20 + i * 30;
    s.append(sv('text', { x: 4, y: y + 3, fill: C.dim, class: 'tug-i' }, String(rows.length - shown.length + i + 1)));
    if (r.them != null && r.bot != null) s.append(sv('line', { x1: x(r.them), x2: x(r.bot), y1: y, y2: y, stroke: '#3b3f74', 'stroke-width': 2, 'stroke-dasharray': '2 3' }));
    if (r.them != null) pill(x(r.them), y, String(r.them), C.red, '#2a1418', 28);
    else {
      s.append(sv('circle', { cx: X0, cy: y, r: 8, fill: 'none', stroke: '#b8c6ff', 'stroke-dasharray': '2 2' }));
      s.append(sv('text', { x: X0, y: y + 3, 'text-anchor': 'middle', fill: '#b8c6ff', class: 'tug-n' }, r.glyph));
    }
    if (r.bot != null) pill(x(r.bot), y, (r.lock ? '🔒' : r.deal ? '✓' : '') + r.bot, C.veg, r.deal ? '#1d4a2c' : '#13301f', r.lock || r.deal ? 38 : 30);
    if (r.stop) s.append(sv('text', { x: X1, y: y + 3, 'text-anchor': 'end', fill: C.red, class: 'tug-n' }, '!'));
  });
  // Now: the bot's lowest, and the gap to their last offer.
  if (v.state === 'active') {
    const y = ny;
    if (b.offer && b.offer < v.lowest) s.append(sv('rect', { x: x(b.offer), y: y - 4, width: x(v.lowest) - x(b.offer), height: 8, fill: C.red, 'fill-opacity': 0.6 }));
    s.append(sv('rect', { x: x(v.lowest), y: y - 4, width: X1 - x(v.lowest), height: 8, rx: 2, fill: C.veg, 'fill-opacity': 0.35 }));
    s.append(sv('circle', { cx: x(v.lowest), cy: y, r: 5, fill: v.held ? C.gold : C.veg }));
    s.append(sv('text', { x: x(v.lowest) + 8, y: y - 7, fill: v.held ? C.gold : C.veg, class: 'tug-n' }, `${v.held ? '🔒' : ''}${v.lowest} NOW`));
    s.append(sv('text', { x: 4, y: y + 3, fill: C.veg, class: 'tug-i' }, '▸'));
  }
  for (let p = lo; p <= hi; p += Math.max(5, Math.ceil((hi - lo) / 5 / 5) * 5)) {
    const col = p === v.min ? C.red : p === v.target ? C.gold : C.dim;
    s.append(sv('text', { x: x(p), y: H - 4, 'text-anchor': 'middle', fill: col, class: 'tug-i' }, String(p)));
  }
  const box = el('div', 'sb-tug');
  box.append(s);
  const key = el('div', 'sb-key');
  for (const [cls, t] of [['red', 'THEM'], ['grn', 'BOT'], ['gold', 'TARGET'], ['red dash', 'FLOOR']]) key.append(el('span', cls, t));
  box.append(key);
  return box;
}

// payPips: armed, paid, read, QR, ✅ for one buyer's turn.
function payPips(b, v) {
  const mine = v.pay && v.pay.buyer === b.key;
  const paid = has(b, 'payment') || (mine && v.pay.paid);
  const unreadable = has(b, 'pay_unreadable');
  const rec = last(b, 'recovery');
  const sent = has(b, 'qr_sent') || (mine && v.pay.state === 'sent');
  const steps = [
    ['🔒', 'ARMED', has(b, 'prompt') || mine ? 'ok' : ''],
    ['₹', 'PAID', paid || unreadable ? 'ok' : sentPicture(b) ? 'warn' : b.state === 'timed_out' ? 'bad' : ''],
    ['👁', 'READ', paid ? 'ok' : unreadable ? (b.state === 'offered' ? 'go' : 'bad') : ''],
    ['▣', 'QR', sent ? 'ok' : ''],
    ['✅', 'TICK', sent && v.ticked ? 'ok' : ''],
  ];
  const box = el('div', 'pips');
  steps.forEach(([ic, word, st], i) => {
    const p = el('div', 'pip ' + st);
    p.append(el('span', 'c', ic));
    if (i === 2 && unreadable && !paid) {
      const t = el('span', 'tries');
      for (let k = 1; k <= 6; k++) t.append(el('i', rec && k <= rec.n ? 'on' : ''));
      p.append(t);
    } else {
      p.append(el('span', 'l', word));
    }
    box.append(p);
  });
  return box;
}

// postList is the sheet's POSTS: each group with every copy under it.
function postList(v) {
  const posts = el('div', 'mp-posts');
  for (const g of postGroups(v)) {
    const h = el('div', 'mp-group');
    h.append(el('b', g.cur.state === 'failed' ? 'red' : g.cur.state === 'posted' ? 'grn' : '', postMark(g.cur)), el('span', 'name', g.name));
    if (g.copies.length > 1) h.append(el('span', 'times', `×${g.copies.length}`));
    posts.append(h);
    g.copies.forEach((p, i) => {
      const r = el('div', 'mp-post' + (p === g.cur ? '' : ' old'));
      r.append(el('span', 'when', hm(p.at)));
      const bar = el('span', 'mp-bar' + (p.state === 'failed' ? ' failed' : ''));
      if (p.state === 'posted') {
        const me = el('i', 'me'), after = el('i', 'after' + (p.after > BURIED ? ' red' : ''));
        after.style.setProperty('width', `${Math.min(1, p.after / 25) * 96}%`);
        bar.append(me, after);
      }
      r.append(bar, el('span', 'n' + (p.state === 'failed' || p.after > BURIED ? ' red' : ''),
        p.state === 'failed' ? errCode(p.error) : p.state === 'posting' ? '…' : `${p.after}↑`));
      if (p.resend) r.append(resendButton(v, p));
      tip(r, () => [[{ t: g.name, c: 'nm' }], copyLine(p, i)]);
      posts.append(r);
    });
  }
  return posts;
}

function openMealSheet(v, _key, keep) {
  saleSheet = { id: v.id, meal: true };
  const restore = sheetFor(v, keep);
  const top = el('div', 'ms-top');
  const dot = el('span', 'ms-mess');
  dot.style.setProperty('--m', messColor(v.mess));
  const diet = el('span', 'ms-diet');
  diet.append(svgEl(dietMark(v.diet)));
  top.append(dot, diet, el('span', 'ms-meal', `${v.mess.toUpperCase()} ${mealLabel(v).toUpperCase()}`));
  if (v.state === 'active') {
    const cd = el('b', 'sb-count small');
    cd.dataset.until = v.ends;
    cd.dataset.fmt = 'mins';
    top.append(cd);
  } else {
    top.append(el('span', 'sb-chip ' + (won(v) ? 'grn' : ''), v.state.replace('_', ' ').toUpperCase()));
  }
  sheet.append(top);
  sheet.append(priceChart(v));
  if (v.state === 'active') sheet.append(saleTaskActions(v));

  sheet.append(el('div', 'sec', 'POSTS'));
  sheet.append(postList(v));

  sheet.append(el('div', 'sec', `${v.history.length} ${v.history.length === 1 ? 'TRY' : 'TRIES'}`));
  const tries = el('div', 'mp-tries');
  for (const x of v.history) {
    const t = el('div', 'mp-try h-' + x.state + (x.id === v.id ? ' cur' : ''), `${x.state === 'active' ? '' : x.state === 'sold' ? '✓' : '✕'}${x.id}`);
    tip(t, () => [[{ t: `#${x.id} ${x.state.replace('_', ' ')}`, c: 'nm' }], [{ t: `${hm(x.started)}–${x.ended ? hm(x.ended) : ''} · ${x.buyers} buyers`, c: 'c8' }]]);
    tries.append(t);
  }
  sheet.append(tries);
  if (v.detail && v.state !== 'active') sheet.append(el('div', 'sb-note', v.detail));

  const acts = el('div', 'sb-acts');
  const close = el('button', 'sb-act');
  close.append(el('b', '', '✕'), el('span', '', 'CLOSE'));
  close.addEventListener('click', closeModal);
  acts.append(close);
  sheet.append(acts);
  tickSales();
  restore();
}

// walk is market.Walk: the lowest price the bot takes at t when nobody else
// is interested.
function walk(v, t) {
  const unit = v.max < 20 ? 1 : 5;
  const frac = Math.min(1, Math.max(0, (v.ends - t) / (3 * 3600)));
  const w = v.min + Math.ceil(frac * (v.target - v.min) / unit) * unit;
  return Math.min(Math.max(w, v.min), v.target);
}

// priceChart: the bot's lowest over the task, offers as dots, now marked.
function priceChart(v) {
  const t0 = Math.min(v.started, v.ends - 3 * 3600), t1 = v.ends;
  const offers = v.buyers.flatMap((b) => b.events.filter((e) => e.kind === 'read' && e.n).map((e) => ({ at: e.at, n: e.n, b })));
  const lo = Math.floor(Math.min(v.min, ...offers.map((o) => o.n)) / 5) * 5, hi = v.max;
  const W = 316, H = 118, X0 = 8, X1 = 308, Y0 = 10, Y1 = 100;
  const x = (t) => X0 + (t - t0) / Math.max(1, t1 - t0) * (X1 - X0);
  const y = (p) => Y1 - (p - lo) / Math.max(1, hi - lo) * (Y1 - Y0);
  const s = sv('svg', { viewBox: `0 0 ${W} ${H}`, width: '100%', class: 'chart' });
  for (const [p, col] of [[v.target, C.gold], [v.min, C.red]]) {
    s.append(sv('line', { x1: X0, x2: X1, y1: y(p), y2: y(p), stroke: col, 'stroke-opacity': 0.6, 'stroke-dasharray': '3 3' }));
    s.append(sv('text', { x: X1, y: y(p) - 3, 'text-anchor': 'end', fill: col, class: 'tug-i' }, String(p)));
  }
  const n = Math.min(now(), t1);
  const pts = (a, b) => { const out = []; for (let t = a; t <= b; t += 60) out.push(`${x(t).toFixed(1)},${y(walk(v, t)).toFixed(1)}`); return out.join(' '); };
  if (v.negotiates) {
    s.append(sv('polyline', { points: pts(t0, n), fill: 'none', stroke: '#e8ecf3', 'stroke-width': 2 }));
    s.append(sv('polyline', { points: pts(n, t1), fill: 'none', stroke: '#e8ecf3', 'stroke-opacity': 0.3, 'stroke-width': 2, 'stroke-dasharray': '2 3' }));
  }
  s.append(sv('line', { x1: x(v.started), x2: x(v.started), y1: Y0, y2: Y1, stroke: C.dim, 'stroke-dasharray': '1 2' }));
  for (const o of offers) {
    const l = look(o.b, v);
    s.append(sv('circle', { cx: x(o.at), cy: y(o.n), r: 4, fill: l.cls === 'need' || l.cls === 'gone' ? 'none' : l.tone, stroke: l.tone, 'stroke-width': 2 }));
  }
  if (v.state === 'active') {
    s.append(sv('circle', { cx: x(n), cy: y(v.lowest), r: 4.5, fill: v.held ? C.gold : C.veg }));
    s.append(sv('text', { x: x(n), y: H - 4, 'text-anchor': 'middle', fill: C.veg, class: 'tug-i' }, 'NOW'));
  }
  s.append(sv('text', { x: X0, y: H - 4, fill: C.dim, class: 'tug-i' }, hm(t0)));
  s.append(sv('text', { x: X1, y: H - 4, 'text-anchor': 'end', fill: C.dim, class: 'tug-i' }, hm(t1)));
  return s;
}


// Operator forms stay open across live updates, preserving entered values.
function saleActionButton(glyph, label, action) {
  const b = el('button', 'sb-act');
  b.type = 'button';
  b.append(el('b', '', glyph), el('span', '', label));
  b.addEventListener('click', action);
  return b;
}
function saleTaskActions(v) {
  const acts = el('div', 'sb-acts');
  acts.append(saleActionButton('✓', 'MARK SOLD', () => saleActionForm(v, 'sold')),
    saleActionButton('+', 'ADD BUYER', () => saleActionForm(v, 'add')));
  return acts;
}
function saleActionForm(v, action, buyer) {
  saleSheet = { id: v.id, editing: true };
  sheetFor(v, false);
  const titles = { sold: 'MARK SOLD MANUALLY', add: 'ADD BUYER', mute: 'MUTE BUYER', unmute: 'UNMUTE BUYER' };
  sheet.append(el('div', 'sb-name', titles[action]));
  const form = el('form', 'sale-action-form');
  const fields = {};
  const field = (key, label, value = '', type = 'text') => {
    const wrap = el('label', '', label), input = el('input');
    input.type = type;
    input.value = value;
    input.name = key;
    fields[key] = input;
    wrap.append(input);
    form.append(wrap);
    return input;
  };
  let pick;
  if (action === 'sold') {
    const wrap = el('label', '', 'Buyer');
    pick = el('select');
    pick.append(new Option('Other buyer (phone or name below)', ''));
    for (const b of v.buyers) pick.append(new Option(b.name || b.phone || b.key, b.key));
    pick.value = buyer ? buyer.key : '';
    wrap.append(pick);
    form.append(wrap);
  }
  if (action === 'sold' || action === 'add') {
    field('buyer', 'Phone with country code or JID').required = action === 'add';
    field('name', 'Name (optional with phone)');
  }
  if (action === 'sold') {
    const price = field('price', 'Sale price (₹)', buyer ? buyer.agreed || buyer.asked || buyer.offer || '' : '', 'number');
    price.min = '1'; price.max = '10000'; price.step = '1'; price.required = true;
    field('note', 'Note (optional)');
    form.append(el('p', 'sb-note', 'Confirm only after you sold the meal. This stops negotiation and records it as paid and sent. The buyer receives no message. Other buyers get the usual sold notice.'));
  } else if (action === 'add') {
    form.append(el('p', 'sb-note', 'The bot will send this buyer its opening quote.'));
  } else {
    form.append(el('p', 'sb-note', `${buyer.name || buyer.phone || buyer.key}: ${action === 'mute' ? 'stop all bot messages for this task, including sold notices?' : 'allow bot messages for this task again?'}`));
  }
  const error = el('p', 'sb-note');
  error.setAttribute('role', 'alert');
  const acts = el('div', 'sb-acts');
  const submit = saleActionButton('✓', action === 'sold' || action === 'mute' ? 'CONFIRM' : titles[action], () => {});
  submit.type = 'submit';
  acts.append(submit, saleActionButton('✕', 'CANCEL', () => buyer ? openBuyerSheet(v, buyer.key) : openMealSheet(v)));
  form.append(error, acts);
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = { action, id: v.id };
    for (const [k, input] of Object.entries(fields)) data[k] = input.value.trim();
    if (pick && pick.value) { data.buyer = pick.value; data.name = ''; }
    if (buyer && (action === 'mute' || action === 'unmute')) data.buyer = buyer.key;
    if (action === 'sold') {
      data.price = Number(data.price);
      if (!data.buyer && !data.name) { error.textContent = 'Enter a buyer phone or name.'; return; }
    }
    submit.disabled = true;
    error.textContent = '';
    try {
      await send('POST', 'api/sell-action', data);
      closeModal();
      toast('Action saved');
      await load();
    } catch (err) { error.textContent = err.message; submit.disabled = false; }
  });
  sheet.append(form);
}
