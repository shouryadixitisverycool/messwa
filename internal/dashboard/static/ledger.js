'use strict';

// The meal calendar on the sales page: every registered meal from four
// weeks back to four weeks ahead, one Monday-to-Sunday week at a time.
// Past meals show what became of them; meals still to come can be sold
// or scheduled from the panel beside the grid.
//
// Clicking a meal opens it in the panel. Shift-, Ctrl- or Cmd-clicking,
// clicking a day or meal heading, or turning on "Pick several" picks more
// than one meal still to come, and the panel then sets them all at once.
// Every button in the panel saves straight away; nothing waits for a save
// bar.

let calWeek = '';        // Monday of the week shown
let calPicked = [];      // "date|meal" keys, in the order picked
let calMulti = false;    // taps add to the pick instead of replacing it
let calBusy = false;     // a request from the panel is in flight
let calForm = null;      // the panel's form values, kept across reloads
let calNote = null;      // {key, error|text}: what the last action did
let calPanelSig = '';    // what the panel was last drawn from

const calKey = (date, c) => `${date}|${c.meal}`;
const calStep = 5;       // posting time moves in 5-minute steps
const mondayOf = (date) => addDays(date, -((dayOf(date).getUTCDay() + 6) % 7));

// calCells maps every date with data to its cells. Days before today come
// from the ledger, days after it from the planner's four weeks. Today
// comes from the ledger, whose cells say which meal is up next.
function calCells() {
  const m = new Map();
  for (const d of D.days) m.set(d.date, d.cells);
  for (const d of D.upcoming || []) if (d.date > D.today) m.set(d.date, d.cells);
  return m;
}
const calFirst = () => mondayOf(D.days[0]?.date || D.today);
const calLast = () => mondayOf((D.upcoming || []).at(-1)?.date || D.today);
const weekDates = () => Array.from({ length: 7 }, (_, i) => addDays(calWeek, i));

// calPlannable: a meal still to come that the panel can schedule, or
// reschedule while its schedule waits. Running sales go to the sell sheet.
const calPlannable = (c) => !!D.owner && !c.task && c.end > now()
  && (c.state === 'open' || (c.state === 'scheduled' && c.schedule && (!c.schedule.state || c.schedule.state === 'waiting')));

function calFind(key) {
  const [date, meal] = key.split('|');
  const c = calCells().get(date)?.find((x) => x.meal === meal);
  return c ? [date, c] : null;
}
const calPicks = () => calPicked.map(calFind).filter(Boolean);

// ---------- posting time ----------

// The server takes a posting time from 30 minutes before serving starts to
// 90 after, and before the meal ends. Offsets are minutes from the start.
const OFF_MIN = -30;
const calOffMax = (c) => Math.min(90, Math.floor((c.end - c.start) / 60) - 1);
const calClampOff = (c, off) => Math.max(OFF_MIN, Math.min(calOffMax(c), off));
const calDefaultOff = () => Math.round((D.list_at || 0) / 60);
// calAt is when an offer set off minutes from c's start really goes up:
// never while the meal before it is still being served.
const calAt = (c, off) => Math.max(c.start + calClampOff(c, off) * 60, c.opens || 0);
const offWords = (off) => (off === 0 ? 'as serving starts' : `${Math.abs(off)} min ${off < 0 ? 'before' : 'after'} serving starts`);

// ---------- what Save sends ----------

// calForms gives the form's starting values for the picked meals: the
// saved schedule when they all share one, else prices around the median.
function calFormFor(picks) {
  const scs = picks.map(([, c]) => c.schedule).filter(Boolean);
  const off = (c) => Math.round((c.schedule.at - c.start) / 60);
  const same = scs.length === picks.length && picks.every(([, c]) => c.schedule.min === scs[0].min && c.schedule.max === scs[0].max
    && !!c.schedule.autopilot === !!scs[0].autopilot && off(c) === off(picks[0][1]));
  if (same) return { min: scs[0].min, max: scs[0].max, off: off(picks[0][1]), autopilot: !!scs[0].autopilot };
  const c = picks.find(([, x]) => x.ask != null)?.[1] || picks[0][1];
  const [min, max] = sellDefaults({ ...c, schedule: null, planner: true }, sellBounds({ ...c, planner: true }));
  return { min, max, off: calDefaultOff(), autopilot: false };
}

