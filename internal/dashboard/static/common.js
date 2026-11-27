'use strict';

// Shared by the board (app.js) and the sales page (sales.js): DOM helpers,
// pixel art and mess identities.

const $ = (id) => document.getElementById(id);
function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}
function svgEl(markup) {
  const t = document.createElement('template');
  t.innerHTML = markup.trim(); // only our own icon markup, never user text
  return t.content.firstChild;
}
const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : '');

// keepScroll runs fn, a live re-render, without moving what's on screen.
// Browsers pin the scroll to an element in view so content growing above it
// doesn't shove the page, but a re-render replaces that element and the pin
// is lost. Safari has no pin at all. So this notes where the elements with
// an id at the top of the screen sit, runs fn, and scrolls by however far
// the deepest one still on the page moved. At the very top it does nothing,
// so a banner appearing there stays in view.
function keepScroll(fn) {
  if (window.scrollY <= 0) return fn();
  const fixed = (n) => { for (; n; n = n.parentElement) if (getComputedStyle(n).position === 'fixed') return true; return false; };
  // Probe just under the sticky shell bar, not the bar itself.
  const bar = document.querySelector('.shell');
  const y = Math.max(1, Math.ceil(bar ? bar.getBoundingClientRect().bottom : 0) + 1);
  const hit = document.elementsFromPoint(innerWidth / 2, y).find((n) => !fixed(n) && !n.closest('.shell'));
  const pins = [];
  for (let n = hit; n && n !== document.body; n = n.parentElement) {
    if (n.id) pins.push([n, n.getBoundingClientRect().top]);
  }
  const out = fn();
  for (const [n, top] of pins) {
    if (!n.isConnected || !n.getClientRects().length) continue;
    // Chrome's own pin may already have done most of it; skip its rounding.
    const moved = n.getBoundingClientRect().top - top;
    if (Math.abs(moved) >= 1) window.scrollBy(0, moved);
    break;
  }
  return out;
}

// ---------- pixel art ----------

function pix(rows, pal) {
  const H = rows.length, W = rows[0].length;
  let r = '';
  rows.forEach((row, y) => {
    for (let x = 0; x < W;) {
      const c = row[x];
      if (c === '.') { x++; continue; }
      let n = 1;
      while (x + n < W && row[x + n] === c) n++;
      r += `<rect x="${x}" y="${y}" width="${n}" height="1" fill="${pal[c]}"/>`;
      x += n;
    }
  });
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${H}" shape-rendering="crispEdges" aria-hidden="true">${r}</svg>`;
}

