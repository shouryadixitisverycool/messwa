'use strict';

// Recurring rules: a list in its own chest, edited in a sheet like the
// sell and friend forms. The sheet's fields are static HTML so a list
// refresh never wipes a half-edited rule.

let recurringRules = [];
let recurringBusy = false;
let recurringPreviewKey = '';
let recurringInitialized = false;
let recurringLoadedAt = 0;
let recurringLoading = false;
let recurringSlider = null;
let recurringEditing = null;
// The % of median a meal sells at, per meal; kept while meals are toggled.
let recurringRelative = {};
const recurringRelDefault = { low: -15, high: 25 };
const recurringDays = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
// Monday first, as the planner reads.
const recurringDayOrder = [1, 2, 3, 4, 5, 6, 0];
const recurringOffsets = [30, 20, 15, 10, 5, 0, -10, -15, -30, -45, -60, -90];

function ruleForm() {
  return {
    id: Number($('ruleID').value || 0), revision: Number($('ruleRevision').value || 0),
    name: $('ruleName').value.trim(), from: $('ruleFrom').value, through: $('ruleThrough').value,
    weekdays: [...$('ruleDays').querySelectorAll('input:checked')].map((i) => Number(i.value)).sort((a, b) => a - b),
    meals: [...$('ruleMeals').querySelectorAll('input:checked')].map((i) => i.value),
    min: Number($('ruleMin').value), max: Number($('ruleMax').value), before: Number($('ruleBefore').value),
    autopilot: $('ruleAuto').checked, enabled: $('ruleEnabled').checked,
    ...rulePricingForm(),
  };
}

function rulePricingMode() {
  return $('rulePricing').querySelectorAll('input:checked')[0]?.value || '';
}

function rulePricingForm() {
  if (rulePricingMode() !== 'median') return { pricing: '' };
  const relative = {};
  for (const i of $('ruleMeals').querySelectorAll('input:checked')) relative[i.value] = { ...(recurringRelative[i.value] || recurringRelDefault) };
  return { pricing: 'median', relative };
}

const signedPct = (v) => `${v > 0 ? '+' : v < 0 ? '−' : '±'}${Math.abs(v)}%`;

// ruleMedian is the recent median ask for meal in the diet the owner
// usually registers, for the sheet's estimate. The service prices each
// meal from its own mess, diet and weekday, so this is a guide.
function ruleMedian(meal) {
  if (typeof marketFor !== 'function' || !D?.market) return null;
  const cell = (D.upcoming || []).flatMap((d) => d.cells).find((c) => c.meal === meal && c.diet);
  const m = marketFor(meal, cell?.diet || 'veg');
  return m ? m.median : null;
}

// The meal rows' bar runs from 50% under the median to double it.
const relBar = (v) => Math.min(100, Math.max(0, ((v + 50) / 150) * 100));

// pctBox is a −/+ box for a percentage around the median, in steps of 5.
function pctBox(label, aria, get, set) {
  const w = el('div', 'pr-box'), field = el('div', 'pr-in sm');
  const minus = el('button', '', '−'), plus = el('button', '', '+'), n = el('input', 'pr-num');
  Object.assign(n, { type: 'text', inputMode: 'numeric', autocomplete: 'off', enterKeyHint: 'done' });
  n.setAttribute('aria-label', `${aria}, percent above or below the median`);
  const show = () => { n.value = get() > 0 ? `+${get()}` : String(get()); };
  for (const [btn, d, word] of [[minus, -5, 'down'], [plus, 5, 'up']]) {
    btn.type = 'button';
    btn.setAttribute('aria-label', `${aria} ${word} 5%`);
    btn.addEventListener('click', () => { set(Math.round((get() + d) / 5) * 5); show(); });
  }
  n.addEventListener('input', () => { const v = parseInt(n.value.replace(/[^\d-]/g, ''), 10); if (!Number.isNaN(v)) set(v, true); });
  n.addEventListener('change', show);
  n.addEventListener('keydown', (k) => {
    if (k.key === 'ArrowUp' || k.key === 'ArrowDown') { k.preventDefault(); (k.key === 'ArrowUp' ? plus : minus).click(); }
  });
  field.append(minus, el('span', 'cur pct', '%'), n, plus);
  w.append(el('span', 'pr-k', label), field);
  show();
  return { node: w, show };
}

