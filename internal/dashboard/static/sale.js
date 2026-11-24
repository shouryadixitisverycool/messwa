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
// The home page's Today widget shows a card per meal today (renderToday),
// and its Scheduled widget the sales waiting for their meal, as dashed
// cards (scheduledCard). Finished sales fold to one line under ENDED.
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

let saleSheet = null; // {id, key} while a buyer sheet is open, {id, editing: true} while a form is
// rangeEdit is the price range being edited on a card: {id, lo, hi}. While
// it is open that card keeps its DOM across live updates, so a drag in
// progress isn't cut off.
let rangeEdit = null;
const showGone = new Set(); // sale ids whose finished buyers are listed
const ENDED_SHOWN = 3; // ended sales listed before the rest fold away
let allEnded = false;

const payingNow = (v) => (v.state === 'active' && v.buyers.some((b) => b.state === 'offered')) || (v.pay && LIVE_PAY.includes(v.pay.state) && v.pay.paid);
const isOver = (v) => v.state !== 'active' && !payingNow(v);
const rangeText = (lo, hi) => (lo === hi ? rup(lo) : `${rup(lo)}–${rup(hi)}`);

// renderSales draws the home page's Today and Scheduled widgets, whichever
// are on the page.
function renderSales() {
  const list = (D && D.sales) || [];
  const live = list.filter((v) => !isOver(v));
  if (rangeEdit && !live.some((v) => v.id === rangeEdit.id && v.state === 'active')) rangeEdit = null;
  const kept = rangeEdit && document.querySelector(`#todayBox .mysale[data-id="${rangeEdit.id}"] .ms-edit`)?.closest('.mysale');
  if ($('todayBox')) renderToday($('todayBox'), kept);
  if ($('schedBox')) renderScheduled($('schedBox'));
  tickSales();
  if (saleSheet && !saleSheet.editing && !modal.hidden) {
    const v = list.find((x) => x.id === saleSheet.id);
    if (v) openBuyerSheet(v, saleSheet.key, true);
  }
}
function saleClosed() { saleSheet = null; }

// renderToday fills box with a card per meal on today's board: the sale
// card while it sells or once it sold, the scheduled card while it waits,
// and otherwise what the ledger says (eaten, wasted, not registered) with a
// button to sell a meal still to come. Running sales for another day or a
// friend follow, then sales that ended without selling.
function renderToday(box, kept) {
  const list = (D && D.sales) || [];
  const waiting = (D && D.scheduled) || [];
  const date = ymd(now());
  const used = new Set();
  const cards = boardMeals().map((meal) => {
    const v = list.find((x) => !x.for_friend && x.date === date && mealOf(x) === meal);
    if (v && (!isOver(v) || won(v))) {
      used.add(v.id);
      return kept && v.id === rangeEdit.id ? kept : chest(v);
    }
    const s = waiting.find((x) => !x.for_friend && x.date === date && x.meal === meal);
    if (s) return scheduledCard(s);
    if (v) used.add(v.id);
    return mealTile(date, meal, v);
  });
  for (const v of list) {
    if (used.has(v.id) || isOver(v)) continue;
    used.add(v.id);
    cards.push(kept && v.id === rangeEdit.id ? kept : chest(v));
  }
  const grid = el('div', 'ms-grid today-grid');
  grid.append(...cards);
  packToday(box, grid);
  const parts = [grid];
  const over = list.filter((v) => isOver(v) && !used.has(v.id));
  if (over.length) {
    const shown = allEnded ? over : over.slice(0, ENDED_SHOWN);
    parts.push(el('div', 'ms-sec', 'ENDED'), ...shown.map(endedRow));
    if (over.length > ENDED_SHOWN) {
      const more = el('button', 'ms-more', allEnded ? 'Show fewer' : `+${over.length - ENDED_SHOWN} more ended`);
      more.type = 'button';
      more.addEventListener('click', () => { allEnded = !allEnded; renderSales(); });
      parts.push(more);
    }
  }
  box.replaceChildren(...parts);
}