const ART = {
  kadamba: [[
    '...o..o.o...',
    '.o.oooooo.o.',
    '..owwoooooo.',
    '.owwoooooooo',
    'oowoooooooO.',
    '.oooooooOOOo',
    'oooooooOOOO.',
    '.ooooOOOOOd.',
    '..oOOOOOdd..',
    '.o..dddd..o.',
    '.....gG.....',
    '....gGG.....',
  ], { o: '#f7c548', O: '#d99a1e', w: '#fff0b3', d: '#a8721a', g: '#5aa85d', G: '#2f6b34' }],
  palash: [[
    '.......r....',
    '......rr....',
    '.....rrr..r.',
    '....rryr.rr.',
    '...rryyrrrr.',
    '..rryyyyrr..',
    '..ryylyyrR..',
    '..ryyyyrrR..',
    '..rryyrrRR..',
    '...rrrrRR...',
    '....RRkk....',
    '.....kk.....',
  ], { r: '#ff5a36', R: '#c63a1f', y: '#ffb04a', l: '#ffe2a8', k: '#6b3a2a' }],
  yuktahar: [[
    '...SSSSSS...',
    '..SssssssS..',
    '.SsyysgggsS.',
    'SsyyysggggsS',
    'SsyyysggggsS',
    'SssysssggssS',
    'SsbbswwwsrsS',
    'SbbbwwwwsrrS',
    'SsbbwwwwsrsS',
    '.SsswwwsssS.',
    '..SssssssS..',
    '...SSSSSS...',
  ], { S: '#7f8b9b', s: '#cdd5df', y: '#f2c14e', g: '#5bb55b', w: '#f6f3ea', b: '#c88d4e', r: '#e2553f' }],
  bakul: [[
    '.....ll.....',
    '.....lL.....',
    '..l..lL..l..',
    '...lLLLLl...',
    '....LccL....',
    'llLLLccLLLll',
    '..DLLccLLD..',
    '...lLLLLl...',
    '..l..LD..l..',
    '.....LD.....',
    '.....D......',
    '............',
  ], { l: '#efe6ff', L: '#b89cff', D: '#8466e0', c: '#f7c548' }],
  all: [[
    '............',
    '.kkkk..yyyy.',
    '.kkkk..yyyy.',
    '.kkkk..yyyy.',
    '.kkkk..yyyy.',
    '............',
    '............',
    '.pppp..bbbb.',
    '.pppp..bbbb.',
    '.pppp..bbbb.',
    '.pppp..bbbb.',
    '............',
  ], { k: '#f7c548', y: '#36c9b0', p: '#ff5a36', b: '#b89cff' }],

  breakfast: [[
    'y..y..y',
    '.y.y.y.',
    '..yyy..',
    '.yyyyy.',
    'yyyyyyy',
    'hhhhhhh',
    '.......',
  ], { y: '#ffc94a', h: '#7f93b3' }],
  lunch: [[
    'y..y..y',
    '.yyyyy.',
    '.yyyyy.',
    'yyyyyyy',
    '.yyyyy.',
    '.yyyyy.',
    'y..y..y',
  ], { y: '#ffd84a' }],
  snacks: [[
    '...a...',
    '..aaa..',
    '..aaa..',
    '.aaaAa.',
    '.aaAaa.',
    'aaaaaAa',
    'AAAAAAA',
  ], { a: '#e8a447', A: '#a8681f' }],
  dinner: [[
    '..mmmm.',
    '.mmm...',
    'mmm....',
    'mmm....',
    'mmm....',
    '.mmm...',
    '..mmmm.',
  ], { m: '#dfe4f2' }],

  clock: [[
    '...RRRRRR...',
    '..RffffffR..',
    '.RffffhfffR.',
    'RfffffhffffR',
    'RfffffhffffR',
    'RfffffhhhffR',
    'RffffffffffR',
    'RffffffffffR',
    '.RffffffffR.',
    '..RffffffR..',
    '...RRRRRR...',
    '............',
  ], { R: '#e8c25a', f: '#f3efe2', h: '#2b2f3a' }],
  coin: [[
    '....GGGG....',
    '..GGggggGG..',
    '.GgwwgggggG.',
    '.GwgddddggG.',
    'GgwdggggdggG',
    'GggdggggdggG',
    'GggdggggdggG',
    'GggdggggdggG',
    '.GggddddggG.',
    '.GgggggggGG.',
    '..GGggggGG..',
    '....GGGG....',
  ], { G: '#c98a12', g: '#ffc53d', w: '#fff2b8', d: '#d99a1e' }],
  ingot: [[
    '............',
    '............',
    '...GGGGGGGG.',
    '..GwwwggggGG',
    '.GwggggggGdG',
    'GGGGGGGGGddG',
    'GwwwgggggGdG',
    'GwgggggggGd.',
    'GggggggggG..',
    'GGGGGGGGGG..',
    '............',
    '............',
  ], { G: '#c98a12', g: '#ffc53d', w: '#fff2b8', d: '#9a6a0e' }],
  paper: [[
    '............',
    '..PPPPPPPP..',
    '..PppppppP..',
    '..PpkkkkpP..',
    '..PppppppP..',
    '..PpkkkkpP..',
    '..PppppppP..',
    '..PpkkkppP..',
    '..PppppppP..',
    '..PPPPPPPP..',
    '............',
    '............',
  ], { P: '#b9a77a', p: '#efe3c2', k: '#6b5a33' }],
  check: [[
    '............',
    '..........cc',
    '.........cc.',
    '........cc..',
    '.......cc...',
    'cc....cc....',
    '.cc..cc.....',
    '..cccc......',
    '...cc.......',
    '............',
    '............',
    '............',
  ], { c: '#5be37a' }],
  // Two sheets, the front one over the back. It takes the button's colour.
  copy: [[
    '............',
    '.cccccc.....',
    '.c....c.....',
    '.c..cccccc..',
    '.c..c....c..',
    '.c..c....c..',
    '.cccc....c..',
    '....c....c..',
    '....c....c..',
    '....cccccc..',
    '............',
    '............',
  ], { c: 'currentColor' }],
  chart: [[
    '............',
    '..........bb',
    '..........bb',
    '....yy....bb',
    '....yy....bb',
    '....yy....bb',
    '.kk.yy....bb',
    '.kk.yy....bb',
    '.kk.yy.pp.bb',
    '.kk.yy.pp.bb',
    '.kk.yy.pp.bb',
    '.kk.yy.pp.bb',
  ], { k: '#f7c548', y: '#36c9b0', p: '#ff5a36', b: '#b89cff' }],
  next: [[
    '............',
    '.....a......',
    '.....aa.....',
    '.....aaa....',
    'aaaaaaaaa...',
    'aaaaaaaaaa..',
    'aaaaaaaaa...',
    '.....aaa....',
    '.....aa.....',
    '.....a......',
    '............',
    '............',
  ], { a: '#dfe4f2' }],
};
ART.prev = [ART.next[0].map((r) => [...r].reverse().join('')), ART.next[1]];
// A boiled egg, for meal extras (Kadamba's dinner eggs) sold apart from the meal.
ART.egg = [[
  '...oo...',
  '..owwo..',
  '.owwwwo.',
  '.owhwwo.',
  'owhwwwso',
  'owwwwwso',
  'owwwwsso',
  '.owwsso.',
  '..oooo..',
], { o: '#8a6d3b', w: '#fff3d6', h: '#ffffff', s: '#e3c48c' }];
const icon = (name) => pix(ART[name][0], ART[name][1]);
// extraIcon is the mark for a registered extra. Eggs are the only extra the
// mess sells today, so every extra gets the egg.
const extraIcon = () => icon('egg');
// extraWord names count of an extra: "1 egg", "3 eggs", or "2× Omelette Full"
// for multi-word items so they don't get an awkward trailing "s".
const extraWord = (name, n) => {
  const raw = (name || 'extra').trim();
  if (raw.includes(' ')) return n === 1 ? `1 ${raw}` : `${n}× ${raw}`;
  const w = raw.toLowerCase();
  return `${n} ${n === 1 || /s$/.test(w) ? w : w + 's'}`;
};

