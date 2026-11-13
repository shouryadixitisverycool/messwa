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
async function loadSignups() {
  const m = await me();
  if (m.role !== 'admin') return;
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
$('sellIcon').append(svgEl(icon('ingot')));

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

// The settings, grouped and in the order a seller thinks about them. Keys
// the server sends that aren't listed here go in a last group.
const deviceGroups = [
  ['General', [
    ['mcp.sell_tasks', 'Let your agent start sell tasks over MCP'],
    ['dms.retention', 'Keep chat text from unsaved numbers for', 's'],
    ['sales.monthly_fee', 'Monthly bill when the portal is unreachable', '₹'],
  ]],
  ['Offers', [
    ['sell.groups', 'Groups to post in (names or JIDs, comma separated)'],
    ['sell.group_count', 'Or the first N allowed groups (0 uses the list above)'],
    ['sell.post_gap', 'Pause between group posts', 's'],
    ['sell.show_price', 'Show the price in offers'],
    ['sell.mention_extras', 'Mention registered extras'],
  ]],
  ['Reposting', [
    ['sell.repost', 'Repost offers that are still open'],
    ['sell.repost_after', 'Repost after (0 turns the timer off)', 's'],
    ['sell.repost_messages', 'Or after this many new messages (0 for off)'],
    ['sell.repost_listings', 'Or after this many new listings (0 for off)'],
    ['sell.max_reposts', 'At most this many reposts per group'],
  ]],
  ['Buyers', [
    ['sell.buyers', 'Who can buy'],
    ['sell.accept_plain_dms', "Accept buyers who don't quote the offer"],
    ['sell.handoff', 'Hand the sale over to you'],
    ['sell.negotiation_silence', 'Wait for a buyer to reply', 's'],
    ['sell.accept_payment_images', 'Count a buyer\'s image as payment (unverified)'],
  ]],
];
const deviceChoices = {
  'sell.handoff': [['never', 'Never, sell automatically'], ['on_reply', 'When the buyer first replies'], ['on_agreement', 'Once a price is agreed']],
  'sell.buyers': [['anyone', 'Anyone'], ['unsaved_only', 'Only numbers not in your contacts']],
};
let deviceValues = {};

function settingField(key, title, unit) {
  const value = deviceValues[key];
  if (typeof value === 'boolean') {
    const label = el('label', 'check');
    const input = el('input');
    input.type = 'checkbox';
    input.checked = value;
    input.dataset.setting = key;
    label.append(input, el('span', '', title));
    return label;
  }
  const label = el('label', 'field');
  label.append(el('span', '', title));
  const choices = deviceChoices[key];
  let input;
  if (choices) {
    input = el('select');
    for (const [v, t] of choices) input.add(new Option(t, v));
    input.value = choices.some(([v]) => v === value) ? value : choices[0][0];
  } else {
    input = el('input');
    input.type = typeof value === 'number' ? 'number' : 'text';
    input.value = Array.isArray(value) ? value.join(', ') : value;
    if (input.type === 'number') input.min = '0';
  }
  input.dataset.setting = key;
  if (unit) {
    const wrap = el('div', 'unit');
    wrap.append(input, el('span', '', unit));
    label.append(wrap);
  } else label.append(input);
  return label;
}

async function loadDeviceSettings() {
  try {
    const response = await fetch('api/account-settings', { cache: 'no-store' });
    if (!response.ok) throw new Error('Could not load your selling settings.');
    deviceValues = await response.json();
    const seen = new Set();
    const groups = deviceGroups.map(([name, items]) => [name, items.filter(([k]) => k in deviceValues)]);
    for (const [, items] of groups) for (const [k] of items) seen.add(k);
    const rest = Object.keys(deviceValues).filter((k) => !seen.has(k)).map((k) => [k, k]);
    if (rest.length) groups.push(['Other', rest]);
    for (const [name, items] of groups) {
      if (!items.length) continue;
      const set = el('fieldset', 'settings-group');
      set.append(el('legend', '', name));
      const checks = el('div', 'checks');
      const fields = el('div', 'fields');
      for (const [k, t, u] of items) (typeof deviceValues[k] === 'boolean' ? checks : fields).append(settingField(k, t, u));
      if (checks.childElementCount) set.append(checks);
      if (fields.childElementCount) set.append(fields);
      $('deviceFields').append(set);
    }
  } catch (error) { showErr('deviceErr', error); }
}

$('deviceSettings').onsubmit = async (event) => {
  event.preventDefault();
  showErr('deviceErr');
  $('deviceOk').hidden = true;
  try {
    const values = {};
    for (const input of $('deviceFields').querySelectorAll('[data-setting]')) {
      const key = input.dataset.setting;
      const old = deviceValues[key];
      values[key] = typeof old === 'boolean' ? input.checked
        : typeof old === 'number' ? Number(input.value)
          : Array.isArray(old) ? input.value.split(',').map((s) => s.trim()).filter(Boolean)
            : input.value;
    }
    await send('PUT', 'api/account-settings', values);
    deviceValues = { ...deviceValues, ...values };
    $('deviceOk').hidden = false;
    setTimeout(() => { $('deviceOk').hidden = true; }, 3000);
  } catch (error) { showErr('deviceErr', error); }
};

loadDeviceSettings();
whatsappStatus();