// packToday lays the Today cards out masonry style. A plain grid row is as
// tall as its tallest card, so a short finished meal next to a live sale
// left a hole under it. The grid's rows are 1px tall and each card spans as
// many as its height plus the gap, so a card sits right under the one above
// it in its column. Cards grow and shrink as buyers come and go, so a
// ResizeObserver keeps the spans current. Until the first measure the grid
// has no .packed class and lays out as a normal grid. box keeps its
// observer, so a redraw drops the old one without touching another box's.
function packToday(box, grid) {
  if (box._pack) box._pack.disconnect();
  if (typeof ResizeObserver === 'undefined') return;
  const fit = () => {
    const gap = parseFloat(getComputedStyle(grid).columnGap) || 0;
    for (const c of grid.children) c.style.gridRowEnd = `span ${Math.max(1, Math.ceil(c.getBoundingClientRect().height + gap))}`;
    grid.classList.add('packed');
  };
  box._pack = new ResizeObserver(fit);
  for (const c of grid.children) box._pack.observe(c);
}

// TILE is how a meal with no sale card reads: its tag and a line under it.
const TILE = {
  none: ['NOT REGISTERED', ''],
  unknown: ['UNKNOWN', 'The mess portal didn\'t answer, so this meal\'s state is unknown.'],
  ate: ['ATE IT', ''],
  wasted: ['WASTED', ''],
  sold: ['SOLD', ''], sending: ['PAID', 'Sending the QR.'], armed: ['ARMED', 'Waiting for the buyer to pay.'], handoff: ['HANDED OFF', 'You took the chat over.'],
  open: ['REGISTERED', ''], selling: ['SELLING', ''], scheduled: ['SCHEDULED', ''],
  loading: ['', 'Loading'],
};

// mealTile is one of today's meals with no sale card: c is its ledger
// cell, and v an ended sale for it, if any.
function mealTile(date, meal, v) {
  const c = ledgerCell(date, meal) || { meal, state: 'loading' };
  const w = window_(meal) || c;
  const n = now();
  const t = el('section', `mysale tile is-${c.state}`);
  t.setAttribute('aria-label', `Your ${meal} today`);
  const top = el('div', 'ms-top');
  top.append(...mealHead({ meal, mess: c.mess, diet: c.diet, date }, 'ms-meal'));
  const [word, line] = TILE[c.state] || [c.state.toUpperCase(), ''];
  if (word) top.append(el('span', 'ms-tag tile-tag', word));
  t.append(top);

  const body = el('div', 'tile-body');
  const when = w.start ? `${hm(w.start)}–${hm(w.end)}` : '';
  const state = n >= w.end ? 'over' : n >= w.start ? `serving, ${dur(w.end - n)} left` : `starts in ${dur(w.start - n)}`;
  body.append(el('span', 'tile-when', [when, w.start ? state : ''].filter(Boolean).join(' · ')));
  const s = c.sale != null && N && N.sales ? N.sales[c.sale] : null;
  if (s) body.append(el('span', 'tile-line', `${rup(s.amount)} from ${s.name || s.phone || 'a buyer'}`));
  else if (c.state === 'wasted' && c.ask) body.append(el('span', 'tile-line', `Would have fetched about ${rup(Math.round(c.ask / 5) * 5)}`));
  else if (v) body.append(el('span', 'tile-line', `Your offer ended unsold, ${v.state.replace('_', ' ')}.`));
  else if (c.state === 'none') body.append(el('span', 'tile-line', 'Nothing to sell.'));
  else if (line) body.append(el('span', 'tile-line', line));
  t.append(body);

  // A meal still to come can start selling. The sell sheet opens here when
  // the ledger has the meal; otherwise the sales page plans it.
  if (c.state === 'open' && c.end > n && N && N.owner && !N.sell_off) {
    if (c.ask) body.append(el('span', 'tile-line', `Goes for about ${rup(Math.round(c.ask / 5) * 5)} today`));
    const a = el('div', 'ms-acts');
    const key = `${date}|${meal}`;
    const go = el('a', 'ms-btn gold tile-go', c.sellable ? '▶ SELL IT' : '⏰ SCHEDULE IT');
    go.href = `sales.html#sell=${encodeURIComponent(key)}`;
    go.addEventListener('click', (e) => { if (c.sellable && openSellHere(date, meal)) e.preventDefault(); });
    a.append(go);
    t.append(a);
    t.classList.add('can-sell');
  }
  if (v && v.buyers.length) tapCard(t, () => openSaleBuyer(v));
  return t;
}