function mealsAllIcon(meals = MEALS) {
  const sub = meals.map((m, i) => {
    const s = icon(m).replace('<svg ', `<svg x="${(i % 2) * 8}" y="${Math.floor(i / 2) * 8}" width="7" height="7" `);
    return s;
  }).join('');
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 15 15" aria-hidden="true">${sub}</svg>`;
}

// face draws a buyer as an 8x8 pixel head. The same buyer always gets the
// same face, so a regular is recognisable in the ledger before hovering.
function face(key) {
  let h = 2166136261;
  for (const ch of key) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0;
  const rnd = () => { h = Math.imul(h ^ (h >>> 15), 2246822507) >>> 0; h = Math.imul(h ^ (h >>> 13), 3266489909) >>> 0; return ((h ^= h >>> 16) >>> 0) / 4294967296; };
  const pick = (xs) => xs[Math.floor(rnd() * xs.length)];
  const pal = {
    s: pick(['#c68642', '#8d5524', '#e0ac69', '#a5673f', '#f1c27d']),
    h: pick(['#2b1a10', '#141414', '#5a3a1a', '#7a4b2a', '#3b2a20', '#6b6b78']),
    e: pick(['#3a2a1a', '#24486b', '#2f5a2f']),
    w: '#f4f4f4', m: '#6b3a2a', n: '#00000033',
  };
  const style = Math.floor(rnd() * 3);
  const rows = [];
  for (let y = 0; y < 8; y++) {
    let row = '';
    for (let x = 0; x < 8; x++) {
      let c = 's';
      if (y === 0) c = 'h';
      else if (y === 1) c = style === 0 || (style === 1 && x < 5) || (style === 2 && (x < 2 || x > 5)) ? 'h' : 's';
      else if (y === 2) c = x === 0 || x === 7 || (style === 1 && x < 2) ? 'h' : 's';
      if (y === 4 && (x === 1 || x === 5)) c = 'w';
      if (y === 4 && (x === 2 || x === 6)) c = 'e';
      if (y === 5 && (x === 3 || x === 4)) c = 'n';
      if (y === 6 && x >= 2 && x <= 5) c = 'm';
      row += c;
    }
    rows.push(row);
  }
  return pix(rows, pal);
}

