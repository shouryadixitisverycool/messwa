'use strict';

// The insights page: one permanent row per ended listing (listing_facts),
// grouped by weekday slot, diet, hour and seller. Each weekday has its own
// menu, so the board treats "Bakul, Tuesday lunch" as one slot across weeks.
// Seller names are untrusted push names and only reach the page through
// textContent.

let D = null;   // last /api/insights
let R = [];     // its rows as objects
let pick = null; // the slot in the inspector: { mess, wd, meal }
let failed = false;

const PREFS = 'messwa.insights';
// season is a season's key, '' for whichever one today is in, or 'all'.
const F = { season: '', weeks: 4, mess: 'all', diet: 'all', metric: 'rate' };
try { Object.assign(F, JSON.parse(localStorage.getItem(PREFS) || '{}')); } catch { /* private mode */ }
// A link like insights.html?mess=bakul&metric=ask opens that view.
for (const [k, v] of new URLSearchParams(location.search)) if (k in F) F[k] = k === 'weeks' ? parseInt(v, 10) || 0 : v;
// A past season isn't remembered, so the next visit opens on today's.
const savePrefs = () => { try { localStorage.setItem(PREFS, JSON.stringify({ ...F, season: F.season === 'all' ? 'all' : '' })); } catch { /* ignore */ } };

const RANGES = [[4, '4 WEEKS'], [8, '8 WEEKS'], [12, '12 WEEKS'], [0, 'ALL']];
const TERM = '#7aa7ff', BREAK = 'var(--gold)'; // season kinds
const MAX_SEASONS = 6; // chips and comparison bars
const METRICS = [['rate', 'SOLD %'], ['listed', 'LISTED'], ['ask', 'ASK'], ['wanted', 'WANTED']];
const WD = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const WD_LONG = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
const BLUE = '#7aa7ff';
const GOAL_DAYS = 28; // four servings of every weekday slot
const MIN_SLOT = 6;   // fewer listings than this and a slot's rate is noise

// ---------- small helpers ----------

const tz = () => (D && D.tz) || 'Asia/Kolkata';
const dayOf = (date) => new Date(date + 'T12:00:00Z');
const fmtD = (date, o) => new Intl.DateTimeFormat('en-GB', { ...o, timeZone: 'UTC' }).format(dayOf(date));
const fmtShort = (date) => fmtD(date, { day: 'numeric', month: 'short' });
const fmtDay = (date) => fmtD(date, { weekday: 'short', day: 'numeric', month: 'short' });
const wdOf = (date) => (dayOf(date).getUTCDay() + 6) % 7; // Monday is 0
const daysBetween = (a, b) => Math.round((dayOf(b) - dayOf(a)) / 86400000);
const sum = (xs) => xs.reduce((a, b) => a + b, 0);
const pc = (a, b) => (b ? Math.round((100 * a) / b) : null);
const pcs = (a, b) => (b ? `${pc(a, b)}%` : '-');
function median(xs) {
  if (!xs.length) return null;
  const v = xs.slice().sort((a, b) => a - b), h = v.length >> 1;
  return v.length % 2 ? v[h] : (v[h - 1] + v[h]) / 2;
}
const fmtPhone = (p) => (/^\+91\d{10}$/.test(p) ? `+91 ${p.slice(3, 8)} ${p.slice(8)}` : p);
function dur(sec) {
  const m = Math.round(Math.max(0, sec) / 60);
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)}h ${m % 60}m`;
}
const messName = (m) => (m ? cap(m) : 'No mess');
const slotName = (mess, wd, meal) => `${messName(mess)} · ${WD_LONG[wd]} ${meal}`;
const vegOnly = (m) => !!(D.messes.find((x) => x.name === m) || {}).veg_only;
const mealNames = () => D.windows.map((w) => w.meal);

const isOffer = (r) => r.kind !== 'buy' && r.mess && !r.deleted;
const isWanted = (r) => r.kind === 'buy' && !r.deleted;
const inMess = (r) => F.mess === 'all' || r.mess === F.mess;
const inDiet = (r) => F.diet === 'all' || r.diet === F.diet;
const view = () => R.filter((r) => inMess(r) && inDiet(r));

// ---------- svg ----------

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
// Bars get a 4px rounded data end and a square baseline.
function barV(x, y, w, h, r = 4) {
  r = Math.min(r, h, w / 2);
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}
function barH(x, y, w, h, r = 4) {
  r = Math.min(r, w, h / 2);
  return `M${x},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h - r}Q${x + w},${y + h} ${x + w - r},${y + h}H${x}Z`;
}
function niceMax(v) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v)), n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}
// yAxis draws gridlines at round steps. max comes from niceMax, so its
// leading digit picks a step that divides it evenly.
function yAxis(s, x0, x1, y, max, fmt = (v) => String(v)) {
  const lead = Math.round(max / 10 ** Math.floor(Math.log10(max)));
  const ticks = lead === 2 ? 4 : 5;
  for (let i = 0; i <= ticks; i++) {
    const v = (max * i) / ticks, yy = y(v);
    S('line', { x1: x0, x2: x1, y1: yy, y2: yy, stroke: i ? '#1d2330' : '#2b3346' }, s);
    T(s, x0 - 6, yy + 3, fmt(Math.round(v * 10) / 10), { 'text-anchor': 'end' });
  }
}

// ---------- data ----------

async function load() {
  try {
    const q = new URLSearchParams({ weeks: F.weeks, season: F.season });
    const r = await fetch(`api/insights?${q}`, { cache: 'no-store' });
    if (r.status === 401) return toLogin();
    if (!r.ok) throw new Error(await r.text());
    D = await r.json();
    parse();
    failed = false;
    // An unknown season key falls back to today's on the server.
    if (F.season && F.season !== 'all' && F.season !== D.season) F.season = '';
    renderFilters();
  } catch (e) {
    failed = true;
    if (!D) {
      $('notice').hidden = false;
      $('notice').textContent = `Couldn't load insights: ${e.message}`;
      return;
    }
  }
  render();
}

function parse() {
  const c = Object.fromEntries(D.cols.map((k, i) => [k, i]));
  const hourFmt = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', hourCycle: 'h23', timeZone: tz() });
  const dateFmt = new Intl.DateTimeFormat('en-CA', { timeZone: tz() });
  R = D.rows.map((x) => {
    const r = {};
    for (const k of D.cols) r[k] = x[c[k]];
    r.wd = wdOf(r.date);
    r.hour = parseInt(hourFmt.format(r.first * 1000), 10);
    r.day = dateFmt.format(r.first * 1000);
    r.soldHour = r.sold ? parseInt(hourFmt.format(r.sold * 1000), 10) : -1;
    return r;
  });
}

// ---------- seasons ----------

