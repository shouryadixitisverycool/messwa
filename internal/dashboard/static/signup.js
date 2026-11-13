'use strict';

// The sign-up page. A new account starts pending and goes to the welcome
// page to finish setting up.

$('logo').append(svgEl(icon('coin')));
$('headIcon').append(svgEl(icon('paper')));

$('form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = $('err');
  err.hidden = true;
  if ($('password').value !== $('again').value) {
    err.textContent = "The passwords don't match.";
    err.hidden = false;
    return;
  }
  $('go').disabled = true;
  try {
    const r = await fetch('api/signup', {
      method: 'POST',
      cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: $('name').value.trim(), password: $('password').value }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    location.href = 'welcome.html';
  } catch (x) {
    err.textContent = x.message;
    err.hidden = false;
  } finally {
    $('go').disabled = false;
  }
});
$('name').focus();