// renderRuleRelative shows the price controls for the chosen mode: the
// ₹ range alone, or a row per meal in % of its median plus the ₹ range for
// meals with no median yet.
function renderRuleRelative() {
  const median = rulePricingMode() === 'median';
  const box = $('ruleRelative'), fixed = $('rulePrice');
  box.hidden = !median; fixed.hidden = median;
  box.replaceChildren();
  if (recurringSlider && !median) fixed.replaceChildren(...recurringSlider.nodes);
  if (!median) return;
  const r5 = (v) => Math.max(1, Math.round(v / 5) * 5);
  for (const input of $('ruleMeals').querySelectorAll('input:checked')) {
    const meal = input.value;
    const rel = recurringRelative[meal] || (recurringRelative[meal] = { ...recurringRelDefault });
    const row = el('div', 'rr-row'), top = el('div', 'rr-top'), name = el('span', 'rr-meal'), est = el('span', 'rr-est');
    name.append(svgEl(icon(meal)), cap(meal));
    top.append(name, est);
    const bar = el('div', 'rr-bar'), band = el('i', 'band'), tick = el('i', 'mid');
    tick.style.left = `${relBar(0)}%`;
    bar.append(band, tick);
    const lo = pctBox('LOWEST', `${cap(meal)} lowest`, () => rel.low, (v, typing) => {
      rel.low = Math.max(-90, Math.min(200, v));
      if (rel.high < rel.low) { rel.high = rel.low; hi.show(); }
      if (!typing) lo.show();
      draw();
    });
    const hi = pctBox('OPENS AT', `${cap(meal)} opening`, () => rel.high, (v, typing) => {
      rel.high = Math.max(-90, Math.min(300, v));
      if (rel.low > rel.high) { rel.low = rel.high; lo.show(); }
      if (!typing) hi.show();
      draw();
    });
    const vals = el('div', 'pr-vals');
    vals.append(lo.node, hi.node);
    const draw = () => {
      band.style.left = `${relBar(rel.low)}%`;
      band.style.right = `${100 - relBar(rel.high)}%`;
      const m = ruleMedian(meal);
      est.replaceChildren();
      if (m == null) { est.append(el('small', '', 'no median yet')); return; }
      const a = r5(m * (100 + rel.low) / 100), b = Math.max(a, r5(m * (100 + rel.high) / 100));
      est.append(el('b', '', a === b ? `₹${a}` : `₹${a}–${b}`), el('small', '', `median ₹${Math.round(m)}`));
    };
    row.append(top, vals, bar);
    draw();
    box.append(row);
  }
  if (!box.children.length) { box.append(el('p', 'sell-note', 'Pick a meal to price it.')); return; }
  if (recurringSlider) {
    const row = el('div', 'rr-row rr-fallback'), top = el('div', 'rr-top'), name = el('span', 'rr-meal');
    name.append(el('i', 'rr-q', '?'), 'No median yet');
    top.append(name, el('span', 'rr-est', ''));
    row.append(top, recurringSlider.nodes[0]);
    box.append(row);
  }
}

function rulePayload(action) {
  return { action, rule: ruleForm(), apply_waiting: $('ruleApply').checked };
}

// Saving needs a fresh preview: any change swaps SAVE back to PREVIEW.
function invalidateRulePreview() {
  recurringPreviewKey = '';
  $('ruleSave').disabled = true;
  $('ruleSave').hidden = true;
  $('rulePreview').hidden = false;
  $('rulePreviewBox').hidden = true;
  $('rulePreviewSum').hidden = true;
  $('rulePreviewText').textContent = '';
}