const seasonYear = (s) => s.from.slice(0, 4);
const seasonLabel = (s) => (s ? `${s.name} ${seasonYear(s)}` : 'All seasons');
const seasonChip = (s) => `${s.name.replace(/ term$/i, '')} '${seasonYear(s).slice(2)}`.toUpperCase();
const seasonByKey = (k) => D.seasons.find((s) => s.key === k);
const seasonColor = (s) => (s.term ? TERM : BREAK);
const seasonKind = (s) => (s.term ? 'Term: everyone is registered with a mess' : 'Break: only people who registered themselves');
// shownSeasons are the latest seasons with data, plus today's.
const shownSeasons = () => D.seasons.filter((s) => s.days || s.key === D.current).slice(-MAX_SEASONS);
const seasonTip = (s) => () => [
  [{ t: seasonLabel(s), c: 'nm', color: seasonColor(s) }],
  [{ t: `${fmtDay(s.from)} to ${fmtDay(s.to)}`, c: 'c7' }],
  [{ t: seasonKind(s), c: 'c8' }],
  null,
  [{ t: s.days ? `${s.days} day${s.days === 1 ? '' : 's'} of data` : 'No data yet', c: 'c6' }],
];

// servings is how many of each weekday the data covers, Monday first.
function servings() {
  const n = [0, 0, 0, 0, 0, 0, 0];
  for (const d of new Set(R.map((r) => r.date))) if (d <= D.today) n[wdOf(d)]++;
  return n;
}

// slot gathers one weekday slot's numbers from rows.
function slot(rows, mess, wd, meal, nServ) {
  const here = rows.filter((r) => r.mess === mess && r.wd === wd && r.meal === meal);
  const off = here.filter(isOffer);
  const sold = off.filter((r) => r.sold).length;
  const asks = off.filter((r) => r.kind === 'sell' && r.price > 0).map((r) => r.price);
  const wanted = here.filter(isWanted).length;
  const serv = Math.max(1, nServ);
  return { mess, wd, meal, here, off, n: off.length, sold, rate: off.length ? sold / off.length : null, ask: median(asks), asks: asks.length, wanted, listedPer: off.length / serv, wantedPer: wanted / serv, serv: nServ };
}

// ---------- filters ----------

function renderFilters() {
  const group = (box, items, on, set) => {
    box.replaceChildren();
    for (const [v, label, iconMarkup, tipFn] of items) {
      const b = el('button', 'win' + (on(v) ? ' on' : ''));
      b.type = 'button';
      if (tipFn) tip(b, tipFn);
      if (iconMarkup) {
        const i = svgEl(iconMarkup);
        i.setAttribute('width', 11); i.setAttribute('height', 11);
        i.style.setProperty('display', 'inline-block');
        i.style.setProperty('vertical-align', '-2px');
        i.style.setProperty('margin-right', '5px');
        b.append(i);
      }
      b.append(label);
      b.addEventListener('click', () => set(v));
      box.append(b);
    }
  };
  const sel = F.season || D.current;
  const swatch = (s) => pix(['xxx', 'xxx', 'xxx'], { x: s.term ? TERM : '#ffc53d' });
  group($('fSeason'), [...shownSeasons().map((s) => [s.key, seasonChip(s), swatch(s), seasonTip(s)]),
    ['all', 'ALL SEASONS', null, () => [[{ t: 'All seasons', c: 'nm' }], [{ t: 'Terms and breaks together. Weekday patterns blend two different markets.', c: 'c7' }]]]],
  (v) => v === sel, (v) => { F.season = v === D.current ? '' : v; pick = null; savePrefs(); load(); renderFilters(); });
  group($('fRange'), RANGES, (v) => F.weeks === v, (v) => { F.weeks = v; savePrefs(); load(); renderFilters(); });
  group($('fMess'), [['all', 'ALL MESSES'], ...D.messes.map((m) => [m.name, m.name.toUpperCase(), messIcon(m.name)])], (v) => F.mess === v,
    (v) => { F.mess = v; if (pick && v !== 'all' && pick.mess !== v) pick = null; savePrefs(); renderFilters(); render(); });
  group($('fDiet'), [['all', 'ANY DIET'], ['veg', 'VEG', dietMark('veg')], ['nonveg', 'NON-VEG', dietMark('nonveg')], ['egg', 'EGG', dietMark('egg')]], (v) => F.diet === v,
    (v) => { F.diet = v; savePrefs(); renderFilters(); render(); });
  group($('fMetric'), METRICS, (v) => F.metric === v, (v) => { F.metric = v; savePrefs(); renderFilters(); renderBoards(); });
}

// ---------- render ----------

function render() {
  if (!D) return;
  const empty = !R.some(isOffer);
  $('notice').hidden = !empty && !failed;
  if (failed) $('notice').textContent = 'Lost the connection to messwa; showing the last data.';
  else if (empty && D.first) $('notice').textContent = `No ended listings from ${seasonLabel(seasonByKey(D.season))} in this range yet. Pick another season above.`;
  else if (empty) $('notice').textContent = 'No ended listings stored yet. messwa run writes one row per listing every hour, once its meal is over.';
  renderOverview();
  renderSeasons();
  renderBoards();
  renderDiet();
  renderHours();
  renderPlaybook();
  renderSellers();
}

