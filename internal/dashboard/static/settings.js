'use strict';

// The settings page. Everyone gets the YOU tab, which is saved in this
// browser. Admins also get the tabs that change config.toml's settings for
// everyone: edits collect in a draft until Save sends them to
// /api/settings. A value that matches config.toml isn't saved, so it keeps
// following the file.

$('logo').append(svgEl(icon('paper')));
$('navIcon').append(svgEl(icon('prev')));

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const clone = (v) => JSON.parse(JSON.stringify(v));
const store = {
  get(k) { try { return JSON.parse(localStorage.getItem(k) || 'null'); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private window */ } },
};

// ---------- tabs ----------

function showPane(id) {
  for (const t of document.querySelectorAll('.set-tab')) t.classList.toggle('on', t.dataset.pane === id);
  for (const p of document.querySelectorAll('.pane')) p.classList.toggle('on', p.id === id);
  store.set('mah-settings-tab', id);
}
for (const t of document.querySelectorAll('.set-tab')) t.addEventListener('click', () => showPane(t.dataset.pane));

// opt builds one choice in a radio group.
function opt(icon, label, sub, onPick) {
  const b = el('button', 'opt');
  b.type = 'button';
  b.setAttribute('role', 'radio');
  if (icon) {
    const ic = el('span', 'ic');
    if (icon.startsWith('<svg')) ic.append(svgEl(icon)); else ic.textContent = icon;
    b.append(ic);
  }
  b.append(el('b', '', label));
  if (sub) b.append(el('span', '', sub));
  b.addEventListener('click', onPick);
  return b;
}

// ---------- you: this browser ----------

// The board keeps its view and the messaging app under "mah".
function messagingApp() { return (store.get('mah') || {}).via === 'beeper' ? 'beeper' : 'whatsapp'; }
function paintVia() {
  const via = messagingApp();
  for (const b of $('via').children) b.setAttribute('aria-checked', String(b.dataset.v === via));
  $('viaNote').hidden = !(via === 'beeper' && matchMedia('(hover: none) and (pointer: coarse)').matches);
}
for (const [v, label, ic] of [['whatsapp', 'WhatsApp', waIcon], ['beeper', 'Beeper', beeperIcon]]) {
  const b = opt(ic, label, '', () => { store.set('mah', Object.assign(store.get('mah') || {}, { via: v })); paintVia(); });
  b.dataset.v = v;
  $('via').append(b);
}
paintVia();

async function paintTiles(m) {
  const key = $('keyState');
  key.textContent = m.mess_key_set ? 'Saved' : 'Not set';
  key.className = m.mess_key_set ? 'okc' : 'red';
  try {
    const r = await fetch('api/mcp-tokens', { cache: 'no-store' });
    if (r.ok) $('tokState').textContent = String((await r.json() || []).filter((t) => !t.revoked).length);
  } catch { /* the tile keeps its placeholder */ }
}

// ---------- admin settings ----------

let D = null;   // last /api/settings
let draft = {}; // D.values with this page's edits
const painters = [];
const paint = (fn) => { painters.push(fn); return fn; };

function set(key, v) {
  draft[key] = v;
  repaint();
}
function repaint() {
  for (const p of painters) p();
  paintSave();
}

const changedKeys = () => (D ? Object.keys(D.values).filter((k) => !same(draft[k], D.values[k])) : []);

function paintSave() {
  const n = changedKeys().length;
  $('save').classList.toggle('clean', !n);
  $('saveTxt').replaceChildren(...(n ? [el('b', '', String(n)), ` unsaved change${n > 1 ? 's' : ''}`] : ['All saved']));
  $('undo').disabled = $('saveBtn').disabled = !n;
}

function showErr(e) {
  $('err').textContent = e ? e.message || String(e) : '';
  $('err').hidden = !e;
}