// Indian food marks: green dot for veg, brown triangle for non-veg.
function dietMark(d) {
  const col = { veg: '#34c463', nonveg: '#c9513a', egg: '#f2b51b' }[d];
  if (!col) return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 12 12" aria-hidden="true"><rect x="1" y="1" width="10" height="10" rx="1.5" fill="none" stroke="#4a5266" stroke-width="1.3" stroke-dasharray="2 1.6"/></svg>';
  const inner = d === 'nonveg'
    ? `<polygon points="6,3 9.2,8.6 2.8,8.6" fill="${col}"/>`
    : `<circle cx="6" cy="6" r="2.7" fill="${col}"/>`;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 12 12" aria-hidden="true"><rect x="1" y="1" width="10" height="10" rx="1.5" fill="none" stroke="${col}" stroke-width="1.4"/>${inner}</svg>`;
}

const waIcon = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M12 2a10 10 0 0 0-8.6 15.1L2 22l5-1.3A10 10 0 1 0 12 2Zm0 18.2a8.2 8.2 0 0 1-4.2-1.2l-.3-.2-3 .8.8-2.9-.2-.3A8.2 8.2 0 1 1 12 20.2Zm4.5-6.1c-.2-.1-1.5-.7-1.7-.8-.2-.1-.4-.1-.6.1l-.8 1c-.1.2-.3.2-.5.1a6.7 6.7 0 0 1-3.3-2.9c-.2-.4.2-.4.7-1.3.1-.2 0-.3 0-.4l-.8-1.8c-.2-.5-.4-.4-.6-.4h-.5a1 1 0 0 0-.7.3 3 3 0 0 0-.9 2.2 5.2 5.2 0 0 0 1.1 2.7 11.8 11.8 0 0 0 4.5 4c1.7.7 2.3.8 3.2.6a2.7 2.7 0 0 0 1.8-1.3 2.2 2.2 0 0 0 .2-1.3c-.1-.1-.3-.2-.5-.3Z"/></svg>';
// Beeper's logo, traced from the PNG.
const beeperIcon = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" fill-rule="evenodd" d="M8.79 0.00 7.58 0.17 6.54 0.52 5.55 1.04 4.68 1.69 3.90 2.51 3.12 3.73 2.64 4.98 2.47 5.89 2.43 7.06 2.51 7.88 2.73 8.79 3.03 9.57 3.51 10.44 4.03 11.13 5.07 12.13 5.81 12.56 5.81 24.00 15.73 24.00 16.90 23.83 17.98 23.44 18.50 23.22 19.23 22.74 20.32 21.70 21.10 20.40 21.49 19.06 21.49 17.11 21.23 16.20 20.88 15.47 20.40 14.77 19.62 13.95 19.54 13.95 18.80 13.30 18.19 12.95 18.19 12.87 20.19 10.87 20.79 9.92 21.10 9.27 21.49 7.88 21.57 7.06 21.49 5.68 21.18 4.46 20.58 3.21 19.80 2.17 18.89 1.34 18.24 0.91 17.24 0.43 16.20 0.13 15.29 0.00ZM8.75 1.86 15.21 1.86 16.59 2.17 17.63 2.73 18.45 3.47 19.10 4.38 19.58 5.63 19.71 6.71 19.58 7.88 19.32 8.66 18.76 9.66 12.13 16.33 10.31 18.15 10.22 18.15 11.52 11.61 8.58 11.61 7.36 11.31 6.67 10.96 6.11 10.57 5.85 10.27 5.76 10.27 5.11 9.49 4.77 8.88 4.42 7.88 4.29 6.80 4.42 5.59 4.68 4.81 5.29 3.77 5.98 3.03 6.71 2.51 7.71 2.08Z"/></svg>';

