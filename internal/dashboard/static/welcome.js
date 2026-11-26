'use strict';

// The welcome page is a chat with the bot. A new account saves its mess
// API key, links its WhatsApp, picks the groups to post in and how it
// sells, saves its UPI ID, and asks the admins for approval, each as an
// answer to the bot. chatPlan (welcome-plan.js) decides what the chat
// says from the server's state; this file loads that state, draws the
// chat and sends the answers. The server sends approved accounts on to
// the board.

const reduced = matchMedia('(prefers-reduced-motion: reduce)');

$('logo').append(svgEl(icon('coin')));
$('send').append(svgEl(pix([
  'x......',
  'xxx....',
  'xxxxx..',
  'xxxxxxx',
  'xxxxx..',
  'xxx....',
  'x......',
], { x: '#241703' })));
$('pick').append(svgEl(pix([
  '..xxx...',
  'xxxxxxxx',
  'xx...xxx',
  'x..o..xx',
  'x.ooo.xx',
  'x..o..xx',
  'xx...xxx',
  'xxxxxxxx',
], { x: '#e8ecf3', o: '#e8ecf3' })));

// S is what chatPlan reads. See welcome-plan.js.
const S = {
  name: '', status: 'pending', canStoreKeys: true, keySet: false,
  wa: null, codeMode: false, phone: '',
  sell: null, upi: null, acks: {}, editUPI: false, errors: {},
};

// The answers the server can't tell from its defaults live in this
// browser. Without storage the chat asks them again, with the saved
// answer picked.
const ackKey = () => `messwa.welcome.${S.name}`;
function loadAcks() {
  try { S.acks = JSON.parse(localStorage.getItem(ackKey())) || {}; } catch { S.acks = {}; }
}
function ack(step) {
  S.acks[step] = true;
  try { localStorage.setItem(ackKey(), JSON.stringify(S.acks)); } catch { /* asks again next time */ }
}

function setErr(step, e) {
  if (e) S.errors[step] = e.message || String(e);
  else delete S.errors[step];
}

// ---------- drawing the chat ----------

const nodes = new Map(); // item key to its row in the log
let plan = { items: [], ask: null, step: 'key' };
let queue = []; // keys waiting to type in, in order
let busy = false;
let drawn = false;
let ready = false; // set once the first answers are all in

// text draws a line, with `backticks` as code.
function text(s) {
  const p = el('p', 'b-text');
  s.split('`').forEach((part, i) => p.append(i % 2 ? el('code', '', part) : part));
  return p;
}

function typingDots() {
  const d = el('span', 'dots');
  d.setAttribute('aria-label', 'typing');
  d.append(el('i'), el('i'), el('i'));
  return d;
}

function bubble(it) {
  const b = el('div', 'bubble');
  if (it.tone) b.classList.add(it.tone);
  if (it.typing && !it.text) { b.classList.add('typing'); b.append(typingDots()); return b; }
  if (it.secret) b.classList.add('secret');
  if (it.text) b.append(text(it.text));
  if ('qr' in it) b.append(qrBox(it.qr));
  if (it.code) b.append(el('div', 'pair-code', it.code.length === 8 ? `${it.code.slice(0, 4)}-${it.code.slice(4)}` : it.code));
  if (it.groups) { b.classList.add('wide'); b.append(groupPicker(it)); }
  if (it.demo) { b.classList.add('wide', 'demo-wrap'); b.append(demo(it.demo)); }
  if (it.waiting) b.append(el('span', 'waiting', 'waiting for an admin'));
  if (it.typing) b.append(typingDots());
  if (it.edit) {
    const e = el('button', 'b-edit', 'Change');
    e.type = 'button';
    e.addEventListener('click', () => { S.editUPI = true; render(); });
    b.append(e);
  }
  if (it.from === 'sys') b.prepend(el('span', 'tick', '✓'));
  return b;
}

