'use strict';

// The "my sale" cards: your running sell tasks, one small card each, in a
// grid. A card reads top to bottom:
//
//   header     meal, a tag saying why it glows (needs you, paying, failed
//              posts) and the minutes left.
//   price      what the bot takes now, the ✎ range button that opens the
//              range editor in place, and a bar: green is what the bot
//              takes, the gold tick its target, dots buyers' offers.
//   buyers     one row each with their state, timer and price. Finished
//              buyers fold into a "+N finished" line.
//   posted     ✓ / ✗ / … per group; red with ↓N once buried.
//   market     the other listings for the meal, yours in gold.
//
// Finished sales fold to one line under ENDED.
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
// rangeEdit is the price range being edited on a card: {id, lo, hi}. While
// it is open that card keeps its DOM across live updates, so a drag in
// progress isn't cut off.
let rangeEdit = null;
const showGone = new Set(); // sale ids whose finished buyers are listed

const payingNow = (v) => (v.state === 'active' && v.buyers.some((b) => b.state === 'offered')) || (v.pay && LIVE_PAY.includes(v.pay.state) && v.pay.paid);
const isOver = (v) => v.state !== 'active' && !payingNow(v);
const rangeText = (lo, hi) => (lo === hi ? rup(lo) : `${rup(lo)}–${rup(hi)}`);

function renderSales() {
  const box = $('sales');
  if (!box) return;
  const list = (D && D.sales) || [];
  box.hidden = !list.length;
  const live = list.filter((v) => !isOver(v)), over = list.filter(isOver);
  if (rangeEdit && !live.some((v) => v.id === rangeEdit.id && v.state === 'active')) rangeEdit = null;
  const kept = rangeEdit && box.querySelector(`.mysale[data-id="${rangeEdit.id}"] .ms-edit`)?.closest('.mysale');
  const parts = [];
  if (live.length) {
    const grid = el('div', 'ms-grid');
    grid.append(...live.map((v) => (kept && v.id === rangeEdit.id ? kept : chest(v))));
    parts.push(grid);
  }
  if (over.length) parts.push(el('div', 'ms-sec', 'ENDED'), ...over.map(endedRow));
  box.replaceChildren(...parts);
  tickSales();
  if (saleSheet && !saleSheet.editing && !modal.hidden) {
    const v = list.find((x) => x.id === saleSheet.id);
    if (v) (saleSheet.meal ? openMealSheet : openBuyerSheet)(v, saleSheet.key, true);
  }
}
function saleClosed() { saleSheet = null; }

function mealHead(v, cls) {
  const dot = el('span', 'ms-mess');
  dot.style.setProperty('--m', messColor(v.mess));
  const diet = el('span', 'ms-diet');
  diet.append(svgEl(dietMark(v.diet)));
  const name = el('span', cls, cap(mealOf(v)));
  const sub = [v.for_friend ? `for ${v.friend || 'a friend'}` : '', v.date === ymd(now()) ? '' : dayWord(v.date)].filter(Boolean).join(' · ');
  if (sub) name.append(el('small', '', ' · ' + sub));
  return [dot, diet, name];
}

