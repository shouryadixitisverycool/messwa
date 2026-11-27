const { test } = require('node:test');
const assert = require('node:assert/strict');
const H = require('./static/home.js');

const TZ = 'Asia/Kolkata';
const at = (s) => Math.floor(Date.parse(s) / 1000);
const win = (meal, a, b) => ({ meal, start: at(`2026-11-26T${a}:00+05:30`), end: at(`2026-11-26T${b}:00+05:30`) });
const WINS = [win('breakfast', '07:15', '10:30'), win('lunch', '12:15', '14:45'), win('dinner', '19:15', '21:45')];

test('the verdict counts what needs you, else says the week', () => {
  assert.equal(H.verdictText(1, 340), '1 thing needs you.');
  assert.equal(H.verdictText(3, 0), '3 things need you.');
  assert.equal(H.verdictText(0, 340), 'All good. ₹340 this week.');
  assert.equal(H.verdictText(0, 1250.5), 'All good. ₹1,251 this week.');
  assert.equal(H.verdictText(0, 0), 'All good.');
});

test('the since line says when, and only what happened', () => {
  const now = at('2026-11-26T13:00:00+05:30');
  assert.equal(H.sinceText({ since: at('2026-11-24T20:00:00+05:30'), sold: 4, earned: 180, refunds: 1 }, now, TZ), 'Since Tuesday: 4 sold, ₹180, 1 refund cleared.');
  assert.equal(H.sinceText({ since: at('2026-11-25T20:00:00+05:30'), sold: 1, earned: 60, refunds: 2 }, now, TZ), 'Since yesterday: 1 sold, ₹60, 2 refunds cleared.');
  assert.equal(H.sinceText({ since: at('2026-11-26T09:40:00+05:30'), sold: 0, earned: 0, refunds: 0 }, now, TZ), 'Nothing new since 09:40.');
  assert.equal(H.sinceText({ since: at('2026-11-12T09:40:00+05:30'), sold: 2, earned: 90 }, now, TZ), 'Since 12 Nov: 2 sold, ₹90.');
  assert.equal(H.sinceText({ since: 0, sold: 3 }, now, TZ), '');
  assert.equal(H.sinceText(null, now, TZ), '');
});

test('the week runs Monday to today and skips unsold', () => {
  const N = { today: '2026-11-26', sales: [
    { date: '2026-11-23', state: 'sold', amount: 50 },
    { date: '2026-11-26', state: 'sending', amount: 40 },
    { date: '2026-11-25', state: 'armed', amount: 99 },
    { date: '2026-11-22', state: 'sold', amount: 70 },
  ] };
  assert.equal(H.weekTotal(N), 90);
  assert.equal(H.weekTotal(null), null);
});

test('rail nodes follow the windows and map states to icons', () => {
  const cells = [{ meal: 'breakfast', state: 'ate' }, { meal: 'lunch', state: 'selling' }, { meal: 'dinner', state: 'scheduled' }];
  const N = { today: '2026-11-26', windows: WINS, days: [{ date: '2026-11-26', cells }], upcoming: [] };
  const nodes = H.railNodes(N, null);
  assert.deepEqual(nodes.map((n) => n.icon), ['fork', 'live', 'clock']);
  assert.deepEqual(nodes.map((n) => n.tap), [false, true, true]);
  // Armed or handed off outside a sale, the sheet has nothing to show.
  const one = (cell) => H.railNodes({ ...N, days: [{ date: '2026-11-26', cells: [{ meal: 'breakfast', ...cell }] }] }, null)[0].tap;
  assert.equal(one({ state: 'armed' }), false);
  assert.equal(one({ state: 'handoff', task: { id: 4 } }), true);
  const icons = (state) => H.railNodes({ ...N, days: [{ date: '2026-11-26', cells: [{ meal: 'breakfast', state }] }] }, null)[0].icon;
  assert.equal(icons('open'), 'ticket');
  assert.equal(icons('sold'), 'coin');
  assert.equal(icons('wasted'), 'crumpled');
  assert.equal(icons('none'), 'empty');
  // A meal missing from the day reads as not registered.
  assert.equal(H.railNodes(N, null).length, 3);
  assert.equal(H.railNodes({ ...N, days: [] }, null)[0].icon, 'empty');
  // Before api/sales loads, api/state's windows draw unknown nodes.
  const D = { today: WINS.map((w) => ({ ...w, date: '2026-11-26' })) };
  assert.deepEqual(H.railNodes(null, D).map((n) => n.icon), ['unknown', 'unknown', 'unknown']);
});

test('hide snacks: the rail shows only the meals api/sales lists', () => {
  const four = [WINS[0], WINS[1], win('snacks', '16:30', '18:00'), WINS[2]];
  assert.equal(H.railNodes({ today: '2026-11-26', windows: four, days: [] }, null).length, 4);
  assert.equal(H.railNodes({ today: '2026-11-26', windows: WINS, days: [] }, null).length, 3);
});