// ruleSpanText is how long the rule runs, for the dates label.
function ruleSpanText() {
  const a = $('ruleFrom').value, b = $('ruleThrough').value;
  if (!a || !b) return '';
  const days = Math.round((Date.parse(b + 'T12:00:00Z') - Date.parse(a + 'T12:00:00Z')) / 86400000) + 1;
  if (!(days > 0)) return '';
  return days % 7 === 0 ? `${days / 7} week${days === 7 ? '' : 's'}` : `${days} day${days === 1 ? '' : 's'}`;
}

function setRuleBusy(busy) {
  recurringBusy = busy;
  $('ruleFields').disabled = busy;
  $('ruleNew').disabled = busy;
  for (const id of ['rulePreview', 'ruleCancel', 'ruleDeleteAsk', 'ruleDelete', 'ruleDeleteBack']) $(id).disabled = busy;
  if (busy) $('ruleSave').disabled = true;
  for (const b of $('ruleList').querySelectorAll('button')) b.disabled = busy;
}

// "Mon-Fri", "Weekends", "Every day" or "Mon, Wed, Fri".
function ruleDaysText(days) {
  const set = new Set(days);
  if (set.size === 7) return 'Every day';
  if (set.size === 5 && [1, 2, 3, 4, 5].every((d) => set.has(d))) return 'Mon-Fri';
  if (set.size === 2 && set.has(0) && set.has(6)) return 'Weekends';
  return recurringDayOrder.filter((d) => set.has(d)).map((d) => recurringDays[d]).join(', ');
}

function ruleTimeText(before) {
  if (before > 0) return `${before} min before serving`;
  if (before < 0) return `${-before} min into serving`;
  return 'when serving starts';
}

function ruleDateText(date) {
  return typeof fmtDay === 'function' ? fmtDay(date) : date;
}

function ruleChip(parent, type, value, label, checked, art, cls = 'rseg-i') {
  const l = el('label', cls), input = el('input'), span = el('span');
  input.type = type; input.value = String(value); input.checked = checked;
  if (art) span.append(svgEl(art));
  span.append(label);
  l.append(input, span);
  parent.append(l);
}

function fillRuleTimes(before) {
  const select = $('ruleBefore');
  if (typeof Option === 'undefined') { select.value = String(before); return; }
  const offsets = recurringOffsets.includes(before) ? recurringOffsets : [...recurringOffsets, before].sort((a, b) => b - a);
  select.replaceChildren(...offsets.map((m) => new Option(ruleTimeText(m).replace(/^./, (c) => c.toUpperCase()), String(m))));
  select.value = String(before);
}

function drawRulePrice(min, max) {
  $('ruleMin').value = min; $('ruleMax').value = max;
  if (typeof priceRange !== 'function') return;
  const hi = Math.max(150, Math.ceil(max / 50) * 50);
  recurringSlider = priceRange({ lo: min, hi: max, bounds: { lo: 1, hi }, axis: ['₹1', '', `₹${hi}`] });
  // Runs before the form's listener, so the preview check sees new prices.
  const sync = () => { const [lo, top] = recurringSlider.get(); $('ruleMin').value = lo; $('ruleMax').value = top; };
  for (const n of recurringSlider.nodes) { n.addEventListener('input', sync); n.addEventListener('click', sync); }
}

function showRuleSheet(open) {
  $('ruleModal').hidden = !open;
  if (open) setTimeout(() => $('ruleName').focus?.(), 0);
}

function closeRuleSheet() {
  if (recurringBusy) return;
  showRuleSheet(false);
}

function showRuleDelete(on) {
  $('ruleDeleteBox').hidden = !on;
  $('ruleActs').hidden = on;
  $('rulePreviewBox').hidden = on;
}

