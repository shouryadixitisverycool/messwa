'use strict';

// The welcome page: a new account saves its mess API key, links its
// WhatsApp, picks the groups to post in and how it sells, confirms
// WhatsApp Pay, and asks the admins for approval. So an approved account
// can sell straight away. The server sends approved accounts on to the
// board.

$('logo').append(svgEl(icon('coin')));
$('keyIcon').append(svgEl(icon('ingot')));
$('waIcon').append(svgEl(icon('paper')));
$('groupIcon').append(svgEl(icon('all')));
$('howIcon').append(svgEl(icon('next')));
$('payIcon').append(svgEl(icon('coin')));
$('askIcon').append(svgEl(icon('check')));

function showErr(id, e) {
  const p = $(id);
  p.textContent = e ? e.message || String(e) : '';
  p.hidden = !e;
}

// A "?" in a step's header shows or hides that step's explanation.
for (const b of document.querySelectorAll('.help')) {
  b.addEventListener('click', () => {
    const open = b.getAttribute('aria-expanded') !== 'true';
    b.setAttribute('aria-expanded', String(open));
    $(b.getAttribute('aria-controls')).hidden = !open;
  });
}

let polling = null;
let waLinked = false;
let waTimer = null;

// The WhatsApp step starts linking as soon as the page opens and starts a
// new session whenever the last one runs out (they last about 160
// seconds), so a fresh QR is always up. It checks every 3 seconds, since
// the QR rotates. The phone-number mode shows a code instead.
let codeMode = false;
let declined = false;
let restarting = false;

async function startLink() {
  if (restarting || document.hidden) return;
  restarting = true;
  try { await send('POST', 'api/whatsapp', {}); } catch (x) { showErr('waErr', x); } finally { restarting = false; }
}

// paintWhatsApp loads the selling steps once WhatsApp shows as linked.
async function paintWhatsApp() {
  if (declined) return;
  try {
    const r = await fetch('api/whatsapp', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    const was = waLinked;
    waLinked = !!d.linked;
    $('waMeta').replaceChildren(d.linked ? el('b', '', 'linked') : el('span', 'red', 'not linked'));
    $('waStep').classList.toggle('done', waLinked);
    $('modeToggle').hidden = d.linked;
    $('modeToggle').textContent = codeMode ? 'Scan a QR code instead' : 'Link with phone number instead';
    $('qrBox').hidden = d.linked || codeMode || !d.qr;
    $('codeBox').hidden = d.linked || !codeMode;
    if (d.qr) $('waQR').src = d.qr;
    $('pairCode').hidden = !d.code;
    if (d.code) $('pairCode').textContent = d.code.length === 8 ? `${d.code.slice(0, 4)}-${d.code.slice(4)}` : d.code;
    $('waStatus').textContent = d.linked ? 'Linked.'
      : codeMode
        ? (d.code ? 'On your phone open WhatsApp, then Settings, Linked devices, Link a device, Link with phone number instead, and type this code.' : 'Enter your number to get a code.')
        : (d.qr ? 'On your phone open WhatsApp, then Settings, Linked devices, Link a device, and scan this.' : 'Getting a QR code…');
    showErr('waErr', d.error && !d.linked ? new Error(d.error) : null);
    if (!d.linked && !d.pairing) startLink();
    if (!d.linked && !waTimer) waTimer = setInterval(paintWhatsApp, 3000);
    if (d.linked && waTimer) { clearInterval(waTimer); waTimer = null; }
    if (was !== waLinked) { paintAsk(await me()); loadSell(); }
  } catch (x) { showErr('waErr', x); }
}

$('modeToggle').addEventListener('click', () => {
  codeMode = !codeMode;
  showErr('waErr');
  paintWhatsApp();
  if (codeMode) $('phone').focus();
});

$('codeForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  showErr('waErr');
  $('codeGo').disabled = true;
  $('codeGo').textContent = 'Getting code…';
  try {
    await send('POST', 'api/whatsapp/code', { phone: $('phone').value });
  } catch (x) { showErr('waErr', x); }
  $('codeGo').disabled = false;
  $('codeGo').textContent = 'Get a new code';
  paintWhatsApp();
});