function qrBox(src) {
  const box = el('div', 'qr');
  if (!src) { box.classList.add('loading'); box.append(typingDots()); return box; }
  const img = el('img');
  img.src = src;
  img.alt = 'QR code to scan in WhatsApp, Linked devices';
  box.append(img);
  return box;
}

function groupPicker(it) {
  const list = el('div', 'g-pick');
  list.setAttribute('role', 'group');
  list.setAttribute('aria-label', 'Groups to post in, in order');
  for (const g of it.groups) {
    const n = it.order.indexOf(g.jid) + 1;
    const b = el('button', 'g-chip');
    b.type = 'button';
    b.setAttribute('aria-pressed', String(n > 0));
    b.disabled = !g.joined && !n;
    b.append(el('span', 'g-n', n ? String(n) : ''), el('span', 'g-name', g.name || g.jid));
    if (!g.joined) b.append(el('span', 'g-tag', 'not in it'));
    b.addEventListener('click', () => toggleGroup(g.jid));
    list.append(b);
  }
  return list;
}

// demo is the sample buyer chat for upi: the buyer asks, the bot answers
// for you with a QR for the price.
function demo(upi) {
  const phone = el('div', 'demo-phone');
  const head = el('div', 'demo-head');
  const who = el('div');
  who.append(el('b', '', 'Asha'), el('small', '', 'buyer'));
  head.append(el('span', 'demo-av', 'A'), who);
  const log = el('div', 'demo-log');
  const qr = el('div', 'demo-b out demo-img');
  const img = el('img');
  img.src = `api/upi-qr-preview?upi=${encodeURIComponent(upi)}`;
  img.alt = `UPI QR paying ₹50 to ${upi}`;
  qr.append(img, el('span', '', `₹50 to ${upi}`));
  log.append(
    el('div', 'demo-b in', 'kadamba lunch?'),
    el('div', 'demo-b out', 'ok 50'),
    el('div', 'demo-b out', 'pay on this qr and send the screenshot, ill send the meal qr after'),
    qr,
    el('div', 'demo-b in', 'sent ✅'),
    el('div', 'demo-b out', 'here is your meal qr'),
  );
  phone.append(head, log);
  return phone;
}

function row(it) {
  const r = el('div', `msg ${it.from}`);
  r.dataset.key = it.key;
  if (it.from === 'bot') {
    const av = el('span', 'msg-av');
    av.append(svgEl(icon('coin')));
    r.append(av);
  }
  r.append(bubble(it));
  r.dataset.sig = JSON.stringify(it);
  return r;
}

// place puts a new row where its item sits in the plan: before the next
// item already drawn.
function place(r, key) {
  const at = plan.items.findIndex((x) => x.key === key);
  const next = plan.items.slice(at + 1).map((x) => nodes.get(x.key)).find(Boolean);
  $('log').insertBefore(r, next || $('typing') || null);
}

// runs marks the first row of each run of bot rows, which shows the avatar.
function runs() {
  let prev = '';
  for (const r of $('log').children) {
    const from = r.classList.contains('bot') ? 'bot' : r.classList.contains('me') ? 'me' : 'sys';
    r.classList.toggle('first', from !== prev);
    prev = from;
  }
}

function toBottom() {
  requestAnimationFrame(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: reduced.matches ? 'auto' : 'smooth' }));
}

function add(it, animate) {
  const r = row(it);
  if (animate) r.classList.add('in');
  if (animate && it.demo) r.classList.add('play');
  nodes.set(it.key, r);
  place(r, it.key);
  runs();
}

