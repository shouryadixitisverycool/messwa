const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// sale.js is a browser script, so it runs in a bare context here.
const ctx = vm.createContext({ setInterval() {} });
vm.runInContext(fs.readFileSync(path.join(__dirname, 'static/sale.js'), 'utf8') + '\nthis.mainBuyer = mainBuyer;', ctx);
const { mainBuyer } = ctx;

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
