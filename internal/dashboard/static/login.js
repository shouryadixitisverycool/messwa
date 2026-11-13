'use strict';

// The sign-in page. It's the only page that works without a session.

$('logo').append(svgEl(icon('coin')));
$('headIcon').append(svgEl(icon('paper')));

fetch('api/setup', { cache: 'no-store' }).then((r) => r.json()).then((d) => { $('setup').hidden = !d.needs_admin; }).catch(() => {});

// Only go back to one of our own pages, never to another site.
function nextPage() {
  const n = new URLSearchParams(location.search).get('next') || '';
  return /^[a-z]+\.html$/.test(n) ? n : './';
}

$('form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = $('err');
  err.hidden = true;
  $('go').disabled = true;
  try {
    const r = await fetch('api/login', {
      method: 'POST',
      cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: $('name').value.trim(), password: $('password').value }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    location.href = d.status === 'active' ? nextPage() : 'welcome.html';
  } catch (x) {
    err.textContent = x.message;
    err.hidden = false;
    $('password').select();
  } finally {
    $('go').disabled = false;
  }
});
$('name').focus();