// render draws the plan for S. Items already on screen change in place;
// new ones type in one by one, unless it's the first draw of answers given
// before, or motion is reduced.
function render() {
  if (!ready) return;
  plan = chatPlan(S);
  const keys = new Set(plan.items.map((x) => x.key));
  for (const [k, r] of nodes) if (!keys.has(k)) { r.remove(); nodes.delete(k); }
  queue = queue.filter((k) => keys.has(k));
  // On the first draw, everything up to the last answer is history.
  let history = -1;
  if (!drawn) plan.items.forEach((x, i) => { if (x.from !== 'bot') history = i; });
  plan.items.forEach((it, i) => {
    const r = nodes.get(it.key);
    if (r) {
      const sig = JSON.stringify(it);
      if (r.dataset.sig !== sig) {
        r.dataset.sig = sig;
        // Keep keyboard focus on the same button when a live bubble redraws.
        const old = [...r.lastChild.querySelectorAll('button')];
        const f = old.indexOf(document.activeElement);
        const b = bubble(it);
        r.lastChild.replaceWith(b);
        if (f >= 0) b.querySelectorAll('button')[f]?.focus({ preventScroll: true });
      }
      return;
    }
    if (queue.includes(it.key)) return;
    if (reduced.matches || i <= history || it.typing) add(it, drawn && !reduced.matches);
    else queue.push(it.key);
  });
  if (!drawn) toBottom();
  drawn = true;
  $('progress').style.width = `${Math.round(chatProgress(plan.step) * 100)}%`;
  pump();
  paintAsk();
}

// pump types in the queued rows: a bot row gets a moment of typing dots,
// longer for longer lines; your own rows appear at once.
function pump() {
  if (busy || !queue.length) return;
  const key = queue[0];
  const it = plan.items.find((x) => x.key === key);
  busy = true;
  const show = () => {
    $('typing')?.remove();
    $('state').textContent = 'online';
    busy = false;
    queue.shift();
    if (nodes.has(key)) return pump();
    const now = plan.items.find((x) => x.key === key);
    if (now) add(now, true);
    toBottom();
    pump();
    paintAsk();
  };
  if (it.from !== 'bot') return show();
  const t = row({ key: 'typing', from: 'bot', typing: true });
  t.id = 'typing';
  t.classList.add('in');
  $('log').append(t);
  runs();
  $('state').textContent = 'typing…';
  toBottom();
  setTimeout(show, Math.min(350 + (it.text || '').length * 9, 900));
}

// ---------- the composer ----------

const INPUTS = {
  key: { type: 'password', placeholder: 'Paste your mess API key' },
  phone: { type: 'tel', placeholder: '+91 98765 43210', autocomplete: 'tel' },
  upi: { type: 'text', placeholder: 'name@okaxis' },
};
let askSig = '';
let sending = false;

function paintAsk() {
  const ask = queue.length || busy ? null : plan.ask;
  const sig = JSON.stringify(ask);
  $('composer').classList.toggle('empty', !ask);
  if (sig === askSig) return;
  const was = askSig ? JSON.parse(askSig) : null;
  askSig = sig;
  const chips = (ask && ask.chips) || [];
  $('chips').replaceChildren(...chips.map((c) => {
    const b = el('button', `chip${c.primary ? ' primary' : ''}${c.sub ? ' two' : ''}`);
    b.type = 'button';
    b.disabled = !!c.disabled;
    b.append(el('b', '', c.label));
    if (c.sub) b.append(el('small', '', c.sub));
    b.addEventListener('click', () => chip(c.id, c.label));
    return b;
  }));
  $('chips').classList.toggle('in', !!chips.length && !reduced.matches);
  // New replies grow the composer; keep the question above them in view.
  if (ask) toBottom();
  const mode = ask && ask.input;
  $('say').hidden = !mode;
  if (!mode) return;
  const input = $('sayInput');
  if (!was || was.input !== mode) {
    const o = INPUTS[mode];
    input.type = o.type;
    input.placeholder = o.placeholder;
    input.autocomplete = o.autocomplete || 'off';
    input.value = ask.value || '';
    $('pick').hidden = mode !== 'upi';
    if (matchMedia('(pointer: fine)').matches) input.focus({ preventScroll: true });
  }
}

