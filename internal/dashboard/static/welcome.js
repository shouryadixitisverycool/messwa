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
  sell: null, upi: null, acks: {}, edit: null, errors: {},
};

// The answers the server can't tell from its defaults live in this
// browser. Without storage the chat asks them again, with the saved
// answer picked.
const ackKey = () => `messwa.welcome.${S.name}`;
function loadAcks() {
  try { S.acks = JSON.parse(localStorage.getItem(ackKey())) || {}; } catch { S.acks = {}; }
}
function ack(step, on = true) {
  if (on) S.acks[step] = true;
  else delete S.acks[step];
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
let typingTimer = null; // the queued row being typed now
let typingKey = '';
let drawn = false;
let ready = false; // set once the first answers are all in
let replay = false; // the next draw brings back rows already seen

// text draws a line, with `backticks` as code.
function text(s) {
  const p = el('p', 'b-text');
  s.split('`').forEach((part, i) => p.append(i % 2 ? el('code', '', part) : part));
  return p;
}

function typingDots() {
  const d = el('span', 'dots');
  d.setAttribute('aria-hidden', 'true');
  d.append(el('i'), el('i'), el('i'));
  return d;
}

const EDITS = { key: 'Change mess key', handoff: 'Change who talks to buyers', buyers: 'Change who can buy', upi: 'Change UPI ID' };

function bubble(it) {
  const b = el('div', 'bubble');
  if (it.tone) b.classList.add(it.tone);
  if (it.tone === 'err') b.setAttribute('role', 'alert');
  if (it.typing && !it.text) { b.classList.add('typing'); b.append(typingDots()); return b; }
  if (it.secret) {
    b.classList.add('secret');
    const t = text(it.text);
    t.setAttribute('aria-hidden', 'true');
    b.append(t, el('span', 'sr', it.secret));
  } else if (it.text) b.append(text(it.text));
  if ('qr' in it) b.append(qrBox(it.qr));
  if (it.code) b.append(el('div', 'pair-code', it.code.length === 8 ? `${it.code.slice(0, 4)}-${it.code.slice(4)}` : it.code));
  if (it.groups) { b.classList.add('wide'); b.append(groupPicker(it)); }
  if (it.demo) { b.classList.add('wide', 'demo-wrap'); b.append(demo(it.demo)); }
  if (it.waiting) b.append(el('span', 'waiting', 'waiting for an admin'));
  if (it.typing) b.append(typingDots());
  if (it.edit) {
    const e = el('button', 'b-edit', 'Change');
    e.type = 'button';
    e.setAttribute('aria-label', EDITS[it.edit] || 'Change');
    e.addEventListener('click', () => {
      // Start from the saved answer, not a draft left from last time.
      $('sayInput').value = '';
      S.edit = it.edit;
      setErr(it.edit);
      render();
    });
    b.append(e);
  }
  if (it.from === 'sys') {
    const t = el('span', 'tick', '✓');
    t.setAttribute('aria-hidden', 'true');
    b.prepend(t);
  }
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
    const b = el('button', 'g-chip');
    b.type = 'button';
    b.dataset.jid = g.jid;
    b.append(el('span', 'g-n'), el('span', 'g-name', g.name || g.jid));
    if (!g.joined) b.append(el('span', 'g-tag', 'not in it'));
    b.addEventListener('click', () => toggleGroup(g.jid));
    list.append(b);
  }
  markGroups(list, it);
  return list;
}

// markGroups shows the picked groups and their order on the chips already
// drawn, so a tap doesn't redraw the picker under the finger.
function markGroups(list, it) {
  for (const b of list.children) {
    const g = it.groups.find((x) => x.jid === b.dataset.jid);
    const n = it.order.indexOf(b.dataset.jid) + 1;
    b.setAttribute('aria-pressed', String(n > 0));
    b.disabled = !g.joined && !n;
    b.firstChild.textContent = n ? String(n) : '';
  }
}

// demo is the sample buyer chat for upi: the buyer asks, the bot answers
// for you with a QR for the price.
function demo(upi) {
  const phone = el('figure', 'demo-phone');
  phone.setAttribute('aria-label', 'What a buyer sees');
  const head = el('div', 'demo-head');
  const who = el('div');
  who.append(el('b', '', 'Asha'), el('small', '', 'buyer'));
  const av = el('span', 'demo-av', 'A');
  av.setAttribute('aria-hidden', 'true');
  head.append(av, who);
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
    av.setAttribute('aria-hidden', 'true');
    av.append(svgEl(icon('coin')));
    r.append(av);
  }
  r.append(bubble(it));
  r.dataset.sig = JSON.stringify(it);
  return r;
}

