// Run with node --test internal/dashboard/money_test.cjs.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

function money(extra = {}) {
  class Element {
    constructor(tag = 'div', cls = '') { this.tag = tag; this.className = cls; this.children = []; this.attrs = {}; this.dataset = {}; this.listeners = {}; this.style = { setProperty() {} }; this.classList = { toggle() {}, add() {}, remove() {} }; }
    append(...xs) { this.children.push(...xs); }
    replaceChildren(...xs) { this.children = xs; }
    setAttribute(k, v) { this.attrs[k] = v; }
    addEventListener(k, fn) { this.listeners[k] = fn; }
    querySelector() { return null; }
    get innerHTML() { throw new Error('innerHTML used'); }
    set innerHTML(v) { throw new Error('innerHTML used'); }
  }
  const nodes = new Map();
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  const ctx = vm.createContext({
    $, el: (tag, cls, text) => { const e = new Element(tag, cls); if (text != null) e.textContent = text; return e; },
    svgEl: () => new Element('svg'), icon: () => '', face: () => '', cap: (s) => s[0].toUpperCase() + s.slice(1),
    rupees: (v) => `₹${v}`, fmtShort: (d) => d, fmtPhone: (p) => p, tip() {}, matchMedia: () => ({ matches: true }),
    document: { createElementNS: (ns, tag) => new Element(tag) }, requestAnimationFrame: () => 0, cancelAnimationFrame() {},
    D: { owner: true }, ...extra,
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'static/money.js'), 'utf8'), ctx);
  return { $, run: (code) => vm.runInContext(code, ctx), ctx };
}

const text = (n) => [n.textContent || '', ...(n.children || []).map(text)].join('');

test('the jar holds nothing until a sale, at least one coin after, and never overflows', () => {
  const m = money();
  const cap = m.run('jarSpots.length');
  assert.ok(cap > 40, 'room for a month of coins');
  assert.equal(m.run('jarCoins({ fee: 4000, earned: 0, potential: 0 })'), 0);
  assert.equal(m.run('jarCoins({ fee: 4000, earned: 10, potential: 0 })'), 1);
  assert.equal(m.run('jarCoins({ fee: 4000, earned: 2000, potential: 0 })'), Math.round(cap / 2));
  assert.equal(m.run('jarCoins({ fee: 4000, earned: 9000, potential: 0 })'), cap);
});

test('with no bill the jar measures against what the month could have made', () => {
  const m = money();
  const cap = m.run('jarSpots.length');
  assert.equal(m.run('jarCoins({ fee: 0, earned: 300, potential: 300 })'), Math.round(cap / 2));
  assert.equal(m.run('jarCoins({ fee: 0, earned: 0, potential: 0 })'), 0);
});

test('the sold slide draws a coin per sold meal and a ring per wasted one', () => {
  const m = money();
  const s = m.run(`soldSlide({ sold: 3, spare: 5, earned: 150, extras_sold: 1, unknown: 0, by_meal: [{ meal: 'lunch', spare: 3, sold: 2 }, { meal: 'dinner', spare: 2, sold: 1 }] })`);
  const dots = s.children.find((c) => c.className === 'rmeals').children.map((r) => r.children[1].children.map((i) => i.className));
  assert.deepEqual(dots, [['on', 'on', ''], ['on', '']]);
  assert.match(text(s), /3 of 5/);
  assert.match(text(s), /\+₹150/);
  assert.match(text(s), /1 extra/);
});

test('the market slide says above, below or at market', () => {
  const m = money();
  assert.match(text(m.run('marketSlide({ vs_market: 4.2 })')), /₹4 above market/);
  assert.match(text(m.run('marketSlide({ vs_market: -3 })')), /₹3 below market/);
  assert.match(text(m.run('marketSlide({ vs_market: 0.4 })')), /At market/);
  assert.match(text(m.run('marketSlide({ vs_market: null })')), /No market asks/);
});

test('a buyer name goes in as text, however it looks', () => {
  const m = money();
  const name = '<img src=x onerror=alert(1)>';
  const s = m.run(`regularSlide({ top_buyer: { key: '+91900', name: ${JSON.stringify(name)}, phone: '+91900', week: 2, all_time: 5 } })`);
  assert.ok(text(s).includes(name));
  assert.match(text(s), /2× this week · 5× in all/);
});

test('rule pills name the rule, its price and whether it is on', () => {
  const m = money({
    recurringRules: [{ name: 'Weekday lunches', enabled: true, min: 45, max: 45 }, { name: 'Weekends', enabled: false, min: 20, max: 30 }, { name: 'Broken', enabled: true, last_error: 'no meals', min: 1, max: 2 }],
    recurringBusy: false, ruleListPrice: (r) => (r.min === r.max ? `₹${r.min}` : `₹${r.min}-${r.max}`), editRecurringRule() {},
  });
  const box = m.$('pills');
  m.run('rulePills($("pills"))');
  assert.deepEqual(box.children.map(text), ['Weekday lunches·₹45·on', 'Weekends·₹20-30·off', 'Broken·₹1-2·check']);
});

test('a week with only unread meals says so instead of 0 of 0', () => {
  const m = money();
  const s = m.run(`soldSlide({ sold: 0, spare: 0, earned: 0, extras_sold: 0, unknown: 2, by_meal: [] })`);
  assert.match(text(s), /Not read yet/);
  assert.match(text(s), /2 meals unread/);
  assert.doesNotMatch(text(s), /0 of 0/);
});

test('a meal with no art gets its first letter instead of throwing', () => {
  const m = money({ icon: (n) => { if (n !== 'lunch') throw new TypeError('no art'); return ''; } });
  const s = m.run(`soldSlide({ sold: 1, spare: 2, earned: 40, by_meal: [{ meal: 'brunch', spare: 1, sold: 1 }, { meal: 'lunch', spare: 1, sold: 0 }] })`);
  const rows = s.children.find((c) => c.className === 'rmeals').children;
  assert.equal(rows[0].children[0].textContent, 'B');
  assert.equal(rows[0].attrs['aria-label'], 'Brunch: 1 of 1 sold');
});

test('taking coins off the jar also stops the ones still falling', () => {
  const m = money({ matchMedia: () => ({ matches: false }) });
  const removed = [];
  m.run('var svg = { querySelector: () => ({ append() {} }) }');
  for (let i = 0; i < 5; i++) m.run(`dropCoin(svg, ${i}, 0)`);
  m.run('jarNodes.forEach((c, i) => { c.remove = () => globalThis.gone.push(i); })');
  m.ctx.gone = removed;
  m.run('for (let i = 2; i < 5; i++) dropCoinOff(i)');
  assert.deepEqual(removed, [2, 3, 4]);
  assert.equal(m.run('JSON.stringify([...jarFlying.keys()])'), '[0,1]');
});