$('say').addEventListener('submit', async (e) => {
  e.preventDefault();
  const mode = plan.ask && plan.ask.input;
  const v = $('sayInput').value.trim();
  if (!mode || !v || sending) return;
  sending = true;
  $('send').disabled = true;
  try {
    if (mode === 'key') await saveKey(v);
    if (mode === 'phone') await getCode(v);
    if (mode === 'upi') await saveUPI(v);
  } finally {
    sending = false;
    $('send').disabled = false;
  }
});

async function saveKey(key) {
  setErr('key');
  try {
    await send('PUT', 'api/mess-key', { key });
    $('sayInput').value = '';
    await paint();
  } catch (x) { setErr('key', x); render(); }
}

async function getCode(phone) {
  setErr('wa');
  S.phone = phone;
  S.wa = { ...S.wa, code: '' };
  render();
  try {
    await send('POST', 'api/whatsapp/code', { phone });
  } catch (x) { setErr('wa', x); }
  paintWhatsApp();
}

async function chip(id, label) {
  if (id === 'phone' || id === 'qr') {
    S.codeMode = id === 'phone';
    setErr('wa');
    return paintWhatsApp();
  }
  if (id === 'groups') { ack('groups'); return render(); }
  const [k, v] = id.split(':');
  if (k === 'handoff' || k === 'buyers') {
    // Picked straight away, so the chat moves on while it saves.
    S.sell = { ...S.sell, [k]: v };
    ack(k);
    render();
    return saveSell({ [k]: v });
  }
  if (id === 'ask') {
    setErr('ask');
    try {
      await send('POST', 'api/approval');
      await paint();
    } catch (x) { setErr('ask', x); render(); }
  }
}

// ---------- WhatsApp ----------

// The chat starts linking as soon as the page opens and starts a new
// session whenever the last one runs out (they last about 160 seconds), so
// a fresh QR is always up. It checks every 3 seconds, since the QR rotates.
// The phone-number mode shows a code instead.
let waTimer = null;
let restarting = false;

async function startLink() {
  if (restarting || document.hidden) return;
  restarting = true;
  try { await send('POST', 'api/whatsapp', {}); } catch (x) { setErr('wa', x); } finally { restarting = false; }
}