// calPlanBody is the sell action that sets c to the form: a new schedule,
// or an edit of the waiting one at the revision the page last read.
function calPlanBody(date, c, f) {
  const sc = c.schedule;
  return { action: 'plan', id: sc?.id || 0, revision: sc?.revision || 0, date, meal: c.meal, min: f.min, max: f.max, at: c.start + calClampOff(c, f.off) * 60, autopilot: !!f.autopilot };
}

// calUnchanged: c's saved schedule already matches the form.
const calUnchanged = (c, f) => !!c.schedule && c.schedule.min === f.min && c.schedule.max === f.max
  && !!c.schedule.autopilot === !!f.autopilot && c.schedule.at === calAt(c, f.off);

function calValid(f) {
  return Number.isInteger(f.min) && Number.isInteger(f.max) && f.min >= 1 && f.max >= f.min && f.max <= 10000;
}

// ---------- the grid ----------

function renderCalendar() {
  const first = calFirst(), last = calLast();
  if (!calWeek) calWeek = mondayOf(D.today);
  calWeek = calWeek < first ? first : calWeek > last ? last : calWeek;
  const cells = calCells();
  // Picks that ended or stopped being plannable drop out; one meal open in
  // the panel stays whatever it is.
  if (calPicked.length > 1) calPicked = calPicked.filter((k) => { const hit = calFind(k); return hit && calPlannable(hit[1]); });
  if (calPicked.length === 1 && !calFind(calPicked[0])) calPicked = [];
  const picked = new Set(calPicked);
  const dates = weekDates();

  const g = $('lgrid');
  g.replaceChildren(el('div'));
  for (const date of dates) {
    const day = cells.get(date);
    const plannable = (day || []).filter((c) => calPlannable(c));
    const h = el(plannable.length ? 'button' : 'div', 'dh' + (date === D.today ? ' today' : date > D.today ? ' later' : ''),
      date === D.today ? 'TODAY' : `${fmt(date, { weekday: 'short' }).toUpperCase()} ${dayOf(date).getUTCDate()}`);
    const total = sum(D.sales.filter((s) => isSold(s) && s.date === date).map((s) => s.amount));
    const queued = (day || []).filter((c) => c.schedule || c.task).length;
    if (!day) h.append(el('span', 'none', ' '));
    else if (total) h.append(el('span', '', rupees(total)));
    else if (queued && date >= D.today) { const s = el('span', 'q', String(queued)); s.append(el('i', '', ' TO SELL')); h.append(s); }
    else h.append(el('span', 'none', '-'));
    if (plannable.length) {
      h.type = 'button';
      h.disabled = calBusy;
      h.dataset.pick = plannable.map((c) => calKey(date, c)).join(',');
      h.title = `Pick every meal on ${fmtDay(date)} still to sell`;
    }
    g.append(h);
  }

  // Skip a meal row when nothing in it was registered this week.
  const cellAt = (date, meal) => cells.get(date)?.find((x) => x.meal === meal);
  const shown = meals().filter((meal) => dates.some((date) => { const c = cellAt(date, meal); return c && c.state !== 'none'; }));
  for (const meal of shown) {
    const plannable = dates.flatMap((date) => { const c = cellAt(date, meal); return c && calPlannable(c) ? [calKey(date, c)] : []; });
    const mh = el(plannable.length ? 'button' : 'div', 'mh');
    mh.append(svgEl(icon(meal)));
    mh.title = cap(meal);
    if (plannable.length) {
      mh.type = 'button';
      mh.disabled = calBusy;
      mh.dataset.pick = plannable.join(',');
      mh.title = `Pick every ${meal} this week still to sell`;
      mh.setAttribute('aria-label', mh.title);
    }
    g.append(mh);
    for (const date of dates) {
      const c = cellAt(date, meal);
      if (!c) { g.append(el('div', 'slot out')); continue; }
      if (c.state === 'none') { g.append(el('div', 'slot')); continue; }
      g.append(calCellNode(date, c, picked));
    }
  }
  if (!shown.length) {
    const empty = el('div', 'empty-note', 'Nothing registered this week.');
    empty.style.setProperty('grid-column', '1 / -1');
    g.append(empty);
  }

  renderCalMeta(dates, cells);
  $('pgPrev').disabled = calWeek <= first;
  $('pgNext').disabled = calWeek >= last;
  $('pgToday').hidden = calWeek === mondayOf(D.today);
  $('calMulti').hidden = !D.owner;
  $('calMulti').setAttribute('aria-pressed', String(calMulti));
  $('calMulti').classList.toggle('on', calMulti);
  renderCalPanel();
  if (tipFor && !tipFor.isConnected) hideTip();
}