function editRecurringRule(rule, open = true) {
  if (rule || D) recurringInitialized = true;
  const r = rule || {
    id: 0, revision: 0, name: '', from: D?.today || '', through: D ? addDays(D.today, 27) : '',
    weekdays: [1, 2, 3, 4, 5], meals: ['lunch'], min: 40, max: 80,
    before: D ? -D.list_at / 60 : 15, autopilot: false, enabled: true, waiting: 0,
  };
  recurringEditing = r.id ? r : null;
  for (const [id, key] of [['ruleID', 'id'], ['ruleRevision', 'revision'], ['ruleName', 'name'], ['ruleFrom', 'from'], ['ruleThrough', 'through']]) $(id).value = r[key];
  $('ruleSpan').textContent = ruleSpanText();
  $('ruleAuto').checked = !!r.autopilot; $('ruleEnabled').checked = !!r.enabled;
  $('ruleApply').checked = false; $('ruleRemoveWaiting').checked = false;
  $('ruleApplyRow').hidden = !r.id || !r.waiting;
  $('ruleWaitingCount').textContent = String(r.waiting || 0);
  const days = $('ruleDays'), mealBox = $('ruleMeals');
  days.replaceChildren(el('legend', '', 'Days')); mealBox.replaceChildren(el('legend', '', 'Meals'));
  recurringDayOrder.forEach((d) => ruleChip(days, 'checkbox', d, recurringDays[d], r.weekdays.includes(d)));
  (D ? meals() : ['breakfast', 'lunch', 'snacks', 'dinner']).forEach((meal) => ruleChip(mealBox, 'checkbox', meal, cap(meal), r.meals.includes(meal), icon(meal)));
  fillRuleTimes(r.before);
  recurringRelative = JSON.parse(JSON.stringify(r.relative || {}));
  const pricing = $('rulePricing');
  pricing.replaceChildren(el('legend', '', 'Pricing'));
  for (const [value, label] of [['', '₹ Fixed'], ['median', '% Median']]) {
    ruleChip(pricing, 'radio', value, label, (r.pricing || '') === value, '', 't2-i');
    const input = pricing.children[pricing.children.length - 1].children[0];
    input.name = 'rulePricing';
  }
  drawRulePrice(r.min, r.max);
  renderRuleRelative();
  $('ruleTitle').textContent = r.id ? r.name : 'New rule';
  $('ruleSub').textContent = r.id
    ? `${r.enabled ? 'Active' : 'Paused'} · ${r.waiting || 0} waiting · changes apply to new meals`
    : 'Sells matching registered meals up to four weeks ahead.';
  $('ruleDeleteAsk').hidden = !r.id;
  $('ruleError').textContent = r.last_error ? `Last run: ${r.last_error}` : '';
  showRuleDelete(false);
  invalidateRulePreview();
  if (open) showRuleSheet(true);
}

// ruleListPrice is a rule's price for its row: the fixed range, or the
// percentages around the median when every meal shares them.
function ruleListPrice(r) {
  if (r.pricing !== 'median') return r.min === r.max ? `₹${r.min}` : `₹${r.min}-${r.max}`;
  const rels = r.meals.map((m) => r.relative?.[m]).filter(Boolean);
  const same = rels.length && rels.every((x) => x.low === rels[0].low && x.high === rels[0].high);
  return same ? `${signedPct(rels[0].low)}/${signedPct(rels[0].high)} median` : 'vs median';
}

function rulePill(r) {
  if (r.enabled && r.last_error) return ['CHECK', 'cc'];
  return r.enabled ? ['ACTIVE', 'ca'] : ['PAUSED', 'c8'];
}

