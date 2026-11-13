'use strict';

// The admin usage page: each account's sales-page totals side by side.
// The server strips buyers, so nothing here can name one. User names are
// account names and only reach the page through textContent.

let D = null; // last /api/usage

const PREFS = 'messwa.usage';
const F = { range: '28d', who: 'all' };
try { Object.assign(F, JSON.parse(localStorage.getItem(PREFS) || '{}')); } catch { /* private mode */ }
const savePrefs = () => { try { localStorage.setItem(PREFS, JSON.stringify(F)); } catch { /* ignore */ } };

const RANGES = [['7d', '7 DAYS'], ['28d', '28 DAYS'], ['month', 'THIS MONTH']];
const WHO = [['all', 'ALL USERS'], ['active', 'SELLING'], ['stuck', 'NEED HELP']];
const COL = { sold: '#35a848', hand: '#4a8fe8', lapse: '#c99322', waste: '#d9503f', man: '#c94fd0' };
const STACK = [['sold', 'Sold', COL.sold], ['handoff', 'Handed off', COL.hand], ['lapsed', 'Lapsed', COL.lapse], ['wasted', 'Wasted', COL.waste]];

// ---------- helpers ----------

const dayOf = (date) => new Date(date + 'T12:00:00Z');
const fmtD = (date, o) => new Intl.DateTimeFormat('en-GB', { ...o, timeZone: 'UTC' }).format(dayOf(date));
const fmtShort = (date) => fmtD(date, { day: 'numeric', month: 'short' });
const fmtDay = (date) => fmtD(date, { weekday: 'short', day: 'numeric', month: 'short' });
const sum = (xs) => xs.reduce((a, b) => a + b, 0);
const pc = (a, b) => (b ? Math.round((100 * a) / b) : null);
const pcs = (a, b) => (b ? `${pc(a, b)}%` : '-');
const n0 = (v) => Math.round(v).toLocaleString('en-IN');
function median(xs) {
  if (!xs.length) return null;
  const v = xs.slice().sort((a, b) => a - b), h = v.length >> 1;
  return v.length % 2 ? v[h] : (v[h - 1] + v[h]) / 2;
}
function dur(sec) {
  const m = Math.round(Math.max(0, sec) / 60);
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)}h ${m % 60}m`;
}
function ago(unix) {
  if (!unix) return '-';
  const s = D.now - unix;
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))}m`;
  if (s < 86400) return `${Math.round(s / 3600)}h`;
  return `${Math.round(s / 86400)}d`;
}
function addDays(date, n) {
  const d = dayOf(date);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}
const clip = (s, n) => (s.length > n ? s.slice(0, n - 1) + '…' : s);

const NS = 'http://www.w3.org/2000/svg';
function S(tag, attrs, parent) {
  const e = document.createElementNS(NS, tag);
  for (const k in attrs) e.setAttribute(k, attrs[k]);
  if (parent) parent.append(e);
  return e;
}
function T(parent, x, y, text, attrs) {
  const e = S('text', { x, y, ...attrs }, parent);
  e.textContent = text;
  return e;
}
function chart(host, h, min = 240) {
  host.replaceChildren();
  const w = Math.max(min, Math.floor(host.clientWidth));
  const s = S('svg', { width: w, height: h, viewBox: `0 0 ${w} ${h}` }, host);
  return [s, w];
}
function niceMax(v) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v)), n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}

// ---------- the view ----------

// from is the first day the range filter shows.
function from() {
  if (F.range === '7d') return addDays(D.today, -6);
  if (F.range === 'month') return D.month;
  return D.recent;
}
const inRange = (date) => date >= from() && date <= D.today;
const live = (u) => u.status === 'active' && !u.disabled;

