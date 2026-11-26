'use strict';

// The welcome chat's script. chatPlan turns what the server knows about a
// new account into the conversation so far and the question it's on, so a
// user who comes back halfway sees their earlier answers and lands on the
// next question. It touches no DOM, so welcome_test.cjs runs it in node.
//
// s holds:
//   name, status ('pending', 'requested', 'declined', 'active')
//   canStoreKeys, keySet
//   wa: the last /api/whatsapp answer, or null before it arrives
//   codeMode, phone: linking by phone number, and the number typed
//   sell: the last /api/setup-sell answer, or null
//   upi: the saved UPI ID, or null before it loads
//   acks: the questions answered on this browser that the server can't
//     tell apart from its defaults (groups, handoff, buyers)
//   editUPI: the user asked to change their UPI ID
//   errors: step to message, shown as the bot's reply under that step
//
// Each item is {key, from: 'bot' | 'me' | 'sys', text, ...}; the key stays
// the same while the item means the same thing, so the page can update it
// in place. ask is what the composer offers: {input, chips}.

const HANDOFF = {
  never: ['You handle it', 'You ask them to pay and send the QR'],
  on_reply: ['Hand them to me', 'You stop, I chat with them'],
};
const BUYERS = {
  anyone: ['Anyone', 'Whoever replies to the offer'],
  unsaved_only: ['Not my contacts', 'Friends stay with me'],
};

// postingOrder lists the picked groups in the order offers go out: by the
// server's order when it sends one, else as listed.
function postingOrder(groups) {
  const picked = groups.filter((g) => g.picked);
  if (picked.every((g) => g.order)) picked.sort((a, b) => a.order - b.order);
  return picked;
}