// patch updates a live bubble without redrawing it where it can: a new QR
// or a new group pick. Redrawing would make screen readers read it again.
function patch(r, was, it) {
  const same = (k) => JSON.stringify(was[k]) === JSON.stringify(it[k]);
  const only = (...ks) => Object.keys({ ...was, ...it }).every((k) => ks.includes(k) || same(k));
  if (only('qr') && was.qr && it.qr) {
    r.querySelector('.qr img').src = it.qr;
    return true;
  }
  if (only('groups', 'order') && was.groups && it.groups.length === was.groups.length
    && it.groups.every((g, i) => g.jid === was.groups[i].jid && g.name === was.groups[i].name && g.joined === was.groups[i].joined)) {
    markGroups(r.querySelector('.g-pick'), it);
    return true;
  }
  return false;
}

// place puts a new row where its item sits in the plan: before the next
// item already drawn.
function place(r, key) {
  const at = plan.items.findIndex((x) => x.key === key);
  const next = plan.items.slice(at + 1).map((x) => nodes.get(x.key)).find(Boolean);
  $('log').insertBefore(r, next || $('typing') || null);
  return !next;
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
  const last = place(r, it.key);
  runs();
  return last;
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
  // A row that was typing in has gone: stop typing it.
  if (typingKey && !keys.has(typingKey)) stopTyping();
  // On the first draw, everything up to the last answer is history.
  let history = -1;
  if (!drawn) plan.items.forEach((x, i) => { if (x.from !== 'bot') history = i; });
  // Rows coming back after a change come back whole.
  if (replay) history = plan.items.length - 1;
  replay = false;
  let appended = false;
  plan.items.forEach((it, i) => {
    const r = nodes.get(it.key);
    if (r) {
      const sig = JSON.stringify(it);
      if (r.dataset.sig === sig) return;
      const was = JSON.parse(r.dataset.sig);
      r.dataset.sig = sig;
      if (patch(r, was, it)) return;
      // Keep keyboard focus on the same button when a live bubble redraws.
      const old = [...r.lastChild.querySelectorAll('button')];
      const f = old.indexOf(document.activeElement);
      const b = bubble(it);
      r.lastChild.replaceWith(b);
      if (f >= 0) b.querySelectorAll('button')[f]?.focus({ preventScroll: true });
      return;
    }
    if (queue.includes(it.key) || typingKey === it.key) return;
    const idle = !queue.length && !typingKey;
    if (reduced.matches || i <= history || (it.typing && idle)) appended = add(it, drawn && !reduced.matches && i > history) || appended;
    else queue.push(it.key);
  });
  if (!drawn) {
    toBottom();
    // Announce new lines from here on, not the history.
    $('log').setAttribute('aria-live', 'polite');
  } else if (appended) toBottom();
  drawn = true;
  $('progress').style.width = `${Math.round(chatProgress(plan.step) * 100)}%`;
  pump();
  paintAsk();
}

function stopTyping() {
  clearTimeout(typingTimer);
  typingTimer = null;
  typingKey = '';
  $('typing')?.remove();
  $('state').textContent = 'online';
}

// pump types in the queued rows: a bot row gets a moment of typing dots,
// longer for longer lines; your own rows and waiting dots appear at once.
function pump() {
  if (typingKey) return;
  while (queue.length) {
    const key = queue.shift();
    const it = plan.items.find((x) => x.key === key);
    if (!it || nodes.has(key)) continue;
    if (it.from !== 'bot' || it.typing) {
      add(it, true);
      toBottom();
      continue;
    }
    typingKey = key;
    const t = row({ key: 'typing', from: 'bot', typing: true });
    t.id = 'typing';
    t.classList.add('in');
    $('log').append(t);
    runs();
    $('state').textContent = 'typing…';
    toBottom();
    typingTimer = setTimeout(() => {
      stopTyping();
      const now = plan.items.find((x) => x.key === key);
      if (now && !nodes.has(key)) add(now, true);
      toBottom();
      pump();
      paintAsk();
    }, Math.min(350 + (it.text || '').length * 9, 900));
    return;
  }
  runs();
}