$('undo').addEventListener('click', () => { draft = clone(D.values); showErr(); repaint(); });
$('saveBtn').addEventListener('click', async () => {
  // Only values that differ from config.toml are saved.
  const values = {};
  for (const k of Object.keys(D.values)) if (!same(draft[k], D.defaults[k])) values[k] = draft[k];
  $('saveBtn').disabled = true;
  try {
    D = await send('PUT', 'api/settings', { values });
    draft = clone(D.values);
    showErr();
  } catch (e) {
    showErr(e);
  }
  repaint();
});
addEventListener('beforeunload', (e) => { if (changedKeys().length) e.preventDefault(); });

// Toggles and pick-one groups bound straight to a key.
for (const b of document.querySelectorAll('.tg[data-key]')) {
  b.setAttribute('role', 'switch');
  b.addEventListener('click', () => set(b.dataset.key, !draft[b.dataset.key]));
  paint(() => b.setAttribute('aria-checked', String(!!draft[b.dataset.key])));
}

const CHOICES = {
  'sell.buyers': [['anyone', '🌐', 'Anyone'], ['unsaved_only', '🙈', 'Skip contacts']],
  'sell.handoff': [['never', '🤖', 'Bot sells', 'Pay → QR'], ['on_reply', '✋', 'On reply', 'First DM is yours']],
};
for (const g of document.querySelectorAll('.pick[data-key]')) {
  const key = g.dataset.key;
  const bs = CHOICES[key].map(([v, ic, label, sub]) => {
    const b = opt(ic, label, sub, () => set(key, v));
    b.dataset.v = v;
    g.append(b);
    return b;
  });
  paint(() => { for (const b of bs) b.setAttribute('aria-checked', String(b.dataset.v === draft[key])); });
}

// ---------- offer ----------

const groupName = (g) => {
  const known = D.groups.find((x) => x.jid === g || x.name === g);
  return known ? known.name || known.jid : g;
};

paint(() => {
  const text = ['sell kadamba veg lunch'];
  if (draft['sell.show_price']) text.push('₹30');
  $('preview').textContent = text.join(' ') + (draft['sell.mention_extras'] ? ' + extras: Omelette' : '');
  const n = draft['sell.group_count'] || draft['sell.groups'].length || 1;
  $('groupCount').textContent = `${n} group${n > 1 ? 's' : ''}`;
});

// Picked groups are named in posting order; "first N" takes the top of
// config.toml's groups.
const byCount = () => draft['sell.group_count'] > 0;
const modes = [
  opt('', 'Pick', '', () => { if (byCount()) { draft['sell.groups'] = D.groups.slice(0, draft['sell.group_count']).map((g) => g.jid); set('sell.group_count', 0); } }),
  opt('', 'First N', '', () => { if (!byCount()) { draft['sell.groups'] = []; set('sell.group_count', 1); } }),
];
$('groupMode').append(...modes);
paint(() => { modes[0].setAttribute('aria-checked', String(!byCount())); modes[1].setAttribute('aria-checked', String(byCount())); });

paint(() => {
  const box = $('groups');
  if (byCount()) {
    const n = draft['sell.group_count'];
    box.replaceChildren(...D.groups.map((g, i) => {
      const c = el('button', 'chip' + (i < n ? '' : ' off'));
      c.type = 'button';
      c.title = 'Post in the first groups up to this one';
      if (i < n) c.append(el('span', 'n', String(i + 1)));
      c.append(g.name || g.jid);
      c.addEventListener('click', () => set('sell.group_count', i + 1));
      return c;
    }));
    return;
  }
  const picked = draft['sell.groups'];
  const chips = picked.map((g, i) => {
    const c = el('span', 'chip drag');
    c.dataset.i = i;
    const x = el('button', 'x', '✕');
    x.type = 'button';
    x.setAttribute('aria-label', `Remove ${groupName(g)}`);
    x.addEventListener('click', () => set('sell.groups', picked.filter((_, j) => j !== i)));
    c.append(el('span', 'grip', '⋮⋮'), el('span', 'n', String(i + 1)), groupName(g), x);
    dragToReorder(c, i);
    return c;
  });
  const left = D.groups.filter((g) => !picked.some((p) => p === g.jid || p === g.name));
  if (left.length) {
    const add = el('label', 'chip add');
    const sel = el('select');
    sel.setAttribute('aria-label', 'Add a group');
    sel.append(new Option('＋ group', ''), ...left.map((g) => new Option(g.name || g.jid, g.jid)));
    sel.addEventListener('change', () => { if (sel.value) set('sell.groups', [...picked, sel.value]); });
    add.append(sel);
    chips.push(add);
  }
  box.replaceChildren(...chips);
});