// chatHref links to a WhatsApp chat with that phone, in Beeper when the
// board's settings say so. Beeper's links only work in Beeper Desktop, so
// touch screens always get WhatsApp. Null without a number.
function chatHref(phone) {
  const ph = (phone || '').replace(/\D/g, '');
  if (!ph) return null;
  let via = '';
  try { via = JSON.parse(localStorage.getItem('mah') || '{}').via; } catch { /* no storage: WhatsApp */ }
  const touch = matchMedia('(hover: none) and (pointer: coarse)').matches;
  return via === 'beeper' && !touch ? `beeper://compose/whatsapp/phone/+${ph}` : `https://wa.me/${ph}`;
}

// ---------- mess identities ----------

const MESS = {
  kadamba: { color: '#f7c548', blurb: 'Golden kadamba blossoms' },
  yuktahar: { color: '#36c9b0', blurb: 'The balanced thali' },
  palash: { color: '#ff6a45', blurb: 'Flame of the forest' },
  bakul: { color: '#b89cff', blurb: 'Star-shaped bakul flowers' },
  all: { color: '#e8ecf3', blurb: 'Every mess at once' },
};
const messColor = (m) => (MESS[m] || {}).color || '#9aa3b5';
const messIcon = (m) => (ART[m] ? icon(m) : pix(['xxxx', 'xxxx', 'xxxx', 'xxxx'], { x: messColor(m) }));
const MEALS = ['breakfast', 'lunch', 'snacks', 'dinner'];
const DIETS = ['all', 'veg', 'nonveg', 'egg'];
const DIET_LABEL = { all: 'All', veg: 'Veg', nonveg: 'Non-veg', egg: 'Egg', extra: 'Extra' };
const DIET_COLOR = { veg: 'ca', nonveg: 'cc', egg: 'c6', extra: 'c6' };

// ---------- tooltip engine ----------

const tipEl = $('tip');
const lastMouse = { x: -1, y: -1 };
addEventListener('mousemove', (e) => { lastMouse.x = e.clientX; lastMouse.y = e.clientY; }, { passive: true });
let tipFor = null;
let tipFn = null;

// fillLines writes tooltip lines into box: segments of {t, c, color}, a
// falsy line for a gap, or {rar, c} for the closing rarity line.
function fillLines(box, lines) {
  box.replaceChildren();
  for (const ln of lines) {
    if (!ln) { box.append(el('div', 'gap')); continue; }
    if (ln.rar) {
      const r = el('div', 'rar ' + (ln.c || ''), ln.rar);
      if (ln.color) r.style.setProperty('color', ln.color);
      box.append(r);
      continue;
    }
    const row = el('div', 'ln');
    for (const seg of ln) {
      if (!seg || !seg.t) continue;
      const s = el('span', seg.c, seg.t);
      if (seg.color) s.style.setProperty('color', seg.color);
      row.append(s);
    }
    if (row.childNodes.length) box.append(row);
  }
}
const fillTip = (lines) => fillLines(tipEl, lines);

