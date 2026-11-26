'use strict';

// The app shell: the top bar, the three main tabs (Home, Buy, Money) and
// the avatar menu (Settings, Account, and for admins Insights and Usage).
// On phones the tabs move to a bar at the bottom of the screen.
//
// To use it, a page loads common.js, then this, and names its place on
// <body>:
//
//   <link rel="stylesheet" href="style.css">
//   <link rel="stylesheet" href="shell.css">
//   <script src="common.js" defer></script>
//   <script src="shell.js" defer></script>
//   <body data-page="money">
//
// data-page is one of home, buy, money (a tab) or settings, account,
// insights, usage (in the avatar menu). The shell goes in as the body's
// first child, so the page needs no header of its own.
//
// It sets a dot on the Home tab while attention.js reports alerts, and
// exposes shellBadge(tab, on) for other pages to do the same.

const SHELL_TABS = [
  { id: 'home', name: 'Home', href: './', art: '<path d="M4 11.5 12 5l8 6.5"/><path d="M6.5 10v9h11v-9"/>' },
  { id: 'buy', name: 'Buy', href: 'buy.html', art: '<path d="M5 8h14l-1.2 11H6.2z"/><path d="M9 8V7a3 3 0 0 1 6 0v1"/>' },
  { id: 'money', name: 'Money', href: 'sales.html', art: '<rect x="3.5" y="6.5" width="17" height="11" rx="2"/><circle cx="12" cy="12" r="2.4"/><path d="M7 9.5v5M17 9.5v5"/>' },
];
const SHELL_MENU = {
  settings: { name: 'Settings', href: 'settings.html', art: '<circle cx="12" cy="12" r="3"/><path d="M12 3.5v2.2M12 18.3v2.2M3.5 12h2.2M18.3 12h2.2M6 6l1.6 1.6M16.4 16.4 18 18M6 18l1.6-1.6M16.4 7.6 18 6"/>' },
  account: { name: 'Account', href: 'account.html', art: '<circle cx="12" cy="9" r="3.5"/><path d="M5 19.5c1.4-3.2 4-4.5 7-4.5s5.6 1.3 7 4.5"/>' },
  insights: { name: 'Insights', href: 'insights.html', art: '<path d="M5 19V11M10 19V6M15 19v-5M20 19V9"/>' },
  usage: { name: 'Usage', href: 'usage.html', art: '<path d="M4 13h3l2-6 4 12 2-6h5"/>' },
};
const SHELL_CROWN = '<path d="M4 17.5 3 8l5 4 4-6.5 4 6.5 5-4-1 9.5z"/><path d="M4.5 20h15"/>';

function shellSvg(paths, cls) {
  return svgEl(`<svg class="${cls || 'shell-ic'}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths}</svg>`);
}

// shellBadge puts a dot on a tab (on = true) or takes it off.
function shellBadge(tab, on) {
  const t = document.querySelector(`.shell-tab[data-tab="${tab}"]`);
  if (t) t.classList.toggle('has-dot', !!on);
}