function renderOverview() {
  // A past season's range runs to its end, so count only the dates with data.
  const dates = [...new Set(R.map((r) => r.date))].sort();
  const days = dates.length, since = dates[0], until = dates[dates.length - 1];
  $('collectV').textContent = days >= GOAL_DAYS ? `${Math.floor(days / 7)} weeks` : `${days} of ${GOAL_DAYS} days`;
  $('collectBar').style.setProperty('--days', GOAL_DAYS);
  $('collectFill').style.setProperty('width', `${Math.min(100, (days / GOAL_DAYS) * 100)}%`);
  const sv = servings(), lo = Math.min(...sv);
  $('collectRow').replaceChildren();
  const span = (html) => { const s = el('span'); s.append(...html); return s; };
  $('collectRow').append(
    span([el('b', '', days ? `Since ${fmtDay(since)}` : 'Nothing yet'), document.createTextNode(` · every weekday slot seen ${lo === Math.max(...sv) ? `${lo}×` : `${lo}–${Math.max(...sv)}×`} in this range`)]),
    span([document.createTextNode(days >= GOAL_DAYS ? 'Weekly patterns are on solid ground' : `Weekly patterns firm up after ${GOAL_DAYS - days} more days`)]),
  );
  const kinds = new Set(D.seasons.filter((s) => s.days && s.from <= D.to && s.to >= D.from).map((s) => s.term));
  if (D.season === 'all' && kinds.size > 1) {
    $('collectRow').append(span([el('b', 'warn', 'Terms and breaks mixed'), document.createTextNode(' · weekday patterns blend two different markets')]));
  }
  const where = seasonLabel(seasonByKey(D.season));
  $('ovMeta').textContent = days ? `${where} · ${fmtShort(since)} to ${fmtShort(until)}, ended meals only` : where;

  const rows = view(), off = rows.filter(isOffer), sold = off.filter((r) => r.sold);
  const waits = sold.filter((r) => r.sold >= r.first).map((r) => r.sold - r.first);
  const asks = off.filter((r) => r.kind === 'sell' && r.price > 0).map((r) => r.price);
  const sells = off.filter((r) => r.kind === 'sell');
  const want = rows.filter(isWanted);
  const nDays = Math.max(1, new Set(rows.map((r) => r.date)).size);
  const sellers = new Set(off.map((r) => r.seller)), buyers = new Set(want.map((r) => r.seller));
  const box = $('kpis');
  box.replaceChildren();
  const kpi = (v, label, sub, tipFn) => {
    const k = el('div', 'kpi');
    k.append(el('b', v == null ? 'none' : '', v == null ? '-' : v), el('span', '', label), el('em', '', sub));
    if (tipFn) tip(k, tipFn);
    box.append(k);
  };
  const med = median(waits);
  kpi(off.length.toLocaleString('en-IN'), 'LISTINGS', `${Math.round(off.length / nDays)} a day`);
  kpi(pcs(sold.length, off.length), 'SOLD', `${sold.length.toLocaleString('en-IN')} ticked`, () => [
    [{ t: 'Sell-through', c: 'nm' }],
    [{ t: 'A listing counts as sold when its seller ticks it or says "sold". Deleted listings are left out.', c: 'c7' }],
    null,
    [{ t: 'Sellers who never tick make this read low. See the flags under Sellers.', c: 'c8' }],
  ]);
  kpi(med == null ? null : dur(med), 'TO SELL', `${pcs(waits.filter((w) => w <= 1800).length, waits.length)} in 30 min`);
  const ma = median(asks);
  kpi(ma == null ? null : rupees(ma), 'MEDIAN ASK', `${pcs(asks.length, sells.length)} state a price`);
  kpi(want.length.toLocaleString('en-IN'), 'WANTED', `${pcs(want.filter((r) => r.sold).length, want.length)} found a seller`);
  kpi(sellers.size.toLocaleString('en-IN'), 'SELLERS', `${buyers.size.toLocaleString('en-IN')} asked to buy`);
}

// ---------- terms vs breaks ----------

// renderSeasons compares whole seasons side by side from the totals, which
// follow the mess and diet filters but not the range.
function renderSeasons() {
  const c = Object.fromEntries(D.total_cols.map((k, i) => [k, i]));
  const shown = D.seasons.map((s, i) => ({ ...s, i })).filter((s) => s.days).slice(-MAX_SEASONS);
  const agg = shown.map((s) => ({ s, offers: 0, sold: 0, wanted: 0, askSum: 0, asks: 0 }));
  const at = new Map(shown.map((s, j) => [s.i, agg[j]]));
  for (const t of D.totals) {
    const a = at.get(t[c.season]);
    if (!a || (F.mess !== 'all' && t[c.mess] !== F.mess) || (F.diet !== 'all' && t[c.diet] !== F.diet)) continue;
    a.offers += t[c.offers]; a.sold += t[c.sold]; a.wanted += t[c.wanted]; a.askSum += t[c.ask_sum]; a.asks += t[c.asks];
  }
  const count = (v) => (v >= 10 ? String(Math.round(v)) : v.toFixed(1));
  const metrics = [
    ['Listings a day', (a) => a.offers / a.s.days, count, 0],
    ['Sold', (a) => (a.offers ? a.sold / a.offers : null), (v) => `${Math.round(v * 100)}%`, 1],
    ['Average ask', (a) => (a.asks ? a.askSum / a.asks : null), (v) => `₹${Math.round(v)}`, 0],
    ['Wanted a day', (a) => a.wanted / a.s.days, count, 0],
  ];
  const host = $('seasons');
  host.replaceChildren();
  const note = $('snNote');
  const kinds = new Set(shown.map((s) => s.term));
  const cur = seasonByKey(D.current);
  note.hidden = kinds.size !== 1 || !cur;
  if (!note.hidden) {
    const other = cur.term ? 'break' : 'term';
    note.textContent = `Only ${cur.term ? 'terms' : 'breaks'} so far. ${seasonLabel(D.next)} starts ${fmtDay(D.next.from)}, and the first ${other} shows up next to this one from then.`;
  }
  if (!shown.length) return;

  for (const [name, fn, fmt, isRate] of metrics) {
    const box = el('div', 'mini');
    const ch = el('div');
    box.append(el('h4', '', name), ch);
    host.append(box);
    const h = 138, b = 34, top = 16;
    const [s, w] = chart(ch, h, 140);
    const vals = agg.map(fn);
    const max = isRate ? 1 : niceMax(Math.max(...vals.filter((v) => v != null), 0.001)), y = (v) => h - b - ((h - b - top) * v) / max;
    S('line', { x1: 0, x2: w, y1: h - b, y2: h - b, stroke: '#2b3346' }, s);
    const bw = w / agg.length, cw = Math.min(30, bw - 10);
    agg.forEach((a, i) => {
      const v = vals[i], x0 = i * bw, cx = x0 + (bw - cw) / 2, on = D.season === 'all' || a.s.key === D.season;
      const g = S('g', { class: 'h' }, s);
      S('rect', { class: 'hit', x: x0, y: 0, width: bw, height: h }, g);
      if (v != null && v > 0) S('path', { class: 'mk', d: barV(cx, y(v), cw, h - b - y(v)), fill: seasonColor(a.s), opacity: on ? 1 : 0.45 }, g);
      T(g, x0 + bw / 2, (v == null ? h - b : y(v)) - 5, v == null ? '-' : fmt(v), { 'text-anchor': 'middle', class: 'v' });
      const word = a.s.name.split(' ')[0];
      T(g, x0 + bw / 2, h - b + 14, bw > 64 ? word : word.slice(0, 3), { 'text-anchor': 'middle', style: on ? 'fill:var(--text)' : '' });
      T(g, x0 + bw / 2, h - b + 27, `'${seasonYear(a.s).slice(2)}`, { 'text-anchor': 'middle' });
      tip(g, () => [
        [{ t: seasonLabel(a.s), c: 'nm', color: seasonColor(a.s) }],
        [{ t: `${a.s.days} day${a.s.days === 1 ? '' : 's'} of data, ${fmtShort(a.s.from)} to ${fmtShort(a.s.to)}`, c: 'c8' }],
        null,
        [{ t: 'Listed ', c: 'c7' }, { t: `${a.offers.toLocaleString('en-IN')} (${count(a.offers / a.s.days)} a day)`, c: 'cf' }],
        [{ t: 'Sold ', c: 'c7' }, { t: `${a.sold.toLocaleString('en-IN')} (${pcs(a.sold, a.offers)})`, c: 'cf' }],
        [{ t: 'Average ask ', c: 'c7' }, { t: a.asks ? `₹${Math.round(a.askSum / a.asks)} from ${a.asks.toLocaleString('en-IN')}` : 'no prices', c: a.asks ? 'c6' : 'c8' }],
        [{ t: 'Wanted ', c: 'c7' }, { t: `${a.wanted.toLocaleString('en-IN')} (${count(a.wanted / a.s.days)} a day)`, c: 'cf' }],
      ]);
    });
  }
}

