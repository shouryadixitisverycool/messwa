'use strict';

// Home: one verdict line over a living orb, today's meals as a rail, a
// "since you were last here" line, and a card per thing that needs you.
//
// app.js loads api/state (D) and api/sales (N) and calls homeRender and
// homeSalesLoaded. attention.js loads api/attention and hands the items to
// homeAttention instead of drawing its banner. api/home/visit says when you
// were last here and what happened since.
//
// The pure helpers come first so attention_test.cjs-style tests can load
// them in node; everything that touches the page is further down.

const homeInr = (v) => '₹' + Math.round(v).toLocaleString('en-IN');
// homeAmt keeps paise, so a refund's label matches what its link asks.
const homeAmt = (v) => (Math.round(v * 100) % 100 ? '₹' + Number(v).toFixed(2) : homeInr(v));

// The icon each meal's state shows on the rail.
const RAIL_ICON = {
  open: 'ticket', scheduled: 'clock',
  selling: 'live', armed: 'live', handoff: 'live',
  sold: 'coin', sending: 'coin',
  ate: 'fork', wasted: 'crumpled',
};
const RAIL_WORD = {
  ticket: 'registered', clock: 'scheduled', live: 'on sale now', coin: 'sold',
  fork: 'you ate it', crumpled: 'wasted', empty: 'not registered', unknown: 'unknown',
};
// States whose meal the sell sheet can open.
const RAIL_TAPPABLE = new Set(['open', 'scheduled', 'selling', 'armed', 'handoff', 'sold', 'sending']);
// A meal armed or handed off outside a sale has nothing for the sheet to
// show but a fresh price picker, so it needs its sale to be tappable.
const RAIL_NEEDS_TASK = new Set(['armed', 'handoff']);

// railNodes is today's meals in order, each with its window, state and
// icon. N is api/sales; it lists only the meals you see (hide snacks drops
// snacks from its windows). Before it loads, D's windows stand in.
function railNodes(N, D) {
  const wins = (N && N.windows && N.windows.length ? N.windows : (D && D.today) || []);
  const today = N ? N.today : wins[0] && wins[0].date;
  const day = N && ([...(N.days || []), ...(N.upcoming || [])].find((d) => d.date === today));
  return wins.map((w) => {
    const c = day && day.cells.find((x) => x.meal === w.meal);
    const state = c ? c.state : N ? 'none' : 'unknown';
    const icon = RAIL_ICON[state] || (state === 'unknown' ? 'unknown' : 'empty');
    const tap = RAIL_TAPPABLE.has(state) && !(RAIL_NEEDS_TASK.has(state) && !c.task);
    return { meal: w.meal, date: today, start: w.start, end: w.end, state, icon, tap };
  });
}

// railProgress is how far along the rail today is, 0 to 1: the line fills
// to the meal being served, and between meals creeps toward the next one.
function railProgress(nodes, now) {
  const n = nodes.length;
  if (n < 2 || now < nodes[0].start) return 0;
  let p = 0;
  for (let i = 0; i < n; i++) {
    const w = nodes[i];
    if (now < w.start) break;
    p = i;
    const next = nodes[i + 1];
    if (now >= w.end && next && now < next.start) p = i + (now - w.end) / (next.start - w.end);
  }
  return Math.min(1, p / (n - 1));
}

// weekTotal is what you made this week, Monday to today: every sold meal
// and extra in N.sales (friends' meals are kept apart, in N.friends).
function weekTotal(N) {
  if (!N || !N.today) return null;
  const d = new Date(N.today + 'T12:00:00Z');
  d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7));
  const mon = d.toISOString().slice(0, 10);
  return N.sales.filter((s) => (s.state === 'sold' || s.state === 'sending') && s.date >= mon && s.date <= N.today)
    .reduce((a, s) => a + s.amount, 0);
}

function verdictText(needs, week) {
  if (needs > 0) return needs === 1 ? '1 thing needs you.' : `${needs} things need you.`;
  return week > 0 ? `All good. ${homeInr(week)} this week.` : 'All good.';
}

