'use strict';

// The sell sheet: the popup that starts, schedules, reprices, marks sold
// (sending the buyer the QR) or stops the sale of one of your meals. The sales page and the board both
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
  // has passed), else that time. Never before c.opens, when the meal before
  // it ends. It matches StartOrSchedule on the server.
  function listAt(c) {
    let at = c.start + (D().list_at || 0);
    if (at >= c.end) at = c.start;
    if (c.opens && at < c.opens) at = c.opens;
    return now() >= c.start || now() >= at ? 0 : at;
  }
  // tooEarly is true while the meal before c is still being served: an
  // offer posted then reads as one for that meal, so Start now becomes
  // "send after snacks ends" and the server schedules it for c.opens.
  const tooEarly = (c) => !!c.opens && now() < c.opens;
  // before is the meal c waits for: "snacks".
  const before = (c) => c.opens_after || 'the meal before';
  const afterLabel = (c) => `SEND AFTER ${(c.opens_after || 'meal').toUpperCase()} ENDS`;
  const afterTitle = (c) => `Posts at ${hm(c.opens)}, once ${before(c)} ends. An offer sent during ${c.opens_after || 'another meal'} gets read as one for ${c.opens_after || 'that meal'}.`;
  // listWord says where list_at puts the offer: "15 min before lunch starts".
  function listWord(meal) {
    const s = D().list_at || 0;
    return s === 0 ? `when ${meal} starts` : `${dur(Math.abs(s))} ${s < 0 ? 'before' : 'after'} ${meal} starts`;
  }
  // listWhen is listWord for c's offer going up at at, or says it waits
  // for the meal before when that pushed it later.
  function listWhen(c, at) {
    let want = c.start + (D().list_at || 0);
    if (want >= c.end) want = c.start;
    return c.opens && at === c.opens && want < c.opens ? `once ${before(c)} ends` : listWord(c.meal);
  }
  const startedToast = (meal, res) => (res && res.at ? `Scheduled. Your ${meal} goes up at ${hm(res.at)}` : `Selling your ${meal}`);
  // what names what c sells: "dinner", or "dinner egg" for an extra sold
  // apart from its meal. slot is the meal the server knows it by: "dinner",
  // or the extra's own slot, "dinner#e1".
  const what = (c) => (c.extra ? `${c.meal} ${c.extra.toLowerCase()}` : c.meal);
  const slot = (c) => c.slot || c.meal;


  // sellBounds is the slider's scale: wide enough for the median ask with
  // room to haggle above it.
  function sellBounds(c) {
    // An extra costs a fraction of a meal: ₹1 up to three times the mess's
    // rate, at least ₹50.
    if (c.extra) {
      const top = Math.max(50, Math.ceil(((c.rate || 20) * 3) / 5) * 5, c.task ? Math.ceil(c.task.max / 5) * 5 : 0, c.schedule ? Math.ceil(c.schedule.max / 5) * 5 : 0);
      return { lo: 1, hi: top };
    }
    const top = Math.max(150, c.ask ? Math.ceil((c.ask * 2.5) / 50) * 50 : 0, c.task ? Math.ceil(c.task.max / 50) * 50 : 0, c.schedule ? Math.ceil(c.schedule.max / 50) * 50 : 0);
    return { lo: c.planner ? 1 : 10, hi: top };
  }

  // sellDefaults opens a new sale a little under the median ask and asks a
  // quarter above it, or the running task's range.
  function sellDefaults(c, b) {
    if (c.task) return [c.task.min, c.task.max];
    if (c.schedule) return [c.schedule.min, c.schedule.max];
    if (c.extra) return c.rate > 0 ? [Math.max(1, Math.round(c.rate * 0.7)), Math.max(1, Math.round(c.rate))] : [10, 15];
    if (c.ask == null) return [40, 80];
    const r5 = (v) => Math.max(b.lo, Math.min(b.hi, Math.round(v / 5) * 5));
    return [r5(c.ask * 0.85), r5(c.ask * 1.25)];
  }

  // Buyer states as the sell menu words them.
  const BUYER_WORD = {
    queued: 'waiting', negotiating: 'haggling', agreed: 'agreed, waiting to pay', offered: 'asked to pay', passed: 'passed',
    timed_out: 'timed out', bought: 'bought it', skipped: 'skipped', told_sold: 'told sold', parked: 'needs you', handed_off: 'handed to you',
  };
  // isSold: c is one of your meals with a sale recorded and no task on it.
  const isSold = (c) => c.friend == null && c.sale != null && (c.state === 'sold' || c.state === 'sending');
  const priceText = (t) => (t.min === t.max ? rupees(t.min) : `${rupees(t.min)}-${rupees(t.max)}`);
  const buyerName = (b) => b.name || fmtPhone(b.phone) || b.key;

  // openSell opens the sell menu for a ledger cell. A meal with no task goes
  // straight to the price range; a running task opens on its overview, with
  // views to mark it sold or edit its prices.
  function openSell(date, c, view = 'menu') {
    hideTip();
    const t = c.task;
    const head = el('div', 'sell-head');
    const sold = !t && isSold(c);
    const h = el('h2', '', `${t ? 'Selling' : c.schedule ? 'Scheduled' : sold ? 'Sold' : 'Sell'} ${messName(c.mess)} ${c.meal}${c.extra ? ` · ${c.extra}` : ''}${c.friend != null ? ` for ${c.friend || 'a friend'}` : ''}`);
    h.id = 'sellTitle';
    h.style.setProperty('color', messColor(c.mess));
    const live = now() >= c.start;
    head.append(h, el('div', 'sub', `${fmtDay(date)}, ${hm(c.start)}-${hm(c.end)} · ${live ? `ends in ${dur(c.end - now())}` : `starts in ${dur(c.start - now())}`}`));
    sheet.setAttribute('aria-labelledby', 'sellTitle');
    sheet.replaceChildren(head);
    if (sold) saleView(date, c);
    else if (!t && c.schedule && view !== 'prices') scheduleView(date, c);
    // An extra is put up from the meal panel, never from here, so one with
    // no task or schedule (armed, handed off) only says where it stands.
    else if (!t && !c.schedule && c.extra) extraView(date, c);
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
    const sold = tool('✓', 'SOLD + SEND QR', 'Sends them the QR', 'grn', () => openSell(date, c, 'sold'));
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
    auto.append(el('b', '', 'On autopilot. '), "Buyers who'd need you are skipped. Screenshots count only if they do in Settings.");
    sheet.append(sum, list, tools, auto, error, acts);
  }

  // saleView is a meal the bot (or you) already sold. Its one action
  // deletes the sale, for when it fell through: the meal goes back to
  // unsold and can be put up again.
  function saleView(date, c) {
    const s = D().sales[c.sale];
    const { error, acts, btn, run } = sellParts();
    const sum = el('div', 'sell-sum');
    const stat = (k, v, cls) => { const d = el('div', 'st'); d.append(el('span', '', k), el('b', cls || '', v)); return d; };
    sum.append(stat('PAID', rupees(s.amount), 'gold'), stat('BUYER', s.name || fmtPhone(s.phone) || 'Unknown'), stat('AT', s.paid_at ? hm(s.paid_at) : '-'));
    const note = el('p', 'sell-note', `Deleting the sale takes it out of your ledger and totals, and you can sell the ${what(c)} again. `
      + 'The bot sends nothing, and the buyer still has the QR it sent them. Reset your QR token on the mess portal if they shouldn\'t be able to use it.');
    const tools = el('div', 'sell-tools one');
    const del = btn('', 'sell-tool red', () => {
      if (del.dataset.sure) return run({ action: 'delete-sale', date, meal: slot(c) }, `Sale deleted. Your ${what(c)} is unsold again.`);
      del.dataset.sure = '1';
      del.querySelector('span').textContent = 'SURE? DELETE';
      del.querySelector('small').textContent = 'Click again to delete';
    });
    del.append(el('b', '', '✕'), el('span', '', 'DELETE SALE'), el('small', '', 'Undo it and sell again'));
    // Deleting only touches the ledger, so it works with sell tasks off.
    del.disabled = false;
    tools.append(del);
    if (D().sell_off) error.textContent = '';
    // Focus starts on CLOSE, not on the destructive button.
    const close = btn('CLOSE', '', closeModal);
    close.dataset.focus = '1';
    acts.append(close);
    sheet.append(sum, note, tools, error, acts);
  }

  // extraView is an extra slot between selling and sold: armed for a buyer
  // to pay, or handed to you. There is nothing to change from here.
  function extraView(date, c) {
    const { error, acts, btn } = sellParts();
    const s = c.sale != null ? D().sales[c.sale] : null;
    const sum = el('div', 'sell-sum');
    const stat = (k, v, cls) => { const d = el('div', 'st'); d.append(el('span', '', k), el('b', cls || '', v)); return d; };
    const word = { armed: 'ARMED', handoff: 'HANDED OFF', sending: 'PAID · SENDING' }[c.state] || String(c.state || '').toUpperCase();
    sum.append(stat('STATE', word, 'gold'), stat('AMOUNT', s ? rupees(s.amount) : '-'), stat('BUYER', s ? s.name || fmtPhone(s.phone) || 'Unknown' : '-'));
    const note = el('p', 'sell-note', c.state === 'armed' ? `Waiting for the buyer to pay for the ${what(c)}. The bot sends the QR once they do.`
      : c.state === 'handoff' ? 'The bot handed this buyer to you and sent them nothing.' : `The ${what(c)} is on its way to the buyer.`);
    if (D().sell_off) error.textContent = '';
    const close = btn('CLOSE', '', closeModal);
    close.dataset.focus = '1';
    acts.append(close);
    sheet.append(sum, note, error, acts);
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
    const early = tooEarly(c);
    const go = early
      ? tool('⏰', afterLabel(c), `At ${hm(c.opens)}`, 'grn', () =>
        run({ action: 'start', date, meal: slot(c), min: sc.min, max: sc.max, now: true, autopilot: !!sc.autopilot }, (res) => startedToast(what(c), res)))
      : tool('▶', 'START NOW', sc.autopilot ? 'On autopilot' : 'Post the offer now', 'grn', () =>
        run({ action: 'start', date, meal: slot(c), min: sc.min, max: sc.max, now: true, autopilot: !!sc.autopilot }, (res) => res && res.at ? startedToast(what(c), res) : `Selling your ${what(c)}${sc.autopilot ? ' on autopilot' : ''}`));
    if (early) go.title = afterTitle(c);
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
    const drop = tool('✕', 'UNSCHEDULE', c.extra ? `Keep the ${c.extra.toLowerCase()}` : 'Keep the meal', 'red', () => {
      if (drop.dataset.sure) return run({ action: 'unschedule', id: sc.id }, `Unscheduled. Your ${what(c)} won't go up.`);
      drop.dataset.sure = '1';
      drop.querySelector('span').textContent = 'SURE?';
      drop.querySelector('small').textContent = 'Click again to drop it';
    });
    tools.classList.add('pairs');
    tools.append(go, auto, tool('₹', 'EDIT PRICES', priceText(sc), 'gold-o', () => openSell(date, c, 'prices')), drop);
    if (D().sell_off) for (const x of tools.children) x.disabled = true;

    const autoNote = el('p', 'sell-note auto-note');
    autoNote.hidden = !sc.autopilot;
    autoNote.append(el('b', '', 'Autopilot. '), "Sells without waiting on you. Anyone who'd need you is skipped, even at a higher price: paying some other way, going below your lowest, replies the bot can't read, saying they paid when no payment came. A screenshot counts only if you turned that on in Settings, and nothing checks it.");
    acts.append(btn('CLOSE', '', closeModal));
    sheet.append(sum, note, tools, autoNote, error, acts);
  }

  // priceSlider is the price picker for a cell-like c (ask, task). It
  // returns its nodes and a getter for [lowest, opening], and calls
  // onChange(lo, hi) after every change, typed, stepped or dragged. A running task
  // can't switch between haggling and a fixed price, so a fixed one shows
  // one price and a haggling one keeps its ends a rupee apart.
  function priceSlider(c, onChange) {
    const t = c.task;
    const b = sellBounds(c);
    const [lo, hi] = sellDefaults(c, b);
    const note = el('p', 'sell-note');
    note.setAttribute('aria-live', 'polite');
    const pr = priceRange({
      lo, hi, bounds: b,
      fixed: t && t.min === t.max,
      gap: t ? 1 : 0,
      marks: c.ask != null ? [{ at: c.ask, cls: 'med' }] : [],
      axis: [rupees(b.lo), c.ask != null ? `median ${rupees(Math.round(c.ask))}` : '', rupees(b.hi)],
      onChange: (lo, hi) => {
        const thing = c.extra ? `each ${c.extra.toLowerCase()}` : 'the meal';
        note.textContent = lo === hi
          ? `Posts ${thing} at ${rupees(lo)} and sells to the first buyer who pays it.`
          : `Opens at ${rupees(hi)} and haggles down${c.ask != null ? ' toward the median' : ''}, reaching ${rupees(lo)} only as the meal nears its end. Never goes below ${rupees(lo)}.`;
        if (t) note.textContent += ' Prices already quoted or agreed stand.';
        if (onChange) onChange(lo, hi);
      },
    });
    pr.focus.dataset.focus = '1';
    return { nodes: [...pr.nodes, note], get: pr.get };
  }

  function priceView(date, c) {
    const t = c.task;
    const slider = priceSlider(c);
    const { error, acts, btn, run } = sellParts();
    if (t) {
      acts.append(btn('BACK', '', () => openSell(date, c)),
        btn('SAVE PRICES', 'gold', () => { const [min, max] = slider.get(); run({ action: 'prices', id: t.id, min, max }, 'Prices updated'); }));
    } else if (c.schedule) {
      // Edit the waiting schedule in place, keeping its saved posting time.
      // Unscheduling it would also keep the meal out of recurring rules.
      acts.append(btn('BACK', '', () => openSell(date, c)),
        btn('SAVE', 'gold', () => {
          const [min, max] = slider.get();
          run({ action: 'plan', id: c.schedule.id, revision: c.schedule.revision, at: c.schedule.at, date, meal: slot(c), min, max, autopilot: !!c.schedule.autopilot },
            (res) => startedToast(what(c), res));
        }));
    } else if (listAt(c)) {
      const at = listAt(c);
      const when = el('p', 'sell-when');
      when.append('Goes up at ', el('b', '', hm(at)), `, ${listWhen(c, at)}, and sells till ${hm(c.end)}. Nothing is posted before then.`);
      slider.nodes.push(when);
      const startNow = () => { const [min, max] = slider.get(); run({ action: 'start', date, meal: c.meal, min, max, now: true }, (res) => (res && res.at ? startedToast(c.meal, res) : `Selling your ${c.meal}`)); };
      const schedule = btn(`SCHEDULE ${hm(at)}`, 'gold', () => { const [min, max] = slider.get(); run({ action: 'start', date, meal: c.meal, min, max }, (res) => startedToast(c.meal, res)); });
      if (!tooEarly(c)) acts.append(btn('CANCEL', '', closeModal), btn('START NOW', '', startNow), schedule);
      else {
        // Before the meal before ends, Start now waits for it. When list_at
        // lands there too, both buttons would do the same, so only one shows.
        const after = btn('⏰ ' + afterLabel(c), at === c.opens ? 'gold' : '', startNow);
        after.title = afterTitle(c);
        acts.append(btn('CANCEL', '', closeModal), after);
        if (at !== c.opens) acts.append(schedule);
      }
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
    const other = [field('Their phone, with country code', phone), field('Their name (optional)', name)];
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

    form.append(el('p', 'sell-note', c.extra
      ? `Only confirm once the ${c.extra.toLowerCase()} is sold and paid for. The bot stops selling, records the sale, sends your buyer the QR, ticks the offer posts and tells other buyers it's sold.`
      : 'Only confirm once the meal is sold and paid for. The bot stops selling, records the sale, sends your buyer the meal QR, ticks the offer posts and tells other buyers it\'s sold.'));
    const submit = btn('CONFIRM + SEND QR', 'gold', () => {});
    submit.type = 'submit';
    acts.append(btn('BACK', '', () => openSell(date, c)), submit);
    form.append(error, acts);
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      const body = { action: 'sold', id: t.id, price: Number(price.value), note: note.value.trim(), send_qr: true };
      if (pick.value) body.buyer = pick.value;
      else { body.buyer = phone.value.replace(/[^\d+]/g, ''); body.name = name.value.trim(); }
      if (!body.buyer) { error.textContent = 'Enter their phone number, so the bot can send the QR.'; return; }
      if (!(body.price >= 1)) { error.textContent = 'Enter the price it sold for.'; return; }
      run(body, 'Sold. The QR is on its way.');
    });
    sheet.append(form);
  }


  // friendCell turns a friend's meal (api/sales friends) into the cell
  // openSell takes. friend is '' for an unnamed friend, so the title still
  // says "for a friend".
  // findExtra finds the extra slot named slotName ("dinner#e1") on meal
  // cell c, as [extra, slot], or null.
  function findExtra(c, slotName) {
    for (const x of (c && c.extras) || []) for (const s of x.slots || []) if (s.slot === slotName) return [x, s];
    return null;
  }

  // extraCell turns one of meal cell c's extra slots into the cell openSell
  // takes: the meal's times, the slot's task or schedule, and its sale in
  // api/sales once one is recorded.
  function extraCell(date, c, x, s) {
    const sales = D().sales || [];
    const i = sales.findIndex((v) => v.date === date && v.slot === s.slot);
    return {
      meal: c.meal, slot: s.slot, extra: x.name, rate: x.rate, mess: x.mess || c.mess, diet: c.diet,
      start: c.start, end: c.end, opens: c.opens, opens_after: c.opens_after,
      state: s.state, task: s.task || null, schedule: s.schedule || null, sale: i >= 0 ? i : null, ask: null,
    };
  }

  const friendCell = (f) => ({ meal: f.meal, mess: f.mess, diet: f.diet, start: f.start, end: f.end, ask: f.ask, task: f.task,
    schedule: f.schedule, opens: f.opens, opens_after: f.opens_after, friend: f.friend || '' });

  return { open: openSell, isSold, friendCell, findExtra, extraCell, priceSlider, priceText, listAt, listWord, listWhen, sellBounds, sellDefaults };
}