// dragToReorder lets a picked group's chip be dragged along the row, with a
// mouse or a finger.
function dragToReorder(chip, from) {
  chip.addEventListener('pointerdown', (e) => {
    if (e.target.closest('.x')) return;
    e.preventDefault();
    chip.setPointerCapture(e.pointerId);
    chip.classList.add('dragging');
    let to = from;
    const move = (ev) => {
      const over = document.elementFromPoint(ev.clientX, ev.clientY);
      const target = over && over.closest('#groups .chip.drag');
      if (target) to = Number(target.dataset.i);
    };
    const up = () => {
      chip.removeEventListener('pointermove', move);
      chip.classList.remove('dragging');
      if (to !== from) {
        const g = [...draft['sell.groups']];
        g.splice(to, 0, ...g.splice(from, 1));
        set('sell.groups', g);
      }
    };
    chip.addEventListener('pointermove', move);
    chip.addEventListener('pointerup', up, { once: true });
    chip.addEventListener('pointercancel', up, { once: true });
  });
}

$('postGap').addEventListener('input', (e) => set('sell.post_gap', Number(e.target.value)));
paint(() => {
  const v = draft['sell.post_gap'];
  $('postGap').max = String(Math.max(60, v));
  $('postGap').value = String(v);
  $('postGapV').textContent = `${v}s`;
});

// ---------- repost ----------

paint(() => $('repostBody').classList.toggle('dimmed', !draft['sell.repost']));

// stepper fills box with − value + for key. step and the limits are in the
// key's own unit; fmt shows a value.
function stepper(box, key, { step, min, max, fmt, toUnit = (v) => v, fromUnit = (v) => v, zero = false }) {
  const minus = el('button', '', '−');
  const plus = el('button', '', '+');
  const out = el('span');
  minus.type = plus.type = 'button';
  minus.setAttribute('aria-label', 'Less');
  plus.setAttribute('aria-label', 'More');
  const cur = () => toUnit(draft[key]);
  minus.addEventListener('click', () => {
    let v = cur() - step;
    if (v < min) v = zero ? 0 : min;
    set(key, fromUnit(v));
  });
  plus.addEventListener('click', () => set(key, fromUnit(Math.min(max, cur() <= 0 && zero ? min : cur() + step))));
  box.replaceChildren(minus, out, plus);
  return paint(() => {
    const v = cur();
    out.textContent = fmt(v);
    minus.disabled = zero ? v <= 0 : v <= min;
    plus.disabled = v >= max;
  });
}

const SVG = 'http://www.w3.org/2000/svg';
function ring(color) {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('viewBox', '0 0 36 36');
  const mk = (stroke) => {
    const c = document.createElementNS(SVG, 'circle');
    for (const [k, v] of Object.entries({ cx: 18, cy: 18, r: 15, fill: 'none', stroke, 'stroke-width': 3.5, 'stroke-linecap': 'round', pathLength: 100 })) c.setAttribute(k, v);
    return c;
  };
  const arc = mk(color);
  svg.append(mk('#252c3a'), arc);
  return { svg, arc };
}

const TRIGGERS = [
  { key: 'sell.repost_after', cap: '⏱ Copy age', color: '#ffc53d', step: 5, min: 5, max: 60, zero: true, toUnit: (s) => Math.round(s / 60), fromUnit: (m) => m * 60, fmt: (m) => (m ? `${m}m` : 'Off') },
  { key: 'sell.repost_messages', cap: '💬 New messages', color: '#5aa9ff', step: 1, min: 1, max: 30, zero: true, fmt: (v) => (v ? String(v) : 'Off') },
  { key: 'sell.repost_listings', cap: '🍱 Rival listings', color: '#e2553f', step: 1, min: 1, max: 10, zero: true, fmt: (v) => (v ? String(v) : 'Off') },
];
for (const t of TRIGGERS) {
  const card = el('div', 'tcard');
  const r = el('div', 'ring');
  const { svg, arc } = ring(t.color);
  const num = el('span', 'num');
  r.append(svg, num);
  const box = el('div', 'step');
  card.append(r, el('span', 'cap', t.cap), box);
  $('triggers').append(card);
  stepper(box, t.key, t);
  paint(() => {
    const v = (t.toUnit || ((x) => x))(draft[t.key]);
    num.textContent = t.fmt(v);
    arc.setAttribute('stroke-dasharray', `${Math.min(100, (v / t.max) * 100)} 100`);
    card.classList.toggle('off', !v);
  });
}