// sinceWord names a past moment the way you'd say it: a time today,
// yesterday, a weekday within the week, else a date.
function sinceWord(u, now, tz) {
  const ymd = (x) => new Intl.DateTimeFormat('en-CA', { timeZone: tz }).format(x * 1000);
  const days = Math.round((Date.parse(ymd(now)) - Date.parse(ymd(u))) / 86400000);
  if (days <= 0) return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz }).format(u * 1000);
  if (days === 1) return 'yesterday';
  if (days < 7) return new Intl.DateTimeFormat('en-GB', { weekday: 'long', timeZone: tz }).format(u * 1000);
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: tz }).format(u * 1000);
}

// sinceText is the line under the verdict, from api/home/visit. Empty on
// your first visit. A refund counts once you mark it done, so it reads
// "cleared": you may have paid it or the buyer got the meal after all.
function sinceText(v, now, tz) {
  if (!v || !v.since) return '';
  const parts = [];
  if (v.sold) parts.push(`${v.sold} sold`);
  if (v.earned) parts.push(homeInr(v.earned));
  if (v.refunds) parts.push(v.refunds === 1 ? '1 refund cleared' : `${v.refunds} refunds cleared`);
  const when = sinceWord(v.since, now, tz);
  return parts.length ? `Since ${when}: ${parts.join(', ')}.` : `Nothing new since ${when}.`;
}

// soldKeys and newSales spot a sale that landed between two api/sales loads.
const soldKeys = (N) => new Set((N && N.sales || []).filter((s) => s.state === 'sold' || s.state === 'sending')
  .map((s) => [s.date, s.meal, s.slot || '', s.extra || '', s.buyer].join('|')));
function newSales(prev, next) {
  if (!prev) return 0;
  let n = 0;
  for (const k of next) if (!prev.has(k)) n++;
  return n;
}

// orbState is what the orb shows: off (WhatsApp down), else live while an
// offer is up in the groups, else idle. attn tints it while things need you.
function orbState({ connected, live, needs }) {
  return { mode: connected === false ? 'off' : live ? 'live' : 'idle', attn: needs > 0 };
}

// The UPI link for a refund. Buyers pay through WhatsApp, so messwa never
// learns their UPI ID: the link fills in the amount and note, and you pick
// who it goes to in your UPI app. Apps that insist on a payee refuse it,
// so the card keeps the sale (or a chat) beside it.
const refundLink = (amount) => `upi://pay?am=${Number(amount).toFixed(2)}&cu=INR&tn=${encodeURIComponent('messwa refund')}`;