function calCellNode(date, c, picked) {
  const key = calKey(date, c);
  const n = cellNode(date, c);
  // Every meal still to come can be sold here, so the call-to-sell border
  // would be everywhere. The next meal keeps it.
  if (!c.sellable || date > D.today) n.classList.remove('sellable');
  n.tabIndex = 0;
  n.setAttribute('role', 'button');
  n.classList.toggle('picked', picked.has(key));
  n.classList.toggle('active', calPicked.length === 1 && calPicked[0] === key);
  if (picked.has(key) && calPicked.length > 1) n.append(el('span', 'pick-mark', '✓'));
  if (c.kept && c.state === 'open') { n.classList.add('kept'); n.append(el('span', 'stamp long', 'KEPT')); }
  if (c.schedule_issue && !c.schedule && c.state === 'open') n.classList.add('issue');
  if (c.schedule?.retry_at) n.classList.add('retry');
  const what = `${cap(c.meal)}, ${fmtDay(date)}`;
  n.setAttribute('aria-label', `${what}: ${c.task ? 'selling' : c.schedule ? `scheduled for ${hm(c.schedule.at)}` : c.state}${picked.has(key) ? ', picked' : ''}`);
  if (calPlannable(c)) n.setAttribute('aria-pressed', String(picked.has(key)));
  // The panel already shows a picked meal, so it needs no tooltip.
  if (!picked.has(key)) tip(n, () => cellLines(date, c));
  return n;
}

function renderCalMeta(dates, cells) {
  const all = dates.flatMap((d) => (cells.get(d) || []).map((c) => [d, c]));
  const sold = all.filter(([, c]) => c.state === 'sold' || c.state === 'sending');
  const earned = sum(sold.map(([, c]) => D.sales[c.sale].amount));
  const wasted = all.filter(([, c]) => c.state === 'wasted');
  const lost = sum(wasted.map(([, c]) => c.ask || 0));
  const queued = all.filter(([, c]) => c.schedule).length;
  const meta = $('ledMeta');
  meta.replaceChildren(`${fmtShort(dates[0])} - ${fmtShort(dates[6])}`);
  if (D.owner) {
    if (sold.length) meta.append(' · ', el('b', '', String(sold.length)), ' sold · ', el('span', 'gold', rupees(earned)));
    if (queued) meta.append(' · ', el('b', '', String(queued)), ' scheduled');
  } else {
    $('ledName').textContent = 'Meal ledger';
  }
  if (wasted.length) meta.append(' · ', el('span', 'red', `${wasted.length} wasted`), lost ? ` (${rupees(Math.round(lost))} at median)` : '');
}

// ---------- picking ----------

// calClick opens one meal, or with add (shift-click, or Pick several on)
// adds or removes a meal still to come from the pick.
function calClick(key, add) {
  const hit = calFind(key);
  if (!hit || calBusy) return;
  const plannable = calPlannable(hit[1]);
  if (add && plannable) {
    // A past or running meal open in the panel can't join a pick.
    const keep = calPicked.filter((k) => { const h = calFind(k); return h && calPlannable(h[1]); });
    calPicked = keep.includes(key) ? keep.filter((k) => k !== key) : [...keep, key];
  } else {
    calPicked = calPicked.length === 1 && calPicked[0] === key ? [] : [key];
  }
  calNote = null;
  renderCalendar();
}

// calPickAll adds a heading's meals to the pick, or takes them out when
// they're all in already.
function calPickAll(keys) {
  if (calBusy) return;
  const keep = calPicked.filter((k) => { const h = calFind(k); return h && calPlannable(h[1]); });
  const all = keys.every((k) => keep.includes(k));
  calPicked = all ? keep.filter((k) => !keys.includes(k)) : [...keep, ...keys.filter((k) => !keep.includes(k))];
  calNote = null;
  renderCalendar();
}

function calClear() { calPicked = []; calNote = null; renderCalendar(); }

// ---------- the panel ----------

