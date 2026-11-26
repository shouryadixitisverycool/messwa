'use strict';

// The Money tab's top half: a jar that fills toward the month's mess bill,
// the Sunday recap card, and the pills for recurring rules and friends'
// meals. sales.js owns the data (D) and calls renderMoney() after each load.
// Buyer names are WhatsApp push names, so they only go in through
// textContent.

const MONEY_STILL = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
const SVGNS = 'http://www.w3.org/2000/svg';
function svgNode(tag, attrs) {
  const n = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, String(v));
  return n;
}

// ---------- the jar ----------

// The jar's inside, in viewBox units. Coins lie flat in rows, each row
// nudged so they pile instead of stacking in columns.
const JAR = { x0: 26, x1: 134, floor: 172, top: 52, rx: 9, ry: 3.6, row: 6.2 };

// jarSpots are where coins come to rest, bottom row first. A small seeded
// jitter keeps the pile from looking like a grid.
const jarSpots = (() => {
  let seed = 11;
  const rnd = () => { seed = (seed * 16807) % 2147483647; return (seed - 1) / 2147483646; };
  const out = [];
  const step = JAR.rx * 2 - 1.5;
  for (let r = 0, y = JAR.floor - JAR.ry; y > JAR.top; r++, y -= JAR.row) {
    const off = r % 2 ? step / 2 : 0;
    for (let x = JAR.x0 + JAR.rx + off; x <= JAR.x1 - JAR.rx; x += step) {
      out.push({ x: x + (rnd() - 0.5) * 3, y: y + (rnd() - 0.5) * 1.6, a: (rnd() - 0.5) * 22 });
    }
  }
  return out;
})();

let jarShown = 0;        // coins on screen
let jarEarned = null;    // earned at the last render, to spot a new sale
let jarNumber = 0;       // the amount on screen, for counting up
let jarRaf = 0;
const jarFlying = new Set();

// jarCoins is how many coins a month fills the jar with: earned against the
// bill, or against everything it could have made when there is no bill.
function jarCoins(M) {
  const scale = M.fee > 0 ? M.fee : M.earned + M.potential;
  if (!scale || !M.earned) return 0;
  return Math.max(1, Math.min(jarSpots.length, Math.round((M.earned / scale) * jarSpots.length)));
}

function jarSvg() {
  const svg = svgNode('svg', { viewBox: '0 0 160 190', class: 'jar-svg', 'aria-hidden': 'true' });
  const defs = svgNode('defs');
  const clip = svgNode('clipPath', { id: 'jarIn' });
  clip.append(svgNode('path', { d: 'M24 40 h112 a12 12 0 0 1 12 12 v108 a18 18 0 0 1 -18 18 h-100 a18 18 0 0 1 -18 -18 v-108 a12 12 0 0 1 12 -12z' }));
  const g = svgNode('linearGradient', { id: 'coinG', x1: 0, y1: 0, x2: 0, y2: 1 });
  g.append(svgNode('stop', { offset: '0', 'stop-color': '#f6d27a' }), svgNode('stop', { offset: '1', 'stop-color': '#c8902e' }));
  defs.append(clip, g);
  svg.append(defs);
  // Glass: body, neck, lid.
  svg.append(svgNode('path', { class: 'jar-glass', d: 'M24 40 h112 a12 12 0 0 1 12 12 v108 a18 18 0 0 1 -18 18 h-100 a18 18 0 0 1 -18 -18 v-108 a12 12 0 0 1 12 -12z' }));
  const pile = svgNode('g', { class: 'jar-pile', 'clip-path': 'url(#jarIn)' });
  svg.append(pile);
  svg.append(svgNode('rect', { class: 'jar-lid', x: 34, y: 22, width: 92, height: 14, rx: 5 }));
  svg.append(svgNode('path', { class: 'jar-shine', d: 'M30 64 v80' }));
  // Coins fall outside the clip until they are inside the jar.
  svg.append(svgNode('g', { class: 'jar-air' }));
  return svg;
}

