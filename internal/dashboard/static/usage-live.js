'use strict';

// Live now on the usage page: every meal users are selling through messwa
// right now. /api/usage/events fires when a listing, sale, WhatsApp
// connection or account changes, and each event refetches /api/usage/live.
// Buyers arrive as counts only.

const LV = { data: null, at: 0, filter: 'all', streaming: false, busy: false, again: false, last: 0 };

// Server time now, moved on by the time since the last fetch.
const lvNow = () => LV.data.now + Math.round((Date.now() - LV.at) / 1000);
const lvBase = (meal) => meal.split('#')[0];
const lvMin = (sec) => Math.max(0, Math.round(sec / 60));
const lvDur = (sec) => { const m = lvMin(sec); return m >= 60 ? `${Math.floor(m / 60)}h ${m % 60}m` : `${m}m`; };
const lvClock = (unix) => new Date(unix * 1000).toLocaleTimeString('en-IN', { hour: 'numeric', minute: '2-digit', timeZone: (typeof D !== 'undefined' && D && D.tz) || undefined }).toLowerCase();
const LV_QUIET = 45 * 60; // no replies this long reads as slow

let lvTimer = 0;
async function lvLoad() {
  // One fetch at a time, and at most one every two seconds: the hub fires
  // on every group message.
  if (LV.busy) { LV.again = true; return; }
  const wait = LV.last + 2000 - Date.now();
  if (wait > 0) {
    if (!lvTimer) lvTimer = setTimeout(() => { lvTimer = 0; lvLoad(); }, wait);
    return;
  }
  LV.busy = true;
  LV.again = false;
  LV.last = Date.now();
  try {
    const r = await fetch('api/usage/live', { cache: 'no-store' });
    if (r.ok) {
      LV.data = await r.json();
      LV.at = Date.now();
      lvRender();
    }
  } catch { /* the stream's error handler shows it */ }
  LV.busy = false;
  if (LV.again) lvLoad();
}

function lvConnect() {
  if (typeof EventSource === 'undefined') return;
  const es = new EventSource('api/usage/events');
  es.addEventListener('open', () => { LV.streaming = true; lvLoad(); lvHead(); });
  es.addEventListener('live', () => { if (document.visibilityState === 'visible') lvLoad(); });
  es.addEventListener('error', () => { LV.streaming = false; lvHead(); });
}

// state is [class, text] for a task's one-line status, worst first.
function lvState(t, now) {
  if (!t.linked) return ['bad', 'WhatsApp not linked'];
  if (!t.connected) return ['bad', 'WhatsApp offline'];
  if (t.failed && !t.groups) return ['bad', `Post failed in ${t.failed === 1 ? 'its group' : `all ${t.failed} groups`}`];
  if (t.failed) return ['bad', `Post failed in ${t.failed} of ${t.failed + t.groups} groups`];
  if (t.paying) {
    const left = t.pay_until ? ` · ${lvDur(t.pay_until - now)} left` : '';
    return ['pay', `${t.paying > 1 ? `${t.paying} buyers` : 'Buyer'} paying${left}`];
  }
  if (t.in_line) return ['line', `${t.in_line} ${t.in_line === 1 ? 'buyer' : 'buyers'} in line`];
  if (!t.groups) return ['slow', 'Not posted yet'];
  const since = Math.max(t.first_post || 0, t.last_reply || 0);
  if (since && now - since >= LV_QUIET) return ['slow', `No replies in ${lvDur(now - since)}`];
  return ['', 'Waiting for buyers'];
}

// window is the serving slot for a task, from today's schedule.
function lvWindow(t) {
  const s = (LV.data.meals || []).find((m) => m.date === t.date && m.meal === lvBase(t.meal));
  if (s) return { start: Date.parse(s.start) / 1000, end: Date.parse(s.end) / 1000 };
  return { start: t.created, end: t.ends };
}

function lvFrac(w, now) {
  if (now < w.start) return 1;
  return Math.max(0, Math.min(1, (w.end - now) / Math.max(1, w.end - w.start)));
}

