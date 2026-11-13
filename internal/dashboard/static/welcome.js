'use strict';

// The welcome page: a new account saves its mess API key, links its
// WhatsApp and asks the admins for approval. The server sends approved
// accounts on to the board.

$('logo').append(svgEl(icon('coin')));
$('keyIcon').append(svgEl(icon('ingot')));
$('waIcon').append(svgEl(icon('paper')));
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
    if (was !== waLinked) paintAsk(await me());
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

function paintAsk(m) {
  const asked = m.status === 'requested';
  $('askMeta').replaceChildren(asked ? el('b', '', 'waiting') : el('span', 'dim', 'not asked'));
  $('ask').hidden = asked;
  $('ask').disabled = !m.mess_key_set || !waLinked;
  $('askNote').textContent = asked
    ? 'Your request is in. This page moves you to the auction house as soon as an admin approves it.'
    : 'Once your key is saved and WhatsApp is linked, ask the admins to approve you.';
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
  for (const id of ['keyStep', 'waStep', 'askStep']) $(id).hidden = declined;
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

paint().then(paintWhatsApp).then(listen).catch(() => {});
