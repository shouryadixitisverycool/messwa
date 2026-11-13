'use strict';

// The account page: your mess API key, MCP tokens and password.

$('logo').append(svgEl(icon('paper')));
$('navIcon').append(svgEl(icon('prev')));
$('keyIcon').append(svgEl(icon('ingot')));
$('tokIcon').append(svgEl(icon('coin')));
$('pwIcon').append(svgEl(icon('check')));
$('suIcon').append(svgEl(icon('paper')));

const when = (iso) => {
  const t = new Date(iso);
  return !iso || t.getFullYear() < 2000 ? 'never' : t.toLocaleString('en-IN', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
};

function showErr(id, e) {
  const p = $(id);
  p.textContent = e ? e.message || String(e) : '';
  p.hidden = !e;
}

async function paintMe() {
  const m = await me();
  $('who').textContent = m.name;
  $('pwUser').value = m.name;
  $('role').textContent = 'Your WhatsApp account, meals and sales are private to you.';
  $('noSecret').hidden = m.can_store_keys;
  $('keyMeta').replaceChildren(m.mess_key_set ? el('b', '', 'saved') : el('span', 'red', 'not set'));
  $('key').placeholder = m.mess_key_set ? 'Paste a new key to replace it' : 'Paste your mess API key';
  $('keyClear').hidden = !m.mess_key_set;
}

async function refreshMe() {
  mePromise = null;
  await paintMe();
}

$('keyForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  showErr('keyErr');
  try {
    await send('PUT', 'api/mess-key', { key: $('key').value });
    $('key').value = '';
    await refreshMe();
  } catch (x) { showErr('keyErr', x); }
});

$('keyClear').addEventListener('click', async () => {
  if (!confirm('Remove your saved mess API key? Your meals page and the portal MCP tools stop working until you add one again.')) return;
  showErr('keyErr');
  try {
    await send('DELETE', 'api/mess-key');
    await refreshMe();
  } catch (x) { showErr('keyErr', x); }
});

async function loadTokens() {
  const r = await fetch('api/mcp-tokens', { cache: 'no-store' });
  if (r.status === 401) return toLogin();
  const ts = await r.json();
  const active = ts.filter((t) => !t.revoked);
  $('tokMeta').replaceChildren(el('b', '', String(active.length)), ` active`);
  $('tokens').hidden = !ts.length;
  const body = $('tokBody');
  body.replaceChildren();
  for (const t of ts) {
    const tr = el('tr', t.revoked ? 'revoked' : '');
    tr.append(el('td', '', t.label || '(no label)'), el('td', 'mono', `${t.prefix}…`), el('td', '', when(t.created_at)), el('td', '', when(t.last_used)));
    const td = el('td');
    if (t.revoked) td.append(el('span', 'dim', 'revoked'));
    else {
      const b = el('button', 'btn small', 'Revoke');
      b.type = 'button';
      b.addEventListener('click', async () => {
        if (!confirm(`Revoke "${t.label || t.prefix}"? Agents using it lose access right away.`)) return;
        try { await send('DELETE', `api/mcp-tokens/${t.id}`); loadTokens(); } catch (x) { showErr('tokErr', x); }
      });
      td.append(b);
    }
    tr.append(td);
    body.append(tr);
  }
}

$('tokForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  showErr('tokErr');
  try {
    const t = await send('POST', 'api/mcp-tokens', { label: $('label').value });
    const m = await me();
    $('label').value = '';
    $('freshTok').textContent = t.token;
    $('freshCmd').textContent = `claude mcp add --transport http messwa ${m.mcp_url} --header "Authorization: Bearer ${t.token}"`;
    $('fresh').hidden = false;
    loadTokens();
  } catch (x) { showErr('tokErr', x); }
});

// Admins approve or decline sign-ups here. Requests come first, then
// accounts still setting up.
let signupEvents = null;