// renderScheduled fills box with the sales waiting for their meal, soonest
// first. The rest are on the sales page.
const SCHED_SHOWN = 6;
function renderScheduled(box) {
  const waiting = (D && D.scheduled) || [];
  if (!waiting.length) {
    const none = el('div', 'empty-note', 'Nothing scheduled. Pick meals in the sales ledger, or make a recurring rule, and they wait here until it\'s time to post.');
    box.replaceChildren(none);
    return;
  }
  const grid = el('div', 'ms-grid');
  grid.append(...waiting.slice(0, SCHED_SHOWN).map(scheduledCard));
  const parts = [grid];
  if (waiting.length > SCHED_SHOWN) {
    const more = el('a', 'ms-more', `+${waiting.length - SCHED_SHOWN} more scheduled in My sales`);
    more.href = 'sales.html';
    parts.push(more);
  }
  box.replaceChildren(...parts);
}

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
  const tag = paying ? ['grn', 'PAYING'] : needs ? ['red', `${needs} NEEDS YOU`] : failed ? ['red', `${failed} ${failed === 1 ? 'POST' : 'POSTS'} FAILED`] : v.autopilot ? ['auto', '✈ AUTOPILOT'] : null;
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

  // Once it sold, where the offer went no longer matters.
  if (!won(v)) c.append(postsRow(v));
  const mk = marketRow(v);
  if (mk) c.append(mk);
  const acts = cardActions(v);
  if (acts) c.append(acts);
  if (v.state === 'active' || v.buyers.length) tapCard(c, () => openTaskMenu(v) || openSaleBuyer(v));
  return c;
}

// openTaskMenu opens the sell menu for a running sale, yours or a friend's,
// the same one the sales page shows. It returns false when the ledger the
// nudge loaded doesn't have this task yet.
function openTaskMenu(v, view) {
  if (v.state !== 'active') return false;
  if (v.for_friend) return openFriendHere(v.id, 'task', view);
  const cell = ledgerCell(v.date, v.meal);
  if (!cell || !cell.task || cell.task.id !== v.id) return false;
  return openSellHere(v.date, v.meal, view);
}

