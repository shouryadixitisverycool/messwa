'use strict';

// The sell sheet: the popup that starts, schedules, reprices, marks sold or
// stops the sale of one of your meals. The sales page and the board both
// open it in their #modal/#sheet. Each page passes sellSheet its own
// state, since the two keep different globals:
//
//   data()   the last api/sales answer (tz, list_at, sell_off)
//   now()    server time, seconds
//   close()  hide the modal and empty the sheet
//   toast(t) a short message
//   reload() refetch after an action succeeds
//
// It returns open(date, c, view) plus the helpers the sales page also
// needs for its ledger.

function sellSheet(ctx) {
  const sheet = $('sheet');
  const modal = $('modal');
  const { now, toast } = ctx;
  const D = () => ctx.data();
  const tz = () => D().tz || 'Asia/Kolkata';
  const hm = (u) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz() }).format(u * 1000);
  const fmtDay = (date) => new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' }).format(new Date(date + 'T12:00:00Z'));
  const dur = (sec) => {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return `${sec}s`;
    const m = Math.round(sec / 60);
    return m < 60 ? `${m} min` : `${Math.floor(m / 60)}h ${m % 60}m`;
  };
  const fmtPhone = (p) => (/^\+91\d{10}$/.test(p) ? `+91 ${p.slice(3, 8)} ${p.slice(8)}` : p);
  const messName = (m) => (m ? cap(m) : 'Unknown mess');
  const closeModal = () => ctx.close();
  const load = () => ctx.reload();

  // listAt is when selling c now would put the offer up: 0 when it would go
  // up straight away (the meal is being served, or list_at around its start
  // has passed), else that time. It matches StartOrSchedule on the server.
  function listAt(c) {
    let at = c.start + (D().list_at || 0);
    if (at >= c.end) at = c.start;
    return now() >= c.start || now() >= at ? 0 : at;
  }
  // listWord says where list_at puts the offer: "15 min before lunch starts".
  function listWord(meal) {
    const s = D().list_at || 0;
    return s === 0 ? `when ${meal} starts` : `${dur(Math.abs(s))} ${s < 0 ? 'before' : 'after'} ${meal} starts`;
  }
  const startedToast = (meal, res) => (res && res.at ? `Scheduled. Your ${meal} goes up at ${hm(res.at)}` : `Selling your ${meal}`);


  // sellBounds is the slider's scale: wide enough for the median ask with
  // room to haggle above it.
  function sellBounds(c) {
    const top = Math.max(150, c.ask ? Math.ceil((c.ask * 2.5) / 50) * 50 : 0, c.task ? Math.ceil(c.task.max / 50) * 50 : 0);
    return { lo: 10, hi: top };
  }

  // sellDefaults opens a new sale a little under the median ask and asks a
  // quarter above it, or the running task's range.
  function sellDefaults(c, b) {
    if (c.task) return [c.task.min, c.task.max];
    if (c.schedule) return [c.schedule.min, c.schedule.max];
    if (c.ask == null) return [40, 80];
    const r5 = (v) => Math.max(b.lo, Math.min(b.hi, Math.round(v / 5) * 5));
    return [r5(c.ask * 0.85), r5(c.ask * 1.25)];
  }

  // Buyer states as the sell menu words them.
  const BUYER_WORD = {
    queued: 'waiting', negotiating: 'haggling', agreed: 'agreed, waiting to pay', offered: 'asked to pay', passed: 'passed',
    timed_out: 'timed out', bought: 'bought it', skipped: 'skipped', told_sold: 'told sold', parked: 'needs you', handed_off: 'handed to you',
  };
  const priceText = (t) => (t.min === t.max ? rupees(t.min) : `${rupees(t.min)}-${rupees(t.max)}`);
  const buyerName = (b) => b.name || fmtPhone(b.phone) || b.key;

  // openSell opens the sell menu for a ledger cell. A meal with no task goes
  // straight to the price range; a running task opens on its overview, with
  // views to mark it sold or edit its prices.
  function openSell(date, c, view = 'menu') {
    hideTip();
    const t = c.task;
    const head = el('div', 'sell-head');
    const h = el('h2', '', `${t ? 'Selling' : c.schedule ? 'Scheduled' : 'Sell'} ${messName(c.mess)} ${c.meal}${c.friend != null ? ` for ${c.friend || 'a friend'}` : ''}`);
    h.id = 'sellTitle';
    h.style.setProperty('color', messColor(c.mess));
    const live = now() >= c.start;
    head.append(h, el('div', 'sub', `${fmtDay(date)}, ${hm(c.start)}-${hm(c.end)} · ${live ? `ends in ${dur(c.end - now())}` : `starts in ${dur(c.start - now())}`}`));
    sheet.setAttribute('aria-labelledby', 'sellTitle');
    sheet.replaceChildren(head);
    if (!t && c.schedule && view !== 'prices') scheduleView(date, c);
    else if (!t) priceView(date, c);
    else if (view === 'prices') priceView(date, c);
    else if (view === 'sold') soldView(date, c);
    else menuView(date, c);
    modal.hidden = false;
    const first = sheet.querySelector('[data-focus]') || sheet.querySelector('input, select, button');
    if (first) first.focus();
  }

  // sellParts are the pieces every view shares: the error line and a row of
  // buttons, and run, which sends one action and closes on success.
  function sellParts() {
    const error = el('p', 'sell-err');
    error.setAttribute('role', 'alert');
    if (D().sell_off) error.textContent = D().sell_off;
    const acts = el('div', 'sell-acts');
    const btn = (label, cls, fn) => {
      const x = el('button', 'sell-btn ' + cls, label);
      x.type = 'button';
      x.addEventListener('click', fn);
      if (D().sell_off && (cls === 'gold' || cls === 'red')) x.disabled = true;
      return x;
    };
    // run sends body, or each of a list of bodies in turn, then closes. done
    // is the toast, or makes it from the last answer.
    const run = async (body, done) => {
      const btns = [...sheet.querySelectorAll('button')];
      for (const k of btns) k.disabled = true;
      error.textContent = '';
      try {
        let res;
        for (const b of Array.isArray(body) ? body : [body]) res = await send('POST', 'api/sell-action', b);
        closeModal();
        toast(typeof done === 'function' ? done(res) : done);
        load();
      } catch (err) {
        error.textContent = err.message;
        for (const k of btns) k.disabled = false;
      }
    };
    return { error, acts, btn, run };
  }

  function menuView(date, c) {
    const t = c.task;
    const { error, acts, btn, run } = sellParts();
    const sum = el('div', 'sell-sum');
    const stat = (k, v, cls) => { const d = el('div', 'st'); d.append(el('span', '', k), el('b', cls || '', v)); return d; };
    const talking = t.buyers.filter((b) => ['queued', 'negotiating', 'agreed', 'offered', 'parked'].includes(b.state));
    sum.append(stat('PRICE', priceText(t), 'gold'), stat('BUYERS', `${talking.length} active`), stat('ENDS', hm(c.end)));

    const list = el('ul', 'sell-buyers');
    list.setAttribute('aria-label', 'Buyers');
    if (!t.buyers.length) list.append(el('li', 'none', 'No replies yet.'));
    for (const b of t.buyers) {
      const li = el('li');
      li.append(el('span', 'who', buyerName(b)), el('span', 'what', (b.muted ? 'muted · ' : '') + (BUYER_WORD[b.state] || b.state)),
        el('span', 'amt', b.price ? rupees(b.price) : ''));
      list.append(li);
    }

    const tools = el('div', 'sell-tools');
    const tool = (glyph, label, hint, cls, fn) => {
      const x = btn('', 'sell-tool ' + cls, fn);
      x.append(el('b', '', glyph), el('span', '', label), el('small', '', hint));
      return x;
    };
    const sold = tool('✓', 'MARK SOLD', 'You sold it yourself', 'grn', () => openSell(date, c, 'sold'));
    sold.dataset.focus = '1';
    const stop = tool('✕', 'STOP SELLING', 'Deletes the offer posts', 'red', () => {
      if (stop.dataset.sure) return run({ action: 'cancel', id: t.id }, 'Stopped. The offer posts are being deleted.');
      stop.dataset.sure = '1';
      stop.querySelector('span').textContent = 'SURE? STOP';
      stop.querySelector('small').textContent = 'Click again to stop';
    });
    tools.append(sold, tool('₹', 'EDIT PRICES', priceText(t), 'gold-o', () => openSell(date, c, 'prices')), stop);
    // Marking sold and editing prices need sell tasks on, like stopping.
    if (D().sell_off) for (const x of tools.children) x.disabled = true;

    acts.append(btn('CLOSE', '', closeModal));
    const auto = el('p', 'sell-note auto-note');
    auto.hidden = !t.autopilot;
    auto.append(el('b', '', 'On autopilot. '), "Buyers who'd need you are skipped, and only WhatsApp Pay counts.");
    sheet.append(sum, list, tools, auto, error, acts);
  }

  // scheduleView is a sale waiting for its meal: when it goes up, at what
  // price, and buttons to start it now, change the price or drop it.
  function scheduleView(date, c) {
    const sc = c.schedule;
    const { error, acts, btn, run } = sellParts();
    const sum = el('div', 'sell-sum');
    const stat = (k, v, cls) => { const d = el('div', 'st'); d.append(el('span', '', k), el('b', cls || '', v)); return d; };
    sum.append(stat('PRICE', priceText(sc), 'gold'), stat('GOES UP', hm(sc.at)), stat('ENDS', hm(c.end)));
    const note = el('p', 'sell-note', `Nothing is posted until ${hm(sc.at)}, in ${dur(sc.at - now())}. Then it sells like any other sale till ${hm(c.end)}.`);

    const tools = el('div', 'sell-tools');
    const tool = (glyph, label, hint, cls, fn) => {
      const x = btn('', 'sell-tool ' + cls, fn);
      x.append(el('b', '', glyph), el('span', '', label), el('small', '', hint));
      return x;
    };
    const go = tool('▶', 'START NOW', sc.autopilot ? 'On autopilot' : 'Post the offer now', 'grn', () =>
      run({ action: 'start', date, meal: c.meal, min: sc.min, max: sc.max, now: true, autopilot: !!sc.autopilot }, `Selling your ${c.meal}${sc.autopilot ? ' on autopilot' : ''}`));
    go.dataset.focus = '1';
    // Autopilot is a switch: it saves at once and keeps the sheet open.
    const auto = tool('✈', 'AUTOPILOT', sc.autopilot ? 'On · never waits on you' : 'Off', 'auto' + (sc.autopilot ? ' on' : ''), async () => {
      auto.disabled = true;
      error.textContent = '';
      try {
        await send('POST', 'api/sell-action', { action: 'autopilot', id: sc.id, autopilot: !sc.autopilot });
        sc.autopilot = !sc.autopilot;
        openSell(date, c);
        load();
      } catch (err) {
        error.textContent = err.message;
        auto.disabled = false;
      }
    });
    auto.setAttribute('role', 'switch');
    auto.setAttribute('aria-checked', String(!!sc.autopilot));
    const drop = tool('✕', 'UNSCHEDULE', 'Keep the meal', 'red', () => {
      if (drop.dataset.sure) return run({ action: 'unschedule', id: sc.id }, `Unscheduled. Your ${c.meal} won't go up.`);
      drop.dataset.sure = '1';
      drop.querySelector('span').textContent = 'SURE?';
      drop.querySelector('small').textContent = 'Click again to drop it';
    });
    tools.classList.add('pairs');
    tools.append(go, auto, tool('₹', 'EDIT PRICES', priceText(sc), 'gold-o', () => openSell(date, c, 'prices')), drop);
    if (D().sell_off) for (const x of tools.children) x.disabled = true;

    const autoNote = el('p', 'sell-note auto-note');
    autoNote.hidden = !sc.autopilot;
    autoNote.append(el('b', '', 'Autopilot. '), "Sells without waiting on you. Anyone who'd need you is skipped, even at a higher price: paying some other way, going below your lowest, replies the bot can't read, saying they paid when no payment came. Only WhatsApp Pay counts, never a screenshot.");
    acts.append(btn('CLOSE', '', closeModal));
    sheet.append(sum, note, tools, autoNote, error, acts);
  }

  // priceSlider is the two-handle price range for a cell-like c (ask,
  // task). It returns its nodes and a getter for [lowest, opening].
  function priceSlider(c) {
    const t = c.task;
    const b = sellBounds(c);
    let [lo, hi] = sellDefaults(c, b);
    // Both handles move in ₹5 steps. A running task can't switch between
    // haggling and a fixed price, so a fixed one moves both handles together
    // and a haggling one keeps them a step apart.
    const STEP = 5;
    const snap = (v) => Math.max(b.lo, Math.min(b.hi, Math.round(v / STEP) * STEP));
    const fixed = t && t.min === t.max;
    const gap = t && !fixed ? STEP : 0;
    lo = snap(lo);
    hi = Math.max(snap(hi), Math.min(b.hi, lo + gap));

    const vals = el('div', 'sell-vals');
    const vMin = el('b'), vMax = el('b');
    const lMin = el('div', 'sv'), lMax = el('div', 'sv');
    lMin.append(el('span', '', fixed ? 'PRICE' : 'LOWEST'), vMin);
    lMax.append(el('span', '', 'OPENS AT'), vMax);
    vals.append(lMin);
    if (!fixed) vals.append(lMax);

    const dual = el('div', 'dual');
    const fill = el('i', 'fill');
    const track = el('div', 'track');
    track.append(fill);
    if (c.ask != null) {
      const m = el('i', 'med');
      m.style.setProperty('left', `${((c.ask - b.lo) / (b.hi - b.lo)) * 100}%`);
      track.append(m);
    }
    const range = (v, label) => {
      const r = el('input');
      Object.assign(r, { type: 'range', min: b.lo, max: b.hi, step: STEP, value: v });
      r.setAttribute('aria-label', label);
      return r;
    };
    const rMin = range(lo, fixed ? 'Price' : 'Lowest price'), rMax = range(hi, 'Opening price');
    rMin.dataset.focus = '1';
    dual.append(track, rMin);
    if (!fixed) dual.append(rMax);

    const axis = el('div', 'sell-axis');
    axis.append(el('span', '', rupees(b.lo)), c.ask != null ? el('span', 'med-l', `median ${rupees(Math.round(c.ask))}`) : el('span'), el('span', '', rupees(b.hi)));

    const note = el('p', 'sell-note');
    note.setAttribute('aria-live', 'polite');
    const draw = () => {
      vMin.textContent = rupees(lo);
      vMax.textContent = rupees(hi);
      rMin.setAttribute('aria-valuetext', rupees(lo));
      rMax.setAttribute('aria-valuetext', rupees(hi));
      fill.style.setProperty('left', `${((lo - b.lo) / (b.hi - b.lo)) * 100}%`);
      fill.style.setProperty('right', `${100 - ((hi - b.lo) / (b.hi - b.lo)) * 100}%`);
      note.textContent = lo === hi
        ? `Posts the meal at ${rupees(lo)} and sells to the first buyer who pays it.`
        : `Opens at ${rupees(hi)} and haggles down toward the median, reaching ${rupees(lo)} only as the meal nears its end. Never goes below ${rupees(lo)}.`;
      if (t) note.textContent += ' Prices already quoted or agreed stand.';
    };
    // The thumb that last moved sits on top, so two thumbs at the same spot
    // can still be pulled apart.
    rMin.addEventListener('input', () => {
      if (fixed) hi = lo = +rMin.value;
      else { lo = Math.min(+rMin.value, hi - gap); rMin.value = lo; }
      rMin.style.zIndex = 3; rMax.style.zIndex = 2; draw();
    });
    rMax.addEventListener('input', () => { hi = Math.max(+rMax.value, lo + gap); rMax.value = hi; rMax.style.zIndex = 3; rMin.style.zIndex = 2; draw(); });
    draw();
    return { nodes: [vals, dual, axis, note], get: () => [lo, hi] };
  }

  function priceView(date, c) {
    const t = c.task;
    const slider = priceSlider(c);
    const { error, acts, btn, run } = sellParts();
    if (t) {
      acts.append(btn('BACK', '', () => openSell(date, c)),
        btn('SAVE PRICES', 'gold', () => { const [min, max] = slider.get(); run({ action: 'prices', id: t.id, min, max }, 'Prices updated'); }));
    } else if (c.schedule) {
      // A schedule's prices change by scheduling it again.
      acts.append(btn('BACK', '', () => openSell(date, c)),
        btn('SAVE', 'gold', () => {
          const [min, max] = slider.get();
          // The new schedule keeps autopilot.
          run([{ action: 'unschedule', id: c.schedule.id }, { action: 'start', date, meal: c.meal, min, max, autopilot: !!c.schedule.autopilot }],
            (res) => startedToast(c.meal, res));
        }));
    } else if (listAt(c)) {
      const at = listAt(c);
      const when = el('p', 'sell-when');
      when.append('Goes up at ', el('b', '', hm(at)), `, ${listWord(c.meal)}, and sells till ${hm(c.end)}. Nothing is posted before then.`);
      slider.nodes.push(when);
      acts.append(btn('CANCEL', '', closeModal),
        btn('START NOW', '', () => { const [min, max] = slider.get(); run({ action: 'start', date, meal: c.meal, min, max, now: true }, `Selling your ${c.meal}`); }),
        btn(`SCHEDULE ${hm(at)}`, 'gold', () => { const [min, max] = slider.get(); run({ action: 'start', date, meal: c.meal, min, max }, (res) => startedToast(c.meal, res)); }));
    } else {
      acts.append(btn('CANCEL', '', closeModal),
        btn('CONFIRM SALE', 'gold', () => { const [min, max] = slider.get(); run({ action: 'start', date, meal: c.meal, min, max }, (res) => startedToast(c.meal, res)); }));
    }
    sheet.append(...slider.nodes, error, acts);
  }

  function soldView(date, c) {
    const t = c.task;
    const { error, acts, btn, run } = sellParts();
    const form = el('form', 'sell-form');
    const field = (label, input) => { const l = el('label', '', label); l.append(input); form.append(l); return l; };
    const input = (type, attrs = {}) => { const i = el('input'); Object.assign(i, { type, ...attrs }); return i; };

    const pick = el('select');
    pick.dataset.focus = '1';
    for (const b of t.buyers) pick.append(new Option(`${buyerName(b)}${b.price ? ` · ${rupees(b.price)}` : ''}`, b.key));
    pick.append(new Option('Someone else', ''));
    // Default to the buyer furthest along.
    const order = ['bought', 'offered', 'agreed', 'handed_off', 'parked', 'negotiating', 'queued'];
    const rank = (x) => { const i = order.indexOf(x.state); return i < 0 ? order.length : i; };
    const best = t.buyers.slice().sort((x, y) => rank(x) - rank(y))[0];
    pick.value = best ? best.key : '';
    field('Buyer', pick);
    const phone = input('tel', { placeholder: '+91 98765 43210', autocomplete: 'off' });
    const name = input('text', { autocomplete: 'off' });
    const other = [field('Their phone, with country code', phone), field('Or their name', name)];
    const price = input('number', { min: 1, max: 10000, step: 1, required: true });
    field('Sold for (₹)', price);
    const note = input('text', { placeholder: 'e.g. paid cash', autocomplete: 'off' });
    field('Note (optional)', note);

    const sync = () => {
      const b = t.buyers.find((x) => x.key === pick.value);
      for (const l of other) l.hidden = !!b;
      price.value = b && b.price ? Math.round(b.price) : price.value || Math.round(t.max);
    };
    pick.addEventListener('change', sync);
    sync();

    form.append(el('p', 'sell-note', 'Only confirm once the meal is sold and paid for. The bot stops selling, records the sale, ticks the offer posts and tells other buyers it\'s sold. It sends your buyer nothing.'));
    const submit = btn('CONFIRM SOLD', 'gold', () => {});
    submit.type = 'submit';
    acts.append(btn('BACK', '', () => openSell(date, c)), submit);
    form.append(error, acts);
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      const body = { action: 'sold', id: t.id, price: Number(price.value), note: note.value.trim() };
      if (pick.value) body.buyer = pick.value;
      else { body.buyer = phone.value.replace(/[^\d+]/g, ''); body.name = name.value.trim(); }
      if (!body.buyer && !body.name) { error.textContent = 'Enter their phone number or name.'; return; }
      if (!(body.price >= 1)) { error.textContent = 'Enter the price it sold for.'; return; }
      run(body, 'Marked sold');
    });
    sheet.append(form);
  }


  return { open: openSell, priceSlider, priceText, listAt, listWord };
}