// ---------- weekly menu board ----------

function cellValue(c) {
  switch (F.metric) {
    case 'rate': return c.n >= 3 ? c.rate : null;
    case 'listed': return c.n ? c.listedPer : null;
    case 'ask': return c.asks ? c.ask : null;
    default: return c.wanted ? c.wantedPer : null;
  }
}
function cellLabel(c, v) {
  if (v == null) return '-';
  switch (F.metric) {
    case 'rate': return `${Math.round(v * 100)}%`;
    case 'ask': return `₹${Math.round(v)}`;
    default: return v >= 10 ? String(Math.round(v)) : v.toFixed(1);
  }
}

function renderBoards() {
  const rows = R.filter(inDiet), sv = servings(), meals = mealNames();
  const messes = D.messes.map((m) => m.name).filter((m) => F.mess === 'all' || m === F.mess);
  const cells = [];
  for (const m of messes) for (let wd = 0; wd < 7; wd++) for (const ml of meals) cells.push(slot(rows, m, wd, ml, sv[wd]));
  const vals = cells.map(cellValue).filter((v) => v != null);
  const lo = F.metric === 'ask' ? Math.min(...vals) : 0, hi = F.metric === 'rate' ? 1 : Math.max(...vals, 0.001);
  const shade = (v) => (v == null ? 0 : (v - lo) / Math.max(hi - lo, 0.001));
  const todayWd = wdOf(D.today);
  const box = $('boards');
  box.className = 'boards' + (messes.length === 1 ? ' one' : '');
  box.replaceChildren();
  if (!pick || !messes.includes(pick.mess)) pick = best(cells, 1)[0] || null;

  for (const m of messes) {
    const card = el('div', 'board');
    card.style.setProperty('--m', messColor(m));
    const head = el('div', 'board-head');
    head.append(svgEl(messIcon(m)));
    const nm = el('b', '', m.toUpperCase());
    nm.style.setProperty('color', messColor(m));
    const mine = cells.filter((c) => c.mess === m);
    const n = sum(mine.map((c) => c.n)), s = sum(mine.map((c) => c.sold));
    head.append(nm, el('span', '', `${n.toLocaleString('en-IN')} listed · ${pcs(s, n)} sold`));
    const g = el('div', 'wgrid');
    g.append(el('div'));
    WD.forEach((d, i) => {
      const h = el('div', 'dh' + (i === todayWd ? ' today' : ''), d.toUpperCase());
      const pips = el('span', sv[i] ? '' : 'none', sv[i] ? `×${sv[i]}` : '×0');
      h.append(pips);
      tip(h, () => [[{ t: WD_LONG[i], c: 'nm' }], [{ t: `${sv[i]} serving${sv[i] === 1 ? '' : 's'} of each meal in this range`, c: 'c7' }]]);
      g.append(h);
    });
    for (const ml of meals) {
      const mh = el('div', 'mh');
      mh.append(svgEl(icon(ml)));
      tip(mh, () => [[{ t: cap(ml), c: 'nm' }]]);
      g.append(mh);
      for (let wd = 0; wd < 7; wd++) {
        const c = mine.find((x) => x.wd === wd && x.meal === ml);
        const v = cellValue(c);
        const b = el('button', 'wc');
        b.type = 'button';
        if (!c.n && !c.wanted) {
          b.classList.add('none');
          g.append(b);
          continue;
        }
        if (v == null) b.classList.add('thin');
        const a = v == null ? 6 : Math.round(12 + shade(v) * 76);
        b.style.setProperty('background', `color-mix(in srgb, ${messColor(m)} ${a}%, var(--slot))`);
        if (a > 58) b.classList.add('hi');
        if (F.metric === 'rate' && c.n < MIN_SLOT && v != null) b.style.setProperty('opacity', '.6');
        if (pick && pick.mess === m && pick.wd === wd && pick.meal === ml) b.classList.add('on');
        b.append(el('b', '', cellLabel(c, v)));
        tip(b, () => slotLines(c, rows));
        b.addEventListener('click', () => { pick = { mess: m, wd, meal: ml }; renderBoards(); });
        g.append(b);
      }
    }
    card.append(head, g);
    box.append(card);
  }
  const key = el('div', 'wkey');
  const ramp = el('span', 'ramp');
  for (const a of [12, 31, 50, 69, 88]) {
    const i = el('i');
    i.style.setProperty('background', `color-mix(in srgb, ${messes.length === 1 ? messColor(messes[0]) : 'var(--text)'} ${a}%, var(--slot))`);
    ramp.append(i);
  }
  const what = { rate: 'share of listings that sold', listed: 'listings per serving', ask: 'median asking price', wanted: 'buy requests per serving' }[F.metric];
  key.append(el('span', '', F.metric === 'ask' ? `₹${Math.round(lo) || 0}` : '0'), ramp,
    el('span', '', F.metric === 'rate' ? '100%' : cellLabel({}, hi)), el('span', '', `· ${what}. ×N under a day is how many of it this range covers.${F.metric === 'rate' ? ` Faded slots have fewer than ${MIN_SLOT} listings.` : ''}`));
  box.append(key);
  renderRanks(cells);
  renderSlot(rows, sv);
}

function slotLines(c, rows) {
  const lines = [
    [{ t: slotName(c.mess, c.wd, c.meal), c: 'nm', color: messColor(c.mess) }],
    [{ t: `${c.serv} serving${c.serv === 1 ? '' : 's'} in range`, c: 'c8' }],
    null,
    [{ t: 'Sold ', c: 'c7' }, { t: `${c.sold} of ${c.n}`, c: 'cf' }, { t: c.n ? ` (${pcs(c.sold, c.n)})` : '', c: 'c6' }],
    [{ t: 'Listed per serving ', c: 'c7' }, { t: c.listedPer.toFixed(1), c: 'cf' }],
    [{ t: 'Median ask ', c: 'c7' }, { t: c.ask == null ? 'no prices' : `${rupees(c.ask)} from ${c.asks}`, c: c.ask == null ? 'c8' : 'c6' }],
    [{ t: 'Wanted per serving ', c: 'c7' }, { t: c.wantedPer.toFixed(1), c: 'cf' }],
  ];
  if (!vegOnly(c.mess) && F.diet === 'all') {
    const parts = [];
    for (const d of ['veg', 'nonveg', 'egg']) {
      const o = c.off.filter((r) => r.diet === d);
      if (o.length) parts.push({ t: `${DIET_LABEL[d]} `, c: DIET_COLOR[d] }, { t: `${pcs(o.filter((r) => r.sold).length, o.length)} of ${o.length}  `, c: 'c7' });
    }
    if (parts.length) lines.push(null, parts);
  }
  lines.push({ rar: 'CLICK FOR EVERY WEEK', c: 'c8' });
  return lines;
}