// ---------- the composer ----------

const INPUTS = {
  key: { type: 'password', placeholder: 'Paste your mess API key', label: 'Mess API key' },
  phone: { type: 'tel', placeholder: '+91 98765 43210', autocomplete: 'tel', label: 'WhatsApp number' },
  upi: { type: 'text', placeholder: 'name@okaxis', label: 'UPI ID' },
};
let askSig = 'null';
let inputMode = ''; // the input shown last, kept while the bot types
let sending = false; // an answer is on its way to the server

// paintAsk shows the replies for the question the chat is on. While the
// bot is still typing, the last replies stay up but can't be sent.
function paintAsk() {
  const typing = queue.length > 0 || !!typingKey;
  const ask = typing ? null : plan.ask;
  const hold = typing && askSig !== 'null';
  $('composer').classList.toggle('empty', !ask && !hold);
  const off = typing || sending;
  $('send').disabled = off;
  $('pick').disabled = off;
  if (hold) {
    // Rebuilt when typing ends, even if the question hasn't changed.
    for (const b of $('chips').children) b.disabled = true;
    askSig = 'held';
    return;
  }
  const sig = JSON.stringify(ask) + sending;
  if (sig === askSig) return;
  const inComposer = $('composer').contains(document.activeElement) || document.activeElement === document.body;
  askSig = ask ? sig : 'null';
  const chips = (ask && ask.chips) || [];
  $('chips').replaceChildren(...chips.map((c) => {
    const b = el('button', `chip${c.primary ? ' primary' : ''}${c.sub ? ' two' : ''}`);
    b.type = 'button';
    b.disabled = !!c.disabled || sending;
    if (c.on) b.setAttribute('aria-current', 'true');
    b.append(el('b', '', c.label));
    if (c.sub) b.append(el('small', '', c.sub));
    b.addEventListener('click', () => chip(c.id));
    return b;
  }));
  $('chips').classList.toggle('in', !!chips.length && !reduced.matches);
  // New replies grow the composer; keep the question above them in view.
  if (ask) toBottom();
  const mode = (ask && ask.input) || '';
  const wasHidden = $('say').hidden;
  $('say').hidden = !mode;
  if (mode) {
    const input = $('sayInput');
    if (mode !== inputMode || (wasHidden && !input.value)) {
      const o = INPUTS[mode];
      input.type = o.type;
      input.placeholder = o.placeholder;
      input.autocomplete = o.autocomplete || 'off';
      input.setAttribute('aria-label', o.label);
      input.value = ask.value || '';
      $('pick').hidden = mode !== 'upi';
    }
    inputMode = mode;
  }
  // Move focus to the new replies when it was in the composer or nowhere,
  // so a keyboard user lands on the next answer. Touch screens don't get
  // the input focused, which would pop the keyboard up.
  if (ask && inComposer && !sending) {
    const first = [...$('chips').children].find((b) => !b.disabled);
    if (mode && matchMedia('(pointer: fine)').matches) $('sayInput').focus({ preventScroll: true });
    else if (first && !mode) first.focus({ preventScroll: true });
  }
}

$('say').addEventListener('submit', async (e) => {
  e.preventDefault();
  const mode = plan.ask && plan.ask.input;
  const v = $('sayInput').value.trim();
  if (!mode || !v || sending || queue.length || typingKey) return;
  sending = true;
  paintAsk();
  try {
    if (mode === 'key') await saveKey(v);
    if (mode === 'phone') await getCode(v);
    if (mode === 'upi') await saveUPI(v);
  } finally {
    sending = false;
    paintAsk();
  }
});

async function saveKey(key) {
  setErr('key');
  try {
    await send('PUT', 'api/mess-key', { key });
    $('sayInput').value = '';
    if (S.edit === 'key') { S.edit = null; replay = true; }
    await paint();
  } catch (x) { setErr('key', x); render(); }
}

async function getCode(phone) {
  setErr('code');
  S.phone = phone;
  S.wa = { ...S.wa, code: '' };
  render();
  try {
    const d = await send('POST', 'api/whatsapp/code', { phone });
    if (d.code) S.wa = { ...S.wa, code: d.code };
  } catch (x) { setErr('code', x); }
  render();
}