test('rail progress fills to the meal being served and creeps between meals', () => {
  const p = (t) => H.railProgress(WINS, at(`2026-11-26T${t}:00+05:30`));
  assert.equal(p('06:00'), 0);
  assert.equal(p('08:00'), 0);
  assert.equal(p('13:00'), 0.5);
  assert.ok(p('11:00') > 0 && p('11:00') < 0.5);
  assert.equal(p('23:00'), 1);
});

test('a sale is new only against a previous load', () => {
  const a = H.soldKeys({ sales: [{ date: 'd', meal: 'lunch', state: 'sold', buyer: 'x' }] });
  const b = H.soldKeys({ sales: [{ date: 'd', meal: 'lunch', state: 'sold', buyer: 'x' }, { date: 'd', meal: 'dinner', state: 'sending', buyer: 'y' }, { date: 'd', meal: 'snacks', state: 'armed', buyer: 'z' }] });
  assert.equal(H.newSales(null, b), 0);
  assert.equal(H.newSales(a, b), 1);
  assert.equal(H.newSales(b, b), 0);
});

test('the orb: off beats live beats idle; attention tints it', () => {
  assert.deepEqual(H.orbState({ connected: false, live: true, needs: 0 }), { mode: 'off', attn: false });
  assert.deepEqual(H.orbState({ connected: true, live: true, needs: 2 }), { mode: 'live', attn: true });
  assert.deepEqual(H.orbState({ connected: null, live: false, needs: 0 }), { mode: 'idle', attn: false });
});

test('each kind of item gets its one fix', () => {
  const refund = H.cardSpec({ kind: 'refund', detail: 'Send ₹60 back to Kabir, unless they got the meal.', amount: 60, phone: '+91 90000 00103' }, 'Lunch · yesterday');
  assert.equal(refund.title, 'Refund ₹60');
  assert.equal(refund.sub, 'to Kabir · Lunch · yesterday');
  assert.equal(refund.action.href, 'upi://pay?am=60.00&cu=INR&tn=messwa%20refund');
  assert.equal(refund.alt.href, 'https://wa.me/919000000103');
  // With a sale, its buyer sheet sits beside the UPI link.
  assert.equal(H.cardSpec({ kind: 'refund', amount: 60, task: 7 }).alt.kind, 'sale');
  // Paise stay, so the label matches the link.
  const odd = H.cardSpec({ kind: 'refund', amount: 52.5, task: 7 });
  assert.equal(odd.action.label, 'Pay ₹52.50');
  assert.equal(odd.action.href, 'upi://pay?am=52.50&cu=INR&tn=messwa%20refund');
  assert.equal(H.cardSpec({ kind: 'refund', detail: 'x', task: 7 }).action.kind, 'sale');
  assert.equal(H.cardSpec({ kind: 'whatsapp' }).action.kind, 'relink');
  assert.equal(H.cardSpec({ kind: 'post', task: 3, group: 'g@g.us' }).action.kind, 'retry');
  assert.equal(H.cardSpec({ kind: 'post', task: 3 }).action.kind, 'sale');
  assert.equal(H.cardSpec({ kind: 'delivery', task: 3 }).action.kind, 'sale');
  // No sale to open: the meal on Sales instead.
  assert.equal(H.cardSpec({ kind: 'delivery', date: '2026-11-26', meal: 'lunch' }).action.href, 'sales.html#attention-meal=2026-11-26%7Clunch');
  assert.equal(H.cardSpec({ kind: 'blocked' }).action.href, 'sales.html');
  // A blocked sale often ended before anyone answered: always the meal.
  assert.equal(H.cardSpec({ kind: 'blocked', task: 9, date: '2026-11-26', meal: 'lunch' }).action.href, 'sales.html#attention-meal=2026-11-26%7Clunch');
  assert.equal(H.cardSpec({ kind: 'rule', rule: 4 }).action.href, 'sales.html#recurring-rule=4');
  assert.equal(H.cardSpec({ kind: 'schedule', date: '2026-11-26', meal: 'lunch' }).action.href, 'sales.html#attention-meal=2026-11-26%7Clunch');
  assert.equal(H.cardSpec({ kind: 'setup-groups' }).action.label, 'Pick groups');
  assert.equal(H.cardSpec({ kind: 'setup-upi' }).action.label, 'Add UPI');
  assert.equal(H.cardSpec({ kind: 'mystery', title: 'Huh' }).title, 'Huh');
});

test('setup gaps become cards that cannot be swiped away', () => {
  assert.deepEqual(H.setupItems({ no_groups: true, no_upi: true }).map((x) => [x.id, x.resolvable]), [['setup-groups', false], ['setup-upi', false]]);
  assert.deepEqual(H.setupItems(null), []);
});

test('a swipe dismisses past a third of the card', () => {
  assert.equal(H.swipeDone(100, 300), false);
  assert.equal(H.swipeDone(-110, 300), true);
});