function renderRecurringRules() {
  const list = $('ruleList'); list.replaceChildren();
  const on = recurringRules.filter((r) => r.enabled).length, waiting = recurringRules.reduce((n, r) => n + (r.waiting || 0), 0);
  $('ruleMeta').replaceChildren(el('b', '', String(on)), ' active · ', el('b', '', String(waiting)), ' meals waiting');
  if (!recurringRules.length) {
    list.append(el('div', 'empty-note', 'Selling the same meals every week? Make a rule once. It schedules matching registered meals four weeks ahead, at your prices. Untick a meal in the planner to keep it.'));
    return;
  }
  for (const r of recurringRules) {
    const row = el('button', 'friend live rule-row' + (r.enabled ? '' : ' off'));
    row.type = 'button'; row.disabled = recurringBusy;
    row.setAttribute('aria-label', `Edit recurring rule ${r.name}`);
    row.onclick = () => editRecurringRule(r);
    const ic = el('span', 'fr-ic');
    ic.append(svgEl(r.meals.length === 1 ? icon(r.meals[0]) : mealsAllIcon(r.meals)));
    const what = el('div', 'fr-what');
    what.append(el('b', '', r.name),
      el('span', '', `${ruleDaysText(r.weekdays)} · ${r.meals.map(cap).join(', ')} · up ${ruleTimeText(r.before)}`),
      el('span', '', `${ruleDateText(r.from)} - ${ruleDateText(r.through)}${r.autopilot ? ' · autopilot' : ''}`));
    if (r.enabled && r.last_error) what.append(el('span', 'rule-err', r.last_error));
    const [word, cls] = rulePill(r);
    const right = el('div', 'fr-right');
    right.append(el('span', 'pill ' + cls, word), el('span', 'fr-amt', ruleListPrice(r)), el('span', 'fr-buyer', `${r.waiting} waiting`));
    row.append(ic, what, right);
    list.append(row);
  }
}

async function loadRecurringRules() {
  if (recurringLoading) return;
  recurringLoading = true;
  try {
    const response = await fetch('api/recurring-rules', { cache: 'no-store' });
    if (response.status === 401 || response.status === 403) return;
    if (!response.ok) throw new Error('Could not refresh recurring rules.');
    recurringRules = await response.json();
    recurringLoadedAt = Date.now();
    $('ruleListError').textContent = '';
    renderRecurringRules();
    if (!recurringInitialized && D) editRecurringRule(undefined, false);
    openRecurringHash();
  } catch (e) { $('ruleListError').textContent = e.message; }
  finally { recurringLoading = false; }
}

function refreshRecurringRulesView() {
  $('ruleSec').hidden = !D?.owner;
  if (!D?.owner) return;
  if (!recurringInitialized) editRecurringRule(undefined, false);
  if (Date.now() - recurringLoadedAt > 30000) loadRecurringRules();
}

function openRecurringHash() {
  if (recurringBusy) return;
  const match = /^#recurring-rule=(\d+)$/.exec(location.hash);
  if (!match) return;
  const rule = recurringRules.find((r) => r.id === Number(match[1]));
  if (!rule) return;
  $('ruleSec').scrollIntoView({ block: 'start' });
  editRecurringRule(rule);
  history.replaceState(null, '', location.pathname + location.search);
}

function previewTiles(out, applying) {
  const tiles = [['NEW', out.new, out.new ? 'gold' : ''], ['START NOW', out.starts_now, out.starts_now ? 'gold' : ''], ['KEPT', out.kept, '']];
  if (recurringEditing) tiles.push([applying ? 'UPDATED' : 'UNCHANGED', out.waiting, ''], ['YOUR EDITS', out.preserved_overrides, '']);
  if (out.existing) tiles.push(['ALREADY SET', out.existing, '']);
  if (out.new && out.highest) tiles.push(['PRICES', out.lowest === out.highest ? `₹${out.lowest}` : `₹${out.lowest}–${out.highest}`, 'gold']);
  if (out.fallback) tiles.push(['NO MEDIAN', out.fallback, '']);
  return tiles.map(([k, v, cls]) => { const t = el('div', 'st'); t.append(el('span', '', k), el('b', cls, String(v))); return t; });
}