function renderCalPanel() {
  const picks = calPicks();
  const one = picks.length === 1 ? picks[0] : null;
  const sig = JSON.stringify([calPicked, picks, calBusy, calNote, D.sell_off, calMulti, calPicked.length ? '' : weekDates()[0]]);
  // A reload that changed nothing for these meals keeps the panel as it is,
  // so a slider being dragged or a box being typed in isn't rebuilt.
  if (sig === calPanelSig && $('insp').childNodes.length) return;
  calPanelSig = sig;
  const formKey = calPicked.join(',');
  // The form follows the saved schedule until you change something in it.
  if (!calForm || calForm.key !== formKey || !calForm.touched) calForm = picks.length && picks.every(([, c]) => calPlannable(c)) ? { key: formKey, ...calFormFor(picks) } : null;

  const panel = $('insp');
  panel.classList.toggle('calm', !picks.length);
  if (!picks.length) { $('sideK').textContent = 'This week'; return calWeekPanel(panel); }
  $('sideK').textContent = one ? 'Meal' : `${picks.length} meals picked`;
  if (one && !calPlannable(one[1])) return calInfoPanel(panel, ...one);
  return calPlanPanel(panel, picks);
}

// calWeekPanel is the panel with nothing picked: the week in numbers and
// how to pick.
function calWeekPanel(panel) {
  const cells = calCells();
  const all = weekDates().flatMap((d) => (cells.get(d) || []).map((c) => [d, c]));
  const n = (f) => all.filter(([, c]) => f(c)).length;
  const sold = n((c) => c.state === 'sold' || c.state === 'sending');
  const free = n((c) => calPlannable(c) && !c.schedule);
  const queued = n((c) => !!c.schedule), selling = n((c) => !!c.task);
  const lines = [[{ t: `Week of ${fmtShort(calWeek)}`, c: 'nm' }], null];
  if (sold) lines.push([{ t: 'Sold ', c: 'c7' }, { t: String(sold), c: 'cf' }, { t: ' for ', c: 'c7' }, { t: rupees(sum(all.filter(([, c]) => c.sale != null && (c.state === 'sold' || c.state === 'sending')).map(([, c]) => D.sales[c.sale].amount))), c: 'c6' }]);
  if (selling) lines.push([{ t: 'Selling now ', c: 'c7' }, { t: String(selling), c: 'c6' }]);
  if (queued) lines.push([{ t: 'Scheduled ', c: 'c7' }, { t: String(queued), c: 'ce' }]);
  if (D.owner && free) lines.push([{ t: 'Registered, not up for sale ', c: 'c7' }, { t: String(free), c: 'cf' }]);
  if (lines.length === 2) lines.push([{ t: all.length ? 'Nothing sold or scheduled.' : 'Nothing registered this week.', c: 'c8' }]);
  fillLines(panel, lines);
}

// calInfoPanel is a meal the panel can't schedule: past, selling or sold.
// Its sale opens in the sell sheet.
function calInfoPanel(panel, date, c) {
  // The head names the meal and its time, so the lines start after them;
  // the click hints are for the grid.
  fillLines(panel, cellLines(date, c).slice(2).filter((l) => !(Array.isArray(l) && l[0]?.c === 'lore')));
  panel.prepend(calHead(c, date));
  if (!D.owner) return;
  const open = (label, cls) => { const b = el('button', 'sell-btn ' + cls, label); b.type = 'button'; b.onclick = () => openSell(date, c); return b; };
  if (c.task) panel.append(calActs(open('MANAGE SALE', 'gold')));
  else if (SELL.isSold(c)) panel.append(calActs(open('DELETE SALE', 'red')));
  else if (c.schedule) panel.append(calActs(open('OPEN SCHEDULE', 'gold')));
}

function calHead(c, date) {
  const head = el('div', 'cal-head');
  const name = el('div', 'nm', `${messName(c.mess)} ${c.diet ? DIET_LABEL[c.diet].toLowerCase() + ' ' : ''}${c.meal}`);
  name.style.setProperty('color', messColor(c.mess));
  const box = el('div');
  box.append(name, el('div', 'ln c8', `${fmtDay(date)}, ${hm(c.start)}-${hm(c.end)}`));
  const close = el('button', 'pg cal-x', '×'); close.type = 'button'; close.setAttribute('aria-label', 'Close');
  close.onclick = calClear;
  head.append(box, close);
  return head;
}

const calActs = (...buttons) => { const a = el('div', 'sell-acts cal-acts'); a.append(...buttons.filter(Boolean)); return a; };

