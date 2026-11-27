const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// welcome-plan.js is a browser script, so it runs in a bare context here.
const ctx = vm.createContext({});
vm.runInContext(fs.readFileSync(path.join(__dirname, 'static/welcome-plan.js'), 'utf8') + '\nthis.chatPlan = chatPlan; this.chatProgress = chatProgress; this.postingOrder = postingOrder;', ctx);
const { chatPlan, chatProgress, postingOrder } = ctx;

const group = (jid, name, joined, order = 0) => ({ jid, name, joined, picked: order > 0, order });
const sell = (more = {}) => ({
  checked: true, saved: true, handoff: 'never', buyers: 'anyone',
  groups: [group('a', 'Mess Buy Sell', true, 1), group('b', '', false), group('c', 'Mess 2.0', true, 2)],
  ...more,
});
const state = (more = {}) => ({
  name: 'asha', status: 'pending', canStoreKeys: true, keySet: false,
  wa: { linked: false, pairing: true, qr: 'data:x' }, codeMode: false, phone: '',
  sell: null, upi: '', acks: {}, edit: null, errors: {}, ...more,
});
// A plan's keys, and the ids of what it asks with.
const keys = (p) => [...p.items.map((x) => x.key)];
const asks = (p) => (p.ask ? [p.ask.input, ...(p.ask.chips || []).map((c) => c.id)].filter(Boolean) : []);

test('a new account is asked for its key first', () => {
  const p = chatPlan(state());
  assert.deepEqual(keys(p), ['hi', 'key-q', 'key-net']);
  assert.deepEqual(asks(p), ['key']);
  assert.equal(p.step, 'key');
});

test('a key error shows as the bot answer, capitalised', () => {
  const p = chatPlan(state({ errors: { key: 'mess portal rejected the mess API key' } }));
  assert.equal(p.items.at(-1).key, 'key-err');
  assert.equal(p.items.at(-1).text, 'Mess portal rejected the mess API key');
  assert.equal(p.items.at(-1).tone, 'err');
});

test('without a secret key the chat warns and moves on to WhatsApp', () => {
  const p = chatPlan(state({ canStoreKeys: false }));
  assert.ok(keys(p).includes('key-nosecret'));
  assert.equal(p.step, 'wa');
  // and the approval step later says the key is missing
  const end = chatPlan(state({ canStoreKeys: false, wa: { linked: true }, sell: sell(), acks: { groups: 1, handoff: 1, buyers: 1 }, upi: 'a@ok' }));
  assert.equal(end.step, 'ask');
  assert.match(end.items.find((x) => x.key === 'ask-q').text, /mess key/);
  assert.equal(end.ask.chips[0].disabled, true);
});

test('the WhatsApp step offers a QR, or a phone code', () => {
  let p = chatPlan(state({ keySet: true }));
  assert.deepEqual(keys(p).slice(-2), ['wa-q', 'wa-qr']);
  assert.equal(p.items.at(-1).qr, 'data:x');
  assert.deepEqual(asks(p), ['phone']);

  p = chatPlan(state({ keySet: true, codeMode: true, phone: '+91 90000 00000', wa: { linked: false, pairing: true, code: 'ABCD1234' } }));
  assert.deepEqual(keys(p).slice(-4), ['wa-mode', 'wa-phone-q', 'wa-phone-a', 'wa-code']);
  assert.equal(p.items.at(-1).code, 'ABCD1234');
  assert.deepEqual(asks(p), ['phone', 'qr']);
});

test('before WhatsApp answers, the bot is typing', () => {
  const p = chatPlan(state({ keySet: true, wa: null }));
  assert.equal(p.items.at(-1).typing, true);
  assert.equal(p.ask, null);
});

test('linked, the chat checks groups, then asks to confirm them', () => {
  let p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: { checked: false, connecting: true, groups: [] } }));
  assert.deepEqual(keys(p).slice(-3), ['wa-ok', 'groups-q', 'groups-wait']);
  assert.equal(p.ask, null);

  p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell() }));
  assert.equal(p.step, 'groups');
  assert.deepEqual(asks(p), ['groups']);
  assert.deepEqual([...p.items.find((x) => x.key === 'groups-pick').order], ['a', 'c']);
});

test('not in any group: the bot says so and waits', () => {
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell({ saved: false, groups: [group('a', 'A', false), group('b', 'B', false)] }) }));
  assert.equal(p.items.at(-1).key, 'groups-none');
  assert.equal(p.ask, null);
});

test('confirming groups needs a saved pick', () => {
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell({ saved: false }) }));
  assert.equal(p.ask.chips[0].disabled, true);
});

test('posting order follows the server, else the list', () => {
  assert.deepEqual([...postingOrder([group('a', '', true, 2), group('b', '', true, 1)]).map((g) => g.jid)], ['b', 'a']);
  const noOrder = [{ jid: 'a', picked: true }, { jid: 'b', picked: true }];
  assert.deepEqual([...postingOrder(noOrder).map((g) => g.jid)], ['a', 'b']);
});

test('how you sell: two questions, answered with chips', () => {
  const base = { keySet: true, wa: { linked: true }, sell: sell() };
  let p = chatPlan(state({ ...base, acks: { groups: true } }));
  assert.equal(p.step, 'handoff');
  assert.deepEqual(asks(p), ['handoff:never', 'handoff:on_reply']);
  p = chatPlan(state({ ...base, sell: sell({ handoff: 'on_reply' }), acks: { groups: true, handoff: true } }));
  assert.equal(p.items.find((x) => x.key === 'handoff-a').text, 'Hand them to me');
  assert.equal(p.step, 'buyers');
  assert.deepEqual(asks(p), ['buyers:anyone', 'buyers:unsaved_only']);
});

