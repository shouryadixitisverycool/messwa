'use strict';

// The "needs you" banner: problems the seller has to act on, from
// /api/attention. On the home page it's the Alerts widget (#attnHost); on
// the sales page it sits under the header. It stays hidden while there are
// none, and says how many there are with an "attention" event on document.
// Browser alerts only fire while a dashboard tab is open.

function attentionUnseen(items, seen) { return items.filter((x) => !seen.has(x.id)); }
if (typeof module !== 'undefined') module.exports = { attentionUnseen };

// Each kind's tag and colour. Red needs money or a fix now; egg needs a look.
const ATTN_KIND = {
  refund: ['REFUND', 'red'],
  blocked: ['BLOCKED', 'red'],
  schedule: ['SCHEDULE', 'red'],
  whatsapp: ['OFFLINE', 'red'],
  delivery: ['CHECK QR', 'egg'],
  post: ['NOT POSTED', 'egg'],
  rule: ['RULE', 'egg'],
};
const ATTN_SHOWN = 3; // rows before the rest fold away

if (typeof document !== 'undefined') document.addEventListener('DOMContentLoaded', async () => {
  let user;
  try { user = await me(); } catch { return; }
  if (!user) return;
  const key = 'messwa-attention-' + user.id + '-' + user.name + '-seen';
  let memorySeen = [];
  const readSeen = () => { try { return JSON.parse(localStorage.getItem(key)) || memorySeen; } catch { return memorySeen; } };
  const writeSeen = (ids) => { memorySeen = ids; try { localStorage.setItem(key, JSON.stringify(ids)); } catch {} };

  const box = el('section', 'attn');
  box.hidden = true;
  box.setAttribute('aria-label', 'Needs your attention');
  const head = el('div', 'attn-head');
  const mark = el('span', 'attn-mark', '!');
  mark.setAttribute('aria-hidden', 'true');
  const text = el('div', 'attn-text');
  const line = el('span', 'attn-line');
  line.setAttribute('role', 'status');
  text.append(line);
  const bell = el('button', 'attn-bell');
  bell.type = 'button';
  head.append(mark, text, bell);
  const list = el('div', 'attn-list');
  const more = el('button', 'attn-more');
  more.type = 'button';
  const note = el('p', 'attn-note');
  note.setAttribute('role', 'status');
  box.append(head, list, more, note);
  const host = document.getElementById('attnHost');
  if (host) host.append(box);
  else {
    const pageHead = document.querySelector('.app .page-head');
    // After the page's heading, so screen readers meet the title first.
    const heading = pageHead || document.querySelector('.app > h1');
    if (heading) heading.after(box); else document.querySelector('.app')?.prepend(box);
  }

  let open = false; // the folded rows are showing
  more.onclick = () => { open = !open; draw(); };

  const canAlert = typeof Notification !== 'undefined';
  function drawBell() {
    const p = canAlert ? Notification.permission : 'denied';
    bell.hidden = p === 'denied';
    bell.disabled = p === 'granted';
    bell.classList.toggle('on', p === 'granted');
    bell.textContent = p === 'granted' ? 'ALERTS ON' : 'ALERT ME';
    bell.title = p === 'granted'
      ? 'This browser alerts you about new problems while a Messwa tab is open'
      : 'Get a browser alert about new problems while a Messwa tab is open';
  }
  bell.onclick = async () => {
    try { await Notification.requestPermission(); } catch { note.textContent = 'This browser can\'t show alerts.'; }
    drawBell();
  };
  drawBell();

  const target = (item) => item.account ? 'account.html'
    : item.task ? './#attention-sale=' + item.task + (item.buyer ? '&buyer=' + encodeURIComponent(item.buyer) : '')
    : item.rule ? 'sales.html#recurring-rule=' + item.rule
    : 'sales.html#attention-meal=' + encodeURIComponent(item.date + '|' + item.meal);

  // openSale opens the buyer an item is about, or the one who most likely
  // needs you. A sale nobody answered has no buyer sheet; a running one
  // opens its sell menu, which needs the sales ledger loaded.
  async function openSale(id, buyer) {
    const r = await fetch('api/attention-sale?id=' + id, { cache: 'no-store' });
    if (!r.ok) throw new Error('Could not open that sale.');
    const v = await r.json();
    if (openSaleBuyer(v, buyer)) return;
    if (v.state === 'active') {
      // The ledger may be missing or stale (Buy never refreshes it), so
      // fetch it again when the sale isn't in it.
      if (openTaskMenu(v)) return;
      await loadNudge(true);
      if (openTaskMenu(v)) return;
    }
    throw new Error(v.state === 'active' ? 'Nobody has answered this sale yet.' : 'Nobody answered this sale before it ended.');
  }

  // "Lunch · Fri 13 Nov", or today/tomorrow/yesterday when it is close.
  function when(item) {
    if (!item.date) return '';
    const day = (d) => new Date(d + 'T12:00:00Z');
    const today = new Date().toLocaleDateString('en-CA');
    const diff = Math.round((day(item.date) - day(today)) / 86400000);
    const word = { 0: 'today', 1: 'tomorrow', '-1': 'yesterday' }[diff]
      || new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' }).format(day(item.date));
    return [cap(item.meal || ''), word].filter(Boolean).join(' · ');
  }

  function row(item) {
    const [tag, tone] = ATTN_KIND[item.kind] || ['CHECK', 'egg'];
    const r = el('div', 'attn-row ' + tone);
    const t = el('span', 'attn-tag', tag);
    const copy = el('div', 'attn-copy');
    const title = el('b', '', item.title);
    const w = when(item);
    if (w) title.append(el('small', '', ' · ' + w));
    copy.append(title);
    if (item.detail) copy.append(el('p', '', item.detail));
    const acts = el('div', 'attn-acts');
    const go = el('a', 'attn-btn go', item.account ? 'RECONNECT' : item.task ? 'OPEN SALE' : item.rule ? 'OPEN RULE' : 'VIEW MEAL');
    go.href = target(item);
    if (item.task && typeof openSaleBuyer === 'function') {
      go.onclick = async (e) => {
        e.preventDefault();
        note.textContent = '';
        try { await openSale(item.task, item.buyer); } catch (err) { note.textContent = err.message; }
      };
    }
    acts.append(go);
    if (item.resolvable) {
      const done = el('button', 'attn-btn', 'DONE');
      done.type = 'button';
      done.title = 'Hide this until it changes';
      done.onclick = async () => {
        done.disabled = true;
        try {
          await send('POST', 'api/attention', { id: item.id });
          items = items.filter((x) => x.id !== item.id);
          draw();
        } catch (err) { note.textContent = err.message; done.disabled = false; }
      };
      acts.append(done);
    }
    r.append(t, copy, acts);
    return r;
  }

  let items = [];
  function draw() {
    box.hidden = !items.length;
    document.dispatchEvent(new CustomEvent('attention', { detail: items.length }));
    if (!items.length) return;
    box.classList.toggle('red', items.some((x) => (ATTN_KIND[x.kind] || [])[1] === 'red'));
    line.textContent = items.length === 1 ? '1 THING NEEDS YOU' : `${items.length} THINGS NEED YOU`;
    const shown = open ? items : items.slice(0, ATTN_SHOWN);
    list.replaceChildren(...shown.map(row));
    const hidden = items.length - ATTN_SHOWN;
    more.hidden = hidden <= 0;
    more.textContent = open ? 'Show fewer' : `${hidden} more`;
  }

  function alertNew() {
    const current = new Set(items.map((x) => x.id));
    const seen = new Set(readSeen().filter((id) => current.has(id)));
    if (canAlert && Notification.permission === 'granted') {
      for (const item of attentionUnseen(items, seen)) {
        try {
          const n = new Notification(item.title, { body: [when(item), item.detail].filter(Boolean).join(' · '), tag: item.id });
          n.onclick = () => { window.focus(); location.href = target(item); n.close(); };
        } catch {}
      }
    }
    // Everything on screen counts as seen, so granting alerts later doesn't
    // fire one for each problem you are already looking at.
    writeSeen([...current]);
  }

  let busy = false;
  async function refresh() {
    if (busy) return;
    busy = true;
    try {
      const r = await fetch('api/attention', { cache: 'no-store' });
      if (r.status === 401 || r.status === 403) { items = []; draw(); return; }
      if (!r.ok) throw new Error('Couldn\'t refresh this list.');
      items = await r.json();
      note.textContent = '';
      keepScroll(draw);
      alertNew();
      const m = /^#attention-sale=(\d+)(?:&buyer=([^&]*))?$/.exec(location.hash);
      if (m && typeof openSaleBuyer === 'function' && typeof D !== 'undefined' && D) {
        history.replaceState(null, '', location.pathname + location.search);
        // Its own catch, so a sale with nothing to open doesn't read as a
        // failed refresh.
        try { await openSale(Number(m[1]), m[2] && decodeURIComponent(m[2])); } catch (err) { note.textContent = err.message; }
      }
    } catch (e) {
      note.textContent = e.message + ' Showing what loaded last.';
    } finally { busy = false; }
  }

  await refresh();
  setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, 15000);
  window.addEventListener('hashchange', refresh);
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') refresh(); });
});