function calBtn(label, cls, fn) {
  const b = el('button', 'sell-btn ' + cls, label);
  b.type = 'button';
  b.disabled = calBusy || ((cls.includes('gold') || cls.includes('red')) && !!D.sell_off);
  b.onclick = fn;
  return b;
}

// calPlanPanel sells, schedules or reschedules the picked meals.
function calPlanPanel(panel, picks) {
  panel.replaceChildren();
  const one = picks.length === 1 ? picks[0] : null;
  const f = calForm;
  const scheduled = picks.filter(([, c]) => c.schedule);
  const fresh = picks.filter(([, c]) => !c.schedule);

  if (one) {
    const [date, c] = one;
    panel.append(calHead(c, date));
    if (c.schedule?.retry_at) panel.append(el('p', 'sell-err', `${c.schedule.detail}. Retrying at ${hm(c.schedule.retry_at)}.`));
    if (c.schedule_issue && !c.schedule) panel.append(el('p', 'sell-err', `${cap(c.schedule_state)}: ${c.schedule_issue}`));
    if (c.schedule?.rule_id) panel.append(calRuleNote(c.schedule));
    if (c.kept && !c.schedule) {
      const allow = calBtn('ALLOW RULES AGAIN', '', () => calRun(async () => {
        await send('POST', 'api/recurring-rules', { action: 'allow', date, meal: c.meal });
        return { text: 'Rules can schedule it again.' };
      }));
      const note = el('div', 'cal-kept');
      note.append(el('p', 'sell-note', 'You kept this one, so recurring rules skip it.'), allow);
      panel.append(note);
    }
  } else {
    const head = el('div', 'cal-head');
    const box = el('div');
    box.append(el('div', 'nm', `${picks.length} meals`));
    const list = el('div', 'cal-list');
    for (const [date, c] of picks) {
      const chip = el('button', 'cal-chip' + (c.schedule ? ' on' : ''));
      chip.type = 'button';
      chip.style.setProperty('--m', messColor(c.mess));
      chip.append(svgEl(icon(c.meal)), `${fmt(date, { weekday: 'short', day: 'numeric' })}`);
      chip.title = `${cap(c.meal)}, ${fmtDay(date)}${c.schedule ? `, scheduled ${priceText(c.schedule)}` : ''}. Click to drop it from the pick.`;
      chip.onclick = () => calClick(calKey(date, c), true);
      list.append(chip);
    }
    box.append(list);
    const close = el('button', 'pg cal-x', '×'); close.type = 'button'; close.setAttribute('aria-label', 'Clear the pick'); close.onclick = calClear;
    head.append(box, close);
    panel.append(head);
    const parts = [fresh.length && `${fresh.length} not up for sale yet`, scheduled.length && `${scheduled.length} already scheduled`].filter(Boolean);
    panel.append(el('div', 'ln c7 cal-sum', parts.join(' · ')));
  }

  // Prices: the median mark only means something when the meals share it.
  const asks = [...new Set(picks.map(([, c]) => c.ask).filter((a) => a != null).map(Math.round))];
  const base = one ? one[1] : picks[0][1];
  const fieldset = el('fieldset', 'plan-fieldset cal-form');
  fieldset.disabled = calBusy || !!D.sell_off;
  const slider = priceSlider({ ...base, ask: asks.length === 1 ? asks[0] : null, task: null, planner: true, schedule: { min: f.min, max: f.max } },
    (lo, hi) => { if (lo !== f.min || hi !== f.max) f.touched = true; f.min = lo; f.max = hi; calDirty(); });
  fieldset.append(...slider.nodes);

  const time = calTimeBox(one ? one[1] : null, f, () => { f.touched = true; calDirty(); });
  // Autopilot: a switch the size of the posting-time box beside it.
  const auto = el('div', 'pr-box cal-auto');
  const sw = el('button', 'cal-switch');
  sw.type = 'button';
  sw.setAttribute('role', 'switch');
  sw.setAttribute('aria-label', 'Autopilot');
  const autoNote = el('p', 'sell-note');
  const drawAuto = () => {
    sw.classList.toggle('on', f.autopilot);
    sw.setAttribute('aria-checked', String(f.autopilot));
    sw.replaceChildren(el('b', '', '✈'), f.autopilot ? 'ON' : 'OFF', el('i', 'cal-knob'));
    autoNote.textContent = f.autopilot ? 'Sells without you. Skips buyers who would need you.' : 'Off. Buyers who need you wait for you.';
  };
  sw.onclick = () => { f.autopilot = !f.autopilot; f.touched = true; drawAuto(); calDirty(); };
  drawAuto();
  auto.append(el('span', 'pr-k', 'AUTOPILOT'), sw, autoNote);
  const row = el('div', 'cal-row');
  row.append(time, auto);
  fieldset.append(row);
  panel.append(fieldset);

  const err = el('p', 'sell-err');
  err.setAttribute('role', 'alert');
  err.id = 'calErr';
  const note = el('p', 'sell-note cal-done');
  note.setAttribute('role', 'status');
  if (calNote?.key === calPicked.join(',') || calNote?.key === '*') {
    if (calNote.error) err.textContent = calNote.error; else note.textContent = calNote.text;
  }
  if (D.sell_off) err.textContent = D.sell_off;

  const acts = el('div', 'sell-acts cal-acts');
  acts.id = 'calActs';
  panel.append(err, note, acts);
  calDirty();
}