// scheduledCard is a sale waiting for its meal: nothing is posted yet. It
// says when the offer goes up and at what price, and can start it now or
// switch autopilot. Clicking the card opens the sales page's sheet to
// change the price or unschedule. The mess and diet come from the ledger the nudge reads
// (N in app.js), when it has loaded.
function scheduledCard(s) {
  const cell = ledgerCell(s.date, s.meal);
  const v = { ...s, mess: s.mess || (cell && cell.mess), diet: cell && cell.diet };
  const c = el('section', 'mysale sched');
  c.dataset.schedule = s.id;
  c.setAttribute('aria-label', `Your ${mealLabel(v)} sale, scheduled for ${hm(s.at)}`);

  const top = el('div', 'ms-top');
  top.append(...mealHead(v, 'ms-meal'));
  top.append(el('span', 'ms-tag ' + (s.autopilot ? 'auto' : 'sch'), s.autopilot ? '✈ AUTOPILOT' : 'SCHEDULED'));
  const t = el('span', 'ms-time');
  const b = el('b', 'dim');
  b.dataset.until = s.at;
  b.dataset.fmt = 'mins';
  t.append(el('small', '', 'IN'), b);
  tip(t, () => [[{ t: `Goes up at ${hm(s.at)}, in ${dur(s.at - now())}`, c: 'c7' }]]);
  top.append(t);
  c.append(top);

  const price = el('div', 'ms-price');
  price.append(el('b', 'sch', rangeText(s.min, s.max)));
  price.append(el('span', 'what', `goes up ${hm(s.at)} · sells till ${hm(s.ends)}`));
  tip(price, () => [
    [{ t: `${v.mess ? cap(v.mess) + ' ' : ''}${mealLabel(v)}`, c: 'nm', color: messColor(v.mess) }, { t: ` ${dayWord(s.date)}`, c: 'c8' }],
    [{ t: `Nothing is posted until ${hm(s.at)}, in ${dur(s.at - now())}.`, c: 'c7' }],
    [{ t: `Then it sells till ${hm(s.ends)}, opening at ${rup(s.max)} with a floor of ${rup(s.min)}.`, c: 'c7' }],
    s.autopilot ? [{ t: 'On autopilot: it skips any buyer who would need you.', c: 'cc' }] : null,
  ].filter(Boolean));
  c.append(price);

  const err = el('p', 'ms-err');
  err.setAttribute('role', 'alert');
  const a = el('div', 'ms-acts');
  const act = async (x, body, done) => {
    for (const k of a.querySelectorAll('button')) k.disabled = true;
    err.textContent = '';
    try {
      await send('POST', 'api/sell-action', body);
      if (done) toast(done);
      load();
    } catch (e) {
      err.textContent = e.message;
      for (const k of a.querySelectorAll('button')) k.disabled = false;
    }
  };
  const btn = (cls, text, fn) => {
    const x = el('button', 'ms-btn ' + cls, text);
    x.type = 'button';
    x.addEventListener('click', () => fn(x));
    a.append(x);
    return x;
  };
  // An offer posted while the meal before is still served reads as one for
  // that meal, so till it ends Start now says it will wait, and the server
  // schedules the offer for then.
  const early = s.opens && now() < s.opens;
  const go = btn(early ? 'grn after' : 'grn', early ? `⏰ Send after ${s.opens_after || 'meal'} ends` : '▶ Start now', (x) => act(x, { action: 'start', date: s.date, meal: s.meal, min: s.min, max: s.max, now: true, autopilot: !!s.autopilot },
    early ? `Scheduled. Your ${mealOf(v)} goes up at ${hm(s.opens)}` : `Selling your ${mealOf(v)}${s.autopilot ? ' on autopilot' : ''}`));
  if (early) {
    const prev = s.opens_after;
    go.title = `Posts at ${hm(s.opens)}, once ${prev || 'the meal before'} ends. An offer sent during ${prev || 'another meal'} gets read as one for ${prev || 'that meal'}.`;
    setTimeout(() => { if (go.isConnected) { go.textContent = '▶ Start now'; go.title = ''; go.classList.remove('after'); } }, (s.opens - now()) * 1000 + 500);
  }
  const auto = btn('auto' + (s.autopilot ? ' on' : ''), s.autopilot ? '✈ Autopilot on' : '✈ Autopilot off', (x) =>
    act(x, { action: 'autopilot', id: s.id, autopilot: !s.autopilot }));
  auto.setAttribute('role', 'switch');
  auto.setAttribute('aria-checked', String(!!s.autopilot));
  auto.title = 'Autopilot sells without waiting on you: it skips any buyer who would need you, even one offering more.';
  // Without the ledger loaded it goes to the sales page instead.
  tapCard(c, () => {
    if (s.for_friend ? openFriendHere(s.id, 'schedule') : openSellHere(s.date, s.meal)) return;
    location.href = s.for_friend ? 'sales.html' : `sales.html#sell=${encodeURIComponent(`${s.date}|${s.meal}`)}`;
  });
  c.append(a, err);
  return c;
}

// tapCard makes a whole sale card open its sheet. Clicks on the card's own
// controls (buttons, links, the range editor) and text selections don't.
function tapCard(c, open) {
  c.classList.add('tap');
  c.tabIndex = 0;
  c.addEventListener('click', (e) => {
    if (e.target.closest('button, a, input, label, .ms-edit') || String(getSelection())) return;
    open();
  });
  c.addEventListener('keydown', (e) => {
    if (e.target === c && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); open(); }
  });
}

