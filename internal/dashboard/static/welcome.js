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

// paintWhatsApp shows the link state, and the QR while pairing. It checks
// every 3 seconds while a QR is up, since the code rotates.
async function paintWhatsApp() {
  try {
    const r = await fetch('api/whatsapp', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    const was = waLinked;
    waLinked = !!d.linked;
    $('waStatus').textContent = d.linked ? 'Linked.'
      : d.pairing ? (d.qr ? 'On your phone open WhatsApp, then Settings, Linked devices, Link a device, and scan this.' : 'Getting a QR code…')
        : 'Not linked yet.';
    $('waQR').hidden = !d.qr;
    if (d.qr) $('waQR').src = d.qr;
    $('waLink').hidden = d.linked;
    $('waLink').disabled = d.pairing;
    $('waMeta').replaceChildren(d.linked ? el('b', '', 'linked') : el('span', 'red', 'not linked'));
    $('waStep').classList.toggle('done', waLinked);
    showErr('waErr', d.error ? new Error(d.error) : null);
    if (d.pairing && !waTimer) waTimer = setInterval(paintWhatsApp, 3000);
    if (!d.pairing && waTimer) { clearInterval(waTimer); waTimer = null; }
    if (was !== waLinked) paintAsk(await me());
  } catch (x) { showErr('waErr', x); }
}

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
  const declined = m.status === 'declined';
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

  // Check for a decision every 20 seconds while the request is open.
  if (asked && !polling) polling = setInterval(() => paint().catch(() => {}), 20000);
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

$('waLink').addEventListener('click', async () => {
  showErr('waErr');
  $('waLink').disabled = true;
  try {
    await send('POST', 'api/whatsapp', {});
  } catch (x) { showErr('waErr', x); }
  await paintWhatsApp();
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

paint().then(paintWhatsApp).catch(() => {});