// calDirty redraws the panel's buttons for the form as it stands, without
// rebuilding the form.
function calDirty() {
  const acts = $('calActs');
  if (!acts) return;
  const picks = calPicks(), f = calForm;
  if (!f || !picks.length) return;
  const one = picks.length === 1 ? picks[0] : null;
  const scheduled = picks.filter(([, c]) => c.schedule);
  const changed = picks.filter(([, c]) => !calUnchanged(c, f));
  const ok = calValid(f);
  const bs = [];
  if (one) {
    const [date, c] = one;
    const at = calAt(c, f.off);
    if (!c.schedule) {
      const label = at <= now() ? 'START SELLING NOW' : `SCHEDULE FOR ${hm(at)}`;
      bs.push(calBtn(label, 'gold', () => calSave(picks)));
    } else {
      const save = calBtn(changed.length ? 'SAVE CHANGES' : 'SAVED', 'gold', () => calSave(picks));
      save.disabled ||= !changed.length;
      // Start now sells at the prices on screen, saved or not.
      const start = calBtn('START NOW', '', () => calRun(async () => {
        const res = await send('POST', 'api/sell-action', { action: 'start', now: true, date, meal: c.meal, min: f.min, max: f.max, autopilot: !!f.autopilot });
        return { text: res?.at ? `Goes up at ${hm(res.at)}, once ${c.opens_after || 'the meal before'} ends.` : 'Selling now.' };
      }));
      start.disabled ||= !ok;
      bs.push(save, start, calUnscheduleBtn(scheduled));
    }
  } else {
    const add = changed.filter(([, c]) => !c.schedule).length, upd = changed.length - add;
    const label = [add && `SCHEDULE ${add}`, upd && `UPDATE ${upd}`].filter(Boolean).join(' · ') || 'ALL SET';
    const save = calBtn(label, 'gold', () => calSave(picks));
    save.disabled ||= !changed.length;
    bs.push(save);
    if (scheduled.length) bs.push(calUnscheduleBtn(scheduled));
  }
  for (const b of bs) if (b.classList.contains('gold')) b.disabled ||= !ok;
  acts.replaceChildren(...bs);
  // Say when each picked meal will go up.
  const when = document.querySelector('#insp .pr-when');
  if (when) when.textContent = one ? calWhenText(one[1], f.off) : `Each meal goes up ${offWords(f.off)}.`;
}

function calUnscheduleBtn(scheduled) {
  const n = scheduled.length;
  const b = calBtn(n > 1 ? `UNSCHEDULE ${n}` : 'UNSCHEDULE', 'red', () => {
    if (!b.dataset.sure) {
      b.dataset.sure = '1';
      b.textContent = 'SURE? CLICK AGAIN';
      const ruled = scheduled.some(([, c]) => c.schedule.rule_id);
      if (ruled) $('calErr').textContent = 'Recurring rules will skip these meals until you allow them again.';
      return;
    }
    calRun(async () => {
      const fails = [];
      for (const [date, c] of scheduled) {
        try { await send('POST', 'api/sell-action', { action: 'unplan', id: c.schedule.id, revision: c.schedule.revision }); }
        catch (e) { fails.push(`${fmtDay(date)} ${c.meal}: ${e.message}`); }
      }
      if (fails.length) throw new Error(fails.join('\n'));
      return { text: n > 1 ? `Unscheduled ${n} meals.` : 'Unscheduled. It stays yours.' };
    });
  });
  return b;
}

