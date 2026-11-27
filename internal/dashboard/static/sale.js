'use strict';

// Your running sales: the buyer sheet (who is haggling, paying or needs
// you, with the chat), the sale's action forms, and the hooks the sell
// sheet and attention cards use to open them (openSaleBuyer, openTaskMenu,
// ledgerCell).
//
// Uses app.js's globals (D, now, hm, dur, sheet, modal, closeModal,
// messagingApp, digits, toast, openListing). Buyer names are untrusted and
// only ever reach the page through textContent.

const C = { red: '#ff6b5b', egg: '#f2b51b', veg: '#34c463', gold: '#ffc53d', blue: '#5aa9ff', dim: '#586076', mut: '#8b94a7', line: '#262d3b' };
const rup = (x) => '₹' + (Number.isInteger(x) ? x : x.toFixed(2));
// A friend's meal is stored under a slot like "dinner#f1"; show "dinner"
// and whose it is.
// An extra sold apart from its meal (an egg with Kadamba's dinner) is
// stored under a slot like "dinner#e1" and names itself in extra: show
// "dinner · Egg".
const mealOf = (v) => v.base_meal || v.meal.split('#')[0];
const mealLabel = (v) => (v.for_friend ? `${mealOf(v)} · for ${v.friend || 'a friend'}` : v.extra ? `${mealOf(v)} · ${v.extra}` : mealOf(v));
// saleWord names a sale in a sentence: "dinner", "dinner egg".
const saleWord = (v) => (v.extra ? `${mealOf(v)} ${v.extra.toLowerCase()}` : mealOf(v));

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

// ---------- open sales ----------

let saleSheet = null; // {id, key} while a buyer sheet is open, {id, editing: true} while a form is

// renderSales runs after each api/state: it moves the clocks and redraws an
// open buyer sheet with the sale's latest state.
function renderSales() {
  const list = (D && D.sales) || [];
  tickSales();
  if (saleSheet && !saleSheet.editing && !modal.hidden) {
    const v = list.find((x) => x.id === saleSheet.id);
    if (v) openBuyerSheet(v, saleSheet.key, true);
  }
}
function saleClosed() { saleSheet = null; }

// openTaskMenu opens the sell menu for a running sale, yours or a friend's,
// the same one the sales page shows. It returns false when the ledger the
// nudge loaded doesn't have this task yet.
function openTaskMenu(v, view) {
  if (v.state !== 'active') return false;
  if (v.for_friend) return openFriendHere(v.id, 'task', view);
  if (v.extra) return openExtraHere(v.date, v.meal, view);
  const cell = ledgerCell(v.date, v.meal);
  if (!cell || !cell.task || cell.task.id !== v.id) return false;
  return openSellHere(v.date, v.meal, view);
}

// openExtraHere opens the sell sheet for an extra sold apart from its meal,
// by its slot ("dinner#e1"), from the ledger the nudge loaded. It returns
// false when that ledger doesn't have the slot yet.
function openExtraHere(date, slot, view) {
  if (typeof SELL === 'undefined' || typeof N === 'undefined' || !N || !N.owner) return false;
  const c = ledgerCell(date, slot.split('#')[0]);
  const hit = c && SELL.findExtra(c, slot);
  if (!hit) return false;
  sheet.className = 'sheet sell-sheet';
  SELL.open(date, SELL.extraCell(date, c, ...hit), view);
  return true;
}

// ledgerCell finds a meal in the sales ledger app.js loaded for the nudge.
function ledgerCell(date, meal) {
  if (typeof N === 'undefined' || !N) return null;
  const days = [...(N.days || []), ...(N.upcoming || [])];
  if (N.ahead) days.push({ date: N.ahead.date, cells: [N.ahead.cell] });
  const d = days.find((x) => x.date === date);
  return (d && d.cells.find((x) => x.meal === meal)) || null;
}

// ---------- buyers ----------

function stateWord(b, l) {
  if (b.muted) return 'muted';
  if (l.cls === 'need') { const [ic, w] = reason(b); return `${ic} ${w.toLowerCase()}`; }
  if (sentPicture(b)) return 'sent a picture';
  return STATE_WORD[b.state] || b.state;
}
const buyerPrice = (b, l) => (l.cls === 'won' ? b.asked || b.agreed : b.refund || b.agreed || b.offer || b.asked);

// tickSales moves the buyer sheet's countdown and bar between fetches.
function tickSales() {
  const n = now();
  const frac = (e) => Math.min(1, Math.max(0, (+e.dataset.until - n) / Math.max(1, +e.dataset.until - +e.dataset.from)));
  for (const b of document.querySelectorAll('.sheet [data-bar]')) b.style.setProperty('width', `${frac(b) * 100}%`);
  for (const t of document.querySelectorAll('.sheet [data-fmt]')) {
    const left = Math.max(0, +t.dataset.until - n);
    t.textContent = `${Math.floor(left / 60)}:${String(Math.floor(left % 60)).padStart(2, '0')}`;
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
    form.append(el('p', 'sb-note', v.extra
      ? `Confirm only after you sold the ${saleWord(v)}. This stops negotiation, records it as paid and sends the buyer the QR. Other buyers get the usual sold notice.`
      : 'Confirm only after you sold the meal. This stops negotiation, records it as paid and sends the buyer the meal QR. Other buyers get the usual sold notice.'));
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