async function chip(id) {
  if (sending) return;
  if (id === 'keep') {
    $('sayInput').value = '';
    setErr(S.edit);
    S.edit = null;
    replay = true;
    return render();
  }
  if (id === 'phone' || id === 'qr') {
    S.codeMode = id === 'phone';
    setErr('code');
    render();
    return paintWhatsApp();
  }
  if (id === 'groups') { ack('groups'); return render(); }
  const [k, v] = id.split(':');
  if (k === 'handoff' || k === 'buyers') {
    // Picked straight away, so the chat moves on while it saves. A failed
    // save takes the answer back and asks again (see saveSell).
    if (S.edit === k || S.errors[k]) replay = true;
    // Until it saves, the admins can't be asked.
    S.sell = { ...S.sell, [k]: v, pending: true };
    ack(k);
    S.edit = null;
    setErr(k);
    render();
    return saveSell(null, k);
  }
  if (id === 'ask') {
    setErr('ask');
    sending = true;
    paintAsk();
    try {
      await send('POST', 'api/approval');
      await paint();
    } catch (x) { setErr('ask', x); } finally { sending = false; }
    render();
  }
}

// ---------- WhatsApp ----------

// The chat starts linking as soon as the page opens and starts a new
// session whenever the last one runs out (they last about 160 seconds), so
// a fresh QR is always up. It checks every 3 seconds, since the QR rotates.
// The phone-number mode shows a code instead.
let waTimer = null;
let waRetry = null;
let restarting = false;

async function startLink() {
  if (restarting || document.hidden) return;
  restarting = true;
  try { await send('POST', 'api/whatsapp', {}); } catch (x) { setErr('wa', x); } finally { restarting = false; }
}

const done = () => S.status === 'declined' || S.status === 'active';