function lvHead() {
  const dot = $('lvDot');
  if (!dot) return;
  dot.classList.toggle('off', !LV.streaming);
  const meta = $('lvMeta');
  if (!LV.data) { meta.textContent = LV.streaming ? 'loading' : 'connecting'; return; }
  const users = new Set(LV.data.tasks.map((t) => t.user)).size;
  const bits = [LV.data.tasks.length ? `${LV.data.tasks.length} ${LV.data.tasks.length === 1 ? 'meal' : 'meals'} on sale by ${users} ${users === 1 ? 'user' : 'users'}` : 'Nothing on sale'];
  if (!LV.streaming) bits.push(`reconnecting, updated ${lvDur((Date.now() - LV.at) / 1000)} ago`);
  meta.textContent = bits.join(' · ');
}

function lvRender() {
  if (!LV.data) return;
  const now = lvNow();
  const tasks = LV.data.tasks.map((t) => ({ ...t, st: lvState(t, now), w: lvWindow(t) }))
    .sort((a, b) => a.w.end - b.w.end || a.created - b.created);
  lvHead();

  // filters: all, each meal on sale, problems
  const groups = [];
  for (const t of tasks) {
    const k = `${t.date}|${lvBase(t.meal)}`;
    let g = groups.find((x) => x.k === k);
    if (!g) groups.push(g = { k, meal: lvBase(t.meal), date: t.date, w: t.w, rows: [] });
    g.rows.push(t);
  }
  const opts = [['all', 'ALL'], ...[...new Set(groups.map((g) => g.meal))].map((m) => [m, m.toUpperCase()]), ['problems', 'PROBLEMS']];
  if (!opts.some(([v]) => v === LV.filter)) LV.filter = 'all';
  $('lvWins').replaceChildren(...opts.map(([v, label]) => {
    const b = el('button', 'win' + (LV.filter === v ? ' on' : ''), label);
    b.type = 'button';
    b.addEventListener('click', () => { LV.filter = v; lvRender(); });
    return b;
  }));

  // counts
  const sumOf = (k) => tasks.reduce((a, t) => a + (t[k] || 0), 0);
  const priced = tasks.filter((t) => t.market != null);
  const gap = priced.length ? Math.round(priced.reduce((a, t) => a + t.max - t.market, 0) / priced.length) : null;
  const problems = tasks.filter((t) => t.st[0] === 'bad').length;
  const users = new Set(tasks.map((t) => t.user)).size;
  const kp = [
    ['', String(tasks.length), 'ON SALE', `${users} ${users === 1 ? 'user' : 'users'} · ${sumOf('groups')} group posts`],
    ['hand', String(sumOf('in_line')), 'IN LINE', 'buyers who replied'],
    ['ok', String(sumOf('paying')), 'PAYING NOW', 'payment prompt open'],
    ['', gap == null ? '-' : `${gap > 0 ? '+' : gap < 0 ? '-' : ''}₹${Math.abs(gap)}`, 'ASK VS MARKET', 'average over median ask'],
    [problems ? 'bad' : '', String(problems), 'PROBLEMS', 'offline or failed posts'],
  ];
  $('lvKpis').replaceChildren(...kp.map(([c, v, k, e]) => {
    const d = el('div', 'kpi');
    d.append(el('b', c, v), el('span', '', k), el('em', '', e));
    return d;
  }));

  const host = $('lvMeals');
  const keep = (t) => LV.filter === 'all' || (LV.filter === 'problems' ? t.st[0] === 'bad' : lvBase(t.meal) === LV.filter);
  const nodes = [];
  for (const g of groups) {
    const rows = g.rows.filter(keep);
    if (!rows.length) continue;
    const sec = el('div', 'lv-meal');
    const h = el('div', 'lv-mh');
    const frac = lvFrac(g.w, now);
    const when = now < g.w.start ? `starts ${lvClock(g.w.start)}` : `ends ${lvClock(g.w.end)} · ${lvDur(g.w.end - now)} left`;
    const tr = el('span', 'track'), fi = el('i');
    fi.style.setProperty('width', `${frac * 100}%`);
    fi.style.setProperty('background', `hsl(${Math.round(frac * 120)} 75% 52%)`);
    tr.append(fi);
    const day = typeof D !== 'undefined' && D && g.date !== D.today ? ` ${fmtShort(g.date)}` : '';
    h.append(el('b', '', `${g.meal.toUpperCase()}${day.toUpperCase()} · ${rows.length}`), el('span', '', when), tr);
    const grid = el('div', 'lv-cards');
    grid.append(...rows.map((t) => lvCard(t, frac)));
    sec.append(h, grid);
    nodes.push(sec);
  }
  if (!nodes.length) {
    nodes.push(el('p', 'lv-empty', tasks.length ? 'Nothing matches this filter.' : 'Nobody is selling through messwa right now.'));
  }
  host.replaceChildren(...nodes);
}