function chest(v) {
  const looks = v.buyers.map((b) => look(b, v));
  const needs = looks.filter((l) => l.cls === 'need' || l.cls === 'chk').length;
  const paying = payingNow(v);
  const failed = v.posts.filter((p) => p.state === 'failed').length;
  const c = el('section', 'mysale' + (won(v) ? ' sold' : paying ? ' pay' : needs ? ' hot' : ''));
  c.dataset.id = v.id;
  c.setAttribute('aria-label', v.for_friend ? `Your sale of ${mealLabel(v)}` : `Your ${mealOf(v)} sale`);

  const top = el('div', 'ms-top');
  top.append(...mealHead(v, 'ms-meal'));
  const tag = paying ? ['grn', 'PAYING'] : needs ? ['red', `${needs} NEEDS YOU`] : failed ? ['red', `${failed} ${failed === 1 ? 'POST' : 'POSTS'} FAILED`] : null;
  if (tag) top.append(el('span', 'ms-tag ' + tag[0], tag[1]));
  top.append(timeBadge(v));
  c.append(top);

  const price = el('div', 'ms-price');
  const at = won(v) && v.pay ? v.pay.amount : v.lowest;
  price.append(el('b', v.held ? 'held' : '', (v.held ? '🔒' : '') + rup(at)));
  const words = el('span', 'what', won(v) ? 'sold for' : !v.negotiates ? 'fixed price' : v.held ? 'held at target' : 'lowest now');
  if (v.negotiates && !won(v) && !v.held && v.target > v.lowest && v.target < v.max) words.append(' · aims for ', el('em', '', rup(v.target)));
  price.append(words);
  if (v.state === 'active') price.append(rangeButton(v));
  price.addEventListener('click', () => openMealSheet(v));
  tip(price, () => heroTip(v));
  c.append(price);
  c.append(rangeEdit && rangeEdit.id === v.id ? rangeEditor(v) : priceBar(v, looks));

  const buyers = el('div', 'ms-buyers');
  const open = showGone.has(v.id);
  const gone = looks.filter((l) => l.cls === 'gone').length;
  v.buyers.forEach((b, i) => { if (open || looks[i].cls !== 'gone') buyers.append(buyerRow(b, v, looks[i])); });
  if (!v.buyers.length) buyers.append(el('div', 'ms-none', 'No buyers yet'));
  if (gone) {
    const more = el('button', 'ms-more', open ? 'Hide finished buyers' : `+${gone} finished (passed, timed out, muted)`);
    more.type = 'button';
    more.addEventListener('click', () => { if (open) showGone.delete(v.id); else showGone.add(v.id); renderSales(); });
    buyers.append(more);
  }
  c.append(buyers);

  c.append(postsRow(v));
  const mk = marketRow(v);
  if (mk) c.append(mk);
  c.append(cardActions(v));
  return c;
}

// timeBadge is the minutes left in the meal, with a ring that empties.
function timeBadge(v) {
  const t = el('span', 'ms-time');
  if (v.state !== 'active') {
    t.append(el('b', won(v) ? 'grn' : 'dim', won(v) ? '✓' : '✕'));
    return t;
  }
  const R = 7, L = 2 * Math.PI * R;
  const s = sv('svg', { viewBox: '0 0 18 18' });
  s.append(sv('circle', { cx: 9, cy: 9, r: R, fill: 'none', stroke: C.line, 'stroke-width': 3 }));
  const arc = sv('circle', { cx: 9, cy: 9, r: R, fill: 'none', stroke: C.gold, 'stroke-width': 3, 'stroke-dasharray': L.toFixed(1), transform: 'rotate(-90 9 9)' });
  arc.dataset.from = v.started;
  arc.dataset.until = v.ends;
  arc.dataset.len = L;
  s.append(arc);
  const b = el('b');
  b.dataset.until = v.ends;
  b.dataset.fmt = 'mins';
  t.append(s, b);
  tip(t, () => [[{ t: `Ends ${hm(v.ends)}, in ${dur(v.ends - now())}`, c: 'c7' }]]);
  return t;
}