// best ranks slots by sell-through, highest first, skipping thin ones.
function best(cells, dir) {
  const ok = cells.filter((c) => c.n >= MIN_SLOT);
  return ok.sort((a, b) => dir * (b.rate - a.rate) || b.n - a.n);
}

function renderRanks(cells) {
  const fill = (box, list, lo) => {
    box.replaceChildren();
    if (!list.length) { box.append(el('div', 'none', `No slot has ${MIN_SLOT}+ listings yet`)); return; }
    for (const c of list.slice(0, 3)) {
      const b = el('button', 'rank');
      b.type = 'button';
      b.style.setProperty('--m', messColor(c.mess));
      b.append(svgEl(messIcon(c.mess)), el('span', 'w', `${cap(c.mess)} · ${WD[c.wd]} ${c.meal}`), el('span', 'v' + (lo ? ' lo' : ''), `${Math.round(c.rate * 100)}%`));
      tip(b, () => [[{ t: slotName(c.mess, c.wd, c.meal), c: 'nm', color: messColor(c.mess) }], [{ t: `${c.sold} of ${c.n} sold`, c: 'c7' }]]);
      b.addEventListener('click', () => { pick = { mess: c.mess, wd: c.wd, meal: c.meal }; renderBoards(); });
      box.append(b);
    }
  };
  fill($('best'), best(cells.slice(), 1), false);
  fill($('worst'), best(cells.slice(), -1), true);
}

// renderSlot shows one slot across every week in range: how much was
// listed and sold each time, so a menu that sells shows up week after week.
function renderSlot(rows, sv) {
  const box = $('slot');
  box.replaceChildren();
  if (!pick) { $('slotK').textContent = 'Click a slot'; box.append(el('div', 'ln c8', 'Click any slot on the board to see it week by week.')); return; }
  const c = slot(rows, pick.mess, pick.wd, pick.meal, sv[pick.wd]);
  $('slotK').textContent = 'Every week of this slot';
  fillLines(box, slotLines(c, rows).filter((l) => !(l && l.rar)));
  const dates = [...new Set(R.map((r) => r.date))].filter((d) => wdOf(d) === pick.wd && d <= D.today).sort();
  const hist = el('div', 'hist');
  box.append(hist);
  if (!dates.length) return;
  const per = dates.map((d) => {
    const o = c.off.filter((r) => r.date === d);
    return { d, n: o.length, s: o.filter((r) => r.sold).length, w: c.here.filter((r) => r.date === d && isWanted(r)).length, ask: median(o.filter((r) => r.kind === 'sell' && r.price > 0).map((r) => r.price)) };
  });
  const h = 120, b = 18, lp = 30;
  const [s, w] = chart(hist, h);
  const max = niceMax(Math.max(...per.map((p) => p.n), 1)), y = (v) => h - b - ((h - b - 12) * v) / max;
  yAxis(s, lp, w, y, max);
  const bw = Math.min(64, (w - lp) / per.length);
  const col = messColor(pick.mess);
  per.forEach((p, i) => {
    const x0 = lp + i * bw, cw = Math.min(24, bw - 8), cx = x0 + (bw - cw) / 2;
    const g = S('g', { class: 'h' }, s);
    S('rect', { class: 'hit', x: x0, y: 0, width: bw, height: h }, g);
    if (p.n) S('path', { class: 'mk', d: barV(cx, y(p.n), cw, h - b - y(p.n)), fill: col, opacity: 0.3 }, g);
    if (p.s) S('path', { class: 'mk', d: barV(cx + 4, y(p.s), cw - 8, h - b - y(p.s), 3), fill: col }, g);
    if (p.n) T(g, cx + cw / 2, y(p.n) - 4, pcs(p.s, p.n), { 'text-anchor': 'middle', class: 'v' });
    T(g, x0 + bw / 2, h - 4, fmtShort(p.d), { 'text-anchor': 'middle' });
    tip(g, () => [
      [{ t: `${cap(pick.meal)}, ${fmtDay(p.d)}`, c: 'nm', color: col }],
      [{ t: 'Sold ', c: 'c7' }, { t: `${p.s} of ${p.n}`, c: 'cf' }],
      [{ t: 'Median ask ', c: 'c7' }, { t: p.ask == null ? 'no prices' : rupees(p.ask), c: p.ask == null ? 'c8' : 'c6' }],
      [{ t: 'Wanted ', c: 'c7' }, { t: String(p.w), c: 'cf' }],
    ]);
  });
}

// ---------- veg vs non-veg ----------