function coinNode() {
  const c = svgNode('g', { class: 'coin' });
  c.append(svgNode('ellipse', { class: 'coin-edge', rx: JAR.rx, ry: JAR.ry, cy: 1.4 }),
    svgNode('ellipse', { class: 'coin-face', rx: JAR.rx, ry: JAR.ry }),
    svgNode('ellipse', { class: 'coin-ring', rx: JAR.rx - 3, ry: JAR.ry - 1.3 }));
  return c;
}
const placeCoin = (c, x, y, a, squash = 1) => c.setAttribute('transform', `translate(${x.toFixed(1)} ${y.toFixed(1)}) rotate(${a.toFixed(1)}) scale(1 ${squash.toFixed(2)})`);

// dropCoin drops coin i into the jar: it falls from the lid, spinning, under
// gravity, drifts toward its spot, and bounces twice before it settles.
function dropCoin(svg, i, delay) {
  const s = jarSpots[i];
  const pile = svg.querySelector('.jar-pile'), air = svg.querySelector('.jar-air');
  const c = coinNode();
  if (MONEY_STILL()) { placeCoin(c, s.x, s.y, s.a); pile.append(c); return; }
  const x0 = 62 + ((i * 37) % 36), y0 = 6;
  const g = 0.0016;                       // viewBox units per ms²
  const fall = Math.sqrt((2 * (s.y - y0)) / g);
  const v = g * fall;
  const b1 = (2 * v * 0.28) / g, b2 = (2 * v * 0.1) / g;
  const spin = 0.018 + ((i * 13) % 7) / 1000;
  const f = { c, start: 0, delay, done: false };
  f.step = (t) => {
    const e = t - f.start - f.delay;
    if (e < 0) return;
    if (!c.parentNode) air.append(c);
    if (e < fall) {
      const k = e / fall;
      const y = y0 + 0.5 * g * e * e;
      // Once below the lid, the coin is inside and the glass hides its edges.
      if (y > 42 && c.parentNode !== pile) pile.append(c);
      placeCoin(c, x0 + (s.x - x0) * k * k, y, s.a * k + e * spin * 57, 0.35 + 0.65 * Math.abs(Math.cos(e * spin)));
      return;
    }
    const u = e - fall;
    if (c.parentNode !== pile) pile.append(c);
    if (u < b1) { const w = v * 0.28; placeCoin(c, s.x, s.y - (w * u - 0.5 * g * u * u), s.a); return; }
    if (u < b1 + b2) { const w = v * 0.1, q = u - b1; placeCoin(c, s.x, s.y - (w * q - 0.5 * g * q * q), s.a); return; }
    placeCoin(c, s.x, s.y, s.a);
    f.done = true;
  };
  jarFlying.add(f);
  if (!jarRaf) jarRaf = requestAnimationFrame(jarTick);
}

function jarTick(t) {
  for (const f of jarFlying) {
    if (!f.start) f.start = t;
    f.step(t);
    if (f.done) jarFlying.delete(f);
  }
  jarRaf = jarFlying.size ? requestAnimationFrame(jarTick) : 0;
}