// ledgerCell finds a meal in the sales ledger app.js loaded for the nudge.
function ledgerCell(date, meal) {
  if (typeof N === 'undefined' || !N) return null;
  const days = N.ahead ? [...N.days, { date: N.ahead.date, cells: [N.ahead.cell] }] : N.days;
  const d = days.find((x) => x.date === date);
  return (d && d.cells.find((x) => x.meal === meal)) || null;
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
    if (!v.smart) lines.push([{ t: `Takes anything from ${rup(v.min)} up`, c: 'ca' }]);
    else lines.push([{ t: `Aims for ${rup(v.target)}, takes ${rup(v.lowest)} now`, c: 'ca' }]);
    if (v.held) lines.push([{ t: 'Held at the target: three buyers are active.', c: 'c6' }]);
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
// shows one price and a haggling one keeps its ends a rupee apart.
function rangeEditor(v) {
  const e = rangeEdit;
  const fixed = v.min === v.max;
  // The scale hugs the current range, so a ₹5 step still moves a thumb a
  // useful distance. Typing goes past it.
  const B = { lo: Math.max(5, Math.floor((v.min * 0.6) / 5) * 5), hi: Math.max(20, Math.ceil((v.max * 1.5) / 5) * 5) };

  const box = el('div', 'ms-edit');
  const head = el('div', 'eh');
  head.append(el('b', '', fixed ? 'PRICE' : 'PRICE RANGE'), el('span', '', `was ${rangeText(v.min, v.max)}`));

  const hint = el('p', 'hint');
  hint.setAttribute('aria-live', 'polite');
  const error = el('p', 'err');
  error.setAttribute('role', 'alert');
  const acts = el('div', 'eacts');
  const cancel = el('button', 'ms-btn ghost', 'Cancel'), save = el('button', 'ms-btn gold', 'Save range');
  cancel.type = save.type = 'button';

  const pr = priceRange({
    lo: e.lo, hi: e.hi, bounds: B, fixed, gap: 1,
    marks: (fixed ? [v.min] : [v.min, v.max]).map((at) => ({ at, cls: 'was' })),
    axis: [rup(B.lo), '', rup(B.hi)],
    onChange: (lo, hi) => {
      e.lo = lo;
      e.hi = hi;
      hint.textContent = rangeHint(v, lo, hi);
      save.disabled = lo === v.min && hi === v.max;
    },
  });

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

  box.append(head, ...pr.nodes, hint, error, acts);
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

function postsRow(v) {
  const r = el('div', 'ms-row');
  r.append(el('span', 'lbl', 'Posted'));
  for (const g of postGroups(v)) r.append(groupPill(g, v));
  if (!v.posts.length) r.append(el('span', 'ms-none', 'not yet'));
  return r;
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
    btn('grn', '✓ Sold + QR', () => openTaskMenu(v, 'sold') || saleActionForm(v, 'sold'), 'Mark sold and send the buyer the QR');
    btn('', '+ Add buyer', () => saleActionForm(v, 'add'));
    btn('red', '✕ Stop', () => saleActionForm(v, 'stop'), 'Stop selling and delete the offer posts');
  }
  return a.children.length ? a : null;
}

// endedRow is a finished sale, folded to one line. It opens the sheet of
// the buyer openSaleBuyer picks; a sale nobody answered has nothing to open.
function endedRow(v) {
  const r = el(v.buyers.length ? 'button' : 'div', 'ms-done' + (won(v) ? ' won' : ''));
  if (v.buyers.length) {
    r.type = 'button';
    r.addEventListener('click', () => openSaleBuyer(v));
  }
  r.append(...mealHead(v, 'nm'));
  const bought = v.buyers.find((b) => b.state === 'bought');
  const amount = v.pay ? v.pay.amount : bought && (bought.asked || bought.agreed);
  r.append(el('span', 'st', won(v) ? `✓ SOLD${amount ? ' ' + rup(amount) : ''}` : `✕ ${v.state.replace('_', ' ').toUpperCase()}`));
  const n = v.buyers.length;
  r.append(el('span', 'sub', `${hm(v.started)}–${hm(v.ended || v.ends)} · ${n} ${n === 1 ? 'buyer' : 'buyers'}`));
  tip(r, () => heroTip(v));
  return r;
}

// tickSales moves the clocks, pay rings, countdowns and bars between fetches.
function tickSales() {
  const n = now();
  const frac = (e) => Math.min(1, Math.max(0, (+e.dataset.until - n) / Math.max(1, +e.dataset.until - +e.dataset.from)));
  for (const a of document.querySelectorAll('.sales-box circle[data-until], .sheet circle[data-until]')) {
    a.setAttribute('stroke-dashoffset', (+a.dataset.len * (1 - frac(a))).toFixed(1));
  }
  for (const b of document.querySelectorAll('.sheet [data-bar]')) b.style.setProperty('width', `${frac(b) * 100}%`);
  for (const t of document.querySelectorAll('.sales-box [data-fmt], .sheet [data-fmt]')) {
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
  // Folds inside a closed fold stay shut, so a hidden conversation isn't refetched.
  const opened = keep ? [...sheet.querySelectorAll('details[open][data-fold]')].filter((d) => !d.parentElement.closest('details:not([open])')).map((d) => d.dataset.fold) : [];
  sheet.replaceChildren();
  sheet.className = 'sheet sale-sheet' + (tone ? ' tone-' + tone : '');
  modal.hidden = false;
  return () => {
    for (const d of sheet.querySelectorAll('details[data-fold]')) {
      if (!opened.includes(d.dataset.fold)) continue;
      d.open = true;
      d.fill?.(); // draw a loaded conversation now, before the scroll is restored
    }
    sheet.scrollTop = scroll;
  };
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

// mainBuyer is the buyer a tap on a whole sale opens: one who needs you,
// then the one paying or who bought, then whoever did something last.
// A refund due or a payment picture needs you even from a muted or paying
// buyer; anything else stops needing you once the sale is over. Null while
// nobody has answered.
function mainBuyer(v) {
  const rank = (b) => {
    const c = look(b, v).cls;
    if (b.refund || sentPicture(b) || c === 'need' || (c === 'chk' && v.state === 'active')) return 0;
    return c === 'pay' || c === 'won' || (v.pay && v.pay.buyer === b.key) ? 1 : 2;
  };
  const lastAt = (b) => Math.max(b.joined || 0, ...b.events.map((e) => e.at || 0));
  let best = null;
  for (const b of v.buyers) {
    if (!best || rank(b) < rank(best) || (rank(b) === rank(best) && lastAt(b) > lastAt(best))) best = b;
  }
  return best;
}

// openSaleBuyer opens the sheet of the buyer with key, or else mainBuyer's.
// It returns false when the sale has no buyers, so callers can fall back to
// something else.
function openSaleBuyer(v, key) {
  const b = v.buyers.find((x) => x.key === key) || mainBuyer(v);
  if (!b) return false;
  openBuyerSheet(v, b.key);
  return true;
}

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
    const copy = el('button', 'sb-copy');
    copy.type = 'button';
    copy.title = 'Copy number';
    copy.setAttribute('aria-label', 'Copy number');
    copy.append(svgEl(icon('copy')));
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
  sheet.append(saleInformation(v), conversationFold(v, b, 'CONVERSATION'));

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
    acts.append(saleActionButton('✓', 'SOLD + SEND QR', () => saleActionForm(v, 'sold', b)));
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
    } else if ((e.kind === 'parked' || e.kind === 'auto_skipped') && row) {
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

// Operator forms stay open across live updates, preserving entered values.
function saleActionButton(glyph, label, action) {
  const b = el('button', 'sb-act');
  b.type = 'button';
  b.append(el('b', '', glyph), el('span', '', label));
  b.addEventListener('click', action);
  return b;
}
function saleActionForm(v, action, buyer) {
  saleSheet = { id: v.id, editing: true };
  sheetFor(v, false);
  const titles = { sold: 'MARK SOLD AND SEND QR', add: 'ADD BUYER', mute: 'MUTE BUYER', unmute: 'UNMUTE BUYER', stop: 'STOP SELLING' };
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
    pick.append(new Option('Other buyer (phone below)', ''));
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
    form.append(el('p', 'sb-note', 'Confirm only after you sold the meal. This stops negotiation, records it as paid and sends the buyer the meal QR. Other buyers get the usual sold notice.'));
  } else if (action === 'add') {
    form.append(el('p', 'sb-note', 'The bot will send this buyer its opening quote.'));
  } else if (action === 'stop') {
    form.append(el('p', 'sb-note', `Stop selling your ${mealLabel(v)}? The bot stops talking to buyers and deletes its offer posts from the groups. You can start a new sale after.`));
  } else {
    form.append(el('p', 'sb-note', `${buyer.name || buyer.phone || buyer.key}: ${action === 'mute' ? 'stop all bot messages for this task, including sold notices?' : 'allow bot messages for this task again?'}`));
  }
  const error = el('p', 'sb-note');
  error.setAttribute('role', 'alert');
  const acts = el('div', 'sb-acts');
  const submit = saleActionButton(action === 'stop' ? '✕' : '✓', action === 'sold' || action === 'mute' ? 'CONFIRM' : titles[action], () => {});
  submit.type = 'submit';
  if (action === 'stop') submit.classList.add('red');
  acts.append(submit, saleActionButton('✕', 'CANCEL', () => (buyer ? openBuyerSheet(v, buyer.key) : closeModal())));
  form.append(error, acts);
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = { action: action === 'stop' ? 'cancel' : action, id: v.id };
    for (const [k, input] of Object.entries(fields)) data[k] = input.value.trim();
    if (pick && pick.value) { data.buyer = pick.value; data.name = ''; }
    if (buyer && (action === 'mute' || action === 'unmute')) data.buyer = buyer.key;
    if (action === 'sold') {
      data.price = Number(data.price);
      data.send_qr = true;
      if (!data.buyer) { error.textContent = 'Enter the buyer\'s phone, so the bot can send the QR.'; return; }
    }
    submit.disabled = true;
    error.textContent = '';
    try {
      await send('POST', 'api/sell-action', data);
      closeModal();
      toast(action === 'stop' ? 'Stopped. The offer posts are being deleted.' : action === 'sold' ? 'Sold. The QR is on its way.' : 'Action saved');
      await load();
    } catch (err) { error.textContent = err.message; submit.disabled = false; }
  });
  sheet.append(form);
}