function lvCard(t, frac) {
  const c = el('div', 'lv-card' + (t.st[0] === 'bad' ? ' warn' : ''));
  const tile = el('div', 'lv-tile');
  tile.style.setProperty('--m', messColor(t.mess));
  const p = el('span', 'price' + (t.max >= 100 ? ' wide' : ''));
  p.append(el('span', 'r', '₹'), String(Math.round(t.max)));
  const diet = el('span', 'lv-diet');
  diet.append(svgEl(dietMark(t.diet)));
  const bar = el('span', 'lv-bar'), fill = el('i');
  fill.style.setProperty('width', `${frac * 100}%`);
  fill.style.setProperty('background', `hsl(${Math.round(frac * 120)} 75% 52%)`);
  bar.append(fill);
  tile.append(el('span', 'lv-mess'), diet, p, bar);

  const b = el('div', 'lv-body');
  const l1 = el('div', 'lv-l1');
  const mn = el('span', 'mn', cap(t.mess) || 'Any mess');
  mn.style.setProperty('color', messColor(t.mess));
  l1.append(el('span', 'who', t.user), mn, el('span', 'ago', t.first_post ? `${lvDur(lvNow() - t.first_post)} ago` : 'new'));

  const l2 = el('div', 'lv-l2');
  const range = t.min === t.max ? `ask ₹${t.min}` : `ask ₹${t.min}–${t.max}`;
  l2.append(range);
  if (t.market != null) {
    const d = Math.round(t.max - t.market);
    l2.append(` · market ₹${Math.round(t.market)} `, el('span', d > 0 ? 'pos' : d < 0 ? 'neg' : 'eq', d === 0 ? '(same)' : `(${d > 0 ? '+' : ''}${d})`));
  }
  const st = el('span', 'lv-st ' + t.st[0]);
  st.append(el('i'), el('span', '', t.st[1]));
  const reposts = t.reposts ? `${t.reposts} ${t.reposts === 1 ? 'repost' : 'reposts'}` : 'no reposts';
  const posts = t.failed ? `${t.groups} of ${t.groups + t.failed} groups` : `${t.groups} ${t.groups === 1 ? 'group' : 'groups'}`;
  const l3 = el('div', 'lv-l2', `${posts} · ${reposts}${t.handoff === 'on_reply' ? ' · hands off on reply' : ''}`);
  b.append(l1, l2, st, l3);
  c.append(tile, b);
  c.setAttribute('aria-label', `${t.user}, ${t.mess} ${t.meal}, ${t.st[1]}`);
  return c;
}

$('lvIcon').append(svgEl(icon('clock')));
lvHead();
lvLoad();
lvConnect();
// Countdowns move without a fetch; without the stream, poll once a minute.
setInterval(() => {
  if (document.visibilityState !== 'visible') return;
  if (!LV.streaming && Date.now() - LV.last > 60 * 1000) lvLoad();
  else lvRender();
}, 15 * 1000);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') lvLoad(); });