async function paintWhatsApp() {
  if (S.status === 'declined') return;
  try {
    const r = await fetch('api/whatsapp', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    const was = !!(S.wa && S.wa.linked);
    S.wa = d;
    setErr('wa', d.error && !d.linked ? new Error(d.error) : null);
    if (!d.linked && !d.pairing) startLink();
    if (!d.linked && !waTimer) waTimer = setInterval(paintWhatsApp, 3000);
    if (d.linked && waTimer) { clearInterval(waTimer); waTimer = null; }
    if (was !== !!d.linked) await loadSell();
  } catch (x) { setErr('wa', x); }
  render();
}

// A hidden tab doesn't start sessions; catch up when it comes back.
document.addEventListener('visibilitychange', () => { if (!document.hidden && !(S.wa && S.wa.linked)) paintWhatsApp(); });

// ---------- groups and how you sell ----------

let sellTimer = null;

async function loadSell() {
  clearTimeout(sellTimer);
  if (S.status === 'declined') return;
  try {
    const r = await fetch('api/setup-sell', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    S.sell = d;
    // A fresh account gets every group it's in, so it can sell at once.
    const joined = d.groups.filter((g) => g.joined).map((g) => g.jid);
    if (d.checked && !d.saved && joined.length) return saveSell({ groups: joined });
    setErr('groups', d.error ? new Error(d.error) : null);
    // Not in any group yet: keep looking until they join one.
    if (d.checked && !joined.length) sellTimer = setTimeout(loadSell, 10000);
  } catch (x) { setErr('groups', x); }
  // Still connecting, or not linked yet: look again soon.
  if (S.wa && S.wa.linked && (!S.sell || !S.sell.checked)) sellTimer = setTimeout(loadSell, 3000);
  render();
}

async function saveSell(change) {
  const g = S.sell;
  const body = {
    groups: postingOrder(g.groups).map((x) => x.jid),
    handoff: g.handoff,
    buyers: g.buyers,
    ...change,
  };
  const step = 'groups' in change ? 'groups' : 'how';
  try {
    S.sell = await send('PUT', 'api/setup-sell', body);
    setErr('groups');
    setErr('how');
  } catch (x) { setErr(step, x); }
  render();
}

// toggleGroup adds a group to the end of the posting order, or takes it out.
function toggleGroup(jid) {
  const order = postingOrder(S.sell.groups).map((x) => x.jid);
  const next = order.includes(jid) ? order.filter((x) => x !== jid) : [...order, jid];
  if (!next.length) {
    setErr('groups', new Error('Keep at least one group, or your offers go nowhere.'));
    return render();
  }
  // Draw the new order now; the save confirms it.
  S.sell = { ...S.sell, groups: S.sell.groups.map((x) => ({ ...x, picked: next.includes(x.jid), order: next.indexOf(x.jid) + 1 })) };
  render();
  saveSell({ groups: next });
}

// ---------- UPI ----------

async function loadUPI() {
  try {
    const r = await fetch('api/setup-upi', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    S.upi = d.upi;
  } catch (x) { S.upi = ''; setErr('upi', x); }
  render();
}

async function saveUPI(upi) {
  setErr('upi');
  try {
    const d = await send('PUT', 'api/setup-upi', { upi });
    S.upi = d.upi;
    S.editUPI = false;
    $('sayInput').value = '';
  } catch (x) { setErr('upi', x); }
  render();
}

$('pick').addEventListener('click', () => $('upiFile').click());
// A UPI QR image fills the UPI ID in and saves it.
$('upiFile').addEventListener('change', async () => {
  const file = $('upiFile').files[0];
  $('upiFile').value = '';
  if (!file) return;
  setErr('upi');
  $('pick').disabled = true;
  try {
    const m = await me();
    const body = new FormData();
    body.append('qr', file);
    const r = await fetch('api/upi-qr', { method: 'POST', cache: 'no-store', headers: { 'X-CSRF-Token': m.csrf }, body });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    await saveUPI(d.upi);
  } catch (x) { setErr('upi', x); render(); } finally { $('pick').disabled = false; }
});

// ---------- the account ----------

let polling = null;

async function paint() {
  mePromise = null;
  const m = await me();
  if (m.status === 'active') {
    // Approved while watching: say so, then go.
    const watching = S.status === 'requested' && drawn;
    S.status = 'active';
    if (watching) render();
    setTimeout(() => { location.href = './'; }, watching ? 1400 : 0);
    return;
  }
  if (S.name !== m.name) { S.name = m.name; loadAcks(); }
  S.status = m.status;
  S.canStoreKeys = m.can_store_keys;
  S.keySet = m.mess_key_set;
  render();

  // The server pushes decisions (see listen below). A decision made with
  // the CLI, in another process, only shows up through this slower check.
  const asked = m.status === 'requested';
  if (asked && !polling) polling = setInterval(() => paint().catch(() => {}), 60000);
  if (!asked && polling) { clearInterval(polling); polling = null; }
}

$('logout').addEventListener('click', async () => {
  try { await send('POST', 'api/logout'); } finally { location.href = 'login.html'; }
});

// listen repaints as soon as an admin approves or declines this account.
// A reconnect repaints too, in case a decision landed while it was down.
function listen() {
  if (typeof EventSource === 'undefined') return;
  const es = new EventSource('api/account-events');
  es.addEventListener('account', () => paint().catch(() => {}));
  es.addEventListener('open', () => paint().catch(() => {}));
}

// Draw nothing until the first answers are in, so a returning user's
// history comes in as one piece instead of typing out again.
Promise.all([paint(), loadUPI()])
  .then(() => (S.status === 'declined' ? null : paintWhatsApp()))
  .catch(() => {})
  .then(() => { ready = true; render(); listen(); });