// stats sums one user over the range.
function stats(u) {
  const days = u.days.filter((d) => inRange(d.date));
  const sales = u.sales.filter((s) => inRange(s.date));
  const paid = sales.filter((s) => s.state === 'sold' || s.state === 'sending');
  const priced = paid.filter((s) => s.ask != null);
  const o = {
    sold: sum(days.map((d) => d.sold)), handoff: sum(days.map((d) => d.handoff)),
    lapsed: sum(days.map((d) => d.lapsed)), wasted: sum(days.map((d) => d.wasted)),
    earned: sum(paid.map((s) => s.amount)),
    vs: priced.length ? sum(priced.map((s) => s.amount - s.ask)) / priced.length : null,
    priced: priced.length,
    tasks: u.tasks.filter((t) => inRange(t.date)).length,
    sales, paid,
  };
  o.spare = o.sold + o.handoff + o.lapsed + o.wasted;
  return o;
}

// status is the one thing to know about a user, worst first.
function status(u) {
  if (u.disabled) return ['bad', 'Disabled'];
  if (u.status === 'requested') return ['mid', 'Waiting for approval'];
  if (u.status === 'declined') return ['bad', 'Declined'];
  if (u.status !== 'active') return ['mid', 'Setting up'];
  if (u.error) return ['bad', "Couldn't load"];
  if (!u.mess_key) return ['bad', 'No mess key'];
  if (u.portal) return ['bad', 'Mess portal failing'];
  if (!u.linked) return ['bad', 'WhatsApp not linked'];
  if (!u.connected) return ['mid', 'WhatsApp offline'];
  return ['ok', 'WhatsApp linked'];
}

// staleHandoffs counts chats handed to the user whose meal ended over a day
// ago without the bot learning how they ended.
const staleHandoffs = (u) => u.sales.filter((s) => s.state === 'handoff' && s.end && s.end < D.now - 86400 && inRange(s.date)).length;

// attention lists what an admin could fix for one user, worst first.
function attention(u, st) {
  if (!live(u)) return [];
  const out = [];
  const add = (lvl, text) => out.push({ lvl, name: u.name, text });
  if (u.error) add('crit', `couldn't load: ${clip(u.error, 90)}`);
  if (!u.mess_key) add('crit', "hasn't added a mess API key, so nothing can be registered or sold.");
  else if (u.portal) add('crit', `'s mess key fails on the portal: ${clip(u.portal, 90)}`);
  if (!u.linked && u.ever_task) add('crit', "'s WhatsApp is unlinked, so their sell tasks can't run.");
  else if (u.linked && !u.connected) add('warn', "'s WhatsApp is linked but offline.");
  if (st.vs != null && st.vs > 10 && st.priced >= 2 && pc(st.sold, st.spare) < 40) add('warn', ` sells at ₹${Math.round(st.vs)} over the median ask and sold ${pcs(st.sold, st.spare)} of spare meals.`);
  if (st.wasted >= 5 && st.wasted > st.sold) add('warn', ` wasted ${st.wasted} meals and sold ${st.sold}.`);
  if (u.linked && !u.ever_task) add('info', ' linked WhatsApp but hasn\'t started a sell task.');
  return out;
}

const stuck = (u) => !live(u) || status(u)[0] !== 'ok' || attention(u, stats(u)).length > 0 || staleHandoffs(u) > 0;
function shown() {
  if (F.who === 'active') return D.users.filter((u) => live(u) && stats(u).tasks + stats(u).spare > 0);
  if (F.who === 'stuck') return D.users.filter(stuck);
  return D.users;
}

// ---------- data ----------