const pips = Array.from({ length: 12 }, (_, i) => {
  const p = el('button', 'pip');
  p.type = 'button';
  p.setAttribute('aria-label', `${i + 1}`);
  p.addEventListener('click', () => set('sell.max_reposts', draft['sell.max_reposts'] === i + 1 ? i : i + 1));
  $('pips').append(p);
  return p;
});
paint(() => {
  const n = draft['sell.max_reposts'];
  pips.forEach((p, i) => p.classList.toggle('on', i < n));
  $('maxV').textContent = String(n);
});

// ---------- market ----------

// A list of short strings, as chips with ✕ and a ＋ that opens an input.
for (const box of document.querySelectorAll('[data-list]')) {
  const key = box.dataset.list;
  const emoji = 'emoji' in box.dataset;
  const minimum = key === 'sold.emojis' ? 1 : 0;
  let adding = false;
  paint(() => {
    const list = draft[key];
    const chips = list.map((w, i) => {
      const c = el('span', 'chip' + (emoji ? ' emoji' : ''), w);
      if (list.length > minimum) {
        const x = el('button', 'x', '✕');
        x.type = 'button';
        x.setAttribute('aria-label', `Remove ${w}`);
        x.addEventListener('click', () => set(key, list.filter((_, j) => j !== i)));
        c.append(x);
      }
      return c;
    });
    if (adding) {
      const c = el('span', 'chip' + (emoji ? ' emoji' : ''));
      const inp = el('input');
      inp.maxLength = emoji ? 8 : 24;
      inp.setAttribute('aria-label', emoji ? 'New emoji' : 'New word');
      const done = (keep) => {
        if (!adding) return;
        adding = false;
        const v = inp.value.trim();
        if (keep && v && !list.includes(v)) set(key, [...list, v]); else repaint();
      };
      inp.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); done(true); }
        if (e.key === 'Escape') done(false);
      });
      inp.addEventListener('blur', () => done(true));
      c.append(inp);
      chips.push(c);
      requestAnimationFrame(() => inp.focus());
    } else {
      const add = el('button', 'chip add', emoji ? '＋' : '＋ word');
      add.type = 'button';
      add.addEventListener('click', () => { adding = true; repaint(); });
      chips.push(add);
    }
    box.replaceChildren(...chips);
  });
}

for (const box of document.querySelectorAll('.step[data-key]')) {
  stepper(box, box.dataset.key, { step: 1, min: 1, max: 168, toUnit: (s) => Math.round(s / 3600), fromUnit: (h) => h * 3600, fmt: (h) => `${h}h` });
}

// ---------- calendar ----------

const MEAL_COLOR = { breakfast: '#f2b51b', lunch: '#34c463', snacks: '#5aa9ff', dinner: '#e2553f' };
const MEAL_SHORT = { breakfast: 'BF' };
const clock = (s) => { const [h, m] = s.split(':').map(Number); return h * 60 + m; };

function restartTag(id, key) {
  paint(() => { $(id).hidden = !(D.restart.includes(key) || !same(draft[key], D.values[key])); });
}
restartTag('mealsRestart', 'meals.times');
restartTag('messRestart', 'messes.veg_only');