async function loadSignups() {
  const m = await me();
  if (m.role !== 'admin') return;
  // The server pushes an event whenever someone signs up, asks, or is
  // decided on, and the list reloads right away.
  if (!signupEvents && typeof EventSource !== 'undefined') {
    signupEvents = new EventSource('api/account-events');
    signupEvents.addEventListener('account', () => loadSignups().catch(() => {}));
  }
  const r = await fetch('api/signups', { cache: 'no-store' });
  if (!r.ok) return;
  const us = await r.json();
  $('signupsBox').hidden = false;
  const asked = us.filter((u) => u.status === 'requested').length;
  $('suMeta').replaceChildren(el('b', '', String(asked)), ' waiting');
  const body = $('suBody');
  body.replaceChildren();
  if (!us.length) {
    const tr = el('tr');
    const td = el('td', 'dim empty', 'Nobody is waiting.');
    td.colSpan = 3;
    tr.append(td);
    body.append(tr);
    return;
  }
  for (const u of us) {
    const tr = el('tr');
    const state = u.status === 'requested'
      ? `asked ${when(u.since)}`
      : `still setting up${u.mess_key_set ? '' : ', no mess key yet'}`;
    tr.append(el('td', '', u.name), el('td', u.status === 'requested' ? '' : 'dim', state));
    const td = el('td');
    for (const [label, decision, cls] of [['Approve', 'approve', 'btn small primary'], ['Decline', 'decline', 'btn small']]) {
      const b = el('button', cls, label);
      b.type = 'button';
      b.addEventListener('click', async () => {
        if (decision === 'decline' && !confirm(`Decline ${u.name}? They won't be able to ask again.`)) return;
        showErr('suErr');
        try { await send('POST', `api/signups/${encodeURIComponent(u.name)}/${decision}`); loadSignups(); } catch (x) { showErr('suErr', x); }
      });
      td.append(b);
    }
    tr.append(td);
    body.append(tr);
  }
}

$('pwForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  showErr('pwErr');
  $('pwOk').hidden = true;
  if ($('pwNew').value !== $('pwAgain').value) return showErr('pwErr', new Error("The new passwords don't match."));
  try {
    await send('POST', 'api/password', { current: $('pwOld').value, new: $('pwNew').value });
    for (const id of ['pwOld', 'pwNew', 'pwAgain']) $(id).value = '';
    $('pwOk').hidden = false;
  } catch (x) { showErr('pwErr', x); }
});

$('logout').addEventListener('click', async () => {
  try { await send('POST', 'api/logout'); } finally { location.href = 'login.html'; }
});

paintMe().then(() => Promise.all([loadTokens(), loadSignups()])).catch(() => {});

$('waIcon').append(svgEl(icon('coin')));

let waTimer = null;

async function whatsappStatus() {
  try {
    const response = await fetch('api/whatsapp', { cache: 'no-store' });
    const device = await response.json();
    if (!response.ok) throw new Error(device.error);
    $('waStatus').textContent = device.linked
      ? (device.connected ? 'Linked and connected.' : 'Linked, reconnecting…')
      : device.pairing
        ? (device.qr ? 'On your phone open WhatsApp, then Settings, Linked devices, Link a device, and scan this.' : 'Getting a QR code…')
        : 'Not linked.';
    $('waMeta').replaceChildren(device.linked ? el('b', '', 'linked') : el('span', 'red', 'not linked'));
    $('waQR').hidden = !device.qr;
    if (device.qr) $('waQR').src = device.qr;
    $('waLink').hidden = device.linked;
    $('waLink').disabled = device.pairing;
    $('waUnlink').hidden = !device.linked;
    showErr('waErr', device.error ? new Error(device.error) : null);
    // Poll while the QR is up (it rotates) or the link is settling.
    const busy = device.pairing || (device.linked && !device.connected);
    if (busy && !waTimer) waTimer = setInterval(whatsappStatus, 3000);
    if (!busy && waTimer) { clearInterval(waTimer); waTimer = null; }
  } catch (error) { showErr('waErr', error); }
}

$('waLink').onclick = async () => {
  showErr('waErr');
  $('waLink').disabled = true;
  try { await send('POST', 'api/whatsapp', {}); } catch (error) { showErr('waErr', error); }
  await whatsappStatus();
};

$('waUnlink').onclick = async () => {
  if (!confirm('Unlink your WhatsApp? Your active sell tasks stop.')) return;
  try {
    await send('DELETE', 'api/whatsapp');
    await whatsappStatus();
  } catch (error) { showErr('waErr', error); }
};

whatsappStatus();