function placeTip(x, y) {
  const r = tipEl.getBoundingClientRect();
  let left = x + 16, top = y - 12;
  if (left + r.width > innerWidth - 8) left = x - r.width - 16;
  if (top + r.height > innerHeight - 8) top = innerHeight - r.height - 8;
  tipEl.style.setProperty('transform', `translate(${Math.max(8, left)}px, ${Math.max(8, top)}px)`);
}

function showTip(target, fn, x, y) {
  tipFor = target;
  tipFn = fn;
  fillTip(fn());
  tipEl.hidden = false;
  placeTip(x, y);
}
function hideTip() {
  tipEl.hidden = true;
  tipFor = null;
  tipFn = null;
}

// tip attaches a hover tooltip to a static element (tabs, buttons).
// Touch taps never show tooltips; the detail view covers that.
function tip(node, fn) {
  node._tip = fn;
  node.addEventListener('pointerenter', (e) => { if (e.pointerType === 'mouse') showTip(node, fn, e.clientX, e.clientY); });
  node.addEventListener('pointermove', (e) => { if (tipFor === node) placeTip(e.clientX, e.clientY); });
  node.addEventListener('pointerleave', hideTip);
}

// ---------- keys ----------

// A chart's key hides behind a ? button at the right end of its box's
// header and opens as a popover. keyPop returns the popover for a header,
// making the button the first time, so a page can fill it again on every
// render. The popover lives inside the box so the box's CSS variables still
// color the swatches.
function keyPop(head) {
  const old = head.querySelector(':scope > .keybtn');
  if (old) return old.popoverTargetElement;
  const pop = el('div', 'keypop');
  pop.popover = 'auto';
  pop.setAttribute('role', 'dialog');
  pop.setAttribute('aria-label', 'Key');
  const b = el('button', 'keybtn', '?');
  b.type = 'button';
  b.title = 'What the colors mean';
  b.setAttribute('aria-label', 'Show the key');
  b.setAttribute('aria-expanded', 'false');
  b.popoverTargetElement = pop;
  pop.addEventListener('toggle', (e) => {
    const open = e.newState === 'open';
    b.setAttribute('aria-expanded', String(open));
    if (open) placeKey(b, pop);
  });
  head.append(b);
  head.closest('.chest, .mini').append(pop);
  return pop;
}

// placeKey hangs the popover under its button, right edges lined up, and
// keeps it on screen.
function placeKey(b, pop) {
  const r = b.getBoundingClientRect(), w = pop.offsetWidth, h = pop.offsetHeight;
  const left = Math.max(8, Math.min(r.right - w, innerWidth - w - 8));
  const top = r.bottom + 6 + h > innerHeight - 8 ? Math.max(8, r.top - 6 - h) : r.bottom + 6;
  pop.style.left = `${left}px`;
  pop.style.top = `${top}px`;
}

// The popover is fixed to the screen, so it follows its button on scroll.
const followKey = () => {
  const pop = document.querySelector('.keypop:popover-open');
  if (pop) placeKey(pop.parentElement.querySelector('.keybtn[aria-expanded="true"]'), pop);
};
addEventListener('scroll', followKey, { passive: true, capture: true });
addEventListener('resize', followKey);

// Keys written into the page's HTML move behind their box's button. A key
// inside a small chart (.mini) gets its own button by that chart's title.
for (const leg of document.querySelectorAll('.chest .slegend, .chest .legend, .chest .pleg')) {
  const box = leg.closest('.mini') || leg.closest('.chest');
  const head = box.querySelector(box.matches('.mini') ? 'h4' : '.chest-head');
  if (head) keyPop(head).append(leg);
}

// ---------- money ----------

// rupees prints whole amounts bare and paise with two decimals: ₹60, ₹2.37.
const rupees = (v) => '₹' + (Number.isInteger(v) ? v.toLocaleString('en-IN') : v.toFixed(2));