let mealInputs = null;
paint(() => {
  const times = draft['meals.times'];
  const bars = [el('div', 'axis')];
  for (const name of D.meals) {
    const t = times[name];
    if (!t) continue;
    const st = clock(t.starts);
    const en = clock(t.ends);
    if (!(en > st)) continue;
    const b = el('div', 'meal');
    b.style.left = `${(st / 1440) * 100}%`;
    b.style.width = `${((en - st) / 1440) * 100}%`;
    b.style.background = MEAL_COLOR[name] || '#9aa3b5';
    const label = el('span', '', MEAL_SHORT[name] || name);
    label.style.color = MEAL_COLOR[name] || '#9aa3b5';
    b.append(label);
    bars.push(b);
  }
  const ticks = el('div', 'ticks');
  for (const h of [0, 6, 12, 18, 24]) ticks.append(el('span', '', String(h)));
  $('day').replaceChildren(...bars, ticks);

  // The inputs are built once so typing in one keeps its focus.
  if (!mealInputs) {
    mealInputs = {};
    for (const name of D.meals) {
      const row = el('label', 'time');
      const dot = el('span', 'dot');
      dot.style.background = MEAL_COLOR[name] || '#9aa3b5';
      // Text, not type=time: that shows 12-hour clocks in some locales,
      // and the mess timings are 24-hour.
      const a = el('input');
      const b = el('input');
      for (const x of [a, b]) {
        x.type = 'text';
        x.inputMode = 'numeric';
        x.maxLength = 5;
        x.placeholder = 'HH:MM';
        x.spellcheck = false;
      }
      a.setAttribute('aria-label', `${name} starts`);
      b.setAttribute('aria-label', `${name} ends`);
      const hhmm = (v) => /^([01]\d|2[0-3]):[0-5]\d$/.test(v);
      const update = () => {
        a.classList.toggle('red', !hhmm(a.value));
        b.classList.toggle('red', !hhmm(b.value));
        if (!hhmm(a.value) || !hhmm(b.value)) return;
        set('meals.times', Object.assign(clone(draft['meals.times']), { [name]: { starts: a.value, ends: b.value } }));
      };
      a.addEventListener('change', update);
      b.addEventListener('change', update);
      row.append(dot, el('b', '', MEAL_SHORT[name] || name), a, b);
      $('mealTimes').append(row);
      mealInputs[name] = [a, b];
    }
  }
  for (const [name, [a, b]] of Object.entries(mealInputs)) {
    const t = times[name] || { starts: '', ends: '' };
    if (document.activeElement !== a) a.value = t.starts;
    if (document.activeElement !== b) b.value = t.ends;
    if (document.activeElement !== b) b.classList.toggle('red', !!t.starts && clock(t.ends) <= clock(t.starts));
  }
});

// Seasons: a year bar, then one row per season.
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const DAYS_IN = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
const yearDay = (md) => { const [m, d] = md.split('-').map(Number); return DAYS_IN.slice(0, m - 1).reduce((a, b) => a + b, 0) + d - 1; };

function monthDay(value, onChange, label) {
  const [m, d] = value.split('-').map(Number);
  const ms = el('select');
  const ds = el('select');
  ms.setAttribute('aria-label', `${label} month`);
  ds.setAttribute('aria-label', `${label} day`);
  MONTHS.forEach((n, i) => ms.append(new Option(n, String(i + 1))));
  const days = () => {
    const keep = Number(ds.value) || d;
    ds.replaceChildren(...Array.from({ length: DAYS_IN[Number(ms.value) - 1] }, (_, i) => new Option(String(i + 1), String(i + 1))));
    ds.value = String(Math.min(keep, ds.options.length));
  };
  ms.value = String(m);
  days();
  ds.value = String(d);
  const emit = () => onChange(`${ms.value.padStart(2, '0')}-${ds.value.padStart(2, '0')}`);
  ms.addEventListener('change', () => { days(); emit(); });
  ds.addEventListener('change', emit);
  const box = el('span', 'md');
  box.append(ms, ds);
  return box;
}