$('ruleIcon').append?.(svgEl(icon('clock')));
$('ruleClock').append?.(svgEl(icon('clock')));
$('ruleForm').addEventListener('input', (e) => {
  invalidateRulePreview();
  if (e?.target === $('ruleFrom') || e?.target === $('ruleThrough')) $('ruleSpan').textContent = ruleSpanText();
});
$('ruleForm').addEventListener('change', (e) => {
  invalidateRulePreview();
  if (e.target?.name === 'rulePricing' || e.target?.closest?.('#ruleMeals')) renderRuleRelative();
});
// The −/+ boxes change prices on click, which fires no input event.
$('ruleForm').addEventListener('click', (e) => { if (e.target?.closest?.('.pr-in button')) invalidateRulePreview(); });
$('ruleNew').onclick = () => editRecurringRule();
$('ruleCancel').onclick = closeRuleSheet;
$('ruleDeleteAsk').onclick = () => showRuleDelete(true);
$('ruleDeleteBack').onclick = () => showRuleDelete(false);
$('ruleModal').addEventListener('click', (e) => { if (e.target === $('ruleModal')) closeRuleSheet(); });
addEventListener('keydown', (e) => { if (e.key === 'Escape' && !$('ruleModal').hidden) { e.stopImmediatePropagation(); closeRuleSheet(); } }, true);

$('rulePreview').onclick = async () => {
  setRuleBusy(true); $('ruleError').textContent = '';
  const payload = rulePayload('preview');
  let ok = false;
  try {
    const out = await send('POST', 'api/recurring-rules', payload);
    recurringPreviewKey = JSON.stringify(rulePayload('save'));
    $('rulePreviewText').textContent = [
      out.starts_now ? `${out.starts_now} start selling immediately when you save.` : '',
      out.fallback ? `${out.fallback} ${out.fallback === 1 ? 'has' : 'have'} no median yet and use${out.fallback === 1 ? 's' : ''} the ₹ range.` : '',
    ].filter(Boolean).join(' ');
    $('rulePreviewSum').replaceChildren(...previewTiles(out, payload.apply_waiting));
    $('rulePreviewSum').hidden = false;
    $('rulePreviewBox').hidden = false;
    $('rulePreview').hidden = true;
    $('ruleSave').hidden = false;
    ok = true;
  } catch (e) { $('ruleError').textContent = e.message; invalidateRulePreview(); }
  finally { setRuleBusy(false); $('ruleSave').disabled = !ok; }
};

$('ruleForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const payload = rulePayload('save');
  if (!recurringPreviewKey || recurringPreviewKey !== JSON.stringify(payload)) { invalidateRulePreview(); return; }
  setRuleBusy(true); $('ruleError').textContent = '';
  try {
    const saved = await send('POST', 'api/recurring-rules', payload);
    setRuleBusy(false);
    showRuleSheet(false);
    editRecurringRule(saved, false);
    if (typeof toast === 'function') toast(saved.last_error ? `Rule saved. Scheduling will retry: ${saved.last_error}` : `Saved ${saved.name}. Its meals show in the planner.`);
    await loadRecurringRules(); await load();
  } catch (error) { $('ruleError').textContent = error.message; }
  finally { setRuleBusy(false); }
});

$('ruleDelete').onclick = async () => {
  const rule = ruleForm(); if (!rule.id) return;
  setRuleBusy(true); $('ruleError').textContent = '';
  try {
    await send('POST', 'api/recurring-rules', { action: 'delete', rule, cancel_waiting: $('ruleRemoveWaiting').checked });
    setRuleBusy(false);
    showRuleSheet(false);
    editRecurringRule(undefined, false);
    if (typeof toast === 'function') toast('Rule deleted. Meals already selling carry on.');
    await loadRecurringRules(); await load();
  } catch (e) { $('ruleError').textContent = e.message; }
  finally { setRuleBusy(false); }
};

addEventListener('hashchange', openRecurringHash);
loadRecurringRules();

function recurringRuleName(id) { return recurringRules.find((r) => r.id === id)?.name || `Rule #${id}`; }