function renderDiet() {
  const diets = [['veg', 'var(--veg)'], ['nonveg', 'var(--nonveg)'], ['egg', 'var(--egg)']];
  const leg = $('dietLeg');
  leg.replaceChildren();
  for (const [d, col] of diets) {
    const s = el('span'), i = el('i', 'dv');
    i.style.setProperty('background', col);
    s.append(i, DIET_LABEL[d]);
    leg.append(s);
  }
  const messes = D.messes.filter((m) => !m.veg_only && (F.mess === 'all' || m.name === F.mess)).map((m) => m.name);
  const lines = [];
  for (const m of messes) for (const ml of mealNames()) {
    const off = R.filter((r) => r.mess === m && r.meal === ml && isOffer(r));
    const pts = diets.map(([d, col]) => {
      const o = off.filter((r) => r.diet === d);
      return { d, col, n: o.length, s: o.filter((r) => r.sold).length };
    }).filter((p) => p.n >= 5);
    if (pts.length) lines.push({ m, ml, pts });
  }
  const host = $('diet');
  if (!lines.length) { host.replaceChildren(el('div', 'empty-note', F.mess !== 'all' ? `${cap(F.mess)} serves only veg.` : 'Not enough non-veg listings yet.')); return; }
  // Phones drop the right-hand labels; the dots' tooltips have the numbers.
  const narrow = host.clientWidth < 560;
  const rh = 30, top = 6, lw = narrow ? 118 : 150, rw = narrow ? 14 : 190;
  const [s, w] = chart(host, lines.length * rh + top + 22);
  const x = (v) => lw + ((w - lw - rw) * v) / 100;
  const bottom = top + lines.length * rh;
  for (const t of [0, 25, 50, 75, 100]) {
    S('line', { x1: x(t), x2: x(t), y1: top, y2: bottom, stroke: t ? '#1d2330' : '#2b3346' }, s);
    T(s, x(t), bottom + 14, `${t}%`, { 'text-anchor': 'middle' });
  }
  lines.forEach((ln, i) => {
    const y = top + i * rh + rh / 2;
    T(s, 0, y + 4, `${cap(ln.m)} ${ln.ml}`, { class: 'v' });
    const ps = ln.pts.map((p) => pc(p.s, p.n));
    S('line', { x1: x(Math.min(...ps)), x2: x(Math.max(...ps)), y1: y, y2: y, stroke: '#3a4356', 'stroke-width': 2 }, s);
    ln.pts.forEach((p) => {
      const g = S('g', {}, s);
      S('circle', { class: 'hit', cx: x(pc(p.s, p.n)), cy: y, r: 12 }, g);
      S('circle', { cx: x(pc(p.s, p.n)), cy: y, r: 5.5, fill: p.col, stroke: '#141821', 'stroke-width': 2 }, g);
      tip(g, () => [[{ t: `${cap(ln.m)} ${ln.ml}, ${DIET_LABEL[p.d].toLowerCase()}`, c: 'nm', color: messColor(ln.m) }], [{ t: `${p.s} of ${p.n} sold (${pcs(p.s, p.n)})`, c: 'c7' }]]);
    });
    if (!narrow) T(s, w, y + 4, ln.pts.map((p) => `${DIET_LABEL[p.d].toLowerCase()} ${pcs(p.s, p.n)}`).join(' · '), { 'text-anchor': 'end' });
  });
}

// ---------- rush hours ----------

function renderHours() {
  const rows = view();
  const nDays = Math.max(1, new Set(rows.map((r) => r.day)).size);
  const listed = Array(24).fill(0), sold = Array(24).fill(0), wanted = Array(24).fill(0);
  for (const r of rows) {
    if (isOffer(r)) { listed[r.hour]++; if (r.sold) sold[r.soldHour]++; }
    if (isWanted(r)) wanted[r.hour]++;
  }
  const per = (a) => a.map((v) => v / nDays);
  const L = per(listed), Sd = per(sold), W = per(wanted);
  const h = 190, b = 20, lp = 32, top = 18;
  const [s, w] = chart($('hours'), h);
  const max = niceMax(Math.max(...L, ...W, 1)), y = (v) => h - b - ((h - b - top) * v) / max;
  const bw = (w - lp) / 24;
  for (const win of D.windows) {
    const [a, z] = [win.starts, win.ends].map((t) => { const [hh, mm] = t.split(':').map(Number); return hh + mm / 60; });
    S('rect', { x: lp + a * bw, y: top - 4, width: (z - a) * bw, height: h - b - top + 4, fill: 'rgba(255,255,255,.035)', rx: 4 }, s);
    const ic = svgEl(icon(win.meal));
    ic.setAttribute('x', lp + ((a + z) / 2) * bw - 6); ic.setAttribute('y', 0); ic.setAttribute('width', 12); ic.setAttribute('height', 12);
    s.append(ic);
  }
  yAxis(s, lp, w, y, max);
  for (let i = 0; i < 24; i++) {
    const x0 = lp + i * bw, cw = Math.min(22, bw - 3), cx = x0 + (bw - cw) / 2, iw = cw / 2 - 1;
    const g = S('g', { class: 'h' }, s);
    S('rect', { class: 'hit', x: x0, y: 0, width: bw, height: h }, g);
    if (L[i] > 0.05) S('path', { class: 'mk', d: barV(cx, y(L[i]), cw, h - b - y(L[i])), fill: 'color-mix(in srgb, var(--gold) 28%, transparent)' }, g);
    if (Sd[i] > 0.05) S('path', { class: 'mk', d: barV(cx, y(Sd[i]), iw, h - b - y(Sd[i]), 3), fill: 'var(--gold)' }, g);
    if (W[i] > 0.05) S('path', { class: 'mk', d: barV(cx + iw + 2, y(W[i]), iw, h - b - y(W[i]), 3), fill: BLUE }, g);
    if (i % 3 === 0) T(s, x0 + bw / 2, h - 5, String(i).padStart(2, '0'), { 'text-anchor': 'middle' });
    const hh = String(i).padStart(2, '0');
    tip(g, () => [
      [{ t: `${hh}:00 to ${hh}:59`, c: 'nm' }],
      [{ t: 'Listed ', c: 'c7' }, { t: `${L[i].toFixed(1)} a day`, c: 'cf' }],
      [{ t: 'Sold ', c: 'c7' }, { t: `${Sd[i].toFixed(1)} a day`, c: 'c6' }],
      [{ t: 'Wanted ', c: 'c7' }, { t: `${W[i].toFixed(1)} a day`, c: 'cf' }],
      null,
      [{ t: `Averaged over ${nDays} day${nDays === 1 ? '' : 's'}`, c: 'c8' }],
    ]);
  }
  const peak = L.indexOf(Math.max(...L));
  if (L[peak] > 0) T(s, lp + peak * bw + bw / 2, y(L[peak]) - 5, String(Math.round(L[peak])), { 'text-anchor': 'middle', class: 'v' });
}

// ---------- seller's playbook ----------