async function paintWhatsApp() {
  clearTimeout(waRetry);
  if (done()) return;
  try {
    const r = await fetch('api/whatsapp', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    const was = !!(S.wa && S.wa.linked);
    if (done()) return;
    S.wa = d;
    setErr('wa', d.error && !d.linked ? new Error(d.error) : null);
    if (!d.linked && !d.pairing) startLink();
    if (!d.linked && !waTimer) waTimer = setInterval(paintWhatsApp, 3000);
    if (d.linked && waTimer) { clearInterval(waTimer); waTimer = null; }
    if (was !== !!d.linked) await loadSell();
  } catch (x) {
    setErr('wa', x);
    // Not polling yet: try again soon.
    if (!waTimer) waRetry = setTimeout(paintWhatsApp, 3000);
  }
  render();
}

// A hidden tab doesn't start sessions; catch up when it comes back.
document.addEventListener('visibilitychange', () => { if (!document.hidden && !(S.wa && S.wa.linked)) paintWhatsApp(); });

// ---------- groups and how you sell ----------

let sellTimer = null;
let writes = Promise.resolve(); // settings saves, one at a time
let sellSeq = 0; // bumped by each save, so older answers don't win
let sellSaved = null; // the selling answers as the server last had them

async function loadSell() {
  clearTimeout(sellTimer);
  if (done()) return;
  const seq = sellSeq;
  try {
    const r = await fetch('api/setup-sell', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    // A save went out meanwhile: its answer is newer than this one.
    if (seq !== sellSeq || done()) return;
    S.sell = d;
    sellSaved = d;
    // A fresh account gets every group it's in, so it can sell at once.
    const joined = d.groups.filter((g) => g.joined).map((g) => g.jid);
    if (d.checked && !d.saved && joined.length) { saveSell(joined); return; }
    setErr('groups', d.error ? new Error(d.error) : null);
    // Not in any group yet: keep looking until they join one.
    if (d.checked && !joined.length) sellTimer = setTimeout(loadSell, 10000);
  } catch (x) { setErr('groups', x); }
  // Still connecting, or not linked yet: look again soon.
  if (S.wa && S.wa.linked && (!S.sell || !S.sell.checked)) sellTimer = setTimeout(loadSell, 3000);
  render();
}

// write queues a settings save behind the others, since each one sends
// the whole set and the last to land wins.
function write(fn) {
  const p = writes.then(fn);
  writes = p.catch(() => {});
  return p;
}

// saveSell sends the groups and selling answers as the chat has them now.
// A save that fails while a newer one waits does nothing: the newer one
// carries the same answers. The newest one failing puts back what the
// server has, and asks again whatever didn't save.
function saveSell(groups, step = 'groups') {
  const seq = ++sellSeq;
  return write(async () => {
    const g = S.sell;
    const body = {
      groups: groups || postingOrder(g.groups).map((x) => x.jid),
      handoff: g.handoff,
      buyers: g.buyers,
    };
    try {
      const d = await send('PUT', 'api/setup-sell', body);
      sellSaved = d;
      if (seq === sellSeq) {
        S.sell = d;
        for (const k of ['groups', 'handoff', 'buyers']) setErr(k);
        // WhatsApp was reconnecting: look at the groups again soon.
        if (!d.checked) { clearTimeout(sellTimer); sellTimer = setTimeout(loadSell, 3000); }
      }
    } catch (x) {
      if (seq !== sellSeq) return;
      const was = sellSaved || g;
      const lost = ['handoff', 'buyers'].filter((k) => S.sell[k] !== was[k]);
      const groupsLost = JSON.stringify(postingOrder(S.sell.groups).map((y) => y.jid)) !== JSON.stringify(postingOrder(was.groups).map((y) => y.jid));
      S.sell = { ...S.sell, groups: was.groups, handoff: was.handoff, buyers: was.buyers, pending: false };
      for (const k of lost) { ack(k, false); setErr(k, x); }
      if (groupsLost) setErr('groups', x);
      // Nothing changed on screen: say so where it was asked.
      if (!groupsLost && !lost.length) { if (step !== 'groups') ack(step, false); setErr(step, x); }
    }
    render();
  });
}

// toggleGroup adds a group to the end of the posting order, or takes it out.
function toggleGroup(jid) {
  const order = postingOrder(S.sell.groups).map((x) => x.jid);
  const next = order.includes(jid) ? order.filter((x) => x !== jid) : [...order, jid];
  if (!next.length) {
    setErr('groups', new Error('Keep at least one group, or your offers go nowhere.'));
    return render();
  }
  // Draw the new order now; the save confirms it. Until then the admins
  // can't be asked.
  S.sell = { ...S.sell, pending: true, groups: S.sell.groups.map((x) => ({ ...x, picked: next.includes(x.jid), order: next.indexOf(x.jid) + 1 })) };
  setErr('groups');
  render();
  saveSell(next);
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
    const d = await write(() => send('PUT', 'api/setup-upi', { upi }));
    S.upi = d.upi;
    if (S.edit === 'upi') { S.edit = null; replay = true; }
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
  if (sending) return;
  setErr('upi');
  sending = true;
  paintAsk();
  try {
    const m = await me();
    const body = new FormData();
    body.append('qr', file);
    const r = await fetch('api/upi-qr', { method: 'POST', cache: 'no-store', headers: { 'X-CSRF-Token': m.csrf }, body });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    await saveUPI(d.upi);
  } catch (x) { setErr('upi', x); render(); } finally { sending = false; paintAsk(); }
});

// ---------- the account ----------

let polling = null;

// stop ends all checking once there's nothing left to set up.
function stop() {
  clearInterval(waTimer); waTimer = null;
  clearTimeout(waRetry);
  clearTimeout(sellTimer);
  clearInterval(polling); polling = null;
}

// paint loads the account. It resolves to false when the page is leaving.
async function paint() {
  mePromise = null;
  const m = await me();
  if (m.status === 'active') {
    // Approved while watching: say so, then go.
    const watching = S.status === 'requested' && drawn;
    S.status = 'active';
    stop();
    if (watching) render();
    setTimeout(() => { location.href = './'; }, watching ? 1400 : 0);
    return false;
  }
  if (S.name !== m.name) { S.name = m.name; loadAcks(); }
  S.status = m.status;
  S.canStoreKeys = m.can_store_keys;
  S.keySet = m.mess_key_set;
  if (m.status === 'declined') stop();
  render();

  // The server pushes decisions (see listen below). A decision made with
  // the CLI, in another process, only shows up through this slower check.
  const asked = m.status === 'requested';
  if (asked && !polling) polling = setInterval(() => paint().catch(() => {}), 60000);
  if (!asked && polling) { clearInterval(polling); polling = null; }
  return true;
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
  .then(async ([go]) => {
    if (!go || !S.name) return;
    if (S.status !== 'declined') await paintWhatsApp();
    ready = true;
    render();
    listen();
  })
  .catch(() => {
    $('log').append(row({ key: 'boot', from: 'bot', text: 'I couldn\'t load your account. Reload the page to try again.', tone: 'err' }));
    $('composer').classList.add('empty');
  });
