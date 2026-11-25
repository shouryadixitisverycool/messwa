const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// sale.js is a browser script, so it runs in a bare context here.
const ctx = vm.createContext({ setInterval() {} });
vm.runInContext(fs.readFileSync(path.join(__dirname, 'static/sale.js'), 'utf8') + '\nthis.mainBuyer = mainBuyer; this.todayPlan = todayPlan; this.mealLabel = mealLabel;', ctx);
const { mainBuyer, todayPlan, mealLabel } = ctx;

const buyer = (key, state, at, extra = {}) => ({ key, state, joined: at, events: [], ...extra });
const sale = (buyers, extra = {}) => ({ state: 'active', buyers, ...extra });

test('a buyer who needs you comes first', () => {
  const v = sale([buyer('a', 'offered', 50), buyer('b', 'parked', 10), buyer('c', 'negotiating', 90)]);
  assert.equal(mainBuyer(v).key, 'b');
});

test('then the buyer paying or who bought', () => {
  assert.equal(mainBuyer(sale([buyer('a', 'negotiating', 90), buyer('b', 'offered', 10)])).key, 'b');
  assert.equal(mainBuyer(sale([buyer('a', 'passed', 90), buyer('b', 'bought', 10, { asked: 60 })], { state: 'sold' })).key, 'b');
});

test('otherwise whoever did something last', () => {
  const v = sale([
    buyer('a', 'negotiating', 10, { events: [{ kind: 'read', at: 200 }] }),
    buyer('b', 'queued', 100),
  ]);
  assert.equal(mainBuyer(v).key, 'a');
});

test('a refund due beats a buyer who bought, even when muted, after the sale ends', () => {
  const v = sale([buyer('a', 'bought', 90, { asked: 60 }), buyer('b', 'passed', 10, { refund: 60, muted: true })], { state: 'sold' });
  assert.equal(mainBuyer(v).key, 'b');
});

test('a handed-off buyer needs you only while the sale runs', () => {
  const bs = [buyer('a', 'bought', 10, { asked: 60 }), buyer('b', 'handed_off', 90)];
  assert.equal(mainBuyer(sale(bs)).key, 'b');
  assert.equal(mainBuyer(sale(bs, { state: 'sold' })).key, 'a');
});

test('no buyers, no pick', () => {
  assert.equal(mainBuyer(sale([])), null);
});

// ---------- extras sold apart from their meal ----------

const DAY = '2026-11-25';
const run = (id, meal, extra = {}) => ({ id, date: DAY, meal, base_meal: meal.split('#')[0], state: 'active', buyers: [], ...extra });
const kinds = (plan) => plan.cards.map((c) => (c.kind === 'tile' ? `tile:${c.meal}` : c.kind === 'sched' ? `sched:${c.s.meal}` : `sale:${c.v.meal}`));

test('an egg on sale never takes the place of its dinner', () => {
  const plan = todayPlan([run(7, 'dinner#e1', { extra: 'Egg' })], [], DAY, ['lunch', 'dinner']);
  assert.deepEqual(kinds(plan), ['tile:lunch', 'tile:dinner', 'sale:dinner#e1']);
});

test('the dinner and its eggs each get a card', () => {
  const plan = todayPlan([run(7, 'dinner#e2', { extra: 'Egg' }), run(3, 'dinner'), run(8, 'dinner#e1', { extra: 'Egg' })], [], DAY, ['lunch', 'dinner']);
  assert.deepEqual(kinds(plan), ['tile:lunch', 'sale:dinner', 'sale:dinner#e1', 'sale:dinner#e2']);
  assert.deepEqual([...plan.used].sort(), [3, 7, 8]);
});

test('a scheduled egg shows today beside the scheduled dinner, not instead of it', () => {
  const waiting = [
    { id: 1, date: DAY, meal: 'dinner#e1', base_meal: 'dinner', extra: 'Egg' },
    { id: 2, date: DAY, meal: 'dinner', base_meal: 'dinner' },
    { id: 3, date: '2026-11-26', meal: 'dinner#e1', base_meal: 'dinner', extra: 'Egg' },
  ];
  assert.deepEqual(kinds(todayPlan([], waiting, DAY, ['dinner'])), ['sched:dinner', 'sched:dinner#e1']);
});

test('a sold egg stays on Today; an unsold one folds under ENDED', () => {
  const sold = run(5, 'dinner#e1', { extra: 'Egg', state: 'sold' });
  const lapsed = run(6, 'dinner#e2', { extra: 'Egg', state: 'expired' });
  const plan = todayPlan([sold, lapsed], [], DAY, ['dinner']);
  assert.deepEqual(kinds(plan), ['tile:dinner', 'sale:dinner#e1']);
  assert.equal(plan.used.has(6), false);
});

test('labels name the extra after its meal', () => {
  assert.equal(mealLabel({ meal: 'dinner#e1', base_meal: 'dinner', extra: 'Egg' }), 'dinner · Egg');
  assert.equal(mealLabel({ meal: 'dinner', base_meal: 'dinner' }), 'dinner');
  assert.equal(mealLabel({ meal: 'dinner#f1', base_meal: 'dinner', for_friend: true, friend: 'Ravi' }), 'dinner · for Ravi');
});