function calWhenText(c, off) {
  const at = calAt(c, off);
  if (at <= now()) return 'Goes up as soon as you schedule it.';
  if (c.opens && at === c.opens && c.start + calClampOff(c, off) * 60 < c.opens) return `Waits for ${c.opens_after || 'the meal before'} to end, then sells till ${hm(c.end)}.`;
  return `${cap(offWords(calClampOff(c, off)))}, sells till ${hm(c.end)}.`;
}

// calTimeBox is the posting time: a −/+ box like the price boxes, in
// 5-minute steps. With one meal it shows the clock time; with several,
// minutes from each meal's start.
function calTimeBox(c, f, changed) {
  const w = el('div', 'pr-box cal-time');
  const lo = OFF_MIN, hi = c ? calOffMax(c) : 90;
  f.off = Math.max(lo, Math.min(hi, f.off));
  const minus = el('button', '', '−'), plus = el('button', '', '+');
  const out = el('output', 'cal-time-v');
  out.tabIndex = 0;
  out.setAttribute('role', 'spinbutton');
  out.setAttribute('aria-label', 'Posting time');
  out.setAttribute('aria-valuemin', String(lo));
  out.setAttribute('aria-valuemax', String(hi));
  const when = el('p', 'sell-note pr-when');
  const draw = () => {
    if (c) out.replaceChildren(el('span', 'cur', '⏰'), hm(calAt(c, f.off)));
    else out.replaceChildren(el('span', 'cur', '⏰'), `${Math.abs(f.off)}`, el('small', '', f.off < 0 ? ' MIN BEFORE' : f.off > 0 ? ' MIN AFTER' : ' AT START'));
    out.setAttribute('aria-valuenow', String(f.off));
    out.setAttribute('aria-valuetext', c ? `${hm(calAt(c, f.off))}, ${offWords(f.off)}` : offWords(f.off));
    minus.disabled = f.off <= lo;
    plus.disabled = f.off >= hi;
  };
  const step = (d) => {
    // Land on the next multiple of the step, like the price boxes.
    const next = d > 0 ? (Math.floor(f.off / calStep) + 1) * calStep : (Math.ceil(f.off / calStep) - 1) * calStep;
    f.off = Math.max(lo, Math.min(hi, next));
    draw(); changed();
  };
  for (const [b, d, word] of [[minus, -1, 'Earlier'], [plus, 1, 'Later']]) {
    b.type = 'button';
    b.setAttribute('aria-label', `${word} by ${calStep} minutes`);
    b.onclick = () => step(d);
  }
  out.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowUp' || e.key === 'ArrowRight') { e.preventDefault(); step(1); }
    if (e.key === 'ArrowDown' || e.key === 'ArrowLeft') { e.preventDefault(); step(-1); }
  });
  const field = el('div', 'pr-in');
  field.append(minus, out, plus);
  w.append(el('span', 'pr-k', 'GOES UP'), field, when);
  draw();
  return w;
}

// calRun runs one panel action, then reloads. fn returns {text} for the
// note under the buttons.
async function calRun(fn) {
  calBusy = true;
  const key = calPicked.join(',');
  calNote = null;
  renderCalendar();
  try {
    const out = await fn();
    calNote = { key: '*', text: out?.text || 'Done.' };
    calForm = null;
  } catch (e) {
    calNote = { key, error: e.message };
  } finally {
    calBusy = false;
    await load();
    if (typeof loadRecurringRules === 'function') loadRecurringRules();
  }
}