// cardSpec turns an attention item (or a setup gap) into a card: an icon,
// a tone, a short title, a sub line and the one action that fixes it.
// when is attention.js's "Lunch · yesterday".
function cardSpec(item, when = '') {
  const sub = (...xs) => xs.filter(Boolean).join(' · ');
  // The sale's buyer sheet when there is a sale, else the meal on Sales.
  // A blocked sale usually ended before anyone answered, so it always
  // opens the meal.
  const open = (label = 'Open sale', meal = false) => (item.task && !meal ? { kind: 'sale', label }
    : { kind: 'href', label: 'Open meal', href: 'sales.html' + (item.date ? '#attention-meal=' + encodeURIComponent(item.date + '|' + item.meal) : '') });
  switch (item.kind) {
    case 'refund': {
      const who = /back to (.+?), unless/.exec(item.detail || '');
      const amt = item.amount > 0 ? item.amount : null;
      return {
        icon: 'refund', tone: 'bad', title: amt ? `Refund ${homeAmt(amt)}` : 'Refund due',
        sub: sub(who ? `to ${who[1]}` : item.detail, when),
        action: amt ? { kind: 'href', label: `Pay ${homeAmt(amt)}`, href: refundLink(amt) } : open(),
        alt: !amt ? null : item.task ? open('Open sale')
          : item.phone ? { kind: 'href', label: 'Chat', href: `https://wa.me/${item.phone.replace(/\D/g, '')}` } : null,
      };
    }
    case 'whatsapp':
      return { icon: 'link', tone: 'bad', title: 'WhatsApp is offline', sub: 'Offers can\'t go out', action: { kind: 'relink', label: 'Relink' } };
    case 'post':
      return {
        icon: 'retry', tone: 'warn', title: 'Offer didn\'t post', sub: sub(item.group_name, when),
        action: item.group && item.task ? { kind: 'retry', label: 'Retry' } : open(),
      };
    case 'delivery':
      return { icon: 'qr', tone: 'warn', title: 'Did the QR reach them?', sub: when, action: open() };
    case 'blocked':
      return { icon: 'stop', tone: 'bad', title: 'Sale blocked', sub: sub(item.detail, when), action: open('Open sale', true) };
    case 'schedule':
      return { icon: 'clock', tone: 'bad', title: 'Schedule failed', sub: sub(item.detail, when), action: { kind: 'href', label: 'Open meal', href: 'sales.html#attention-meal=' + encodeURIComponent(item.date + '|' + item.meal) } };
    case 'rule':
      return { icon: 'rule', tone: 'warn', title: 'Rule needs a look', sub: item.detail, action: { kind: 'href', label: 'Fix rule', href: 'sales.html#recurring-rule=' + item.rule } };
    case 'setup-groups':
      return { icon: 'groups', tone: 'warn', title: 'No groups to post in', sub: '', action: { kind: 'href', label: 'Pick groups', href: 'settings.html#offer' } };
    case 'setup-upi':
      return { icon: 'upi', tone: 'warn', title: 'No UPI QR for buyers', sub: '', action: { kind: 'href', label: 'Add UPI', href: 'settings.html#buyers' } };
    default:
      return { icon: 'dot', tone: 'warn', title: item.title || 'Needs a look', sub: sub(item.detail, when), action: open() };
  }
}

// setupItems are the gaps that stop selling, as cards. They can't be
// dismissed; fixing them in Settings makes them go.
function setupItems(D) {
  const out = [];
  if (D && D.no_groups) out.push({ id: 'setup-groups', kind: 'setup-groups', resolvable: false });
  if (D && D.no_upi) out.push({ id: 'setup-upi', kind: 'setup-upi', resolvable: false });
  return out;
}

// A swipe dismisses once it travels past this share of the card's width.
const SWIPE_AT = 0.35;
const swipeDone = (dx, width) => Math.abs(dx) >= width * SWIPE_AT;

if (typeof module !== 'undefined') {
  module.exports = { railNodes, railProgress, weekTotal, verdictText, sinceWord, sinceText, soldKeys, newSales, orbState, refundLink, cardSpec, setupItems, swipeDone };
}

// ---------- the page ----------