function renderPlaybook() {
  const off = view().filter(isOffer);
  const overall = off.length ? off.filter((r) => r.sold).length / off.length : 0;

  // Time to sale.
  const bins = [[5, '<5m'], [15, '5-15'], [30, '15-30'], [60, '30-60'], [120, '1-2h'], [240, '2-4h'], [480, '4-8h'], [1e9, '8h+']];
  const counts = bins.map(() => 0);
  for (const r of off) {
    if (!r.sold || r.sold < r.first) continue;
    const m = (r.sold - r.first) / 60;
    counts[bins.findIndex(([e]) => m < e)]++;
  }
  const tot = sum(counts);
  {
    const h = 170, b = 18, lp = 32;
    const [s, w] = chart($('tts'), h);
    const max = niceMax(Math.max(...counts, 1)), y = (v) => h - b - ((h - b - 16) * v) / max;
    yAxis(s, lp, w, y, max);
    const bw = (w - lp) / bins.length;
    let cum = 0;
    counts.forEach((v, i) => {
      cum += v;
      const c = cum, x0 = lp + i * bw, cw = Math.min(24, bw - 6), cx = x0 + (bw - cw) / 2;
      const g = S('g', { class: 'h' }, s);
      S('rect', { class: 'hit', x: x0, y: 0, width: bw, height: h }, g);
      if (v) S('path', { class: 'mk', d: barV(cx, y(v), cw, h - b - y(v)), fill: 'var(--gold)' }, g);
      if (i < 3 && v) T(s, cx + cw / 2, y(v) - 4, pcs(v, tot), { 'text-anchor': 'middle', class: 'v' });
      T(s, x0 + bw / 2, h - 4, bins[i][1], { 'text-anchor': 'middle' });
      tip(g, () => [[{ t: bins[i][1] === '8h+' ? '8 hours or more' : `${bins[i][1]}${i < 4 ? ' min' : ''}`, c: 'nm' }],
        [{ t: `${v} sales (${pcs(v, tot)})`, c: 'c7' }], [{ t: `${pcs(c, tot)} of sales happen by then`, c: 'c6' }]]);
    });
  }

  // What changes the odds.
  const servMed = new Map();
  {
    const asks = new Map();
    for (const r of off) if (r.kind === 'sell' && r.price > 0) {
      const k = `${r.date}|${r.meal}|${r.mess}`;
      if (!asks.has(k)) asks.set(k, []);
      asks.get(k).push(r.price);
    }
    for (const [k, ps] of asks) if (ps.length >= 4) servMed.set(k, median(ps));
  }
  const groups = [
    ['Posted before the meal ends', [['<1h', (r) => r.end - r.first < 3600], ['1-3h', (r) => r.end - r.first >= 3600 && r.end - r.first < 10800], ['3-6h', (r) => r.end - r.first >= 10800 && r.end - r.first < 21600], ['6h+', (r) => r.end - r.first >= 21600]]],
    ['Times posted', [['1', (r) => r.posts === 1], ['2', (r) => r.posts === 2], ['3', (r) => r.posts === 3], ['4+', (r) => r.posts >= 4]]],
    ['Ask vs that serving\'s median', (() => {
      const rel = (r) => { const m = servMed.get(`${r.date}|${r.meal}|${r.mess}`); return r.kind === 'sell' && r.price > 0 && m != null ? r.price - m : null; };
      return [['Below', (r) => { const d = rel(r); return d != null && d < -2; }], ['At', (r) => { const d = rel(r); return d != null && Math.abs(d) <= 2; }], ['Above', (r) => { const d = rel(r); return d != null && d > 2; }], ['No price', (r) => r.price < 0]];
    })()],
    ['Groups posted in', [['1', (r) => r.groups === 1], ['2', (r) => r.groups === 2], ['3+', (r) => r.groups >= 3]]],
  ];
  const host = $('odds');
  host.replaceChildren();
  for (const [title, buckets] of groups) {
    const m = el('div', 'mini');
    m.append(el('h4', '', title));
    host.append(m);
    const holder = el('div');
    m.append(holder);
    const data = buckets.map(([lab, f]) => { const o = off.filter(f); return { lab, n: o.length, s: o.filter((r) => r.sold).length }; });
    oddsChart(holder, data, overall);
  }
}

function oddsChart(host, data, overall) {
  const rh = 26, lw = 64;
  const [s, w] = chart(host, data.length * rh + 18);
  const max = Math.max(0.6, ...data.map((d) => (d.n ? d.s / d.n : 0))) * 1.0;
  const x = (v) => lw + ((w - lw - 40) * v) / max;
  data.forEach((d, i) => {
    const y0 = i * rh + 4, v = d.n ? d.s / d.n : 0;
    const g = S('g', { class: 'h' }, s);
    S('rect', { class: 'hit', x: 0, y: y0 - 4, width: w, height: rh }, g);
    T(g, 0, y0 + 12, d.lab);
    if (d.n) S('path', { class: 'mk', d: barH(lw, y0 + 2, Math.max(2, x(v) - lw), 14), fill: 'var(--gold)', opacity: d.n < 30 ? 0.45 : 1 }, g);
    T(g, (d.n ? x(v) : lw) + 5, y0 + 13, d.n ? pcs(d.s, d.n) : '-', { class: 'v' });
    tip(g, () => [[{ t: d.lab, c: 'nm' }], [{ t: `${d.s} of ${d.n} sold`, c: 'c7' }], d.n < 30 ? [{ t: 'Few listings, so this can swing a lot', c: 'c8' }] : null].filter(Boolean));
  });
  if (overall) S('line', { x1: x(overall), x2: x(overall), y1: 0, y2: data.length * rh + 2, stroke: 'var(--text)', opacity: 0.4 }, s);
  for (const t of [0, 0.3, 0.6]) if (t <= max) T(s, x(t), data.length * rh + 16, `${Math.round(t * 100)}%`, { 'text-anchor': 'middle' });
}

// ---------- sellers ----------