// countTo rolls the big number from what it shows to v.
function countTo(node, v) {
  const from = jarNumber;
  jarNumber = v;
  if (MONEY_STILL() || from === v) { node.textContent = rupees(Math.round(v)); return; }
  const t0 = performance.now(), dur = Math.min(1400, 500 + Math.abs(v - from));
  const tick = (t) => {
    const k = Math.min(1, (t - t0) / dur);
    const e = 1 - Math.pow(1 - k, 3);
    node.textContent = rupees(Math.round(from + (v - from) * e));
    if (k < 1) requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
}

function renderJar() {
  const M = D.month;
  const box = $('jar');
  let svg = box.querySelector('.jar-svg');
  if (!svg) { svg = jarSvg(); $('jarArt').replaceChildren(svg); }

  if (!D.owner) {
    // Members don't sell through messwa: the jar shows nothing, the number
    // shows what went to waste.
    box.classList.add('member');
    $('jarK').textContent = `Wasted in ${fmt(M.start, { month: 'long' })}`;
    $('jarV').textContent = rupees(Math.round(M.potential));
    $('jarOf').textContent = M.wasted ? `${M.wasted} meal${M.wasted === 1 ? '' : 's'}` : '';
    return;
  }
  box.classList.remove('member');
  $('jarK').textContent = fmt(M.start, { month: 'long' });
  countTo($('jarV'), M.earned);
  $('jarOf').textContent = M.fee > 0 ? `of ${rupees(M.fee)}` : '';
  box.classList.toggle('full', M.fee > 0 && M.earned >= M.fee);
  box.style.setProperty('--fill', String(M.fee > 0 ? Math.min(1, M.earned / M.fee) : 0));

  const want = jarCoins(M);
  const pile = svg.querySelector('.jar-pile');
  if (want < jarShown) {
    // A deleted sale or a new month: take coins off the top.
    while (pile.childNodes.length > want) pile.lastChild.remove();
    jarShown = want;
  }
  const first = jarEarned === null;
  const fresh = !first && M.earned > jarEarned;
  if (want > jarShown) {
    const n = want - jarShown;
    // Arrival fills the jar in about a second, however many coins it takes.
    const gap = first ? Math.min(60, 1100 / n) : 140;
    for (let i = 0; i < n; i++) dropCoin(svg, jarShown + i, i * gap);
    jarShown = want;
  } else if (fresh && want) {
    // A sale too small to add a coin still drops one onto the pile.
    pile.lastChild && pile.lastChild.remove();
    dropCoin(svg, want - 1, 0);
  }
  jarEarned = M.earned;
  renderSpark();
}

// renderSpark is the four weeks' takings under the jar, a bar a day.
function renderSpark() {
  const box = $('jarSpark');
  const days = [];
  for (let d = D.recent; d <= D.today; d = addDays(d, 1)) days.push(d);
  const by = new Map(days.map((d) => [d, []]));
  for (const s of recentSales()) by.get(s.date)?.push(s);
  const vals = days.map((d) => sum(by.get(d).map((s) => s.amount)));
  const mx = Math.max(1, ...vals);
  box.replaceChildren();
  box.style.setProperty('--n', String(days.length));
  days.forEach((d, i) => {
    const b = el('i', vals[i] ? '' : 'zero');
    b.style.setProperty('--h', String(vals[i] / mx));
    if (dayOf(d).getUTCDay() === 1) b.classList.add('mon');
    tip(b, () => {
      const ss = by.get(d);
      return [
        [{ t: fmtDay(d), c: 'nm' }],
        ...(ss.length ? ss.map((s) => [{ t: `${cap(saleWhat(s))} `, c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ` · ${s.name || fmtPhone(s.phone) || 'Unknown buyer'}`, c: 'c8' }]) : [[{ t: 'No sales', c: 'c8' }]]),
        ss.length > 1 ? [{ t: 'Total ', c: 'c7' }, { t: rupees(vals[i]), c: 'cf' }] : null,
      ];
    });
    box.append(b);
  });
  box.setAttribute('aria-label', `Sales over the last four weeks: ${rupees(sum(vals))}`);
}

// ---------- the Sunday recap ----------

let recapWeeks = null;   // api/recap weeks, newest first
let recapAt = 0;         // which week is showing
let recapLoaded = 0;

async function loadRecap() {
  if (!D || !D.owner || Date.now() - recapLoaded < 5 * 60e3) return;
  recapLoaded = Date.now();
  try {
    const r = await fetch('api/recap', { cache: 'no-store' });
    if (!r.ok) throw new Error(r.status);
    const j = await r.json();
    recapWeeks = j.owner ? j.weeks || [] : [];
  } catch (e) {
    recapLoaded = 0;
    recapWeeks = recapWeeks || [];
  }
  renderRecap();
}

const weekLabel = (w) => `${fmtShort(w.start)} to ${fmtShort(w.end)}`;

function slide(cls, ...kids) {
  const s = el('div', 'rslide ' + cls);
  s.append(...kids);
  return s;
}

// The first slide: how many spare meals sold, as a row of coins per meal.
function soldSlide(w) {
  const big = el('div', 'rbig');
  big.append(el('b', '', String(w.sold)), el('span', '', ` of ${w.spare}`));
  const line = el('p', 'rline', w.spare ? 'spare meals sold' : 'No spare meals this week');
  const rows = el('div', 'rmeals');
  for (const m of w.by_meal || []) {
    const r = el('div', 'rmeal');
    r.title = cap(m.meal);
    r.append(svgEl(icon(m.meal)));
    const dots = el('span', 'rdots');
    for (let i = 0; i < m.spare; i++) dots.append(el('i', i < m.sold ? 'on' : ''));
    r.append(dots);
    rows.append(r);
  }
  const foot = el('div', 'rfoot');
  if (w.earned) foot.append(el('b', '', `+${rupees(Math.round(w.earned))}`));
  if (w.extras_sold) foot.append(el('span', '', `${w.extras_sold} extra${w.extras_sold === 1 ? '' : 's'}`));
  if (w.unknown) foot.append(el('span', 'warn', `${w.unknown} unread`));
  return slide('s-sold', big, line, rows, foot);
}

// The market slide: the week against the market, then four weeks of price
// check underneath.
function marketSlide(w) {
  const d = w.vs_market;
  const big = el('div', 'rbig');
  if (d == null) big.append(el('span', 'quiet', 'No market asks'));
  else if (Math.abs(d) < 1) big.append(el('b', '', 'At market'));
  else big.append(el('b', d > 0 ? 'up' : 'down', rupees(Math.round(Math.abs(d)))), el('span', '', d > 0 ? ' above market' : ' below market'));
  const plot = el('div', 'pplot');
  plot.id = 'pplot';
  const cap4 = el('p', 'rnote', 'Last 4 weeks');
  return slide('s-market', big, plot, cap4);
}

// The regulars slide: the week's top buyer, then everyone who came back.
function regularSlide(w) {
  const b = w.top_buyer;
  const kids = [];
  if (b) {
    const who = el('div', 'rwho');
    const f = el('span', 'rface');
    f.append(svgEl(face(b.key || b.phone || b.name || '')));
    const nm = el('div', 'rname');
    nm.append(el('b', '', b.name || fmtPhone(b.phone) || 'Unknown buyer'));
    nm.append(el('span', '', `${b.week}× this week${b.all_time > b.week ? ` · ${b.all_time}× in all` : ''}`));
    who.append(f, nm);
    kids.push(el('p', 'rline', 'Your regular'), who);
  } else {
    kids.push(el('div', 'rbig'), el('p', 'rline', 'No sales this week'));
    kids[0].append(el('span', 'quiet', 'Quiet week'));
  }
  const heads = el('div', 'rheads');
  heads.id = 'heads';
  kids.push(heads);
  return slide('s-reg', ...kids);
}

function renderRecap() {
  const card = $('recap');
  const ws = recapWeeks || [];
  card.hidden = !D.owner || !ws.length;
  if (card.hidden) return;
  recapAt = Math.max(0, Math.min(recapAt, ws.length - 1));
  const w = ws[recapAt];
  $('recapWeek').textContent = recapAt === 0 ? 'Last week' : weekLabel(w);
  $('recapWeek').title = weekLabel(w);
  $('recapOlder').disabled = recapAt >= ws.length - 1;
  $('recapNewer').disabled = recapAt === 0;

  const track = $('recapTrack');
  const keep = track.scrollLeft;
  track.replaceChildren(soldSlide(w), marketSlide(w), regularSlide(w));
  track.scrollLeft = keep;
  renderPrices();
  renderHeads();
  const dots = $('recapDots');
  dots.replaceChildren();
  [...track.children].forEach((s, i) => {
    const b = el('button', 'rdot');
    b.type = 'button';
    b.setAttribute('aria-label', `Slide ${i + 1} of ${track.children.length}`);
    b.addEventListener('click', () => track.scrollTo({ left: s.offsetLeft - track.offsetLeft, behavior: MONEY_STILL() ? 'auto' : 'smooth' }));
    dots.append(b);
  });
  recapDot();
}

// recapDot marks the slide on screen and fits the card to it, so a short
// slide doesn't sit in a tall one's space.
function recapDot() {
  const track = $('recapTrack');
  const i = Math.round(track.scrollLeft / Math.max(1, track.clientWidth));
  [...$('recapDots').children].forEach((b, k) => b.setAttribute('aria-current', String(k === i)));
  const s = track.children[i];
  if (s) track.style.setProperty('height', `${s.offsetHeight}px`);
}

// renderHeads is every buyer who came back, all time, as faces with the
// count on them. Hovering one shows what they buy.
function renderHeads() {
  const box = $('heads');
  if (!box) return;
  const bs = buyers().filter((b) => b.n > 1).slice(0, 14);
  box.replaceChildren();
  for (const b of bs) {
    const t = tierOf(b.n);
    const n = el('span', 'rhead');
    n.style.setProperty('--m', t.color);
    n.append(svgEl(face(b.key)), el('b', '', String(b.n)));
    n.tabIndex = 0;
    n.setAttribute('aria-label', `${b.name || fmtPhone(b.phone) || 'Unknown buyer'}, ${b.n} meals`);
    tip(n, () => buyerLines(b));
    box.append(n);
  }
}

// ---------- rules and friends' meals as pills ----------

const LOOP = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 12a7 7 0 0 1 12-5l2 2"/><path d="M18 4v5h-5"/><path d="M20 12a7 7 0 0 1-12 5l-2-2"/><path d="M6 20v-5h5"/></svg>';
const FRIEND = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="9" r="3"/><path d="M3.5 19c.9-3 3-4.5 5.5-4.5s4.6 1.5 5.5 4.5"/><circle cx="17" cy="8" r="2.4"/><path d="M16 13.6c2.2 0 3.8 1.3 4.5 3.9"/></svg>';

function pill(art, bits, cls, onClick, label) {
  const p = el(onClick ? 'button' : 'span', 'mpill ' + (cls || ''));
  if (onClick) { p.type = 'button'; p.addEventListener('click', onClick); }
  if (label) p.setAttribute('aria-label', label);
  p.append(svgEl(art));
  bits.filter(Boolean).forEach((b, i) => {
    if (i) p.append(el('span', 'dotsep', '·'));
    p.append(typeof b === 'string' ? el('span', '', b) : b);
  });
  return p;
}

// rulePills draws recurring rules: name, price and whether it is on.
function rulePills(box) {
  for (const r of recurringRules) {
    const [word, cls] = !r.enabled ? ['off', 'off'] : r.last_error ? ['check', 'warn'] : ['on', 'on'];
    const p = pill(LOOP, [r.name, ruleListPrice(r), el('span', 'st', word)], 'mp-rule ' + cls,
      () => editRecurringRule(r), `Edit rule ${r.name}, ${word}`);
    p.disabled = recurringBusy;
    if (r.last_error) p.title = r.last_error;
    box.append(p);
  }
}

// friendPills draws friends' meals: running ones open the sell sheet, ones
// that ended this week stay as a quiet record.
function friendPills(box) {
  const weekAgo = addDays(D.today, -6);
  for (const f of D.friends.slice().reverse()) {
    const open = f.task || f.schedule;
    if (!open && f.date < weekAgo) continue;
    const who = f.friend ? `${f.friend}'s ${f.meal}` : `Friend's ${f.meal}`;
    const price = f.task ? priceText(f.task) : f.schedule ? priceText(f.schedule) : f.amount ? rupees(f.amount) : '';
    const word = f.schedule ? hm(f.schedule.at) : { selling: 'selling', armed: 'paying', sending: 'paid', sold: 'sold', handoff: 'yours', ended: 'unsold' }[f.state] || f.state;
    const cls = f.state === 'sold' ? 'on' : open ? 'live' : 'off';
    const p = pill(FRIEND, [who, price, el('span', 'st', word)], 'mp-friend ' + cls,
      open ? () => openSell(f.date, friendCell(f)) : null,
      `${who}, ${word}`);
    p.style.setProperty('--m', messColor(f.mess));
    box.append(p);
  }
}

function renderPills() {
  const box = $('pills');
  box.hidden = !D.owner;
  if (!D.owner) return;
  box.replaceChildren();
  rulePills(box);
  friendPills(box);
  const add = (text, fn, label, cls = '') => {
    const b = el('button', 'mpill add ' + cls, text);
    b.type = 'button';
    b.setAttribute('aria-label', label);
    b.addEventListener('click', fn);
    box.append(b);
  };
  add('+ Rule', () => editRecurringRule(undefined), 'New recurring rule', 'mp-rule');
  if (recurringBusy) box.querySelector('.add.mp-rule').disabled = true;
  add("+ Friend's meal", () => { if (D) openFriendForm(); }, "Sell a friend's meal");
}

function renderMoney() {
  renderJar();
  renderPills();
  if (recapWeeks) renderRecap();
  loadRecap();
}

(() => {
  $('recapOlder').addEventListener('click', () => { recapAt++; renderRecap(); });
  $('recapNewer').addEventListener('click', () => { recapAt--; renderRecap(); });
  let t = 0;
  $('recapTrack').addEventListener('scroll', () => { cancelAnimationFrame(t); t = requestAnimationFrame(recapDot); }, { passive: true });
  tip($('jarArt'), () => (D ? savedTip() : []));
})();