async function load() {
  try {
    const r = await fetch('api/usage', { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    if (!r.ok) throw new Error((await r.text()).trim() || r.status);
    D = await r.json();
    $('notice').hidden = true;
  } catch (e) {
    if (!D) {
      $('notice').hidden = false;
      $('notice').textContent = `Couldn't load usage: ${e.message}`;
      return;
    }
  }
  renderFilters();
  render();
}

function renderFilters() {
  const group = (box, items, on, set) => {
    box.replaceChildren();
    for (const [v, label] of items) {
      const b = el('button', 'win' + (on(v) ? ' on' : ''), label);
      b.type = 'button';
      b.addEventListener('click', () => { set(v); savePrefs(); renderFilters(); render(); });
      box.append(b);
    }
  };
  group($('fRange'), RANGES, (v) => F.range === v, (v) => { F.range = v; });
  group($('fWho'), WHO, (v) => F.who === v, (v) => { F.who = v; });
}

function render() {
  if (!D) return;
  const users = shown();
  renderOverview(users);
  renderDaily(users);
  renderUsers(users);
  renderFunnel();
  renderAttention(users);
  renderMix(users);
  renderPrice(users);
}

// ---------- overview ----------

function renderOverview(users) {
  const act = users.filter(live);
  $('ovMeta').textContent = `${fmtShort(from())} to ${fmtShort(D.today)} · ${act.length} approved user${act.length === 1 ? '' : 's'} shown`;

  // The bar is always this month, since bills are monthly.
  let fee = 0, earned = 0, missed = 0, best = null;
  for (const u of act) {
    const e = sum(u.sales.filter((s) => s.date >= D.month && (s.state === 'sold' || s.state === 'sending')).map((s) => s.amount));
    const m = sum(u.days.filter((d) => d.date >= D.month).map((d) => d.missed || 0));
    earned += e; missed += m;
    if (u.fee > 0) {
      fee += u.fee;
      if (!best || e / u.fee > best.r) best = { name: u.name, r: e / u.fee };
    }
  }
  $('monthV').replaceChildren(rupees(Math.round(earned)), el('small', '', fee ? `of ${rupees(Math.round(fee))} in bills` : 'no bills known'));
  const w = fee ? Math.min(100, (100 * earned) / fee) : 0;
  $('monthFill').style.setProperty('width', `${w}%`);
  $('monthCould').style.setProperty('left', `${w}%`);
  $('monthCould').style.setProperty('width', `${fee ? Math.min(100 - w, (100 * missed) / fee) : 0}%`);
  const row = $('monthRow');
  row.replaceChildren();
  const part = (...kids) => { const s = el('span'); s.append(...kids); row.append(s); };
  part(el('b', 'x', fee ? pcs(earned, fee) : '-'), ' of mess bills covered by sales');
  part(el('b', 'g', rupees(Math.round(missed))), ' more if wasted meals had sold');
  if (best) part(el('span', 'dim', `best: ${best.name} ${Math.round(best.r * 100)}%`));

  const box = $('kpis');
  box.replaceChildren();
  const kpi = (v, label, sub, tipFn) => {
    const k = el('div', 'kpi');
    k.append(el('b', v == null ? 'none' : '', v == null ? '-' : v), el('span', '', label), el('em', '', sub));
    if (tipFn) tip(k, tipFn);
    box.append(k);
  };
  const ss = act.map(stats);
  const t = (k) => sum(ss.map((s) => s[k]));
  const selling = ss.filter((s) => s.tasks + s.sold + s.handoff > 0).length;
  const linked = act.filter((u) => u.linked).length;
  const tasks = t('tasks');
  const listings = sum(Object.entries(D.listings).filter(([d]) => inRange(d)).map(([, n]) => n));
  kpi(String(selling), 'SELLING', `of ${act.length} approved`, () => [[{ t: 'Selling', c: 'nm' }], [{ t: 'Users with a sell task or a sale in this range.', c: 'c7' }]]);
  kpi(`${linked}/${act.length}`, 'WA LINKED', `${act.filter((u) => u.linked && !u.connected).length} offline now`);
  kpi(n0(t('sold')), 'MEALS SOLD', `${rupees(Math.round(t('earned')))} earned`);
  kpi(pcs(t('sold'), t('spare')), 'SELL-THROUGH', 'of spare meals', () => [[{ t: 'Sell-through', c: 'nm' }],
    [{ t: "Sold, out of every registered meal the user didn't eat: sold, handed off, lapsed or wasted.", c: 'c7' }]]);
  kpi(n0(t('wasted')), 'WASTED', 'unsold, uneaten', () => [[{ t: 'Wasted', c: 'nm' }],
    [{ t: "Users whose mess portal can't be read have no wasted count.", c: 'c7' }]]);
  kpi(pcs(tasks, listings), 'MARKET SHARE', `${n0(tasks)} of ${n0(listings)} listings`, () => [[{ t: 'Market share', c: 'nm' }],
    [{ t: 'Sell tasks started on messwa, out of every sell listing seen in the groups for the same meal dates.', c: 'c7' }]]);
}

// ---------- sales per day ----------

function renderDaily(users) {
  const dates = [];
  for (let d = from(); d <= D.today; d = addDays(d, 1)) dates.push(d);
  const by = new Map(dates.map((d) => [d, { sold: 0, handoff: 0, lapsed: 0, wasted: 0 }]));
  for (const u of users.filter(live)) {
    for (const d of u.days) {
      const b = by.get(d.date);
      if (b) for (const [k] of STACK) b[k] += d[k];
    }
  }
  const H = 190, L = 28, B = 20, TOP = 6;
  const [s, W] = chart($('daily'), H);
  const max = niceMax(Math.max(4, ...[...by.values()].map((b) => sum(STACK.map(([k]) => b[k])))));
  const y = (v) => TOP + (H - B - TOP) * (1 - v / max);
  const lead = Math.round(max / 10 ** Math.floor(Math.log10(max)));
  const ticks = lead === 2 ? 4 : 5;
  for (let i = 0; i <= ticks; i++) {
    const v = (max * i) / ticks;
    S('line', { x1: L, x2: W, y1: y(v), y2: y(v), stroke: i ? '#1d2330' : '#2b3346' }, s);
    T(s, L - 6, y(v) + 3, String(Math.round(v)), { 'text-anchor': 'end' });
  }
  const bw = (W - L) / dates.length;
  const gap = Math.min(4, bw * 0.25);
  const every = dates.length > 14 ? 7 : 1;
  dates.forEach((date, i) => {
    const b = by.get(date), x = L + i * bw + gap / 2, w = bw - gap;
    const keys = STACK.filter(([k]) => b[k] > 0);
    let acc = 0;
    keys.forEach(([k, , c], j) => {
      const y0 = y(acc), y1 = y(acc + b[k]);
      const h = Math.max(1, y0 - y1 - (acc ? 2 : 0));
      if (j === keys.length - 1) {
        const r = Math.min(4, h, w / 2);
        S('path', { d: `M${x},${y1 + h}V${y1 + r}Q${x},${y1} ${x + r},${y1}H${x + w - r}Q${x + w},${y1} ${x + w},${y1 + r}V${y1 + h}Z`, fill: c }, s);
      } else {
        S('rect', { x, y: y1, width: w, height: h, fill: c }, s);
      }
      acc += b[k];
    });
    if (i % every === 0 || i === dates.length - 1) T(s, x + w / 2, H - 5, every > 1 ? fmtShort(date) : fmtD(date, { weekday: 'short' }), { 'text-anchor': 'middle' });
    const hit = S('rect', { x: L + i * bw, y: TOP, width: bw, height: H - B - TOP, class: 'hit' }, s);
    tip(hit, () => [[{ t: fmtDay(date), c: 'nm' }], ...STACK.map(([k, n, c]) => [{ t: '■ ', color: c }, { t: `${n} `, c: 'c7' }, { t: String(b[k]) }])]);
  });
}

// ---------- users ----------

function renderUsers(users) {
  const t = $('users');
  t.replaceChildren();
  const hr = el('tr');
  for (const [h, n] of [['USER', 0], ['STATUS', 0], ['ACTIVE', 1], ['SOLD / SPARE', 0], ['EARNED', 1], ['VS MARKET', 1], ['LAST 28 DAYS', 0]]) hr.append(el('th', n ? 'n' : '', h));
  t.append(hr);
  const rows = users.map((u) => ({ u, st: stats(u) }))
    .sort((a, b) => (live(b.u) - live(a.u)) || (b.st.earned - a.st.earned) || a.u.name.localeCompare(b.u.name));
  for (const { u, st } of rows) {
    const tr = el('tr', live(u) ? '' : 'off');
    const who = el('td');
    who.append(el('span', 'nm', u.name));
    if (u.role === 'admin') who.append(el('span', 'role', 'ADMIN'));
    const [cls, label] = status(u);
    const stc = el('td'), sp = el('span', 'st ' + cls);
    sp.append(el('i'), label);
    stc.append(sp);
    const thru = el('td');
    if (st.spare) {
      const r = el('span', 'rate'), f = el('i');
      f.style.setProperty('width', `${pc(st.sold, st.spare)}%`);
      r.append(f);
      thru.append(r, `${st.sold}/${st.spare} `, el('span', 'dim', pcs(st.sold, st.spare)));
    } else thru.append(el('span', 'dim', live(u) ? 'nothing spare' : '-'));
    const vs = el('td', 'n');
    if (st.vs == null) vs.append(el('span', 'dim', '-'));
    else {
      const v = Math.round(st.vs);
      vs.append(el('span', v > 5 ? 'neg' : v < -5 ? 'pos' : '', `${v > 0 ? '+' : ''}${v}`));
    }
    const spark = el('td');
    if (live(u) && u.days.length) spark.append(sparkline(u, cls));
    tr.append(who, stc, el('td', 'n dim', ago(u.last_active)), thru,
      el('td', 'n', st.earned ? rupees(Math.round(st.earned)) : '₹0'), vs, spark);
    tip(tr, () => {
      const lines = [[{ t: u.name, c: 'nm' }], [{ t: label, c: 'c7' }], null];
      if (live(u)) {
        lines.push([{ t: 'Sell tasks ', c: 'c7' }, { t: String(st.tasks) }]);
        for (const [k, n, c] of STACK) lines.push([{ t: '■ ', color: c }, { t: `${n} `, c: 'c7' }, { t: String(st[k]) }]);
        if (st.sold) lines.push([{ t: 'Average sale ', c: 'c7' }, { t: rupees(Math.round(st.earned / st.sold)) }]);
        if (u.fee) lines.push([{ t: `Mess bill${u.bill ? '' : ' (from settings)'} `, c: 'c7' }, { t: rupees(Math.round(u.fee)) }]);
      }
      lines.push(null, [{ t: `Joined ${new Date(u.created * 1000).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })}`, c: 'c8' }]);
      return lines;
    });
    t.append(tr);
  }
  if (!rows.length) {
    const tr = el('tr'), td = el('td', 'dim', 'Nobody matches this filter.');
    td.colSpan = 7;
    tr.append(td);
    t.append(tr);
  }
}

function sparkline(u, cls) {
  const days = u.days.filter((d) => d.date >= D.recent);
  const w = 84, h = 20, xs = days.map((d) => d.sold);
  const mx = Math.max(2, ...xs);
  const s = S('svg', { width: w, height: h, viewBox: `0 0 ${w} ${h}` });
  const step = days.length > 1 ? w / (days.length - 1) : 0;
  S('path', {
    d: xs.map((v, i) => `${i ? 'L' : 'M'}${(i * step).toFixed(1)},${(h - 2 - (v / mx) * (h - 4)).toFixed(1)}`).join(''),
    fill: 'none', stroke: cls === 'bad' ? COL.waste : COL.sold, 'stroke-width': 1.5, 'stroke-linejoin': 'round',
  }, s);
  return s;
}

// ---------- onboarding ----------

function renderFunnel() {
  const all = D.users;
  const steps = [
    ['Signed up', () => true],
    ['Approved', (u) => u.status === 'active'],
    ['Mess key added', (u) => u.mess_key],
    ['WhatsApp linked', (u) => u.linked],
    ['First sell task', (u) => u.ever_task],
    ['First sale', (u) => u.ever_sold],
  ];
  const box = $('funnel');
  box.replaceChildren();
  let pool = all, prev = null;
  const top = all.length || 1;
  for (const [label, ok] of steps) {
    pool = pool.filter(ok);
    const n = pool.length;
    const r = el('div', 'u-fr'), track = el('div', 'track'), fill = el('i');
    fill.style.setProperty('width', `${(100 * n) / top}%`);
    track.append(fill);
    const num = el('span', 'n', String(n));
    if (prev != null && prev > n) num.append(el('small', '', `−${prev - n}`));
    r.append(el('span', 'lab', label), track, num);
    const names = pool.map((u) => u.name);
    tip(r, () => [[{ t: label, c: 'nm' }], [{ t: names.length ? clip(names.join(', '), 240) : 'Nobody yet', c: 'c7' }]]);
    box.append(r);
    prev = n;
  }
  const p = $('pending');
  p.hidden = !D.waiting;
  if (D.waiting) {
    p.replaceChildren(el('b', '', `${D.waiting} sign-up${D.waiting > 1 ? 's' : ''}`),
      ` waiting for approval${D.oldest_wait ? `, the oldest for ${ago(D.oldest_wait)}` : ''}. `);
    const a = el('a', '', 'Approve on the Account page');
    a.href = 'account.html';
    p.append(a, '.');
  }
}

// ---------- attention ----------

function renderAttention(users) {
  const order = { crit: 0, warn: 1, info: 2 };
  const items = users.flatMap((u) => attention(u, stats(u)));
  // Handoffs are common, so they share one line instead of one per user.
  const stale = users.filter(live).map((u) => [u.name, staleHandoffs(u)]).filter(([, n]) => n).sort((a, b) => b[1] - a[1]);
  if (stale.length) {
    const n = sum(stale.map(([, k]) => k));
    items.push({ lvl: 'warn', name: `${n} handoff${n > 1 ? 's' : ''}`, text: ` with no known outcome: ${clip(stale.map(([who, k]) => `${who} ${k}`).join(', '), 140)}.` });
  }
  items.sort((a, b) => order[a.lvl] - order[b.lvl]);
  const box = $('attention');
  box.replaceChildren();
  if (!items.length) {
    box.append(el('li', 'none', 'Nothing needs you right now.'));
    return;
  }
  for (const it of items.slice(0, 12)) {
    const li = el('li');
    const text = el('span');
    // Messages starting with ' or a space attach to the name directly.
    text.append(el('b', '', it.name), /^['\s]/.test(it.text) ? it.text : ' ' + it.text);
    li.append(el('span', 'ic ' + it.lvl, it.lvl === 'crit' ? '!' : it.lvl === 'warn' ? '?' : 'i'), text);
    box.append(li);
  }
}

// ---------- close mix ----------

function renderMix(users) {
  const sales = users.filter(live).flatMap((u) => stats(u).sales);
  const paid = sales.filter((s) => s.state === 'sold' || s.state === 'sending');
  const parts = [
    ['WhatsApp Pay', 'u-sold', paid.filter((s) => !s.paid_by)],
    ['Screenshot', 'u-img', paid.filter((s) => s.paid_by === 'image')],
    ['By hand', 'u-man', paid.filter((s) => s.paid_by === 'other' || s.paid_by === 'manual')],
    ['Handed off', 'u-hand', sales.filter((s) => s.state === 'handoff')],
  ];
  const tot = sum(parts.map((p) => p[2].length));
  $('mxMeta').textContent = `${tot} sale${tot === 1 ? '' : 's'}`;
  const box = $('mix');
  box.replaceChildren();
  for (const [name, cls, xs] of parts) {
    if (!xs.length) continue;
    const d = el('div', cls, xs.length / tot >= 0.12 ? pcs(xs.length, tot) : '');
    d.style.setProperty('flex', String(xs.length));
    tip(d, () => [[{ t: name, c: 'nm' }], [{ t: `${xs.length} sales · ${pcs(xs.length, tot)}`, c: 'c7' }]]);
    box.append(d);
  }
  const wait = (xs) => median(xs.filter((s) => s.wait > 0).map((s) => s.wait));
  const auto = wait(parts[0][2]), shot = wait(parts[1][2]);
  const note = $('mixNote');
  note.replaceChildren();
  if (auto == null && shot == null) { note.textContent = tot ? '' : 'No sales in this range.'; return; }
  note.append('Median time from offer to payment: ');
  if (auto != null) note.append(el('b', '', dur(auto)), ' with WhatsApp Pay');
  if (auto != null && shot != null) note.append(', ');
  if (shot != null) note.append(el('b', '', dur(shot)), ' by screenshot');
  note.append('.');
}

// ---------- price vs market ----------

function renderPrice(users) {
  const rows = users.filter(live).map((u) => ({ u, st: stats(u) })).filter((r) => r.st.vs != null).sort((a, b) => a.st.vs - b.st.vs);
  const host = $('price');
  if (!rows.length) {
    host.replaceChildren(el('p', 'u-note', 'No sales with a market price in this range.'));
    return;
  }
  const rh = 22, TOP = 16, B = 18, L = 76, R = 12;
  const H = TOP + rows.length * rh + B;
  const [s, W] = chart(host, H);
  const ext = niceMax(Math.max(10, ...rows.map((r) => Math.abs(r.st.vs))));
  const x = (v) => L + ((W - L - R) * (v + ext)) / (2 * ext);
  for (const v of [-ext, -ext / 2, 0, ext / 2, ext]) {
    S('line', { x1: x(v), x2: x(v), y1: TOP - 4, y2: H - B, stroke: v ? '#1d2330' : '#586076' }, s);
    T(s, x(v), H - 4, `${v > 0 ? '+' : ''}${v}`, { 'text-anchor': 'middle' });
  }
  T(s, x(-ext) + 2, 10, '← cheaper');
  T(s, x(ext) - 2, 10, 'pricier →', { 'text-anchor': 'end' });
  rows.forEach(({ u, st }, i) => {
    const cy = TOP + i * rh + rh / 2, v = st.vs;
    T(s, L - 8, cy + 3, clip(u.name, 11), { 'text-anchor': 'end', fill: '#8b94a7' });
    S('line', { x1: x(0), x2: x(v), y1: cy, y2: cy, stroke: '#586076', 'stroke-width': 2 }, s);
    S('circle', { cx: x(v), cy, r: 5, fill: v > 5 ? COL.waste : v < -5 ? COL.hand : '#8b94a7', stroke: '#141821', 'stroke-width': 2 }, s);
    const hit = S('rect', { x: 0, y: cy - rh / 2, width: W, height: rh, class: 'hit' }, s);
    tip(hit, () => [[{ t: u.name, c: 'nm' }], [{ t: 'vs median ask ', c: 'c7' }, { t: `${v > 0 ? '+' : '−'}${rupees(Math.abs(Math.round(v)))}` }],
      [{ t: `over ${st.priced} priced sale${st.priced === 1 ? '' : 's'}`, c: 'c8' }], [{ t: 'Sell-through ', c: 'c7' }, { t: pcs(st.sold, st.spare) }]]);
  });
}

// ---------- boot ----------

$('logo').append(svgEl(icon('check')));
$('navIcon').append(svgEl(icon('prev')));
$('salesIcon').append(svgEl(icon('coin')));
$('insIcon').append(svgEl(icon('chart')));
me().then(userLink).catch(() => {});
$('ovIcon').append(svgEl(icon('paper')));
$('dyIcon').append(svgEl(icon('chart')));
$('usIcon').append(svgEl(icon('coin')));
$('obIcon').append(svgEl(icon('next')));
$('atIcon').append(svgEl(icon('clock')));
$('mxIcon').append(svgEl(icon('check')));
$('pvIcon').append(svgEl(icon('ingot')));

load();
let resizeT = 0;
addEventListener('resize', () => { clearTimeout(resizeT); resizeT = setTimeout(render, 150); });
// The server rebuilds at most every two minutes.
setInterval(() => { if (document.visibilityState === 'visible') load(); }, 2 * 60 * 1000);