// priceRange is the price picker the sell sheet and the sale card's range
// editor share: a −/+ box you can type into for each end, over a two-thumb
// slider. The slider and the −/+ buttons move in ₹5 steps; typing takes any
// whole rupee from 1 to 10000. o is
//
//   lo, hi     the starting floor and opening price
//   bounds     {lo, hi}, the slider's scale
//   fixed      one price: a single box, and both ends move together
//   gap        how far apart the two ends must stay (0 lets them meet)
//   marks      [{at, cls}] ticks on the track, e.g. the median ask
//   axis       the three labels under the track, or null for none
//   onChange   called with (lo, hi) after every change
//
// It returns {nodes, get}, get giving [lo, hi].
const PRICE_STEP = 5;
function priceRange(o) {
  const B = o.bounds, gap = o.fixed ? 0 : o.gap || 0;
  // The range inputs start on a multiple of ₹5. A range from ₹1 with a
  // step of 5 would only stop on 1, 6, 11 and so on.
  const R0 = Math.floor(B.lo / PRICE_STEP) * PRICE_STEP;
  const clamp = (x) => Math.min(10000, Math.max(1, Math.round(x)));
  const pct = (p) => Math.min(100, Math.max(0, ((p - R0) / (B.hi - R0)) * 100));
  let lo = clamp(o.lo), hi = o.fixed ? lo : Math.max(clamp(o.hi), lo + gap);

  // The −/+ buttons land on the next multiple of ₹5, so ₹17 goes to ₹20 or ₹15.
  const stepFrom = (x, d) => (d > 0 ? Math.floor(x / PRICE_STEP) + 1 : Math.ceil(x / PRICE_STEP) - 1) * PRICE_STEP;
  const setLo = (x) => { x = clamp(x); if (o.fixed) lo = hi = x; else lo = Math.min(x, Math.max(1, hi - gap)); };
  const setHi = (x) => { hi = Math.max(clamp(x), lo + gap); };

  const box = (label, aria, get, set) => {
    const w = el('div', 'pr-box');
    const minus = el('button', '', '−'), plus = el('button', '', '+');
    const inp = el('input');
    inp.className = 'pr-num';
    Object.assign(inp, { type: 'text', inputMode: 'numeric', autocomplete: 'off', enterKeyHint: 'done' });
    inp.setAttribute('aria-label', `${aria} in rupees`);
    for (const [btn, d, word] of [[minus, -1, 'down'], [plus, 1, 'up']]) {
      btn.type = 'button';
      btn.setAttribute('aria-label', `${aria} ${word} ₹${PRICE_STEP}`);
      btn.addEventListener('click', () => { set(stepFrom(get(), d)); draw(); });
    }
    // Typing redraws the slider as you go but leaves the box alone until
    // you leave it, so clearing it to retype doesn't snap back to ₹1.
    inp.addEventListener('input', () => {
      const n = parseInt(inp.value.replace(/\D/g, ''), 10);
      if (n > 0) { set(n); draw(inp); }
    });
    inp.addEventListener('change', () => draw());
    inp.addEventListener('keydown', (k) => {
      if (k.key === 'ArrowUp' || k.key === 'ArrowDown') { k.preventDefault(); set(stepFrom(get(), k.key === 'ArrowUp' ? 1 : -1)); draw(); }
    });
    const field = el('div', 'pr-in');
    field.append(minus, el('span', 'cur', '₹'), inp, plus);
    w.append(el('span', 'pr-k', label), field);
    return [w, inp];
  };
  const vals = el('div', 'pr-vals');
  const [bLo, iLo] = box(o.fixed ? 'PRICE' : 'LOWEST', o.fixed ? 'Price' : 'Lowest price', () => lo, setLo);
  const [bHi, iHi] = box('OPENS AT', 'Opening price', () => hi, setHi);
  vals.append(bLo);
  if (!o.fixed) vals.append(bHi);

  const dual = el('div', 'dual');
  const track = el('div', 'track');
  const fill = el('i', 'fill');
  track.append(fill);
  for (const m of o.marks || []) {
    const t = el('i', m.cls);
    t.style.setProperty('left', `${pct(m.at)}%`);
    track.append(t);
  }
  const range = (label) => {
    const r = el('input');
    Object.assign(r, { type: 'range', min: R0, max: B.hi, step: PRICE_STEP });
    r.setAttribute('aria-label', label);
    return r;
  };
  const rLo = range(o.fixed ? 'Price' : 'Lowest price'), rHi = range('Opening price');
  dual.append(track, rLo);
  if (!o.fixed) dual.append(rHi);
  // The thumb that last moved sits on top, so two thumbs at the same spot
  // can still be pulled apart.
  rLo.addEventListener('input', () => { setLo(+rLo.value); rLo.style.zIndex = 3; rHi.style.zIndex = 2; draw(); });
  rHi.addEventListener('input', () => { setHi(+rHi.value); rHi.style.zIndex = 3; rLo.style.zIndex = 2; draw(); });

  const nodes = [vals, dual];
  if (o.axis) {
    const axis = el('div', 'sell-axis');
    axis.append(el('span', '', o.axis[0]), el('span', 'med-l', o.axis[1] || ''), el('span', '', o.axis[2]));
    nodes.push(axis);
  }

  // draw syncs everything to lo/hi, skipping the box being typed in.
  function draw(typing) {
    if (iLo !== typing) iLo.value = lo;
    if (iHi !== typing) iHi.value = hi;
    rLo.value = lo;
    rHi.value = hi;
    rLo.setAttribute('aria-valuetext', rupees(lo));
    rHi.setAttribute('aria-valuetext', rupees(hi));
    fill.style.setProperty('left', `${pct(lo)}%`);
    fill.style.setProperty('right', `${100 - pct(hi)}%`);
    if (o.onChange) o.onChange(lo, hi);
  }
  draw();
  return { nodes, get: () => [lo, hi], focus: rLo };
}