// seasonRows is the list the rows show, so a repaint rebuilds them only
// when something else changed it (add, remove, undo, save).
let seasonRows = null;
paint(() => {
  const ss = draft['seasons'];
  const bar = [];
  for (const s of ss) {
    const a = yearDay(s.starts);
    const b = yearDay(s.ends);
    const parts = b >= a ? [[a, b]] : [[a, 364], [0, b]];
    for (const [x, y] of parts) {
      const seg = el('div', 'yseg ' + (s.term ? 'term' : 'brk'), (y - x) > 40 ? s.name.split(' ')[0] : '');
      seg.style.left = `${(x / 365) * 100}%`;
      seg.style.width = `${((y - x + 1) / 365) * 100}%`;
      seg.title = s.name;
      bar.push(seg);
    }
  }
  $('year').replaceChildren(...bar);

  // Typing in a row updates seasonRows first, so the row keeps its focus.
  if (same(seasonRows, ss)) return;
  seasonRows = clone(ss);
  const rows = ss.map((s, i) => {
    const row = el('div', 'season');
    const name = el('input');
    name.type = 'text';
    name.value = s.name;
    name.maxLength = 40;
    name.setAttribute('aria-label', 'Season name');
    const edit = (patch) => {
      const next = clone(draft['seasons']);
      Object.assign(next[i], patch);
      seasonRows = clone(next);
      set('seasons', next);
    };
    name.addEventListener('input', () => edit({ name: name.value }));
    const term = el('button', 'term-tg', 'Term');
    term.type = 'button';
    term.setAttribute('aria-pressed', String(s.term));
    term.addEventListener('click', () => { term.setAttribute('aria-pressed', String(!draft['seasons'][i].term)); edit({ term: !draft['seasons'][i].term }); });
    const del = el('button', 'icon-btn', '✕');
    del.type = 'button';
    del.setAttribute('aria-label', `Remove ${s.name}`);
    del.addEventListener('click', () => set('seasons', draft['seasons'].filter((_, j) => j !== i)));
    row.append(name, term, monthDay(s.starts, (v) => edit({ starts: v }), 'Starts'), el('span', 'to', '→'), monthDay(s.ends, (v) => edit({ ends: v }), 'Ends'), del);
    return row;
  });
  const add = el('button', 'chip add', '＋ season');
  add.type = 'button';
  add.addEventListener('click', () => set('seasons', [...draft['seasons'], { name: 'New season', term: false, starts: '01-01', ends: '01-01' }]));
  $('seasons').replaceChildren(...rows, add);
});

paint(() => {
  const veg = draft['messes.veg_only'];
  $('messes').replaceChildren(...D.messes.map((m) => {
    const c = el('button', 'chip mess');
    c.type = 'button';
    c.setAttribute('aria-pressed', String(!!veg[m]));
    c.title = veg[m] ? 'Veg only. Click to allow non-veg.' : 'Click to mark veg only';
    const ic = el('span', 'mess-ic');
    ic.append(svgEl(messIcon(m)));
    c.append(ic, cap(m));
    if (veg[m]) c.append(el('span', 'leaf', '🌿'));
    c.addEventListener('click', () => set('messes.veg_only', Object.assign(clone(veg), { [m]: !veg[m] })));
    return c;
  }));
});

const fee = document.querySelector('[data-key="sales.monthly_fee"]');
fee.addEventListener('input', () => { const v = Math.max(0, Math.floor(Number(fee.value) || 0)); set('sales.monthly_fee', v); });
paint(() => { if (document.activeElement !== fee) fee.value = String(draft['sales.monthly_fee']); });

// ---------- start ----------

(async () => {
  const m = await me();
  $('role').textContent = m.role === 'admin' ? 'Admin: these change messwa for everyone' : 'Saved in this browser';
  paintTiles(m);
  if (m.role !== 'admin') return;
  try {
    const r = await fetch('api/settings', { cache: 'no-store' });
    if (r.status === 401) { toLogin(); return; }
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `HTTP ${r.status}`);
    D = await r.json();
  } catch (e) {
    showErr(e);
    return;
  }
  draft = clone(D.values);
  $('tabs').hidden = false;
  for (const t of document.querySelectorAll('[data-admin]')) t.hidden = false;
  $('save').hidden = false;
  repaint();
  const last = store.get('mah-settings-tab');
  if (last && document.getElementById(last)) showPane(last);
})();
