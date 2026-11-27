'use strict';

// Home page widgets drawn from api/sales (the ledger app.js loads as N):
// the sales ledger, earnings, regulars, price check and friends' meals, plus
// recurring rules from api/recurring-rules. Each draw function takes the box
// to fill and the data, so the widget picker can draw the same widget from
// made-up data (widget-demo.js). They are compact versions of the sections
// on the sales page, and link there for anything more.
//
// Uses common.js and app.js's now(). Buyer names are untrusted and only
// ever reach the page through textContent.

const HW = (() => {
  const dayOf = (date) => new Date(date + 'T12:00:00Z');
  const fmt = (date, o) => new Intl.DateTimeFormat('en-GB', { ...o, timeZone: 'UTC' }).format(dayOf(date));
  const fmtDay = (date) => fmt(date, { weekday: 'short', day: 'numeric', month: 'short' });
  const fmtShort = (date) => fmt(date, { day: 'numeric', month: 'short' });
  const addDays = (date, n) => { const d = dayOf(date); d.setUTCDate(d.getUTCDate() + n); return d.toISOString().slice(0, 10); };
  const mondayOf = (date) => addDays(date, -((dayOf(date).getUTCDay() + 6) % 7));
  const sum = (xs) => xs.reduce((a, b) => a + b, 0);
  const med = (xs) => {
    if (!xs.length) return null;
    const v = xs.slice().sort((a, b) => a - b), h = v.length >> 1;
    return v.length % 2 ? v[h] : (v[h - 1] + v[h]) / 2;
  };
  const isSold = (s) => s.state === 'sold' || s.state === 'sending';
  const priceDiet = (d) => (d === 'nonveg' || d === 'egg' ? d : 'veg');
  const dietOf = (s) => (s.extra ? 'extra' : priceDiet(s.diet));
  const dietName = (d) => (d === 'extra' ? 'Extras' : DIET_LABEL[d]);
  const saleWhat = (s) => (s.extra ? `${s.meal} · ${s.extra}` : s.meal);
  const messName = (m) => (m ? cap(m) : 'Unknown mess');
  const fmtPhone = (p) => (/^\+91\d{10}$/.test(p) ? `+91 ${p.slice(3, 8)} ${p.slice(8)}` : p);
  const signed = (v) => `${v > 0 ? '+' : v < 0 ? '−' : '±'}${rupees(Math.abs(Math.round(v)))}`;
  const hmz = (N, u) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: N.tz || 'Asia/Kolkata' }).format(u * 1000);
  const dayWord = (N, date) => (date === N.today ? 'today' : date === addDays(N.today, -1) ? 'yesterday' : date === addDays(N.today, 1) ? 'tomorrow' : fmtDay(date));
  const recentSales = (N) => N.sales.filter((s) => isSold(s) && s.date >= N.recent && s.date <= N.today);
  const mealsOf = (N) => N.windows.map((w) => w.meal);
  function span(cls, markup) {
    const s = el('span', cls);
    s.append(svgEl(markup));
    return s;
  }
  const empty = (box, text) => box.replaceChildren(el('div', 'empty-note', text));
  const link = (href, text) => { const a = el('a', 'w-link', text); a.href = href; return a; };

  // ---------- sales ledger ----------

  // The ledger widget is this week's meals, Monday to Sunday, with the
  // month's savings bar on top. A meal still to come opens the sell sheet
  // here when the board's ledger has it, and the sales page otherwise.
  function ledger(box, N) {
    box.replaceChildren();
    box.append(savedBar(N));
    const cells = new Map();
    for (const d of N.days) cells.set(d.date, d.cells);
    for (const d of N.upcoming || []) if (d.date > N.today) cells.set(d.date, d.cells);
    const week = Array.from({ length: 7 }, (_, i) => addDays(mondayOf(N.today), i));
    const meals = mealsOf(N);

    const wrap = el('div', 'ledger w-ledger-body');
    const grid = el('div', 'lgrid');
    grid.append(el('div'));
    for (const d of week) {
      const h = el('div', 'dh' + (d === N.today ? ' today' : ''), fmt(d, { weekday: 'short' }).toUpperCase());
      const earned = sum((cells.get(d) || []).filter((c) => c.sale != null && isSold(N.sales[c.sale])).map((c) => N.sales[c.sale].amount))
        + sum(N.sales.filter((s) => s.extra && isSold(s) && s.date === d).map((s) => s.amount));
      h.append(el('span', earned ? '' : 'none', earned ? rupees(Math.round(earned)) : '-'));
      grid.append(h);
    }
    for (const meal of meals) {
      const mh = el('div', 'mh');
      mh.append(svgEl(icon(meal)));
      mh.title = cap(meal);
      grid.append(mh);
      for (const d of week) {
        const c = (cells.get(d) || []).find((x) => x.meal === meal);
        grid.append(c ? cellNode(N, d, c) : el('div', 'slot out'));
      }
    }
    wrap.append(grid);

    // This week in numbers, beside the grid.
    const all = week.flatMap((d) => (cells.get(d) || []).map((c) => c));
    const count = (st) => all.filter((c) => st.includes(c.state)).length;
    const sold = all.filter((c) => c.sale != null && isSold(N.sales[c.sale]));
    const xsold = N.sales.filter((s) => s.extra && isSold(s) && week.includes(s.date));
    const side = el('div', 'w-week');
    const tiles = el('div', 'w-week-tiles');
    const tile = (k, v, cls, sub) => {
      const t = el('div', 'w-stat');
      t.append(el('b', cls || '', v), el('span', '', k));
      if (sub) t.append(el('small', '', sub));
      return t;
    };
    const wasted = count(['wasted']);
    const soldSub = sold.length && xsold.length
      ? `${sold.length} meal${sold.length === 1 ? '' : 's'} + ${xsold.length} extra${xsold.length === 1 ? '' : 's'}`
      : xsold.length ? `${xsold.length} extra${xsold.length === 1 ? '' : 's'}`
      : `${sold.length} meal${sold.length === 1 ? '' : 's'}`;
    tiles.append(
      tile('SOLD', rupees(Math.round(sum(sold.map((c) => N.sales[c.sale].amount)) + sum(xsold.map((s) => s.amount)))), 'g', soldSub),
      tile('SELLING OR SCHEDULED', String(count(['selling', 'scheduled', 'armed'])), 'gold'),
      tile('STILL OPEN', String(all.filter((c) => c.state === 'open' && c.end > now()).length)),
      tile('ATE', String(count(['ate']))),
      tile('WASTED', String(wasted), wasted ? 'r' : ''));
    side.append(el('div', 'side-k', 'THIS WEEK'), tiles, link('sales.html', 'Open the full ledger ›'));
    wrap.append(side);
    box.append(wrap);
  }

  function savedBar(N) {
    const M = N.month;
    const box = el('div', 'saved');
    const monthName = fmt(M.start, { month: 'long' });
    const top = el('div', 'saved-top');
    const v = el('span', 'saved-v', rupees(N.owner ? M.earned : Math.round(M.potential)));
    if (N.owner && M.fee > 0) v.append(el('small', '', `${Math.round((M.earned / M.fee) * 100)}% of ${rupees(M.fee)}`));
    top.append(el('span', 'saved-k', N.owner ? `Saved in ${monthName}` : `Wasted in ${monthName}`), v);
    const scale = M.fee > 0 ? M.fee : M.earned + M.potential;
    const ew = scale ? Math.min(1, M.earned / scale) : 0;
    const cw = scale ? Math.min(1 - ew, M.potential / scale) : 0;
    const bar = el('div', 'xp-bar');
    const e = el('i', 'earned'), c = el('i', 'could');
    e.style.setProperty('width', `${ew * 100}%`);
    c.style.setProperty('left', `${ew * 100}%`);
    c.style.setProperty('width', `${cw * 100}%`);
    bar.append(e, c);
    const soldWord = M.extras_sold
      ? (M.sold ? `${M.sold} meal${M.sold === 1 ? '' : 's'} + ${M.extras_sold} extra${M.extras_sold === 1 ? '' : 's'}` : `${M.extras_sold} extra${M.extras_sold === 1 ? '' : 's'}`)
      : `${M.sold} sold`;
    tip(bar, () => [
      [{ t: `${monthName} so far`, c: 'nm' }],
      N.owner ? [{ t: 'Earned ', c: 'c7' }, { t: rupees(M.earned), c: 'ca' }, { t: ` from ${soldWord}`, c: 'c8' }] : null,
      M.wasted ? [{ t: 'Left on the table ', c: 'c7' }, { t: rupees(Math.round(M.potential)), c: 'c6' }, { t: ` from ${M.wasted} wasted`, c: 'c8' }] : null,
      M.fee > 0 ? [{ t: 'Mess bill ', c: 'c7' }, { t: rupees(M.fee), c: 'cf' }] : null,
    ].filter(Boolean));
    box.append(top, bar);
    return box;
  }

  // cellNode is one meal in the ledger grid, drawn as the sales page draws it.
  function cellNode(N, date, c) {
    const n = el('div', `slot item cell is-${c.state}`);
    n.style.setProperty('--m', messColor(c.mess));
    const s = c.sale != null ? N.sales[c.sale] : null;
    if (s && ['sold', 'sending', 'armed', 'handoff'].includes(c.state)) {
      if (c.diet) n.append(span('diet', dietMark(c.diet)));
      n.append(span('face', face(s.buyer)), span('messg', messIcon(c.mess)));
      const v = s.amount;
      const p = el('div', 'price' + (v >= 100 || !Number.isInteger(v) ? ' wide' : ''));
      p.append(el('span', 'r', '₹'), Number.isInteger(v) ? String(v) : v.toFixed(2));
      n.append(p);
      if (c.state === 'handoff') n.append(el('span', 'stamp long', 'HANDOFF'));
    } else if ((c.state === 'selling' && c.task) || (c.state === 'scheduled' && c.schedule)) {
      const t = c.task || c.schedule;
      const p = el('div', 'price range');
      p.append(el('span', 'r', '₹'), t.min === t.max ? String(t.min) : `${t.min}-${t.max}`);
      if (c.diet) n.append(span('diet', dietMark(c.diet)));
      if (c.task) n.append(span('messg', messIcon(c.mess)), p, el('span', 'sell-tag', 'SELLING'));
      else n.append(p, el('span', 'sell-tag', hmz(N, c.schedule.at)));
      if (t.autopilot) n.append(el('span', 'auto-tag', '✈'));
    } else if (c.state === 'unknown') {
      n.append(el('div', 'ghost', '?'));
    } else if (c.state !== 'none') {
      const g = el('div', 'ghost');
      g.append(svgEl(messIcon(c.mess)));
      n.append(g);
      if (c.state === 'ate') n.append(el('span', 'stamp', 'ATE'));
      if (c.state === 'wasted') n.append(el('span', 'stamp', 'WASTED'));
      if (c.state === 'open') n.append(el('span', 'sell-tag', c.sellable ? 'SELL' : 'PLAN'));
    }
    const key = `${date}|${c.meal}`;
    const still = c.end > now() && c.state !== 'none';
    if (N.owner && (still || s)) {
      n.classList.add('sellable');
      n.tabIndex = 0;
      n.setAttribute('role', 'link');
      n.setAttribute('aria-label', `${cap(c.meal)} ${dayWord(N, date)}: ${c.state}`);
      const go = () => {
        if (typeof openSellHere === 'function' && (c.task || c.sellable) && openSellHere(date, c.meal)) return;
        location.href = `sales.html#sell=${encodeURIComponent(key)}`;
      };
      n.addEventListener('click', go);
      n.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go(); } });
    }
    tip(n, () => [
      [{ t: `${c.mess ? messName(c.mess) + ' ' : ''}${c.meal}`, c: 'nm', color: messColor(c.mess) }, { t: ` ${dayWord(N, date)}`, c: 'c8' }],
      s ? [{ t: `${isSold(s) ? 'Sold' : 'Armed'} for `, c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ` to ${s.name || fmtPhone(s.phone) || 'a buyer'}`, c: 'c8' }] : null,
      c.schedule ? [{ t: `Goes up ${hmz(N, c.schedule.at)} at ${rupees(c.schedule.min)}-${rupees(c.schedule.max)}`, c: 'c7' }] : null,
      c.task ? [{ t: `Selling at ${rupees(c.task.min)}-${rupees(c.task.max)}`, c: 'c7' }] : null,
      c.state === 'none' ? [{ t: 'Not registered', c: 'c8' }] : null,
      c.state === 'wasted' && c.ask ? [{ t: `Could have fetched about ${rupees(Math.round(c.ask))}`, c: 'c6' }] : null,
      c.state === 'open' && c.ask && c.end > now() ? [{ t: `Median ask ${rupees(Math.round(c.ask))}`, c: 'c7' }] : null,
    ].filter(Boolean));
    return n;
  }

  // ---------- earnings ----------

  // A card per mess and diet you sold in over four weeks: totals and ₹ per day.
  function earnings(box, N) {
    const mine = recentSales(N);
    const days = [];
    for (let d = N.recent; d <= N.today; d = addDays(d, 1)) days.push(d);
    const names = N.messes.map((m) => m.name);
    const messOf = (s) => (names.includes(s.mess) ? s.mess : '');
    const keyOf = (m, d) => `${m}|${d}`;
    const cards = [];
    for (const m of [...N.messes, { name: '' }]) {
      const sold = mine.filter((s) => messOf(s) === m.name);
      for (const diet of ['veg', 'nonveg', 'egg', 'extra']) if (sold.some((s) => dietOf(s) === diet)) cards.push({ mess: m.name, diet });
    }
    if (!cards.length) return empty(box, 'No sales in the last 4 weeks.');
    const per = new Map(cards.map((k) => [keyOf(k.mess, k.diet), days.map(() => 0)]));
    for (const s of mine) per.get(keyOf(messOf(s), dietOf(s)))[days.indexOf(s.date)] += s.amount;
    const mx = Math.max(1, ...[...per.values()].flat());
    const totals = cards.map((k) => sum(per.get(keyOf(k.mess, k.diet))));
    const best = Math.max(...totals);
    const top = totals.filter((t) => t === best).length === 1 && best > 0 ? totals.indexOf(best) : -1;
    const w0 = days.indexOf(mondayOf(N.today));

    const grid = el('div', 'ecards');
    cards.forEach(({ mess: m, diet }, ci) => {
      const list = mine.filter((s) => messOf(s) === m && dietOf(s) === diet);
      const c = el('div', 'card');
      c.style.setProperty('--m', messColor(m));
      const head = el('div', 'card-head');
      head.append(svgEl(messIcon(m)));
      const nm = el('span', 'card-name', messName(m));
      nm.style.setProperty('color', messColor(m));
      const dt = span('card-diet', diet === 'extra' ? extraIcon() : dietMark(diet));
      dt.append(dietName(diet));
      nm.append(dt);
      head.append(nm);
      if (ci === top) head.append(el('span', 'verdict hot', 'TOP EARNER'));
      const kpis = el('div', 'kpis');
      const kpi = (v, label) => { const k = el('div', 'kpi'); k.append(el('b', v == null ? 'none' : '', v == null ? '-' : v), el('span', '', label)); return k; };
      const m2 = med(list.map((s) => s.amount));
      kpis.append(kpi(String(list.length), diet === 'extra' ? 'EXTRAS SOLD' : 'MEALS SOLD'), kpi(rupees(Math.round(totals[ci])), 'EARNED'), kpi(m2 == null ? null : rupees(Math.round(m2)), 'MEDIAN PRICE'));
      const spark = el('div', 'spark');
      spark.style.setProperty('--n', days.length);
      if (w0 >= 0) {
        const band = el('div', 'band');
        band.style.setProperty('left', `${(w0 / days.length) * 100}%`);
        band.style.setProperty('width', `${((days.length - w0) / days.length) * 100}%`);
        band.style.setProperty('right', 'auto');
        spark.append(band);
      }
      per.get(keyOf(m, diet)).forEach((v, i) => {
        const bk = el('div', 'bk');
        if (v) {
          const bar = el('i');
          bar.style.setProperty('height', `${Math.max(6, (v / mx) * 100)}%`);
          bk.append(bar);
        }
        const ds = list.filter((s) => s.date === days[i]);
        tip(bk, () => [
          [{ t: `${messName(m)} ${dietName(diet).toLowerCase()}, ${fmtDay(days[i])}`, c: 'nm', color: messColor(m) }],
          ...(ds.length ? ds.map((s) => [{ t: `${cap(saleWhat(s))} `, c: 'c7' }, { t: rupees(s.amount), c: 'c6' }, { t: ` · ${s.name || fmtPhone(s.phone) || 'Unknown buyer'}`, c: 'c8' }]) : [[{ t: 'No sales', c: 'c8' }]]),
        ]);
        spark.append(bk);
      });
      const axis = el('div', 'axis');
      axis.append(el('span', '', fmtShort(days[0])), el('span', '', fmtShort(days[Math.floor(days.length / 2)])), el('span', '', 'today'));
      c.append(head, kpis, spark, axis);
      grid.append(c);
    });
    box.replaceChildren(grid);
  }

  const earnMeta = (N) => {
    const mine = recentSales(N);
    const meals = mine.filter((s) => !s.extra).length;
    const extras = mine.length - meals;
    const count = extras
      ? (meals ? `${meals} meal${meals === 1 ? '' : 's'} + ${extras} extra${extras === 1 ? '' : 's'}` : `${extras} extra${extras === 1 ? '' : 's'}`)
      : `${meals} meals`;
    return `${rupees(Math.round(sum(mine.map((s) => s.amount))))} from ${count} · 4 weeks`;
  };

  // ---------- regulars ----------

  const TIERS = [
    { min: 10, name: 'LEGENDARY', color: '#ffaa1f' },
    { min: 7, name: 'EPIC', color: '#c86bff' },
    { min: 4, name: 'RARE', color: '#6b8cff' },
    { min: 2, name: 'UNCOMMON', color: '#5be37a' },
    { min: 1, name: 'COMMON', color: '#f4f5f8' },
  ];
  const tierOf = (n) => TIERS.find((t) => n >= t.min);

  function buyers(N) {
    const by = new Map();
    for (const s of N.sales) {
      if (!isSold(s)) continue;
      let b = by.get(s.buyer);
      if (!b) by.set(s.buyer, (b = { key: s.buyer, phone: s.phone, name: '', sales: [] }));
      b.sales.push(s);
      if (s.name) b.name = s.name;
    }
    return [...by.values()]
      .map((b) => ({ ...b, n: b.sales.length, spent: sum(b.sales.map((s) => s.amount)) }))
      .sort((a, b) => b.n - a.n || b.spent - a.spent);
  }

  let picked = '';
  function regulars(box, N) {
    const bs = buyers(N);
    if (!bs.length) return empty(box, 'No sales yet. Buyers of meals you sell show up here.');
    if (!bs.some((b) => b.key === picked)) picked = bs[0].key;
    const heads = el('div', 'heads');
    const panel = el('div', 'panel');
    for (const b of bs.slice(0, 16)) {
      const t = tierOf(b.n);
      const n = el('div', 'slot item' + (t.min > 1 ? ' tiered' : '') + (b.key === picked ? ' active' : ''));
      n.style.setProperty('--m', t.color);
      n.append(svgEl(face(b.key)), el('span', 'pip'));
      if (b.n > 1) n.append(el('span', 'qty', String(b.n)));
      n.setAttribute('aria-label', `${b.name || 'Buyer'}: ${b.n} meals`);
      const show = () => {
        picked = b.key;
        for (const x of heads.children) x.classList.toggle('active', x === n);
        fillLines(panel, buyerLines(N, b));
      };
      n.addEventListener('pointerenter', show);
      n.addEventListener('click', show);
      heads.append(n);
    }
    fillLines(panel, buyerLines(N, bs.find((b) => b.key === picked)));
    const body = el('div', 'rbody w-reg');
    body.append(heads, panel);
    box.replaceChildren(body);
  }

  function buyerLines(N, b) {
    const t = tierOf(b.n);
    const usual = new Map();
    for (const s of b.sales) {
      const k = `${messName(s.mess)} ${saleWhat(s)}`;
      usual.set(k, (usual.get(k) || 0) + 1);
    }
    const fav = [...usual.entries()].sort((x, y) => y[1] - x[1])[0];
    const waits = b.sales.filter((s) => s.offer_at && s.paid_at && s.paid_at >= s.offer_at).map((s) => s.paid_at - s.offer_at);
    const last = b.sales[b.sales.length - 1].date;
    return [
      [{ t: b.name || fmtPhone(b.phone) || 'Unknown buyer', c: 'nm', color: t.color }],
      null,
      [{ t: 'Meals bought ', c: 'c7' }, { t: String(b.n), c: 'cf' }, { t: '   Spent ', c: 'c7' }, { t: rupees(Math.round(b.spent)), c: 'c6' }],
      [{ t: 'Usual ', c: 'c7' }, { t: fav[0], c: 'cf' }],
      [{ t: 'Last ', c: 'c7' }, { t: dayWord(N, last), c: 'c8' }],
      waits.length ? [{ t: `Pays about ${dur(med(waits))} after your offer.`, c: 'lore' }] : null,
      { rar: `${t.name} BUYER`, color: t.color },
    ];
  }

  const regMeta = (N) => {
    const bs = buyers(N);
    return `${bs.length} buyers · ${bs.filter((b) => b.n > 1).length} came back`;
  };

  // ---------- price check ----------

  // A row per meal and diet you sold in: the market's middle half and median
  // ask against your own sales. Drawn as one SVG at the box's width.
  function prices(box, N) {
    // Extras sell for a fraction of a meal and have no market median.
    const mine = recentSales(N).filter((s) => !s.extra);
    const rows = [];
    for (const meal of mealsOf(N)) {
      for (const diet of ['veg', 'egg', 'nonveg']) {
        const m = N.market.find((x) => x.meal === meal && x.diet === diet);
        const ms = mine.filter((s) => s.meal === meal && priceDiet(s.diet) === diet);
        if (ms.length) rows.push({ meal, diet, m, ms });
      }
    }
    if (!rows.length) return empty(box, 'No sales in the last 4 weeks.');
    const vals = [];
    for (const r of rows) {
      if (r.m) vals.push(r.m.q1, r.m.q3);
      for (const s of r.ms) vals.push(s.amount);
    }
    let lo = Math.floor(Math.min(...vals) / 10) * 10, hi = Math.ceil(Math.max(...vals) / 10) * 10;
    if (hi - lo < 20) { lo = Math.max(0, lo - 10); hi = lo + 30; }
    const W = Math.max(300, box.clientWidth || 600);
    const x0 = 120, x1 = W - 110;
    const top = 14, rowH = 34, H = top + rows.length * rowH + 14;
    const X = (v) => x0 + ((v - lo) / (hi - lo)) * (x1 - x0);
    const step = [5, 10, 20, 25, 50, 100].find((s) => (hi - lo) / s <= (W < 500 ? 3 : 6)) || 100;
    let svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${W} ${H}" role="img" aria-label="Your sale prices against the market's asking prices">`;
    for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) {
      svg += `<line x1="${X(v)}" x2="${X(v)}" y1="4" y2="${H - 18}" stroke="#252c3a" stroke-dasharray="2 4"/>`
        + `<text x="${X(v)}" y="${H - 4}" fill="#586076" font-size="10.5" text-anchor="middle">₹${v}</text>`;
    }
    rows.forEach(({ meal, diet, m, ms }, i) => {
      const y = top + i * rowH + 6;
      svg += icon(meal).replace('<svg ', `<svg x="2" y="${y - 6}" width="12" height="12" `)
        + dietMark(diet).replace('<svg ', `<svg x="18" y="${y - 6}" width="12" height="12" `)
        + `<text class="px" x="36" y="${y + 4}" fill="#8b94a7">${meal.toUpperCase()}</text>`;
      if (m) {
        svg += `<rect x="${X(m.q1)}" y="${y - 9}" width="${Math.max(2, X(m.q3) - X(m.q1))}" height="18" rx="4" fill="rgba(232,236,243,.14)"/>`
          + `<line x1="${X(m.median)}" x2="${X(m.median)}" y1="${y - 12}" y2="${y + 12}" stroke="#e8ecf3" stroke-opacity=".8" stroke-width="2" stroke-linecap="round"/>`;
      }
      ms.forEach((s, k) => {
        const cx = X(s.amount) + ((k % 3) - 1) * 2, cy = y + (((k * 37) % 13) - 6);
        svg += `<circle cx="${cx}" cy="${cy}" r="4" fill="#ffc53d" stroke="#141923" stroke-width="2"/>`;
      });
      const diffs = ms.filter((s) => s.ask != null).map((s) => s.amount - s.ask);
      const d = diffs.length ? sum(diffs) / diffs.length : null;
      const word = d == null ? 'NO ASKS' : d <= -3 ? 'UNDERCUT' : d >= 3 ? 'PREMIUM' : 'AT MARKET';
      const hot = word === 'UNDERCUT';
      const pw = word.length * 7 + 14;
      svg += `<g transform="translate(${W - 100} ${y - 9})"><rect width="${pw}" height="18" rx="9" fill="none" stroke="${hot ? 'rgba(255,197,61,.55)' : '#252c3a'}"/>`
        + `<text class="px pill" x="${pw / 2}" y="12.5" text-anchor="middle" font-size="8" fill="${hot ? '#ffc53d' : d == null ? '#586076' : '#8b94a7'}">${word}</text></g>`;
      if (d != null && W >= 420) svg += `<text x="${W}" y="${y + 4}" text-anchor="end" fill="#8b94a7" font-size="11">${signed(d)}</text>`;
    });
    svg += '</svg>';
    const plot = el('div', 'pplot');
    plot.append(svgEl(svg));
    box.replaceChildren(plot);
  }

  // ---------- friends' meals ----------

  const FRIEND_LOOK = {
    scheduled: ['SCHEDULED', 'ce'], selling: ['SELLING', 'c6'], armed: ['ARMED', 'ce'], sending: ['PAID · SENDING', 'c6'], sold: ['SOLD', 'ca'],
    handoff: ['HANDED OFF', 'c6'], ended: ['ENDED UNSOLD', 'c8'],
  };
  function friends(box, N) {
    const fs = (N.friends || []).slice().reverse().slice(0, 5);
    const list = el('div', 'friends');
    if (!fs.length) list.append(el('div', 'empty-note', 'Selling a meal for a friend? Add it with their QR on the sales page. Its money stays out of your numbers.'));
    for (const f of fs) {
      const row = el('a', 'friend' + (f.task || f.schedule ? ' live' : ''));
      row.href = 'sales.html#pills';
      if (f.task || f.schedule) row.addEventListener('click', (e) => {
        if (typeof openFriendHere === 'function' && openFriendHere(f.task ? f.task.id : f.schedule.id, f.task ? 'task' : 'schedule')) e.preventDefault();
      });
      row.style.setProperty('--m', messColor(f.mess));
      const ic = el('span', 'fr-ic');
      ic.append(svgEl(messIcon(f.mess)));
      const what = el('div', 'fr-what');
      const nm = el('b', '', `${messName(f.mess)} ${f.meal}`);
      nm.style.setProperty('color', messColor(f.mess));
      what.append(nm, el('span', '', `${f.friend ? `for ${f.friend} · ` : ''}${dayWord(N, f.date)}`));
      const [word, cls] = FRIEND_LOOK[f.state] || [f.state.toUpperCase(), 'c8'];
      const right = el('div', 'fr-right');
      const t = f.task || f.schedule;
      right.append(el('span', 'pill ' + cls, f.schedule ? `${word} ${hmz(N, f.schedule.at)}` : word),
        el('span', 'fr-amt', t ? `${rupees(t.min)}-${rupees(t.max)}` : f.amount ? rupees(f.amount) : ''));
      row.append(ic, what, right);
      list.append(row);
    }
    const foot = el('div', 'w-foot');
    foot.append(link('sales.html#pills', '+ Sell a friend\'s meal'));
    box.replaceChildren(list, foot);
  }

  const friendMeta = (N) => {
    const fs = N.friends || [];
    const sold = fs.filter((f) => f.state === 'sold');
    return `${fs.filter((f) => f.task || f.schedule).length} live · ${sold.length} sold for ${rupees(sum(sold.map((f) => f.amount)))}`;
  };

  // ---------- recurring rules ----------

  const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  function daysText(days) {
    const set = new Set(days);
    if (set.size === 7) return 'Every day';
    if (set.size === 5 && [1, 2, 3, 4, 5].every((d) => set.has(d))) return 'Mon-Fri';
    if (set.size === 2 && set.has(0) && set.has(6)) return 'Weekends';
    return [1, 2, 3, 4, 5, 6, 0].filter((d) => set.has(d)).map((d) => DAYS[d]).join(', ');
  }
  const pct = (v) => `${v > 0 ? '+' : v < 0 ? '−' : '±'}${Math.abs(v)}%`;
  function rulePrice(r) {
    if (r.pricing !== 'median') return r.min === r.max ? `₹${r.min}` : `₹${r.min}-${r.max}`;
    const rel = r.meals.map((m) => r.relative?.[m]).filter(Boolean)[0];
    return rel ? `${pct(rel.low)}/${pct(rel.high)} median` : 'vs median';
  }

  function rules(box, list) {
    const out = el('div', 'friends rule-list');
    if (!list.length) out.append(el('div', 'empty-note', 'Selling the same meals every week? A rule schedules matching registered meals four weeks ahead, at your prices.'));
    for (const r of list) {
      const row = el('a', 'friend live rule-row' + (r.enabled ? '' : ' off'));
      row.href = `sales.html#recurring-rule=${r.id}`;
      const ic = el('span', 'fr-ic');
      ic.append(svgEl(r.meals.length === 1 ? icon(r.meals[0]) : mealsAllIcon(r.meals)));
      const what = el('div', 'fr-what');
      what.append(el('b', '', r.name), el('span', '', `${daysText(r.weekdays)} · ${r.meals.map(cap).join(', ')}${r.autopilot ? ' · autopilot' : ''}`));
      if (r.enabled && r.last_error) what.append(el('span', 'rule-err', r.last_error));
      const [word, cls] = r.enabled && r.last_error ? ['CHECK', 'cc'] : r.enabled ? ['ACTIVE', 'ca'] : ['PAUSED', 'c8'];
      const right = el('div', 'fr-right');
      right.append(el('span', 'pill ' + cls, word), el('span', 'fr-amt', rulePrice(r)), el('span', 'fr-buyer', `${r.waiting || 0} waiting`));
      row.append(ic, what, right);
      out.append(row);
    }
    const foot = el('div', 'w-foot');
    foot.append(link('sales.html#recurring-rule=new', '+ New rule'));
    box.replaceChildren(out, foot);
  }

  const ruleMeta = (list) => `${list.filter((r) => r.enabled).length} active · ${list.reduce((n, r) => n + (r.waiting || 0), 0)} meals waiting`;

  return { ledger, earnings, earnMeta, regulars, regMeta, prices, friends, friendMeta, rules, ruleMeta };
})();
