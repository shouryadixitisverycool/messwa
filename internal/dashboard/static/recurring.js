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
// Monday first, as the ledger reads.
const recurringDayOrder = [1, 2, 3, 4, 5, 6, 0];

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

// relBounds picks a meal's % slider scale: -50% to +100% by default,
// expanding if the rule already sits outside that range.
function relBounds(rel) {
  return {
    lo: Math.max(-90, Math.min(-50, Math.floor(rel.low / 5) * 5)),
    hi: Math.min(300, Math.max(100, Math.ceil(rel.high / 50) * 50)),
  };
}

// pctBox is a −/+ box for a percentage around the median, in steps of 5.
function pctBox(label, aria, get, set) {
  const w = el('div', 'pr-box'), field = el('div', 'pr-in sm');
  const minus = el('button', '', '−'), plus = el('button', '', '+'), n = el('input', 'pr-num');
  Object.assign(n, { type: 'text', inputMode: 'numeric', autocomplete: 'off', enterKeyHint: 'done' });
  n.setAttribute?.('aria-label', `${aria}, percent above or below the median`);
  const show = () => { n.value = get() > 0 ? `+${get()}` : String(get()); };
  for (const [btn, d, word] of [[minus, -5, 'down'], [plus, 5, 'up']]) {
    btn.type = 'button';
    btn.setAttribute?.('aria-label', `${aria} ${word} 5%`);
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
    const B = relBounds(rel);
    const pct = (v) => Math.min(100, Math.max(0, ((v - B.lo) / (B.hi - B.lo)) * 100));
    const row = el('div', 'rr-row'), top = el('div', 'rr-top'), name = el('span', 'rr-meal'), est = el('span', 'rr-est');
    name.append(svgEl(icon(meal)), cap(meal));
    top.append(name, est);
    const dual = el('div', 'dual'), track = el('div', 'track'), fill = el('i', 'fill'), tick = el('i', 'med');
    const midPct = pct(0);
    tick.style.left = `${midPct}%`;
    track.append(fill, tick);
    const range = (label) => {
      const r = el('input');
      Object.assign(r, { type: 'range', min: B.lo, max: B.hi, step: 5 });
      r.setAttribute?.('aria-label', label);
      return r;
    };
    const rLo = range(`${cap(meal)} lowest, percent of median`), rHi = range(`${cap(meal)} opening, percent of median`);
    dual.append(track, rLo, rHi);
    const axis = el('div', 'sell-axis rr-axis');
    const medLabel = el('span', 'med-l', midPct >= 20 && midPct <= 80 ? 'median' : '0%');
    medLabel.style.left = `calc(9px + (100% - 18px) * ${midPct / 100})`;
    axis.append(el('span', '', `${B.lo}%`), medLabel, el('span', '', `+${B.hi}%`));
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
    rLo.addEventListener('input', () => {
      rel.low = Math.min(Math.max(-90, Math.min(200, +rLo.value)), rel.high);
      rLo.style.zIndex = 3; rHi.style.zIndex = 2;
      lo.show();
      draw();
    });
    rHi.addEventListener('input', () => {
      rel.high = Math.max(Math.max(-90, Math.min(300, +rHi.value)), rel.low);
      rHi.style.zIndex = 3; rLo.style.zIndex = 2;
      hi.show();
      draw();
    });
    const draw = () => {
      fill.style.left = `${pct(rel.low)}%`;
      fill.style.right = `${100 - pct(rel.high)}%`;
      rLo.value = String(rel.low);
      rHi.value = String(rel.high);
      rLo.setAttribute?.('aria-valuetext', signedPct(rel.low));
      rHi.setAttribute?.('aria-valuetext', signedPct(rel.high));
      if (rel.low === rel.high && rel.low >= B.hi) { rLo.style.zIndex = 3; rHi.style.zIndex = 2; }
      const m = ruleMedian(meal);
      est.replaceChildren();
      if (m == null) { est.append(el('small', '', 'no median yet')); return; }
      const a = r5(m * (100 + rel.low) / 100), b = Math.max(a, r5(m * (100 + rel.high) / 100));
      est.append(el('b', '', a === b ? `₹${a}` : `₹${a}–${b}`), el('small', '', `median ₹${Math.round(m)}`));
    };
    row.append(top, vals, dual, axis);
    draw();
    box.append(row);
  }
  if (!box.children.length) { box.append(el('p', 'sell-note', 'Pick a meal to price it.')); return; }
  if (recurringSlider) {
    const row = el('div', 'rr-row rr-fallback'), top = el('div', 'rr-top'), name = el('span', 'rr-meal');
    name.append(el('i', 'rr-q', '?'), 'No median yet');
    top.append(name, el('span', 'rr-est', ''));
    row.append(top, ...recurringSlider.nodes);
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
  for (const id of ['rulePreview', 'ruleCancel', 'ruleDeleteAsk', 'ruleDelete', 'ruleDeleteBack']) $(id).disabled = busy;
  if (busy) $('ruleSave').disabled = true;
  for (const b of $('pills').querySelectorAll('button.mp-rule')) b.disabled = busy;
}

function ruleChip(parent, type, value, label, checked, art, cls = 'rseg-i') {
  const l = el('label', cls), input = el('input'), span = el('span');
  input.type = type; input.value = String(value); input.checked = checked;
  if (art) span.append(svgEl(art));
  span.append(label);
  l.append(input, span);
  parent.append(l);
}

// ruleBeforeWord formats rule.before (minutes before serving; negative means
// after) like Settings' Offer goes up readout.
function ruleBeforeWord(before) {
  if (before === 0) return 'At the start';
  const m = Math.abs(before);
  const t = m >= 60 ? `${Math.floor(m / 60)}h${m % 60 ? ` ${m % 60}m` : ''}` : `${m}m`;
  return `${t} ${before > 0 ? 'before' : 'after'} start`;
}

function fillRuleTimes(before) {
  const v = Math.max(-90, Math.min(30, Math.round(before || 0)));
  $('ruleBefore').value = String(v);
  const range = $('ruleBeforeRange');
  range.value = String(-v || 0);
  const word = ruleBeforeWord(v);
  range.setAttribute?.('aria-valuetext', word);
  $('ruleBeforeVal').textContent = word;
}

function drawRulePrice(min, max) {
  $('ruleMin').value = min; $('ruleMax').value = max;
  if (typeof priceRange !== 'function') return;
  const hi = Math.max(150, Math.ceil(max / 50) * 50);
  recurringSlider = priceRange({
    lo: min, hi: max, bounds: { lo: 1, hi }, axis: ['₹1', '', `₹${hi}`],
    onChange: (lo, top) => {
      $('ruleMin').value = lo; $('ruleMax').value = top;
      if (recurringPreviewKey) invalidateRulePreview();
    },
  });
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
    ? `${r.enabled ? 'On' : 'Off'} · ${r.waiting || 0} waiting`
    : '';
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

// The rules show as pills on the Money tab; money.js draws them.
function renderRecurringRules() {
  if (typeof renderPills === 'function' && D) renderPills();
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
    keepScroll(renderRecurringRules);
    if (!recurringInitialized && D) editRecurringRule(undefined, false);
    openRecurringHash();
  } catch (e) { $('ruleListError').textContent = e.message; }
  finally { recurringLoading = false; }
}

function refreshRecurringRulesView() {
  if (!D?.owner) return;
  if (!recurringInitialized) editRecurringRule(undefined, false);
  if (Date.now() - recurringLoadedAt > 30000) loadRecurringRules();
}

function openRecurringHash() {
  if (recurringBusy) return;
  const match = /^#recurring-rule=(\d+|new)$/.exec(location.hash);
  if (!match) return;
  // The home page's Recurring rules widget links here with "new".
  const rule = match[1] === 'new' ? undefined : recurringRules.find((r) => r.id === Number(match[1]));
  if (!rule && match[1] !== 'new') return;
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

$('ruleClock').append?.(svgEl(icon('clock')));
$('ruleBeforeRange').addEventListener('input', () => {
  const v = -Number($('ruleBeforeRange').value) || 0;
  $('ruleBefore').value = String(v);
  const word = ruleBeforeWord(v);
  $('ruleBeforeRange').setAttribute?.('aria-valuetext', word);
  $('ruleBeforeVal').textContent = word;
});
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
    if (typeof toast === 'function') toast(saved.last_error ? `Rule saved. Scheduling will retry: ${saved.last_error}` : `Saved ${saved.name}. Its meals show in the ledger.`);
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