test('a returning user lands on the next question with earlier answers shown', () => {
  // UPI saved on another browser: the soft questions count as answered.
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell({ buyers: 'unsaved_only' }), upi: 'asha@okaxis' }));
  assert.equal(p.step, 'ask');
  for (const k of ['key-a', 'wa-ok', 'groups-a', 'handoff-a', 'buyers-a', 'upi-a', 'demo']) assert.ok(keys(p).includes(k), k);
  assert.equal(p.items.find((x) => x.key === 'groups-a').text, 'Mess Buy Sell, Mess 2.0');
  assert.equal(p.items.find((x) => x.key === 'buyers-a').text, 'Not my contacts');
  assert.equal(p.items.find((x) => x.key === 'demo').demo, 'asha@okaxis');
  assert.deepEqual(asks(p), ['ask']);
  assert.equal(p.ask.chips[0].disabled, false);
});

test('UPI: asked for, then changeable', () => {
  const base = { keySet: true, wa: { linked: true }, sell: sell(), acks: { groups: 1, handoff: 1, buyers: 1 } };
  let p = chatPlan(state({ ...base, upi: '' }));
  assert.equal(p.step, 'upi');
  assert.deepEqual(asks(p), ['upi']);
  p = chatPlan(state({ ...base, upi: null }));
  assert.equal(p.items.at(-1).typing, true);
  p = chatPlan(state({ ...base, upi: 'a@ok', edit: 'upi' }));
  assert.equal(p.step, 'upi');
  assert.equal(p.ask.value, 'a@ok');
  assert.deepEqual(asks(p), ['upi', 'keep']);
  assert.ok(!keys(p).includes('upi-a'));
});

test('a saved answer can be changed, or kept', () => {
  const base = { keySet: true, wa: { linked: true }, sell: sell({ handoff: 'on_reply' }), upi: 'a@ok' };
  let p = chatPlan(state({ ...base, edit: 'handoff' }));
  assert.equal(p.step, 'handoff');
  assert.deepEqual(asks(p), ['handoff:never', 'handoff:on_reply', 'keep']);
  assert.equal(p.ask.chips.find((c) => c.on).id, 'handoff:on_reply');
  p = chatPlan(state({ ...base, edit: 'key' }));
  assert.equal(p.step, 'key');
  assert.deepEqual(asks(p), ['key', 'keep']);
  // Each answer offers a change.
  p = chatPlan(state(base));
  for (const k of ['key-a', 'handoff-a', 'buyers-a', 'upi-a']) assert.ok(p.items.find((x) => x.key === k).edit, k);
});

test('a failed selling answer shows its error under its own question', () => {
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell(), acks: { groups: 1 }, errors: { handoff: 'disk full' } }));
  assert.equal(p.step, 'handoff');
  assert.equal(p.items.at(-1).key, 'handoff-err');
});

test('groups must be saved before going on, even with a UPI ID set', () => {
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell({ saved: false }), upi: 'a@ok' }));
  assert.equal(p.step, 'groups');
  assert.equal(p.ask.chips[0].disabled, true);
  assert.ok(!keys(p).includes('ask-q'));
});

test('a rejected phone number keeps its error next to the WhatsApp one', () => {
  const p = chatPlan(state({ keySet: true, codeMode: true, phone: '12', wa: { linked: false, pairing: true }, errors: { code: 'bad number' } }));
  assert.equal(p.items.at(-1).key, 'code-err');
  assert.deepEqual(asks(p), ['phone', 'qr']);
});

test('after asking, the chat waits; approval keeps the history', () => {
  const base = { keySet: true, wa: { linked: true }, sell: sell(), upi: 'a@ok' };
  let p = chatPlan(state({ ...base, status: 'requested' }));
  assert.deepEqual(keys(p).slice(-2), ['ask-a', 'ask-wait']);
  assert.equal(p.step, 'waiting');
  assert.equal(p.ask, null);
  p = chatPlan(state({ ...base, status: 'active' }));
  assert.deepEqual(keys(p).slice(-2), ['ask-a', 'in']);
  assert.ok(keys(p).includes('demo'));
});

test('a declined account only hears that', () => {
  const p = chatPlan(state({ status: 'declined', keySet: true }));
  assert.deepEqual(keys(p), ['hi', 'declined']);
  assert.equal(p.ask, null);
});

test('progress runs from the key to waiting', () => {
  assert.equal(chatProgress('key'), 0);
  assert.equal(chatProgress('waiting'), 1);
  assert.ok(chatProgress('upi') > chatProgress('groups'));
});

test('a failed answer is asked again even when later ones are saved', () => {
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell(), upi: 'a@ok', errors: { buyers: 'disk full' } }));
  assert.equal(p.step, 'buyers');
  assert.equal(p.items.at(-1).key, 'buyers-err');
  assert.deepEqual(asks(p), ['buyers:anyone', 'buyers:unsaved_only']);
});

test('the admins can\'t be asked while a group pick is saving', () => {
  const p = chatPlan(state({ keySet: true, wa: { linked: true }, sell: sell({ pending: true }), upi: 'a@ok' }));
  assert.equal(p.step, 'ask');
  assert.equal(p.ask.chips[0].disabled, true);
});