// ---------- session ----------

// Pages need a signed-in account. A 401 from any API call sends the browser
// to the sign-in page, which brings it back here afterwards.
function toLogin() {
  const next = location.pathname.split('/').pop();
  location.href = 'login.html' + (next && next !== 'index.html' && next !== 'login.html' ? `?next=${encodeURIComponent(next)}` : '');
}

let mePromise = null;
// me is the signed-in user: {name, role, mess_key_set, can_store_keys, csrf}.
function me() {
  if (!mePromise) {
    mePromise = fetch('api/me', { cache: 'no-store' }).then((r) => {
      if (r.status === 401) { toLogin(); throw new Error('signed out'); }
      if (!r.ok) throw new Error(r.status);
      return r.json();
    });
    mePromise.catch(() => { mePromise = null; });
  }
  return mePromise;
}

// send makes a JSON request that changes something, with the CSRF token.
// It resolves to the parsed body ({} when empty) or throws Error(message).
async function send(method, path, body) {
  const m = await me();
  const r = await fetch(path, {
    method,
    cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': m.csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (r.status === 401) { toLogin(); throw new Error('signed out'); }
  const text = await r.text();
  const data = text ? JSON.parse(text) : {};
  if (!r.ok) throw new Error(data.error || `HTTP ${r.status}`);
  return data;
}

// The shared credit keeps every dashboard page in the same pixel style.
{
  const credit = el('footer', 'site-credit');
  const heart = el('span', 'site-credit-heart');
  heart.setAttribute('role', 'img');
  heart.setAttribute('aria-label', 'love');
  heart.append(svgEl(pix([
    '.rr.rr.',
    'rrrrrrr',
    'rhhrrrr',
    '.rrrrr.',
    '..rrr..',
    '...r...',
  ], { r: '#e2553f', h: '#ff9a87' })));
  credit.append('Made with ', heart, ' by ', el('span', 'site-credit-names', 'Shourya and Atharva'));
  document.body.append(credit);
}