function renderSellers() {
  const rows = view(), off = rows.filter(isOffer);
  const per = new Map();
  for (const r of off) {
    if (!per.has(r.seller)) per.set(r.seller, { i: r.seller, rows: [] });
    per.get(r.seller).rows.push(r);
  }
  const list = [...per.values()];
  for (const p of list) {
    p.n = p.rows.length;
    p.s = p.rows.filter((r) => r.sold).length;
    p.never = p.n >= 3 && p.s === 0;
    p.days = new Set(p.rows.map((r) => r.day)).size;
    const slots = new Map();
    for (const r of p.rows) { const k = `${r.mess}|${r.wd}|${r.meal}`; slots.set(k, (slots.get(k) || 0) + 1); }
    const [top, topN] = [...slots.entries()].sort((a, b) => b[1] - a[1])[0];
    const [mess, wd, meal] = top.split('|');
    p.slot = { mess, wd: +wd, meal, n: topN };
    p.ask = median(p.rows.filter((r) => r.kind === 'sell' && r.price > 0).map((r) => r.price));
  }
  list.sort((a, b) => b.n - a.n || b.s - a.s);
  const never = list.filter((p) => p.never), neverN = sum(never.map((p) => p.n));
  const tenth = Math.max(1, Math.round(list.length / 10)), topShare = pc(sum(list.slice(0, tenth).map((p) => p.n)), off.length);
  $('slMeta').textContent = list.length
    ? `${list.length.toLocaleString('en-IN')} sellers. The busiest 10% post ${topShare}% of listings. ${never.length} never tick (${pcs(neverN, off.length)} of listings).`
    : '';

  // New and returning, per day or per week for long ranges.
  const days = [...new Set(rows.map((r) => r.day))].sort();
  const weekly = days.length > 35;
  const bucket = (d) => (weekly ? addDaysISO(d, -wdOf(d)) : d);
  const firstSeen = new Map();
  for (const r of off) if (!firstSeen.has(r.seller) || r.day < firstSeen.get(r.seller)) firstSeen.set(r.seller, r.day);
  const keys = [...new Set(days.map(bucket))].sort();
  const nr = keys.map((k) => {
    const active = new Set(off.filter((r) => bucket(r.day) === k).map((r) => r.seller));
    let n = 0;
    for (const id of active) if (bucket(firstSeen.get(id)) === k) n++;
    return { k, n, r: active.size - n };
  });
  $('nrK').textContent = weekly ? 'People listing per week' : 'People listing per day';
  {
    const h = 170, b = 18, lp = 36;
    const [s, w] = chart($('newret'), h);
    const max = niceMax(Math.max(...nr.map((x) => x.n + x.r), 1)), y = (v) => h - b - ((h - b - 16) * v) / max;
    yAxis(s, lp, w, y, max);
    const bw = (w - lp) / Math.max(1, nr.length), every = Math.ceil(nr.length / 8);
    nr.forEach((x, i) => {
      const x0 = lp + i * bw, cw = Math.min(24, bw - 3), cx = x0 + (bw - cw) / 2, base = h - b;
      const g = S('g', { class: 'h' }, s);
      S('rect', { class: 'hit', x: x0, y: 0, width: bw, height: h }, g);
      const rh = base - y(x.r), nh = base - y(x.n);
      if (x.r) S('rect', { class: 'mk', x: cx, y: base - rh, width: cw, height: rh, fill: 'var(--gold)' }, g);
      if (x.n) S('path', { class: 'mk', d: barV(cx, base - rh - nh - (x.r ? 2 : 0), cw, nh), fill: BLUE }, g);
      else if (x.r) S('path', { class: 'mk', d: barV(cx, base - rh, cw, rh), fill: 'var(--gold)' }, g);
      if (i % every === 0) T(s, x0 + bw / 2, h - 4, fmtShort(x.k), { 'text-anchor': 'middle' });
      tip(g, () => [[{ t: weekly ? `Week of ${fmtDay(x.k)}` : fmtDay(x.k), c: 'nm' }],
        [{ t: 'Returning ', c: 'c7' }, { t: String(x.r), c: 'c6' }], [{ t: 'New ', c: 'c7' }, { t: String(x.n), c: 'cf' }],
        null, [{ t: 'New means their first listing in this range.', c: 'c8' }]]);
    });
  }

  // Listings per seller.
  {
    const bins = [[1, '1'], [3, '2-3'], [6, '4-6'], [10, '7-10'], [20, '11-20'], [1e9, '21+']];
    const cnt = bins.map(() => 0);
    for (const p of list) cnt[bins.findIndex(([e]) => p.n <= e)]++;
    const rh = 26, lw = 44;
    const [s, w] = chart($('freq'), bins.length * rh + 4);
    const max = Math.max(...cnt, 1), x = (v) => lw + ((w - lw - 44) * v) / max;
    bins.forEach(([, lab], i) => {
      const y0 = i * rh + 4, g = S('g', { class: 'h' }, s);
      S('rect', { class: 'hit', x: 0, y: y0 - 4, width: w, height: rh }, g);
      T(g, 0, y0 + 12, lab);
      if (cnt[i]) S('path', { class: 'mk', d: barH(lw, y0 + 2, Math.max(2, x(cnt[i]) - lw), 14), fill: BLUE }, g);
      T(g, (cnt[i] ? x(cnt[i]) : lw) + 5, y0 + 13, String(cnt[i]), { class: 'v' });
      tip(g, () => [[{ t: `${lab} listing${lab === '1' ? '' : 's'}`, c: 'nm' }], [{ t: `${cnt[i]} sellers`, c: 'c7' }]]);
    });
  }

  // The table.
  const t = $('sellers');
  t.replaceChildren();
  const hr = el('tr');
  for (const [h, n] of [['SELLER', 0], ['LISTED', 1], ['SOLD', 1], ['USUAL SLOT', 0], ['ASK', 1], ['DAYS', 1]]) hr.append(el('th', n ? 'n' : '', h));
  t.append(hr);
  for (const p of list.slice(0, 15)) {
    const who = D.sellers[p.i] || {};
    const tr = el('tr');
    const td0 = el('td');
    td0.append(el('span', 'nm', who.name || 'No name'), el('span', 'ph', who.phone ? fmtPhone(who.phone) : ''));
    if (p.never) td0.append(el('span', 'flag', 'NEVER TICKS'));
    const rate = el('span', 'rate'), fill = el('i');
    fill.style.setProperty('width', `${pc(p.s, p.n)}%`);
    rate.append(fill);
    const tdS = el('td', 'n');
    tdS.append(rate, `${p.s} · ${pcs(p.s, p.n)}`);
    const tdSlot = el('td'), sc = el('span', 'slot-cell');
    sc.append(svgEl(messIcon(p.slot.mess)), `${cap(p.slot.mess)} · ${WD[p.slot.wd]} ${p.slot.meal} ×${p.slot.n}`);
    tdSlot.append(sc);
    tr.append(td0, el('td', 'n', String(p.n)), tdS, tdSlot, el('td', 'n', p.ask == null ? '-' : rupees(p.ask)), el('td', 'n', String(p.days)));
    tip(tr, () => {
      const by = new Map();
      for (const r of p.rows) by.set(r.mess, (by.get(r.mess) || 0) + 1);
      return [[{ t: who.name || 'No name', c: 'nm' }], who.phone ? [{ t: fmtPhone(who.phone), c: 'c8' }] : null, null,
        ...[...by.entries()].sort((a, b) => b[1] - a[1]).map(([m, n]) => [{ t: `${messName(m)} `, color: messColor(m) }, { t: `${n} listed`, c: 'c7' }]),
        ...(p.never ? [null, [{ t: "Listed 3+ times and never ticked, so their sales don't show.", c: 'c6' }]] : [])].filter((x) => x !== undefined);
    });
    t.append(tr);
  }
}

function addDaysISO(date, n) {
  const d = dayOf(date);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

// ---------- boot ----------

$('logo').append(svgEl(icon('chart')));
$('navIcon').append(svgEl(icon('prev')));
$('salesIcon').append(svgEl(icon('coin')));
$('useIcon').append(svgEl(icon('check')));
me().then(userLink).catch(() => {});
$('ovIcon').append(svgEl(icon('paper')));
$('snIcon').append(svgEl(icon('chart')));
$('wkIcon').append(svgEl(icon('clock')));
$('dietIcon').append(svgEl(icon('lunch')));
$('hrIcon').append(svgEl(icon('clock')));
$('pbIcon').append(svgEl(icon('check')));
$('slIcon').append(svgEl(icon('coin')));

(async () => {
  await load();
  if (D) renderFilters();
})();
let resizeT = 0;
addEventListener('resize', () => { clearTimeout(resizeT); resizeT = setTimeout(render, 150); });
// Facts land hourly, so a slow refresh is plenty.
setInterval(() => { if (document.visibilityState === 'visible') load(); }, 10 * 60 * 1000);