// saleInformation is a fold with who bought, at what price, and how many
// buyers the bot reached. It is read-only: payment warnings here never
// pause selling.
function saleInformation(v) {
  const buyers = v.buyers || [];
  const winner = buyers.find((b) => b.state === 'bought');
  const contacted = buyers.filter((b) => b.events.some((e) => ['prompt', 'buy_post', 'nudged'].includes(e.kind)) || b.bot_msgs > 0).length;
  const declined = buyers.filter((b) => b.state === 'passed').length;
  const dropped = buyers.filter((b) => ['skipped', 'timed_out', 'told_sold'].includes(b.state)).length;

  const box = fold('sb-fold', 'sale', 'ABOUT THIS SALE',
    winner ? `Sold to ${winner.name || winner.phone || 'buyer'}` : cap(v.state.replaceAll('_', ' ')));
  const body = box.lastChild;
  if (winner) {
    const st = el('div', 'sb-status grn');
    st.append(el('b', 'big', rup(v.pay?.amount || winner.agreed || winner.asked || 0)), el('b', 'ttl', 'AGREED PRICE'),
      el('p', '', `${winner.name || winner.phone || 'The buyer'} bought it.`));
    body.append(st);
  }
  const strip = el('div', 'sb-strip');
  for (const [k, n, cls, why] of [['Recorded', buyers.length, '', ''], ['Contacted', contacted, 'grn', ''],
    ['Declined', declined, declined ? 'red' : 'dim', ''], ['Dropped', dropped, 'dim', 'Skipped, timed out or told sold']]) {
    const c = el('div');
    if (why) c.title = why;
    c.append(el('small', '', k), el('b', cls, String(n)));
    strip.append(c);
  }
  body.append(strip, el('p', 'sb-note', 'Counts cover buyers the bot recorded, not everyone who saw a group post. Dropped means skipped, timed out or told sold. CHECK flags are for you to review and do not pause selling.'));

  return box;
}