if (typeof document !== 'undefined' && document.getElementById('home')) {
  // Line icons, 24px, drawn in currentColor. Only our own markup.
  const I = (body) => `<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;
  const ICONS = {
    ticket: I('<path d="M4 7.5A1.5 1.5 0 0 1 5.5 6h13A1.5 1.5 0 0 1 20 7.5V10a2 2 0 0 0 0 4v2.5a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 16.5V14a2 2 0 0 0 0-4Z"/><path d="M14 6.5v11" stroke-dasharray="1.6 2"/>'),
    clock: I('<circle cx="12" cy="12" r="8"/><path d="M12 7.5V12l3 2"/>'),
    live: I('<circle cx="12" cy="12" r="3" fill="currentColor" stroke="none"/><circle class="rip" cx="12" cy="12" r="6"/><circle class="rip r2" cx="12" cy="12" r="9.5"/>'),
    coin: I('<circle cx="12" cy="12" r="8"/><path d="M9.5 8.5h5M9.5 11h5M12.5 8.5c1.8 0 1.8 4.5 0 4.5H9.5l4 3.5"/>'),
    fork: I('<path d="M8 4v6a2 2 0 0 0 4 0V4M10 4v16M16 4c-1.7 1-2.5 3-2.5 5.5S14.5 13 16 13v7"/>'),
    crumpled: I('<path d="M5 7.5 9 6l3 1.5L15.5 6 19 7.8l-.8 3.4 1.3 2.6-1.4 3.7-3.7-.6-3 1.6-3.2-1.4-3.4.5-.7-3.6 1.3-2.7Z"/><path d="m9.5 10 2 2.2 3-1.4M10 15l2-1.2"/>'),
    empty: I('<circle cx="12" cy="12" r="2" fill="currentColor" stroke="none"/>'),
    unknown: I('<circle cx="12" cy="12" r="8" stroke-dasharray="2 3"/>'),
    refund: I('<path d="M9 7 5 11l4 4"/><path d="M5 11h9a5 5 0 0 1 0 10h-2"/>'),
    link: I('<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/><path d="m4 4 16 16" opacity=".8"/>'),
    retry: I('<path d="M4 12a8 8 0 0 1 14-5.3L20 9"/><path d="M20 4v5h-5"/><path d="M20 12a8 8 0 0 1-14 5.3L4 15"/><path d="M4 20v-5h5"/>'),
    qr: I('<rect x="4" y="4" width="6" height="6" rx="1"/><rect x="14" y="4" width="6" height="6" rx="1"/><rect x="4" y="14" width="6" height="6" rx="1"/><path d="M14 14h2v2M20 14v6h-6v-2M17 17h.01"/>'),
    stop: I('<circle cx="12" cy="12" r="8"/><path d="m6.5 6.5 11 11"/>'),
    rule: I('<path d="M4 12a8 8 0 1 0 2.3-5.7"/><path d="M4 4v4h4"/><path d="M12 8v4l2.5 1.5"/>'),
    groups: I('<circle cx="9" cy="9" r="3"/><path d="M3.5 19a5.5 5.5 0 0 1 11 0"/><circle cx="17" cy="9.5" r="2.4"/><path d="M15.5 14.2A4.5 4.5 0 0 1 21 18.5"/>'),
    upi: I('<rect x="4" y="4" width="16" height="16" rx="3"/><path d="M8 8h3v3H8zM13 13h3v3h-3zM13 8h3M8 13v3"/>'),
    dot: I('<circle cx="12" cy="12" r="3"/>'),
    x: I('<path d="m7 7 10 10M17 7 7 17"/>'),
  };
  const icon24 = (k) => svgEl(ICONS[k] || ICONS.dot);

  const home = $('home');
  const orb = $('orb');
  const verdict = $('verdict');
  const since = $('since');
  const orbSays = $('orbSays');
  const railBox = $('rail');
  const cardsBox = $('fixes');
  const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
  const hmz = (u) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz() }).format(u * 1000);

  let visit = null;          // api/home/visit
  let attn = null;           // attention.js's items and helpers, once loaded
  let connected = null;      // WhatsApp, from api/whatsapp; null while unknown
  let lastSold = null;       // soldKeys of the last api/sales
  let gone = new Set();      // cards swiped away, until attention.js drops them
  let hadWa = false;         // the last items had the WhatsApp offline card

  // ---------- verdict, since and orb ----------

  function cardItems() {
    const items = attn ? attn.items.filter((x) => !gone.has(x.id)) : [];
    return [...setupItems(D), ...items];
  }

  function drawTop() {
    const needs = cardItems().length;
    const wk = weekTotal(N);
    // Until attention.js answers, Home can't say all is good.
    const text = attn || needs ? verdictText(needs, wk != null ? wk : visit ? visit.week_total : 0) : '\u00a0';
    if (verdict.textContent !== text) verdict.textContent = text;
    home.classList.toggle('needs', needs > 0);
    since.textContent = visit ? sinceText(visit, now(), tz()) : '';
    since.hidden = !since.textContent;
    const live = !!(D && (D.sales || []).some((s) => s.state === 'active'));
    const st = orbState({ connected, live, needs });
    orb.dataset.mode = st.mode;
    orb.classList.toggle('is-attn', st.attn);
    const says = st.mode === 'off' ? 'WhatsApp is disconnected.'
      : st.mode === 'live' ? 'An offer is live in your groups.' : connected ? 'Connected and idle.' : 'Idle.';
    if (orbSays.textContent !== says) orbSays.textContent = says;
  }

  // celebrate throws a spark from the orb and arcs a coin to the Money tab.
  function celebrate() {
    orb.classList.remove('is-spark');
    void orb.offsetWidth;
    orb.classList.add('is-spark');
    setTimeout(() => orb.classList.remove('is-spark'), 900);
    if (reduced()) return;
    const tab = document.querySelector('.shell-tab[data-tab="money"]');
    if (!tab || !tab.animate) return;
    const a = orb.getBoundingClientRect(), b = tab.getBoundingClientRect();
    const x0 = a.left + a.width / 2, y0 = a.top + a.height / 2;
    const x1 = b.left + b.width / 2, y1 = b.top + b.height / 2;
    // A quadratic arc that rises above both ends.
    const cx = (x0 + x1) / 2, cy = Math.min(y0, y1) - 140;
    const coin = el('div', 'fly-coin');
    coin.append(icon24('coin'));
    document.body.append(coin);
    const frames = [];
    for (let i = 0; i <= 16; i++) {
      const t = i / 16, u = 1 - t;
      const x = u * u * x0 + 2 * u * t * cx + t * t * x1;
      const y = u * u * y0 + 2 * u * t * cy + t * t * y1;
      frames.push({ transform: `translate(${x - 14}px, ${y - 14}px) scale(${1.3 - t * 0.6}) rotate(${t * 540}deg)`, opacity: t > 0.9 ? (1 - t) * 10 : 1 });
    }
    coin.animate(frames, { duration: 1100, easing: 'cubic-bezier(.45,0,.25,1)' }).finished
      .then(() => {
        coin.remove();
        tab.animate([{ transform: 'scale(1)' }, { transform: 'scale(1.12)' }, { transform: 'scale(1)' }], { duration: 300, easing: 'ease-out' });
      }).catch(() => coin.remove());
  }

  // ---------- today rail ----------

  function drawRail() {
    const nodes = railNodes(N, D);
    railBox.hidden = !nodes.length;
    if (!nodes.length) return;
    const t = now();
    railBox.style.setProperty('--fill', railProgress(nodes, t).toFixed(4));
    railBox.style.setProperty('--n', nodes.length);
    const list = $('railNodes');
    const sig = JSON.stringify(nodes.map((n) => [n.date, n.meal, n.icon, n.tap, t >= n.start && t < n.end]));
    if (list.dataset.sig === sig) return;
    list.dataset.sig = sig;
    list.replaceChildren(...nodes.map((n) => {
      const word = RAIL_WORD[n.icon];
      const name = cap(n.meal);
      const node = el(n.tap ? 'button' : 'div', `rail-node is-${n.icon}${t >= n.start && t < n.end ? ' now' : ''}`);
      if (n.tap) {
        node.type = 'button';
        node.addEventListener('click', () => {
          if (typeof openSellHere !== 'function' || !openSellHere(n.date, n.meal)) location.href = 'sales.html';
        });
      }
      // A plain div needs a role for its label to be read.
      if (!n.tap) node.setAttribute('role', 'img');
      node.setAttribute('aria-label', `${name}, ${hmz(n.start)} to ${hmz(n.end)}: ${word}`);
      node.title = `${name}: ${word}`;
      const dot = el('span', 'rail-dot');
      dot.append(icon24(n.icon));
      node.append(dot, el('span', 'rail-meal', name), el('span', 'rail-time', hmz(n.start)));
      return node;
    }));
  }

  // ---------- fix-it cards ----------

  // drawCards keeps a card's element across redraws and only moves the
  // ones whose place changed, so focus, a drag and an open QR panel survive
  // attention.js's 15s refresh and every api/state.
  const cardEls = new Map();
  function drawCards() {
    const items = cardItems();
    const keep = new Set(items.map((x) => x.id));
    for (const [id, c] of cardEls) {
      if (keep.has(id)) continue;
      cardEls.delete(id);
      clearInterval(c._timer);
      // A dismissed card slides out first; dismiss removes it.
      if (!c.classList.contains('leaving')) c.remove();
    }
    let at = cardsBox.firstChild;
    for (const it of items) {
      let c = cardEls.get(it.id);
      const sig = JSON.stringify(it);
      if (!c || c.dataset.sig !== sig) {
        const fresh = card(it);
        if (c && c._panel) {
          // Carry an open relink over, poll and all.
          Object.assign(fresh, { _panel: c._panel, _timer: c._timer });
          fresh.append(c._panel);
          const go = fresh.querySelector('.fix-go');
          if (go && it.kind === 'whatsapp') go.textContent = 'Close';
        }
        fresh.dataset.sig = sig;
        if (c) { if (at === c) at = fresh; c.replaceWith(fresh); }
        c = fresh;
        cardEls.set(it.id, c);
      }
      while (at && at !== c && at.classList.contains('leaving')) at = at.nextSibling;
      if (at === c) at = c.nextSibling;
      else cardsBox.insertBefore(c, at);
    }
    cardsBox.hidden = !items.length && !cardsBox.querySelector('.leaving');
    jumpToHash();
  }

  function card(it) {
    const spec = cardSpec(it, attn && it.date ? attn.when(it) : '');
    const c = el('article', `fix ${spec.tone}`);
    c.id = 'attn-' + it.id;
    c.setAttribute('aria-label', spec.title);
    const ic = el('span', 'fix-ic');
    ic.append(icon24(spec.icon));
    const text = el('div', 'fix-text');
    text.append(el('b', 'fix-title', spec.title));
    if (spec.sub) text.append(el('span', 'fix-sub', spec.sub));
    const acts = el('div', 'fix-acts');
    if (spec.alt) acts.append(actionEl(spec.alt, it, c, 'fix-alt'));
    acts.append(actionEl(spec.action, it, c, 'fix-go'));
    const row = el('div', 'fix-row');
    row.append(ic, text, acts);
    if (it.resolvable) {
      const x = el('button', 'fix-x');
      x.type = 'button';
      x.title = 'Done, hide this';
      x.setAttribute('aria-label', `Done: ${spec.title}`);
      x.append(icon24('x'));
      x.addEventListener('click', () => dismiss(it, c));
      row.append(x);
      swipeable(c, it);
    }
    c.append(row);
    return c;
  }

  function actionEl(a, it, c, cls) {
    if (a.kind === 'href') {
      const l = el('a', cls, a.label);
      l.href = a.href;
      if (/^https:/.test(a.href)) { l.target = '_blank'; l.rel = 'noopener'; }
      return l;
    }
    const b = el('button', cls, a.label);
    b.type = 'button';
    b.addEventListener('click', () => run(a.kind, it, c, b));
    return b;
  }

  function say(c, text, bad) {
    let n = c.querySelector('.fix-note');
    if (!n) { n = el('p', 'fix-note'); n.setAttribute('role', 'status'); c.append(n); }
    n.textContent = text;
    n.classList.toggle('bad', !!bad);
  }

  async function run(kind, it, c, b) {
    b.disabled = true;
    try {
      if (kind === 'retry') {
        b.textContent = 'Retrying';
        await send('POST', 'api/sell-action', { action: 'resend', id: it.task, group: it.group });
        b.textContent = 'Sent';
        say(c, 'Posting again. This goes once it\'s up.');
        setTimeout(() => attn && attn.refresh(), 4000);
        return;
      }
      if (kind === 'sale') {
        await attn.openSale(it.task, it.buyer);
        b.disabled = false;
        return;
      }
      if (kind === 'relink') {
        b.disabled = false;
        relink(c, b);
      }
    } catch (err) {
      b.disabled = false;
      if (kind === 'retry') b.textContent = 'Retry';
      say(c, err.message, true);
    }
  }

  // relink opens the account page's WhatsApp link flow inside the card: a
  // QR to scan while unlinked, a wait while a linked phone reconnects.
  function relink(c, b) {
    if (c._panel) { c._panel.remove(); c._panel = null; clearInterval(c._timer); b.textContent = 'Relink'; return; }
    const p = el('div', 'fix-panel');
    const status = el('p', 'fix-panel-text', 'Checking WhatsApp');
    status.setAttribute('role', 'status');
    const img = el('img', 'fix-qr');
    img.alt = 'QR code to scan in WhatsApp, Linked devices';
    img.hidden = true;
    p.append(img, status);
    c.append(p);
    c._panel = p;
    b.textContent = 'Close';
    let asked = false;  // a QR was asked for and hasn't expired
    let busy = false;
    const stop = (why) => {
      clearInterval(c._timer);
      status.textContent = why + ' Close and tap Relink to try again.';
    };
    const tick = async () => {
      if (busy || document.visibilityState !== 'visible') return;
      busy = true;
      try {
        const r = await fetch('api/whatsapp', { cache: 'no-store' });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || 'WhatsApp status is unavailable.');
        if (!p.isConnected) return;
        // Mid-pairing the socket can be up before the phone has linked.
        if (d.linked && d.connected) {
          status.textContent = 'Connected.';
          img.hidden = true;
          clearInterval(c._timer);
          connected = true;
          drawTop();
          attn && attn.refresh();
          return;
        }
        if (!d.qr) img.hidden = true;
        if (d.linked) {
          status.textContent = 'Linked. Waiting for your phone to come online.';
        } else if (d.qr) {
          img.src = d.qr;
          img.hidden = false;
          status.textContent = 'On your phone open WhatsApp, Linked devices, Link a device, and scan this.';
        } else if (d.error && asked) {
          // Our link attempt failed: don't start another right away. An error
          // from before this panel opened falls through and a new POST clears it.
          stop(d.error);
        } else if (!d.pairing) {
          // First time, or the last code expired: get a new one.
          status.textContent = asked ? 'That code expired. Getting a new one.' : 'Getting a QR code';
          asked = true;
          await send('POST', 'api/whatsapp', {});
        }
      } catch (err) {
        if (p.isConnected) stop(err.message);
      } finally { busy = false; }
    };
    tick();
    c._timer = setInterval(tick, 3000);
  }

  async function dismiss(it, c) {
    if (c.classList.contains('leaving')) return;
    gone.add(it.id);
    c.classList.add('leaving');
    c.style.removeProperty('opacity');
    drawTop();
    const slid = new Promise((ok) => setTimeout(ok, reduced() ? 0 : 220));
    try {
      await attn.resolve(it.id);
      await slid;
      c.remove();
      drawCards();
    } catch (err) {
      gone.delete(it.id);
      c.classList.remove('leaving');
      c.style.removeProperty('transform');
      cardEls.set(it.id, c);
      drawCards();
      drawTop();
      say(c, err.message, true);
    }
  }

  // swipeable lets a finger (or mouse) fling a card sideways to dismiss it.
  // Vertical drags scroll the page as usual.
  function swipeable(c, it) {
    let x0 = 0, y0 = 0, dx = 0, on = false, id = null;
    c.addEventListener('pointerdown', (e) => {
      if (e.button > 0 || e.target.closest('button, a, img')) return;
      x0 = e.clientX; y0 = e.clientY; dx = 0; on = false; id = e.pointerId;
    });
    c.addEventListener('pointermove', (e) => {
      if (e.pointerId !== id) return;
      const mx = e.clientX - x0, my = e.clientY - y0;
      if (!on) {
        if (Math.abs(my) > 10 && Math.abs(my) > Math.abs(mx)) { id = null; return; }
        if (Math.abs(mx) < 8) return;
        on = true;
        c.setPointerCapture(id);
        c.classList.add('dragging');
      }
      dx = mx;
      c.style.setProperty('transform', `translateX(${dx}px)`);
      c.style.setProperty('opacity', String(1 - Math.min(0.7, Math.abs(dx) / c.offsetWidth)));
    });
    const end = (e) => {
      if (e.pointerId !== id) return;
      id = null;
      if (!on) return;
      c.classList.remove('dragging');
      if (swipeDone(dx, c.offsetWidth)) {
        c.style.setProperty('transform', `translateX(${Math.sign(dx) * c.offsetWidth}px)`);
        dismiss(it, c);
      } else {
        c.style.removeProperty('transform');
        c.style.removeProperty('opacity');
      }
    };
    c.addEventListener('pointerup', end);
    c.addEventListener('pointercancel', end);
  }

  // #attn-<id> (from an attention notification) scrolls to that card once.
  let jumped = '';
  function jumpToHash() {
    const h = location.hash;
    if (!h.startsWith('#attn-') || h === jumped) return;
    const c = document.getElementById(h.slice(1));
    if (!c) return;
    jumped = h;
    c.scrollIntoView({ block: 'center', behavior: reduced() ? 'auto' : 'smooth' });
    c.classList.add('flash');
    setTimeout(() => c.classList.remove('flash'), 1600);
  }
  addEventListener('hashchange', jumpToHash);

  // ---------- data ----------

  // attention.js calls this with its items instead of drawing its banner.
  window.homeAttention = (items, helpers) => {
    attn = { ...helpers, items };
    for (const id of gone) if (!items.some((x) => x.id === id)) gone.delete(id);
    // The offline card went: WhatsApp is probably back, so the orb asks.
    const wa = items.some((x) => x.kind === 'whatsapp');
    if (hadWa && !wa) loadWhatsApp();
    hadWa = wa;
    drawCards();
    drawTop();
  };

  // app.js calls homeRender after each api/state and homeSalesLoaded after
  // each api/sales. A sale shows up in api/sales, so while Home is open it
  // reloads that every 15s at most rather than app.js's three minutes.
  window.homeRender = () => {
    if (typeof nudgeAt !== 'undefined' && Date.now() - nudgeAt > 15000) loadNudge(true);
    drawTop();
    drawRail();
    drawCards();
  };
  window.homeSalesLoaded = () => {
    const keys = soldKeys(N);
    if (N && newSales(lastSold, keys) > 0) celebrate();
    if (N) lastSold = keys;
    drawTop();
    drawRail();
  };

  async function loadWhatsApp() {
    try {
      const r = await fetch('api/whatsapp', { cache: 'no-store' });
      if (!r.ok) { connected = null; return; }
      const d = await r.json();
      connected = !!(d.linked && d.connected);
    } catch { connected = null; }
    drawTop();
  }

  async function loadVisit() {
    try {
      visit = await send('POST', 'api/home/visit');
    } catch {
      try { const r = await fetch('api/home/visit', { cache: 'no-store' }); if (r.ok) visit = await r.json(); } catch {}
    }
    drawTop();
  }

  loadVisit();
  loadWhatsApp();
  let visitAt = Date.now();
  const ping = () => { visitAt = Date.now(); loadVisit(); };
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') ping(); });
  setInterval(() => {
    if (document.visibilityState !== 'visible') return;
    loadWhatsApp();
    drawRail();
    // While Home stays open, keep the visit going (the server starts a new
    // one after 30 minutes away).
    if (Date.now() - visitAt > 5 * 60 * 1000) ping();
  }, 30000);
}
