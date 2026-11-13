'use strict';

// The sign-up page. A new account starts pending and goes to the welcome
// page to finish setting up.

$('logo').append(svgEl(icon('coin')));
$('headIcon').append(svgEl(icon('paper')));

// check returns what's wrong with the form, or ''. The server checks the
// same things; this just answers before a round trip.
function check() {
  const name = $('name').value.trim();
  if (!name) return ['name', 'Pick a name.'];
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(name)) return ['name', 'Names use letters, digits, dots, dashes or underscores, and start with a letter or digit.'];
  if (!$('password').value) return ['password', 'Pick a password.'];
  if ($('password').value !== $('again').value) return ['again', "The passwords don't match."];
  return ['', ''];
}

function fail(field, msg) {
  $('err').textContent = msg;
  $('err').hidden = false;
  if (field) $(field).focus();
}

$('form').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('err').hidden = true;
  const [field, msg] = check();
  if (msg) return fail(field, msg);
  const go = $('go');
  go.disabled = true;
  go.textContent = 'Creating…';
  try {
    const r = await fetch('api/signup', {
      method: 'POST',
      cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: $('name').value.trim(), password: $('password').value }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || `Sign-up failed (HTTP ${r.status}).`);
    go.textContent = 'Account created';
    location.href = 'welcome.html';
  } catch (x) {
    fail(/name/i.test(x.message) ? 'name' : '', x.message === 'Failed to fetch' ? "Couldn't reach the server." : x.message);
    go.disabled = false;
    go.textContent = 'Create account';
  }
});
$('name').focus();