// fold is a themed <details>: a Pixel title, a line of context and a
// chevron. key lets sheetFor reopen it after a live redraw.
function fold(cls, key, title, sub) {
  const d = el('details', cls);
  d.dataset.fold = key;
  const s = el('summary');
  s.append(el('b', 'ttl', title));
  if (sub) s.append(el('span', 'sub', sub));
  const chev = el('i', 'chev', '›');
  chev.setAttribute('aria-hidden', 'true');
  s.append(chev);
  d.append(s, el('div', 'fold-body'));
  return d;
}

// convoCache holds each conversation once fetched, by sale and buyer, so
// a live redraw of the sheet draws it at once. Each open still fetches in
// the background and redraws only when a message changed.
const convoCache = new Map();

// conversationFold loads the retained messages with buyer b when it
// opens. title defaults to the buyer's name.
function conversationFold(v, b, title) {
  const l = look(b, v);
  const d = fold('sb-convo ' + l.cls, 'convo:' + b.key, title || (b.name || b.phone || 'Buyer').toUpperCase(), cap(stateWord(b, l)));
  const content = d.lastChild;
  content.setAttribute('aria-live', 'polite');
  const url = `api/sale-conversation?id=${encodeURIComponent(v.id)}&buyer=${encodeURIComponent(b.key)}`;
  const ck = v.id + ':' + b.key;
  let shown = null; // the messages this fold last drew, as JSON
  const draw = (messages) => {
    shown = JSON.stringify(messages);
    content.replaceChildren();
    if (!messages.length) content.append(el('p', 'sb-note', 'Nothing recorded for this sale. Messages from before recording started are not kept.'));
    let day = v.date;
    for (const m of messages) {
      const md = ymd(m.at);
      content.append(chatBubble(m, b, url, md === day ? '' : dayWord(md)));
      day = md;
    }
    if (messages.length) content.append(el('p', 'sb-note', 'Only messages since recording started are kept. A missing picture may have expired or failed to download.'));
    content.dataset.loaded = 'true';
  };
  d.fill = () => { if (convoCache.has(ck)) draw(convoCache.get(ck)); };
  let loading = false;
  d.addEventListener('toggle', async () => {
    if (!d.open || loading) return;
    loading = true;
    if (!content.dataset.loaded) content.replaceChildren(el('p', 'sb-note', 'Loading the conversation…'));
    try {
      const r = await fetch(url, { cache: 'no-store' });
      if (!r.ok) throw new Error();
      const messages = await r.json();
      convoCache.set(ck, messages);
      if (d.isConnected && JSON.stringify(messages) !== shown) draw(messages);
    } catch {
      // A failed refresh leaves a chat already on screen as it is.
      if (!content.dataset.loaded) {
        const err = el('p', 'sb-note red', 'Could not load the conversation. Close it and open it again to retry.');
        err.setAttribute('role', 'alert');
        content.replaceChildren(err);
      }
    } finally {
      loading = false;
    }
  });
  return d;
}