// priceBar runs from the lowest offer or floor to the opening price. Green
// is what the bot takes now, the gold tick its target, dots buyers' offers.
function priceBar(v, looks) {
  const lo = Math.min(v.min, ...v.buyers.filter((b) => b.offer).map((b) => b.offer)), hi = v.max;
  const pct = (p) => (hi === lo ? 0 : ((Math.min(Math.max(p, lo), hi) - lo) / (hi - lo)) * 100);
  const bar = el('div', 'ms-bar');
  if (v.state === 'active' || won(v)) {
    const on = el('i', 'on');
    on.style.setProperty('left', `${pct(won(v) && v.pay ? v.pay.amount : v.lowest)}%`);
    bar.append(on);
  }
  if (v.negotiates && v.target > v.min && v.target < v.max) {
    const t = el('i', 'tg');
    t.style.setProperty('left', `${pct(v.target)}%`);
    bar.append(t);
  }
  v.buyers.forEach((b, i) => {
    const p = b.agreed || b.offer;
    if (!p || looks[i].cls === 'gone') return;
    const d = el('i', 'of' + (looks[i].cls === 'need' ? ' hollow' : ''));
    d.style.setProperty('left', `${pct(p)}%`);
    d.style.setProperty('--c', looks[i].tone);
    bar.append(d);
  });
  return bar;
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

// ---------- price range ----------

function rangeButton(v) {
  const b = el('button', 'ms-range', `✎ ${rangeText(v.min, v.max)}`);
  b.type = 'button';
  b.setAttribute('aria-label', `Change the price range, now ${rangeText(v.min, v.max)}`);
  b.addEventListener('click', (e) => {
    e.stopPropagation();
    hideTip();
    rangeEdit = rangeEdit && rangeEdit.id === v.id ? null : { id: v.id, lo: v.min, hi: v.max };
    renderSales();
  });
  return b;
}

const nameList = (bs) => {
  const ns = bs.map((b) => b.name || b.phone || 'a buyer');
  return ns.length <= 2 ? ns.join(' and ') : `${ns.slice(0, 2).join(', ')} and ${ns.length - 2} more`;
};

// rangeHint says what saving lo-hi does to the buyers already here.
function rangeHint(v, lo, hi) {
  const out = [lo === hi ? `Sells at ${rup(lo)} to the first buyer who pays it.` : `Opens at ${rup(hi)} and never goes under ${rup(lo)}.`];
  const talking = v.buyers.filter((b) => b.state === 'negotiating');
  if (talking.length) out.push(`The bot uses the new range in its next reply to ${nameList(talking)}.`);
  if (lo < v.min) {
    const back = v.buyers.filter((b) => ['timed_out', 'passed', 'parked'].includes(b.state) && b.offer >= lo && !b.refund);
    if (back.length) out.push(`${nameList(back)} offered enough for the new floor and will show as needing you.`);
  }
  out.push('Prices already quoted or agreed stand.');
  return out.join(' ');
}

// rangeEditor edits a running sale's floor and opening price in place. A
// sale can't switch between haggling and a fixed price, so a fixed one
// moves both ends together and a haggling one keeps them a rupee apart.
function rangeEditor(v) {
  const e = rangeEdit;
  const fixed = v.min === v.max, gap = fixed ? 0 : 1;
  // The scale hugs the current range, so a few rupees still move a thumb a
  // useful distance. The steppers go past it in rupee steps.
  const B = { lo: Math.max(1, Math.floor((v.min * 0.6) / 5) * 5), hi: Math.max(20, Math.ceil((v.max * 1.5) / 5) * 5) };
  const pct = (p) => Math.min(100, Math.max(0, ((p - B.lo) / (B.hi - B.lo)) * 100));
  const clamp = (x) => Math.min(10000, Math.max(1, Math.round(x)));

  const box = el('div', 'ms-edit');
  const head = el('div', 'eh');
  head.append(el('b', '', fixed ? 'PRICE' : 'PRICE RANGE'), el('span', '', `was ${rangeText(v.min, v.max)}`));

  const dual = el('div', 'dual');
  const track = el('div', 'track');
  const fill = el('i', 'fill');
  track.append(fill);
  for (const p of fixed ? [v.min] : [v.min, v.max]) {
    const m = el('i', 'was');
    m.style.setProperty('left', `${pct(p)}%`);
    track.append(m);
  }
  const input = (val, label) => {
    const r = el('input');
    Object.assign(r, { type: 'range', min: B.lo, max: B.hi, step: 1, value: val });
    r.setAttribute('aria-label', label);
    return r;
  };
  const rMin = input(e.lo, fixed ? 'Price' : 'Floor'), rMax = input(e.hi, 'Opening price');
  dual.append(track, rMin);
  if (!fixed) dual.append(rMax);

  const setLo = (x) => { x = clamp(x); if (fixed) e.lo = e.hi = x; else e.lo = Math.min(x, e.hi - gap); draw(); };
  const setHi = (x) => { e.hi = Math.max(clamp(x), e.lo + gap); draw(); };
  const stepper = (label, get, set) => {
    const w = el('div', 'stp');
    const minus = el('button', '', '−'), plus = el('button', '', '+'), val = el('b');
    for (const [btn, d, word] of [[minus, -1, 'down'], [plus, 1, 'up']]) {
      btn.type = 'button';
      btn.setAttribute('aria-label', `${label} ${word} a rupee`);
      btn.addEventListener('click', () => set(get() + d));
    }
    w.append(el('small', '', label), minus, val, plus);
    return [w, val];
  };
  const nums = el('div', 'nums');
  const [sLo, vLo] = stepper(fixed ? 'Price' : 'Floor', () => e.lo, setLo);
  const [sHi, vHi] = stepper('Opens', () => e.hi, setHi);
  nums.append(sLo);
  if (!fixed) nums.append(sHi);

  const hint = el('p', 'hint');
  hint.setAttribute('aria-live', 'polite');
  const error = el('p', 'err');
  error.setAttribute('role', 'alert');
  const acts = el('div', 'eacts');
  const cancel = el('button', 'ms-btn ghost', 'Cancel'), save = el('button', 'ms-btn gold', 'Save range');
  cancel.type = save.type = 'button';
  cancel.addEventListener('click', () => { rangeEdit = null; renderSales(); });
  save.addEventListener('click', async () => {
    save.disabled = true;
    error.textContent = '';
    try {
      await send('POST', 'api/sell-action', { action: 'prices', id: v.id, min: e.lo, max: e.hi });
      rangeEdit = null;
      toast('Prices updated');
      await load();
    } catch (err) {
      error.textContent = err.message;
      save.disabled = false;
    }
  });
  acts.append(cancel, save);

  function draw() {
    rMin.value = e.lo;
    rMax.value = e.hi;
    rMin.setAttribute('aria-valuetext', rup(e.lo));
    rMax.setAttribute('aria-valuetext', rup(e.hi));
    vLo.textContent = rup(e.lo);
    vHi.textContent = rup(e.hi);
    fill.style.setProperty('left', `${pct(e.lo)}%`);
    fill.style.setProperty('right', `${100 - pct(e.hi)}%`);
    hint.textContent = rangeHint(v, e.lo, e.hi);
    save.disabled = e.lo === v.min && e.hi === v.max;
  }
  // The thumb that last moved sits on top, so two thumbs at the same spot
  // can still be pulled apart.
  rMin.addEventListener('input', () => { setLo(+rMin.value); rMin.style.zIndex = 3; rMax.style.zIndex = 2; });
  rMax.addEventListener('input', () => { setHi(+rMax.value); rMax.style.zIndex = 3; rMin.style.zIndex = 2; });
  draw();
  box.append(head, dual, nums, hint, error, acts);
  box.addEventListener('keydown', (k) => { if (k.key === 'Escape') cancel.click(); });
  return box;
}

// ---------- buyers, posts, market ----------

function stateWord(b, l) {
  if (b.muted) return 'muted';
  if (l.cls === 'need') { const [ic, w] = reason(b); return `${ic} ${w.toLowerCase()}`; }
  if (sentPicture(b)) return 'sent a picture';
  return STATE_WORD[b.state] || b.state;
}
const buyerPrice = (b, l) => (l.cls === 'won' ? b.asked || b.agreed : b.refund || b.agreed || b.offer || b.asked);

// buyerTimer counts down a buyer's pay window, or how long the bot waits
// for a haggling buyer to answer.
function buyerTimer(b, cls) {
  const until = b.state === 'offered' ? b.pay_until : b.state === 'negotiating' ? b.quiet_until : 0;
  if (!until) return null;
  const t = el('span', cls + (b.state === 'offered' ? ' pay' : ' quiet'));
  t.dataset.until = until;
  t.dataset.fmt = 'mmss';
  return t;
}

function buyerRow(b, v, l) {
  const r = el('button', 'ms-b ' + l.cls);
  r.type = 'button';
  const f = el('span', 'face');
  f.append(svgEl(face(b.key)));
  const nm = el('span', 'nm', b.name || b.phone || 'Buyer');
  if (b.saved) nm.append(el('span', 'hrt', ' ♥'));
  const st = el('span', 'st', stateWord(b, l));
  r.append(f, nm, st);
  const tm = buyerTimer(b, 'tmr');
  if (tm) r.append(tm);
  const p = buyerPrice(b, l);
  if (p && b.state !== 'offered') {
    const a = el('span', 'amt', rup(p));
    a.style.setProperty('color', l.tone);
    r.append(a);
  }
  r.setAttribute('aria-label', `${b.name || 'Buyer'}: ${stateWord(b, l)}`);
  tip(r, () => buyerTip(b));
  r.addEventListener('click', () => openBuyerSheet(v, b.key));
  return r;
}

// A group's short code from the words that tell groups apart: "Mess Buy
// Sell" -> "BS", "IIIT Mess Exchange" -> "EX".
const FILLER = new Set(['mess', 'iiit', 'iiith', 'the', 'group', 'and', 'of']);
function groupCode(g) {
  const w = g.split(/[^A-Za-z0-9]+/).filter((x) => x && !FILLER.has(x.toLowerCase()));
  return (w.length > 1 ? w.slice(0, 2).map((x) => x[0]) : [(w[0] || '?').slice(0, 2)]).join('').toUpperCase();
}
const errCode = (e) => ((e || '').match(/\b(\d{3})\b/) || [, 'ERR'])[1];
const BURIED = 15;

function postsRow(v) {
  const r = el('div', 'ms-row');
  r.append(el('span', 'lbl', 'Posted'));
  for (const p of v.posts) r.append(postPill(p));
  if (!v.posts.length) r.append(el('span', 'ms-none', 'not yet'));
  r.addEventListener('click', () => openMealSheet(v));
  return r;
}

// postPill: ✓ posted, ✗ failed, … posting. A post with more than BURIED
// listings after it is red, with how far down it sits.
function postPill(p) {
  const buried = p.state === 'posted' && p.after > BURIED;
  const n = el('span', 'ms-post ' + p.state + (buried ? ' buried' : ''));
  n.append(el('b', '', p.state === 'failed' ? '✗' : p.state === 'posted' ? '✓' : '…'), groupCode(p.group));
  if (buried) n.append(el('small', '', `↓${p.after}`));
  tip(n, () => [[{ t: p.group, c: 'nm' }],
    p.state === 'failed' ? [{ t: `Failed at ${hm(p.at)}: ${p.error || 'unknown error'}`, c: 'cc' }]
      : [{ t: `Posted ${hm(p.at)}, ${ago(p.at)}`, c: 'c7' }],
    p.state === 'posted' ? [{ t: `${p.after} listing ${p.after === 1 ? 'post' : 'posts'} since yours`, c: p.after > BURIED ? 'cc' : 'c8' }] : null].filter(Boolean));
  return n;
}

// The other open listings for the same meal, cheapest first, yours in gold.
function marketRow(v) {
  if (!D || v.state !== 'active') return null;
  const mine = new Set(v.listings);
  const ls = D.listings.filter((l) => l.kind !== 'buy' && l.mess === v.mess && l.meal === mealOf(v) && l.meal_date === v.date
    && l.status === 'available' && (!v.diet || !l.diet || l.diet === v.diet) && (l.price != null || mine.has(l.id)));
  if (ls.length < 2) return null;
  ls.sort((a, b) => (a.price ?? 1e9) - (b.price ?? 1e9));
  const row = el('div', 'ms-row');
  row.append(el('span', 'lbl', 'Market'));
  const SHOW = 7;
  for (const l of ls.slice(0, SHOW)) {
    const m = el('button', 'ms-l' + (mine.has(l.id) ? ' me' : ''), l.price != null ? String(l.price) : '?');
    m.type = 'button';
    tip(m, () => itemTip(l));
    m.addEventListener('click', () => openListing(l));
    row.append(m);
  }
  if (ls.length > SHOW) row.append(el('span', 'ms-none', `+${ls.length - SHOW}`));
  return row;
}

function cardActions(v) {
  const a = el('div', 'ms-acts');
  const btn = (cls, text, fn, label) => {
    const b = el('button', 'ms-btn ' + cls, text);
    b.type = 'button';
    if (label) b.setAttribute('aria-label', label);
    b.addEventListener('click', fn);
    a.append(b);
  };
  if (v.state === 'active') {
    btn('grn', '✓ Mark sold', () => saleActionForm(v, 'sold'));
    btn('', '+ Add buyer', () => saleActionForm(v, 'add'));
  }
  btn('ghost more', '···', () => openMealSheet(v), 'Sale details: posts, tries and price over time');
  return a;
}

// endedRow is a finished sale, folded to one line. It opens the meal sheet.
function endedRow(v) {
  const r = el('button', 'ms-done' + (won(v) ? ' won' : ''));
  r.type = 'button';
  r.append(...mealHead(v, 'nm'));
  const bought = v.buyers.find((b) => b.state === 'bought');
  const amount = v.pay ? v.pay.amount : bought && (bought.asked || bought.agreed);
  r.append(el('span', 'st', won(v) ? `✓ SOLD${amount ? ' ' + rup(amount) : ''}` : `✕ ${v.state.replace('_', ' ').toUpperCase()}`));
  const n = v.buyers.length;
  r.append(el('span', 'sub', `${hm(v.started)}–${hm(v.ended || v.ends)} · ${n} ${n === 1 ? 'buyer' : 'buyers'}`));
  tip(r, () => heroTip(v));
  r.addEventListener('click', () => openMealSheet(v));
  return r;
}

// tickSales moves the clocks, pay rings, countdowns and bars between fetches.
function tickSales() {
  const n = now();
  const frac = (e) => Math.min(1, Math.max(0, (+e.dataset.until - n) / Math.max(1, +e.dataset.until - +e.dataset.from)));
  for (const a of document.querySelectorAll('#sales circle[data-until], .sheet circle[data-until]')) {
    a.setAttribute('stroke-dashoffset', (+a.dataset.len * (1 - frac(a))).toFixed(1));
  }
  for (const b of document.querySelectorAll('.sheet [data-bar]')) b.style.setProperty('width', `${frac(b) * 100}%`);
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

function sheetFor(v, keep, tone) {
  hideTip();
  const scroll = keep ? sheet.scrollTop : 0;
  sheet.replaceChildren();
  sheet.className = 'sheet sale-sheet' + (tone ? ' tone-' + tone : '');
  modal.hidden = false;
  return () => { sheet.scrollTop = scroll; };
}

function chatLink(b) {
  const ph = digits(b.phone);
  if (!ph) return null;
  return messagingApp() === 'beeper' ? `beeper://compose/whatsapp/phone/+${ph}` : `https://wa.me/${ph}`;
}

const closeButton = () => {
  const x = el('button', 'sb-x', '✕');
  x.type = 'button';
  x.setAttribute('aria-label', 'Close');
  x.addEventListener('click', closeModal);
  return x;
};

function openBuyerSheet(v, key, keep) {
  const b = v.buyers.find((x) => x.key === key);
  if (!b) return;
  saleSheet = { id: v.id, key };
  const l = look(b, v);
  const restore = sheetFor(v, keep, l.cls);

  const head = el('div', 'sb-head');
  const f = el('span', 'sb-face ' + l.cls);
  f.append(svgEl(face(b.key)));
  const who = el('div', 'sb-who');
  const nm = el('div', 'sb-name', b.name || b.phone || 'Buyer');
  if (b.saved) nm.append(el('span', 'hrt', ' ♥'));
  who.append(nm);
  if (b.phone) {
    const ph = el('div', 'sb-phone');
    const copy = el('button', 'sb-copy', 'copy');
    copy.type = 'button';
    copy.setAttribute('aria-label', 'Copy number');
    copy.addEventListener('click', () => navigator.clipboard?.writeText(b.phone).then(() => toast('Number copied'), () => toast(b.phone)));
    ph.append(el('span', '', b.phone), copy);
    who.append(ph);
  }
  head.append(f, who, closeButton());
  sheet.append(head, buyerStatus(b, v, l));

  const strip = priceStrip(b, v);
  if (strip) sheet.append(strip);
  if (b.events.some((e) => e.kind === 'prompt') || (v.pay && v.pay.buyer === b.key)) {
    sheet.append(el('div', 'sec', 'PAYMENT'), payPips(b, v));
  }
  const rounds = haggleRounds(b, v);
  if (rounds) sheet.append(el('div', 'sec', 'HAGGLE'), rounds);

  const acts = el('div', 'sb-acts');
  const link = chatLink(b);
  const chat = el(link ? 'a' : 'span', 'sb-act chat');
  chat.append(el('b', '', '💬'), el('span', '', 'CHAT'));
  if (link) {
    chat.href = link;
    if (link.startsWith('https:')) { chat.target = '_blank'; chat.rel = 'noopener noreferrer'; }
  } else {
    chat.classList.add('off');
  }
  acts.append(chat);
  if (v.state === 'active') {
    acts.append(saleActionButton('✓', 'MARK SOLD', () => saleActionForm(v, 'sold', b)));
    acts.append(saleActionButton(b.muted ? '🔈' : '🔇', b.muted ? 'UNMUTE' : 'MUTE', () => saleActionForm(v, b.muted ? 'unmute' : 'mute', b)));
  }
  sheet.append(acts);
  tickSales();
  restore();
}

// buyerStatus says in words where the buyer stands, with the amount or a
// countdown beside it and, for a timer, a bar that drains.
function buyerStatus(b, v, l) {
  let tone, title, body = b.note || '', big = null, from = 0, until = 0;
  if (b.muted) {
    [tone, title, body] = ['dim', 'MUTED', 'The bot sends this buyer nothing for this sale.'];
  } else if (l.cls === 'need') {
    const [ic, w] = reason(b);
    [tone, title] = ['red', `${ic} ${w}`];
    if (b.refund || b.offer) big = rup(b.refund || b.offer);
  } else if (b.state === 'offered') {
    [tone, title] = ['grn', `ASKED TO PAY ${rup(b.asked || b.agreed)}`];
    body = 'Waiting for their WhatsApp Pay payment. The QR goes out once it is read.';
    if (b.pay_until) [from, until] = [b.pay_until - 120, b.pay_until];
  } else if (b.state === 'negotiating' && b.quiet_until) {
    [tone, title] = ['blue', 'HAGGLING · TIMES OUT IN'];
    body = 'If they stay quiet this long, the bot drops them. Each bot reply restarts the clock.';
    const lastBot = [...b.events].reverse().find((e) => e.kind === 'bot_said' || e.kind === 'revived');
    [from, until] = [lastBot ? lastBot.at : b.joined, b.quiet_until];
  } else {
    tone = { won: 'grn', chk: 'egg', wait: 'gold', talk: 'blue' }[l.cls] || 'dim';
    title = (STATE_WORD[b.state] || b.state).toUpperCase();
    const p = buyerPrice(b, l);
    if (p) big = rup(p);
  }
  if (sentPicture(b)) body = `${body} Sent a picture, not a WhatsApp Pay payment. They may have paid another way.`.trim();

  const box = el('div', 'sb-status ' + tone);
  if (until) {
    big = el('b', 'big');
    big.dataset.until = until;
    big.dataset.fmt = 'mmss';
  } else if (big) {
    big = el('b', 'big', big);
  }
  if (big) box.append(big);
  box.append(el('b', 'ttl', title));
  if (body) box.append(el('p', '', body));
  if (!until) return box;
  const wrap = el('div', 'sb-statusw');
  const bar = el('div', 'sb-bar ' + tone);
  const fill = el('i');
  fill.dataset.from = from;
  fill.dataset.until = until;
  fill.dataset.bar = '1';
  bar.append(fill);
  wrap.append(box, bar);
  return wrap;
}

// priceStrip: their offer, what the bot asked or agreed, and the bot's
// lowest now, side by side.
function priceStrip(b, v) {
  const cells = [];
  if (b.offer) cells.push(['Their offer', rup(b.offer), 'red']);
  if (b.agreed) cells.push(['Agreed', rup(b.agreed), 'grn']);
  else if (b.ask) cells.push(['Bot asked', rup(b.ask), 'grn']);
  if (v.state === 'active' && v.negotiates) cells.push([v.held ? 'Held at' : 'Lowest now', rup(v.lowest), '']);
  if (v.state === 'active' && cells.length < 3) cells.push(['Floor', rup(v.min), 'dim']);
  if (cells.length < 2) return null;
  const s = el('div', 'sb-strip');
  for (const [k, val, cls] of cells.slice(0, 3)) {
    const c = el('div');
    c.append(el('small', '', k), el('b', cls, val));
    s.append(c);
  }
  return s;
}

// READ_WORD: what the bot read, never what they wrote.
const READ_WORD = { opener: 'Asked the price', question: 'Asked something', ok: 'Said ok', accept: 'Accepted', lower: 'Asked for lower', pay_other: 'Other payment', paid: 'Says paid', pass: 'Passed', unknown: 'Unclear' };

// haggleRounds lists the negotiation round by round: what the bot read
// from them, then what it answered.
function haggleRounds(b, v) {
  if (!v.negotiates) return null;
  const rows = [];
  let row = null;
  for (const e of b.events) {
    if (e.kind === 'read') {
      row = { them: e.text.startsWith('offer') ? e.n : null, word: READ_WORD[e.text] || 'Unclear' };
      rows.push(row);
    } else if (e.kind === 'bot_said' || e.kind === 'agreed') {
      if (!row || row.bot != null) { row = { word: 'Wrote' }; rows.push(row); }
      row.bot = e.n;
      row.lock = row.lock || /lowest/.test(e.text || '');
      row.deal = row.deal || e.kind === 'agreed';
    } else if (e.kind === 'parked' && row) {
      row.stop = true;
    }
  }
  if (!rows.length) return null;
  const SHOW = 6;
  const box = el('div', 'sb-rounds');
  if (rows.length > SHOW) box.append(el('div', 'earlier', `${rows.length - SHOW} earlier ${rows.length - SHOW === 1 ? 'round' : 'rounds'}`));
  rows.slice(-SHOW).forEach((r, i) => {
    const line = el('div', 'rd');
    line.append(el('span', 'i', String(rows.length - Math.min(SHOW, rows.length) + i + 1)));
    const them = el('span', 'them' + (r.them != null ? '' : ' word'));
    them.append(el('small', '', r.them != null ? 'They' : r.word));
    if (r.them != null) them.append(el('em', '', rup(r.them)));
    line.append(them, el('span', 'ar', '→'));
    let bot;
    if (r.bot != null) {
      bot = el('span', 'bot' + (r.deal ? ' deal' : ''));
      bot.append(el('small', '', r.deal ? 'Deal' : r.lock ? 'Bot, lowest' : 'Bot'), el('em', '', (r.deal ? '✓ ' : r.lock ? '🔒' : '') + rup(r.bot)));
    } else if (r.stop) {
      bot = el('span', 'bot stop', 'Stopped, asked you');
    } else {
      bot = el('span', 'bot wait', '…');
    }
    line.append(bot);
    box.append(line);
  });
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
  const posts = el('div', 'mp-posts');
  for (const p of v.posts) {
    const r = el('div', 'mp-post');
    r.append(el('b', p.state === 'failed' ? 'red' : 'grn', p.state === 'failed' ? '✗' : '✓'), el('span', 'code', groupCode(p.group)));
    const bar = el('span', 'mp-bar' + (p.state === 'failed' ? ' failed' : ''));
    if (p.state !== 'failed') {
      const me = el('i', 'me'), after = el('i', 'after' + (p.after > BURIED ? ' red' : ''));
      after.style.setProperty('width', `${Math.min(1, p.after / 25) * 96}%`);
      bar.append(me, after);
    }
    r.append(bar, el('span', 'n' + (p.state === 'failed' || p.after > BURIED ? ' red' : ''), p.state === 'failed' ? errCode(p.error) : `${p.after}↑`));
    tip(r, () => [[{ t: p.group, c: 'nm' }], [{ t: p.state === 'failed' ? p.error : `posted ${hm(p.at)}`, c: 'c8' }]]);
    posts.append(r);
  }
  sheet.append(posts);

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