function chatPlan(s) {
  const items = [];
  const bot = (key, text, more) => items.push({ key, from: 'bot', text, ...more });
  const me = (key, text, more) => items.push({ key, from: 'me', text, ...more });
  const sys = (key, text, more) => items.push({ key, from: 'sys', text, ...more });
  const err = (step) => {
    const e = s.errors && s.errors[step];
    if (e) bot(`${step}-err`, e[0].toUpperCase() + e.slice(1), { tone: 'err' });
  };
  const done = (step, ask) => ({ items, ask, step });
  const acks = s.acks || {};

  bot('hi', `Hi ${s.name}! I sell your spare mess meals on WhatsApp and ping you when I need you.`);
  if (s.status === 'declined') {
    bot('declined', 'An admin declined this account. Talk to them if that\'s a mistake.', { tone: 'err' });
    return done('declined', null);
  }

  // 1. Mess API key. Without MESSWA_SECRET_KEY nobody can save one, so the
  // chat says so and carries on; the approval step lists it as missing.
  bot('key-q', 'First, your mess API key. I use it to read your meals and QR.');
  if (!s.canStoreKeys && !s.keySet) {
    bot('key-nosecret', 'The server has no `MESSWA_SECRET_KEY`, so I can\'t store keys yet. Ask an admin to set it.', { tone: 'warn' });
  } else if (!s.keySet) {
    bot('key-net', 'I check it with the mess portal, so be on the IIIT network.', { tone: 'dim' });
    err('key');
    return done('key', { input: 'key' });
  } else {
    me('key-a', '••••••••••••', { secret: true });
  }

  // 2. WhatsApp.
  const asked = s.status === 'requested' || s.status === 'active';
  bot('wa-q', 'Now link your WhatsApp. Linking sends no messages.');
  if (!s.wa) {
    bot('wa-wait', '', { typing: true });
    err('wa');
    return done('wa', null);
  }
  if (!s.wa.linked) {
    if (!s.codeMode) {
      bot('wa-qr', 'WhatsApp › Settings › Linked devices › Link a device. Then scan:', { qr: s.wa.qr || '' });
      err('wa');
      return done('wa', { chips: [{ id: 'phone', label: 'Use phone number instead' }] });
    }
    me('wa-mode', 'Use phone number instead');
    bot('wa-phone-q', 'What\'s your WhatsApp number?');
    if (s.phone) me('wa-phone-a', s.phone);
    if (s.wa.code) {
      bot('wa-code', 'In WhatsApp tap Link with phone number instead, then type:', { code: s.wa.code });
    }
    err('wa');
    return done('wa', { input: 'phone', chips: [{ id: 'qr', label: 'Scan a QR instead' }] });
  }
  sys('wa-ok', 'WhatsApp linked', { tone: 'ok' });

  // 3. Groups. The picker stays live, so changing it later saves at once.
  // Later answers (a saved UPI ID, a request) mean the user got past this
  // on another browser.
  const later = !!s.upi || asked;
  bot('groups-q', 'Where should I post your offers? I go top to bottom.');
  const g = s.sell;
  if (!g || !g.checked) {
    bot('groups-wait', g && !g.connecting ? 'I couldn\'t check your groups yet. Trying again…' : '', { typing: !g || g.connecting });
    err('groups');
    return done('groups', null);
  }
  bot('groups-pick', '', { groups: g.groups, order: postingOrder(g.groups).map((x) => x.jid) });
  if (!g.groups.some((x) => x.joined)) {
    bot('groups-none', 'You\'re not in any of these yet. Join one on your phone and I\'ll notice.', { tone: 'warn' });
    err('groups');
    return done('groups', null);
  }
  err('groups');
  const picked = postingOrder(g.groups);
  if (!(acks.groups || later)) {
    return done('groups', { chips: [{ id: 'groups', label: picked.length > 1 ? 'Post in these' : 'Post there', disabled: !g.saved || !picked.length }] });
  }
  me('groups-a', picked.map((x) => x.name || x.jid).join(', ') || 'None');

  // 4. How you sell.
  bot('handoff-q', 'A buyer replies to your offer. Who talks to them?');
  if (!(acks.handoff || later)) {
    err('how');
    return done('handoff', { chips: Object.entries(HANDOFF).map(([id, [label, sub]]) => ({ id: `handoff:${id}`, label, sub })) });
  }
  me('handoff-a', (HANDOFF[g.handoff] || HANDOFF.never)[0]);
  bot('buyers-q', 'Who can buy?');
  if (!(acks.buyers || later)) {
    err('how');
    return done('buyers', { chips: Object.entries(BUYERS).map(([id, [label, sub]]) => ({ id: `buyers:${id}`, label, sub })) });
  }
  me('buyers-a', (BUYERS[g.buyers] || BUYERS.anyone)[0]);

  // 5. UPI, then what a buyer sees with it.
  bot('upi-q', 'Buyers pay you on UPI. What\'s your UPI ID?');
  if (s.upi == null) {
    bot('upi-wait', '', { typing: true });
    return done('upi', null);
  }
  if (!s.upi || s.editUPI) {
    err('upi');
    return done('upi', { input: 'upi', value: s.upi || '' });
  }
  me('upi-a', s.upi, { edit: 'upi' });
  err('upi');
  bot('demo-q', 'Here\'s what a buyer gets:');
  bot('demo', '', { demo: s.upi });
  bot('upi-warn', 'I can\'t tell if a screenshot is real, so check your UPI app after each sale.', { tone: 'warn' });

  // 6. Approval.
  const missing = [
    !s.keySet && 'your mess key',
  ].filter(Boolean);
  if (asked) {
    me('ask-a', 'Ask the admins');
    if (s.status === 'active') {
      bot('in', 'You\'re in. Opening the auction house…', { tone: 'ok' });
      return done('in', null);
    }
    bot('ask-wait', 'Sent. I\'ll take you in as soon as one says yes.', { waiting: true });
    return done('waiting', null);
  }
  bot('ask-q', missing.length
    ? `Almost there. I still need ${missing.join(' and ')}.`
    : 'That\'s everything. Shall I ask the admins to let you in?');
  err('ask');
  return done('ask', { chips: [{ id: 'ask', label: 'Ask the admins', primary: true, disabled: missing.length > 0 }] });
}

// The steps, in order, for the progress bar.
const STEPS = ['key', 'wa', 'groups', 'handoff', 'buyers', 'upi', 'ask', 'waiting'];
function chatProgress(step) {
  const i = STEPS.indexOf(step);
  return i < 0 ? 1 : i / (STEPS.length - 1);
}