const ATTACHMENT_WORD = { image: 'Picture', audio: 'Voice note or audio', video: 'Video', document: 'Document' };

// chatBubble draws one message: the buyer's on the left, yours and the
// bot's on the right, and anything to check outlined in egg yellow. day
// names the date when it differs from the message before.
function chatBubble(m, b, url, day) {
  const row = el('article', 'cv-msg' + (m.from_me ? ' me' : '') + (m.checking ? ' chk' : ''));
  row.append(el('small', 'cv-who', `${m.from_me ? 'YOU / BOT' : (b.name || b.phone || 'BUYER').toUpperCase()} · ${day ? day.toUpperCase() + ' ' : ''}${hm(m.at)}`));
  if (m.checking) {
    const w = el('div', 'cv-check');
    w.append(el('b', 'sb-chip egg', 'CHECK'), el('span', '', m.checking));
    row.append(w);
  }
  if (m.text) row.append(el('p', 'cv-text', m.text));
  const media = url + '&media=' + encodeURIComponent(m.id);
  if (m.media && ['audio', 'video', 'document'].includes(m.kind)) {
    const a = el('a', 'sb-chip cv-file', `↓ ${m.kind.toUpperCase()}`);
    a.href = media;
    a.download = 'attachment';
    a.setAttribute('aria-label', `Download ${m.kind}`);
    row.append(a);
  } else if (m.media) {
    const img = el('img');
    img.src = media;
    img.alt = m.from_me ? 'Picture sent to the buyer' : 'Picture the buyer sent';
    img.loading = 'lazy';
    row.append(img);
  } else if (ATTACHMENT_WORD[m.kind]) {
    row.append(el('p', 'cv-text dim', `${ATTACHMENT_WORD[m.kind]} unavailable. It may have been over 10 MiB or failed to download.`));
  }
  return row;
}