// A hidden tab doesn't start sessions; catch up when it comes back.
document.addEventListener('visibilitychange', () => { if (!document.hidden && !waLinked) paintWhatsApp(); });

// The selling steps. G is the last /api/setup-sell answer.
let G = null;
let sellTimer = null;
let lastMe = null;

async function loadSell() {
  clearTimeout(sellTimer);
  if (declined) return;
  try {
    const r = await fetch('api/setup-sell', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    G = d;
    // A fresh account gets every group it's in, so it can sell at once.
    const joined = d.groups.filter((g) => g.joined).map((g) => g.jid);
    if (d.checked && !d.saved && joined.length) return saveSell({ groups: joined });
    showErr('groupErr', d.error ? new Error(d.error) : null);
    paintSell();
  } catch (x) { showErr('groupErr', x); }
  // Still connecting, or not linked yet: look again soon.
  if (waLinked && (!G || !G.checked)) sellTimer = setTimeout(loadSell, 3000);
}

async function saveSell(change) {
  const body = {
    groups: G.groups.filter((g) => g.picked).map((g) => g.jid),
    handoff: G.handoff,
    buyers: G.buyers,
    ...change,
  };
  try {
    G = await send('PUT', 'api/setup-sell', body);
    showErr('groupErr');
    showErr('howErr');
  } catch (x) {
    showErr('groups' in change ? 'groupErr' : 'howErr', x);
  }
  paintSell();
}

function paintSell() {
  const list = $('groupList');
  if (!waLinked || !G) {
    $('groupNote').textContent = 'Link WhatsApp first.';
    $('groupNote').hidden = false;
    list.replaceChildren();
    $('groupMeta').replaceChildren(el('span', 'dim', 'waiting'));
    $('groupStep').classList.remove('done');
  } else {
    const picked = G.groups.filter((g) => g.picked);
    const inAny = G.groups.some((g) => g.joined);
    $('groupNote').hidden = G.checked && inAny;
    $('groupNote').textContent = !G.checked
      ? (G.connecting ? 'Checking which groups your WhatsApp is in…' : 'Couldn\'t check your groups yet.')
      : 'Your WhatsApp isn\'t in any of the mess groups below. Join at least one on your phone; this list updates by itself.';
    list.replaceChildren(...G.groups.map((g) => {
      const b = el('button', 'g-row');
      b.type = 'button';
      b.setAttribute('role', 'checkbox');
      b.setAttribute('aria-checked', String(g.picked));
      b.disabled = G.checked && !g.joined && !g.picked;
      b.append(el('span', 'g-box', g.picked ? '✓' : ''), el('span', 'g-name', g.name || g.jid),
        el('span', 'g-tag', !G.checked ? '' : g.joined ? (g.picked ? 'posting' : 'joined') : 'not in it'));
      b.addEventListener('click', () => {
        const next = G.groups.filter((x) => (x.jid === g.jid ? !x.picked : x.picked)).map((x) => x.jid);
        if (!next.length) return showErr('groupErr', new Error('Keep at least one group, or your offers go nowhere.'));
        saveSell({ groups: next });
      });
      return b;
    }));
    $('groupMeta').replaceChildren(picked.length
      ? el('b', '', `${picked.length} picked`)
      : el('span', 'red', 'none picked'));
    $('groupStep').classList.toggle('done', G.saved && picked.length > 0);
    // Not in any group yet: keep looking until they join one.
    if (G.checked && !inAny) { clearTimeout(sellTimer); sellTimer = setTimeout(loadSell, 10000); }
  }
  paintChoices();
  if (lastMe) paintAsk(lastMe);
}

const CHOICES = {
  handoff: [
    ['never', 'Bot sells', 'It asks the first buyer to pay and sends the QR when the money arrives.'],
    ['on_reply', 'I take over', 'The bot stops when a buyer DMs you, and you chat with them yourself.'],
  ],
  buyers: [
    ['anyone', 'Anyone', 'Whoever replies to your offer.'],
    ['unsaved_only', 'Skip my contacts', 'The bot ignores people saved in your phone, so you deal with friends yourself.'],
  ],
};

function paintChoices() {
  for (const [key, opts] of Object.entries(CHOICES)) {
    const box = $(key);
    box.replaceChildren(...opts.map(([v, name, why]) => {
      const b = el('button', 'choice');
      b.type = 'button';
      b.setAttribute('role', 'radio');
      const on = G ? G[key] === v : v === opts[0][0];
      b.setAttribute('aria-checked', String(on));
      b.disabled = !G || !G.saved;
      b.append(el('b', '', name), el('small', '', why));
      b.addEventListener('click', () => { if (!on) saveSell({ [key]: v }); });
      return b;
    }));
  }
  const ready = !!(G && G.saved);
  $('howMeta').replaceChildren(ready ? el('b', '', 'set') : el('span', 'dim', 'after groups'));
  $('howStep').classList.toggle('done', ready);
}

// WhatsApp Pay can't be checked from here, so the account says it's on.
// Only this browser remembers the tick.
const PAY_KEY = 'messwa.waPayReady';
try { $('payReady').checked = localStorage.getItem(PAY_KEY) === '1'; } catch { /* storage off */ }
function paintPay() {
  const on = $('payReady').checked;
  $('payMeta').replaceChildren(on ? el('b', '', 'ready') : el('span', 'red', 'not confirmed'));
  $('payStep').classList.toggle('done', on);
}
$('payReady').addEventListener('change', () => {
  try { localStorage.setItem(PAY_KEY, $('payReady').checked ? '1' : '0'); } catch { /* storage off */ }
  paintPay();
  if (lastMe) paintAsk(lastMe);
});
paintPay();

function paintAsk(m) {
  lastMe = m;
  const asked = m.status === 'requested';
  const groups = !!(G && G.saved && G.groups.some((g) => g.picked));
  const pay = $('payReady').checked;
  const missing = [
    !m.mess_key_set && 'save your mess key',
    !waLinked && 'link WhatsApp',
    waLinked && !groups && 'pick a group to post in',
    !pay && 'confirm WhatsApp Pay',
  ].filter(Boolean);
  $('askMeta').replaceChildren(asked ? el('b', '', 'waiting') : el('span', 'dim', 'not asked'));
  $('ask').hidden = asked;
  $('ask').disabled = missing.length > 0;
  $('askNote').textContent = asked
    ? 'Your request is in. This page moves you to the auction house as soon as an admin approves it.'
    : missing.length
      ? `Still to do: ${missing.join(', ')}.`
      : 'All set. Ask the admins to approve you, and you can sell as soon as they do.';
}

async function paint() {
  mePromise = null;
  const m = await me();
  if (m.status === 'active') {
    location.href = './';
    return;
  }
  $('who').textContent = `Welcome, ${m.name}`;
  declined = m.status === 'declined';
  $('declined').hidden = !declined;
  for (const id of ['keyStep', 'waStep', 'groupStep', 'howStep', 'payStep', 'askStep']) $(id).hidden = declined;
  if (declined) return;

  $('noSecret').hidden = m.can_store_keys;
  $('keySave').disabled = !m.can_store_keys;
  $('keyMeta').replaceChildren(m.mess_key_set ? el('b', '', 'saved') : el('span', 'red', 'not set'));
  $('key').placeholder = m.mess_key_set ? 'Paste a new key to replace it' : 'Paste your mess API key';
  $('keyStep').classList.toggle('done', m.mess_key_set);

  const asked = m.status === 'requested';
  paintAsk(m);

  // The server pushes decisions (see listen below). A decision made with
  // the CLI, in another process, only shows up through this slower check.
  if (asked && !polling) polling = setInterval(() => paint().catch(() => {}), 60000);
  if (!asked && polling) { clearInterval(polling); polling = null; }
}

$('keyForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  showErr('keyErr');
  $('keySave').disabled = true;
  try {
    await send('PUT', 'api/mess-key', { key: $('key').value });
    $('key').value = '';
    await paint();
  } catch (x) { showErr('keyErr', x); } finally { $('keySave').disabled = false; }
});

$('ask').addEventListener('click', async () => {
  showErr('askErr');
  try {
    await send('POST', 'api/approval');
    await paint();
  } catch (x) { showErr('askErr', x); }
});

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

paintSell();
paint().then(paintWhatsApp).then(listen).catch(() => {});