(() => {
  const page = document.body.dataset.page || '';
  const here = SHELL_TABS.find((t) => t.id === page) || SHELL_MENU[page];

  const bar = el('header', 'shell');
  const inner = el('div', 'shell-in');

  const brand = el('a', 'shell-brand');
  brand.href = './';
  brand.setAttribute('aria-label', 'messwa home');
  const logo = el('span', 'shell-logo');
  logo.append(svgEl(icon('coin')));
  brand.append(logo, el('span', 'shell-name', 'messwa'));
  // Phones have no room for the wordmark, so they name the page instead.
  const title = el('span', 'shell-here', here ? here.name : '');

  const tabs = el('nav', 'shell-tabs');
  tabs.setAttribute('aria-label', 'Main');
  for (const t of SHELL_TABS) {
    const a = el('a', 'shell-tab');
    a.href = t.href;
    a.dataset.tab = t.id;
    if (t.id === page) a.setAttribute('aria-current', 'page');
    a.append(shellSvg(t.art), el('span', 'shell-tab-name', t.name));
    tabs.append(a);
  }

  // ---------- avatar menu ----------

  const me_ = el('div', 'shell-me');
  const avatar = el('button', 'shell-avatar');
  avatar.type = 'button';
  avatar.setAttribute('aria-haspopup', 'menu');
  avatar.setAttribute('aria-expanded', 'false');
  avatar.setAttribute('aria-controls', 'shellMenu');
  avatar.setAttribute('aria-label', 'Your menu');
  const face_ = el('span', 'shell-initial', '');
  face_.setAttribute('aria-hidden', 'true');
  avatar.append(face_);
  if (SHELL_MENU[page]) avatar.classList.add('here');

  // The popup holds the name card, then the menu proper, so only menu
  // items, the admin group and the separator sit inside role=menu.
  const menu = el('div', 'shell-menu');
  menu.hidden = true;
  const list = el('div', 'shell-list');
  list.id = 'shellMenu';
  list.setAttribute('role', 'menu');
  list.setAttribute('aria-label', 'Your menu');
  const who = el('div', 'shell-who');
  const whoName = el('b', '', '');
  const whoRole = el('span', '', '');
  who.append(whoName, whoRole);

  const item = (id, cls) => {
    const m = SHELL_MENU[id];
    const a = el('a', 'shell-item' + (cls ? ' ' + cls : ''));
    a.href = m.href;
    a.setAttribute('role', 'menuitem');
    if (id === page) a.setAttribute('aria-current', 'page');
    a.append(shellSvg(m.art), el('span', '', m.name));
    return a;
  };
  const admin = el('div', 'shell-admin');
  admin.setAttribute('role', 'group');
  admin.setAttribute('aria-labelledby', 'shellCrown');
  admin.hidden = true;
  const crown = el('div', 'shell-crown');
  crown.id = 'shellCrown';
  crown.setAttribute('role', 'presentation');
  crown.append(shellSvg(SHELL_CROWN, 'shell-ic crown'), el('span', '', 'Admin'));
  admin.append(crown, item('insights'), item('usage'));

  const out = el('button', 'shell-item shell-out');
  out.type = 'button';
  out.setAttribute('role', 'menuitem');
  out.append(shellSvg('<path d="M14 5h4.5v14H14"/><path d="M10 8.5 6.5 12l3.5 3.5M6.5 12H15"/>'), el('span', '', 'Sign out'));
  out.addEventListener('click', async () => {
    try { await send('POST', 'api/logout'); } catch {} finally { location.href = 'login.html'; }
  });

  const sep = el('div', 'shell-sep');
  sep.setAttribute('role', 'separator');
  list.append(item('settings'), item('account'), admin, sep, out);
  menu.append(who, list);
  me_.append(avatar, menu);

  const items = () => [...menu.querySelectorAll('.shell-item')].filter((x) => !x.closest('[hidden]'));
  // focus: 'first' or 'last' moves focus into the menu as it opens.
  function openMenu(open, focus) {
    menu.hidden = !open;
    avatar.setAttribute('aria-expanded', String(open));
    document.body.classList.toggle('shell-menu-open', open);
    if (open && focus) { const list = items(); list[focus === 'last' ? list.length - 1 : 0]?.focus(); }
  }
  // A keyboard click (detail 0) lands on the first item; a pointer click
  // leaves focus on the avatar, where the arrows then take over.
  avatar.addEventListener('click', (e) => openMenu(menu.hidden, e.detail === 0 && 'first'));
  avatar.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    openMenu(true, e.key === 'ArrowDown' ? 'first' : 'last');
  });
  document.addEventListener('pointerdown', (e) => { if (!menu.hidden && !me_.contains(e.target)) openMenu(false); });
  // Focus leaving the avatar and menu (Tab, Shift+Tab, a click elsewhere)
  // closes it. focusout fires after the browser has picked the next stop.
  me_.addEventListener('focusout', (e) => { if (!menu.hidden && e.relatedTarget && !me_.contains(e.relatedTarget)) openMenu(false); });
  menu.addEventListener('keydown', (e) => {
    const list = items();
    const i = list.indexOf(document.activeElement);
    const go = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: list.length - 1 }[e.key];
    if (go === undefined) return;
    e.preventDefault();
    list[(go + list.length) % list.length]?.focus();
  });
  // Capture, so Escape closes the menu before a page's own keys see it.
  addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !menu.hidden) { e.stopImmediatePropagation(); openMenu(false); avatar.focus(); }
  }, true);

  inner.append(brand, title, tabs, me_);
  bar.append(inner);
  document.body.prepend(bar);
  document.body.classList.add('has-shell');

  me().then((m) => {
    face_.textContent = (m.name || '?').trim().charAt(0).toUpperCase();
    avatar.title = m.name;
    whoName.textContent = m.name;
    whoRole.textContent = m.role === 'admin' ? 'Admin' : 'Member';
    admin.hidden = m.role !== 'admin';
    avatar.classList.toggle('admin', m.role === 'admin');
  }).catch(() => {});

  document.addEventListener('attention', (e) => shellBadge('home', e.detail > 0));
})();