// calSave schedules every picked meal the form would change. Meals that
// fail stay picked with the reason; the rest leave the pick.
async function calSave(picks) {
  const f = calForm;
  if (!calValid(f)) { $('calErr').textContent = 'Use whole rupees: lowest at least ₹1, opening at least the lowest and at most ₹10,000.'; return; }
  const todo = picks.filter(([, c]) => !calUnchanged(c, f));
  calBusy = true;
  calNote = { key: calPicked.join(','), text: todo.length > 1 ? `Saving 0 of ${todo.length}…` : 'Saving…' };
  renderCalendar();
  const failed = [], lines = [];
  let started = 0, done = 0;
  for (const [date, c] of todo) {
    try {
      const res = await send('POST', 'api/sell-action', calPlanBody(date, c, f));
      if (!res?.at) started++;
      done++;
      const s = document.querySelector('#insp .cal-done');
      if (s && todo.length > 1) s.textContent = `Saving ${done} of ${todo.length}…`;
    } catch (e) {
      failed.push(calKey(date, c));
      lines.push(`${fmtDay(date)} ${c.meal}: ${e.message}`);
    }
  }
  calBusy = false;
  const ok = todo.length - failed.length;
  if (failed.length) {
    calPicked = failed;
    calForm = { ...f, key: failed.join(',') };
    calNote = { key: failed.join(','), error: `${ok ? `Saved ${ok}. ` : ''}${failed.length === 1 && todo.length === 1 ? lines[0].replace(/^[^:]*: /, '') : `These didn't save:\n${lines.join('\n')}`}` };
  } else {
    const what = started === ok ? (ok > 1 ? `Selling ${ok} meals now.` : 'Selling now.')
      : ok > 1 ? `Scheduled ${ok} meals${started ? `, ${started} selling now` : ''}.` : 'Scheduled.';
    calNote = { key: '*', text: what };
    if (picks.length > 1) { calPicked = []; toast(what); }
    calForm = null;
  }
  await load();
  if (typeof loadRecurringRules === 'function') loadRecurringRules();
}

const calHasRules = () => typeof recurringRules !== 'undefined' && recurringRules.some((r) => r.enabled);

// calRuleNote names the rule behind a schedule and says what editing it here does.
function calRuleNote(sc) {
  const r = typeof recurringRules !== 'undefined' ? recurringRules.find((x) => x.id === sc.rule_id) : null;
  const name = r?.name || `Rule #${sc.rule_id}`;
  const rel = r?.pricing === 'median' ? r.relative?.[sc.meal] : null;
  const note = el('p', 'sell-note plan-rule');
  const link = el('a', 'nav-link', 'Edit rule'); link.href = '#recurring-rule=' + sc.rule_id;
  note.append(`From ${name}. `);
  if (sc.overridden) note.append('Edited here, so rule changes leave it alone. ');
  else if (rel) note.append(`Price follows the median at ${signedPct(rel.low)} to ${signedPct(rel.high)}. Saving here fixes it for this meal. `);
  else note.append('Saving here keeps it as is when the rule changes. ');
  note.append(link);
  return note;
}

// calGoto shows the week with date and opens that meal in the panel.
function calGoto(date, meal) {
  calWeek = mondayOf(date);
  calPicked = [`${date}|${meal}`];
  calNote = null;
  renderCalendar();
  $('insp').scrollIntoView({ block: 'center' });
}

// ---------- events ----------

$('lgrid').addEventListener('click', (e) => {
  const h = e.target.closest('[data-pick]');
  if (h) { calPickAll(h.dataset.pick.split(',')); return; }
  const n = e.target.closest('.cell');
  if (n?.dataset.k) calClick(n.dataset.k, calMulti || e.shiftKey || e.ctrlKey || e.metaKey);
});
$('lgrid').addEventListener('keydown', (e) => {
  const n = e.target.closest('.cell');
  if (!n?.dataset.k || (e.key !== 'Enter' && e.key !== ' ')) return;
  e.preventDefault();
  calClick(n.dataset.k, calMulti || e.shiftKey);
  // The grid was rebuilt; keep focus on the same meal.
  for (const x of $('lgrid').querySelectorAll('.cell')) if (x.dataset.k === n.dataset.k) x.focus();
});
$('calMulti').addEventListener('click', () => {
  calMulti = !calMulti;
  // Turning it off with several picked keeps the first open.
  if (!calMulti && calPicked.length > 1) calPicked = [];
  renderCalendar();
});
$('pgPrev').addEventListener('click', () => { if (!D) return; calWeek = addDays(calWeek, -7); renderCalendar(); if (D.owner) renderEarnings(); });
$('pgNext').addEventListener('click', () => { if (!D) return; calWeek = addDays(calWeek, 7); renderCalendar(); if (D.owner) renderEarnings(); });
$('pgToday').addEventListener('click', () => { if (!D) return; calWeek = mondayOf(D.today); renderCalendar(); if (D.owner) renderEarnings(); });
addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && calPicked.length && $('modal').hidden && !e.target.closest('.modal')) calClear();
});
