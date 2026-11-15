// Watchtower: shows the campaigns of one orders file, kept current by
// server-sent events. Everything is rendered from the data the server sends;
// the page never asks again on a timer. A one-second clock only advances the
// durations of what is running.
'use strict';

(function () {
  const app = document.getElementById('app');
  const SVG = 'http://www.w3.org/2000/svg';

  // ---- small DOM helpers -------------------------------------------------

  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    setAttrs(el, attrs);
    append(el, kids);
    return el;
  }

  function sv(tag, attrs, ...kids) {
    const el = document.createElementNS(SVG, tag);
    setAttrs(el, attrs);
    append(el, kids);
    return el;
  }

  function setAttrs(el, attrs) {
    if (!attrs) return;
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (k === 'text') el.textContent = v;
      else el.setAttribute(k, v === true ? '' : String(v));
    }
  }

  function append(el, kids) {
    for (const kid of kids.flat(Infinity)) {
      if (kid === undefined || kid === null || kid === false) continue;
      el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    }
  }

  // ---- formatting --------------------------------------------------------

  // ---- texts ------------------------------------------------------------

  // The texts come from the server's catalogue of the chosen language,
  // with English for anything not translated.
  const I = { lang: 'en', def: 'en', languages: [], texts: {} };
  const LANG_KEY = 'hetman.lang';

  // pick chooses the message of key, by plural form for a message that
  // depends on the number n.
  function pick(key, params) {
    const m = I.texts[key];
    if (m === undefined) return null;
    if (typeof m === 'string') return m;
    const n = params && params.n !== undefined ? Number(params.n) : 0;
    let cat = 'other';
    try { cat = new Intl.PluralRules(m.lang).select(n); } catch (e) { /* other */ }
    if (m.forms[cat] !== undefined) return m.forms[cat];
    if (m.forms.other !== undefined) return m.forms.other;
    return Object.values(m.forms)[0] || '';
  }

  // t renders a message as text.
  function t(key, params) {
    const m = pick(key, params);
    if (m === null) return key;
    return m.replace(/\{([a-z][a-z0-9_]*)\}/g, (all, k) => params && params[k] !== undefined ? String(params[k]) : all);
  }

  // tt is t for code where t names a tile.
  const tt = t;

  // tn renders a message as parts for h(), so that a parameter can be an
  // element such as a node name in bold.
  function tn(key, params) {
    const m = pick(key, params);
    if (m === null) return [key];
    return m.split(/(\{[a-z][a-z0-9_]*\})/).filter((x) => x !== '').map((x) => {
      const k = /^\{([a-z][a-z0-9_]*)\}$/.exec(x);
      return k && params && params[k[1]] !== undefined ? params[k[1]] : x;
    });
  }

  // tk renders the text of a value, such as a status, or the value itself
  // when there is none.
  function tk(prefix, value) {
    return I.texts[prefix + value] !== undefined ? t(prefix + value) : value;
  }

  function statusText(s) { return tk('page.status.', s); }
  function stateText(s) { return I.texts['page.badge.' + s] !== undefined ? t('page.badge.' + s) : tk('page.state.', s); }

  const STATE_MARK = {
    running: 'running', halted: 'halted', done: 'passed', failed: 'failed',
    error: 'error', interrupted: 'interrupted', waiting: 'waiting', limit: 'limit', offline: 'offline', asking: 'asking',
  };

  function savedLang() {
    try { return localStorage.getItem(LANG_KEY) || ''; } catch (e) { return ''; }
  }

  async function loadTexts(lang) {
    const res = await fetch('/api/texts' + (lang ? '?lang=' + encodeURIComponent(lang) : ''));
    const d = await res.json();
    I.lang = d.lang;
    I.def = d.default;
    I.languages = d.languages || [];
    I.texts = d.texts || {};
    document.documentElement.lang = I.lang;
    document.getElementById('brand').textContent = t('page.brand');
    const live = document.getElementById('live');
    live.querySelector('.live-text').textContent = t('page.live.' + (live.dataset.state === 'lost' ? 'lost' : live.dataset.state));
    langSwitch();
  }

  // The language switcher: every language of the list; one without a
  // catalogue shows English texts. The choice is kept in this browser.
  function langSwitch() {
    const sel = document.getElementById('lang');
    sel.setAttribute('aria-label', t('page.lang.label'));
    sel.title = t('page.lang.label');
    sel.replaceChildren(...I.languages.map((l) => h('option', { value: l.code, selected: l.code === I.lang },
      l.native + (l.translated ? '' : ' · ' + t('page.lang.english_only')))));
    sel.onchange = async () => {
      try { localStorage.setItem(LANG_KEY, sel.value); } catch (e) { /* not kept */ }
      await loadTexts(sel.value);
      S.route = { view: '' };
      onRoute();
    };
  }

  function ms(t) { return t ? Date.parse(t) : NaN; }

  function fmtDur(d) {
    if (!isFinite(d) || d < 0) return '';
    if (d < 1000) return t('page.dur.sec', { v: (d / 1000).toFixed(2) });
    if (d < 60000) return t('page.dur.sec', { v: (d / 1000).toFixed(1) });
    const s = Math.floor(d / 1000);
    if (s < 3600) return t('page.dur.min', { m: Math.floor(s / 60), s: String(s % 60).padStart(2, '0') });
    return t('page.dur.hour', { h: Math.floor(s / 3600), m: String(Math.floor(s / 60) % 60).padStart(2, '0') });
  }

  function pad(n, w) { return String(n).padStart(w || 2, '0'); }

  function fmtTime(t, withMs) {
    const d = new Date(t);
    if (isNaN(d)) return '';
    const now = new Date();
    let s = pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
    if (withMs) s += '.' + pad(d.getMilliseconds(), 3);
    if (d.toDateString() !== now.toDateString()) {
      s = d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + s;
    }
    return s;
  }

  // A duration that keeps growing while running: the clock updates every
  // element carrying data-since.
  function liveDur(since, until) {
    if (until) return h('span', { text: fmtDur(ms(until) - ms(since)) });
    return h('span', { 'data-since': since, text: fmtDur(Date.now() - ms(since)) });
  }

  // ---- status marks, drawn here ------------------------------------------

  function mark(status, size) {
    const cls = 'mark ' + (size || '') + ' st-' + status;
    const label = statusText(status);
    const el = sv('svg', { class: cls, viewBox: '0 0 16 16', role: 'img', 'aria-label': label });
    el.append(sv('title', { text: label }));
    const ink = { fill: 'none', stroke: 'currentColor', 'stroke-width': 1.7, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' };
    const paper = { class: 'paper', fill: 'none', 'stroke-width': 1.8, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' };
    switch (status) {
      case 'passed':
      case 'done':
        el.append(sv('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
          sv('path', Object.assign({ d: 'M4.7 8.3l2.2 2.2 4.4-4.7' }, paper)));
        break;
      case 'failed':
      case 'error':
        el.append(sv('rect', { x: 2.6, y: 2.6, width: 10.8, height: 10.8, rx: 2.2, fill: 'currentColor', transform: 'rotate(45 8 8)' }),
          sv('path', Object.assign({ d: 'M6 6l4 4M10 6l-4 4' }, paper)));
        break;
      case 'warning':
        el.append(sv('path', { d: 'M8 1.5l6.9 12.3H1.1z', fill: 'currentColor', stroke: 'currentColor', 'stroke-width': 0.8, 'stroke-linejoin': 'round' }),
          sv('path', Object.assign({ d: 'M8 6v3.4' }, paper)),
          sv('circle', { cx: 8, cy: 11.7, r: 0.95, class: 'paper-fill' }));
        break;
      case 'running':
        el.append(sv('circle', Object.assign({ cx: 8, cy: 8, r: 6, opacity: 0.28 }, ink, { 'stroke-width': 2.2 })),
          sv('path', Object.assign({ d: 'M8 2a6 6 0 0 1 6 6', class: 'spin' }, ink, { 'stroke-width': 2.2 })),
          sv('circle', { cx: 8, cy: 8, r: 1.8, fill: 'currentColor' }));
        break;
      case 'waiting':
        el.append(sv('circle', Object.assign({ cx: 8, cy: 8, r: 6.2 }, ink)),
          sv('path', Object.assign({ d: 'M8 4.6V8l2.3 1.5' }, ink)));
        break;
      case 'interrupted':
        el.append(sv('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
          sv('path', Object.assign({ d: 'M3.6 8.6h2.2l1.2-2.6 2 4.4 1.2-2.4h2.2' }, paper, { 'stroke-width': 1.5 })));
        break;
      case 'halted':
        el.append(sv('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
          sv('path', Object.assign({ d: 'M6.2 5.2v5.6M9.8 5.2v5.6' }, paper, { 'stroke-width': 2 })));
        break;
      case 'limit':
        el.append(sv('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
          sv('path', Object.assign({ d: 'M5.2 5h5.6M5.2 11h5.6M6 5.2c0 2 4 3.6 4 5.6M10 5.2c0 2-4 3.6-4 5.6' }, paper, { 'stroke-width': 1.4 })));
        break;
      case 'asking':
        el.append(sv('path', { d: 'M2.2 3.6a2 2 0 0 1 2-2h7.6a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2H7.4L4.2 14.6v-3H4.2a2 2 0 0 1-2-2z', fill: 'currentColor' }),
          sv('path', Object.assign({ d: 'M6.3 5.2a1.8 1.8 0 1 1 2.4 1.7c-.5.2-.7.5-.7 1v.3' }, paper, { 'stroke-width': 1.4 })),
          sv('circle', { cx: 8, cy: 10, r: 0.8, class: 'paper-fill' }));
        break;
      case 'offline':
        el.append(sv('circle', { cx: 8, cy: 8, r: 7, fill: 'currentColor' }),
          sv('path', Object.assign({ d: 'M4.6 7.4a5 5 0 0 1 6.8 0M6.2 9.4a2.6 2.6 0 0 1 3.6 0M4.4 4.4l7.2 7.2' }, paper, { 'stroke-width': 1.5 })));
        break;
      default: // notrun
        el.append(sv('circle', Object.assign({ cx: 8, cy: 8, r: 6 }, ink, { 'stroke-width': 1.5, 'stroke-dasharray': '2.2 2.1' })));
    }
    return el;
  }

  function stateBadge(state) {
    return h('span', { class: 'badge st-' + state }, mark(STATE_MARK[state] || state), h('span', { class: 'txt', text: stateText(state) }));
  }

  // ---- state and routing -------------------------------------------------

  const S = {
    route: { view: '' }, // nothing shown yet
    stream: null,
    streamKey: '',
    list: null,
    tasks: null,       // null while the folder has no task queue
    campaign: null,
    events: [],
    freshFrom: 0,
    open: new Set(),   // keys of <details> the person opened or closed
    closed: new Set(),
    logs: new Map(),   // log viewers kept across renders
    forms: new Map(),  // what was typed into an answer form, by question number
  };

  function parseRoute() {
    const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
    if (parts[0] === 'c' && /^\d+$/.test(parts[1] || '')) {
      const tab = ['pipeline', 'timeline', 'chronicle', 'words'].includes(parts[2]) ? parts[2] : 'pipeline';
      return { view: 'campaign', id: Number(parts[1]), tab: tab, node: tab === 'pipeline' ? parts[3] || '' : '' };
    }
    if (parts[0] === 'tasks') return { view: 'tasks' };
    return { view: 'list' };
  }

  function href(id, tab, node) {
    let s = '#/c/' + id + '/' + (tab || 'pipeline');
    if (node) s += '/' + encodeURIComponent(node);
    return s;
  }

  function onRoute() {
    const prev = S.route;
    S.route = parseRoute();
    const lists = (v) => v === 'list' || v === 'tasks';
    if (lists(prev.view) && lists(S.route.view)) {
      render();
    } else if (prev.view !== S.route.view || prev.id !== S.route.id) {
      S.campaign = null;
      S.events = [];
      S.open.clear();
      S.closed.clear();
      closeLogs();
      app.replaceChildren(h('p', { class: 'empty', text: t('page.loading') }));
      connect();
    } else {
      if (prev.node !== S.route.node) closeLogs();
      render();
    }
  }

  function setLive(state, key) {
    const el = document.getElementById('live');
    el.dataset.state = state;
    el.querySelector('.live-text').textContent = t('page.live.' + key);
  }

  function connect() {
    if (S.stream) S.stream.close();
    const url = (S.route.view === 'campaign' ? '/api/stream?campaign=' + S.route.id + '&' : '/api/stream?') + 'lang=' + encodeURIComponent(I.lang);
    const es = new EventSource(url);
    S.stream = es;
    setLive('connecting', 'connecting');
    es.onopen = () => setLive('live', 'live');
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) {
        setLive('lost', 'lost');
        if (S.route.view === 'campaign' && !S.campaign) {
          app.replaceChildren(h('p', { class: 'empty' }, t('page.not_found', { id: S.route.id }), ' ', h('a', { href: '#/', text: t('page.all_campaigns') })));
        }
      } else {
        setLive('connecting', 'reconnecting');
      }
    };
    es.addEventListener('list', (ev) => { S.list = JSON.parse(ev.data); render(); });
    es.addEventListener('tasks', (ev) => { S.tasks = JSON.parse(ev.data); render(); });
    es.addEventListener('campaign', (ev) => {
      S.campaign = JSON.parse(ev.data);
      S.campaign.columns = S.campaign.columns || [];
      S.campaign.nodes = S.campaign.nodes || {};
      document.getElementById('orders').textContent = S.campaign.orders;
      render();
    });
    es.addEventListener('chronicle', (ev) => {
      const d = JSON.parse(ev.data);
      if (d.reset) { S.events = []; S.freshFrom = Infinity; }
      else S.freshFrom = d.events.length ? d.events[0].seq : Infinity;
      S.events = S.events.concat(d.events);
      if (S.route.tab === 'chronicle') render();
    });
  }

  // ---- rendering ---------------------------------------------------------

  function render() {
    const focusKey = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.fkey : '';
    const keep = saveScroll();
    if (S.route.view === 'list') renderList();
    else if (S.route.view === 'tasks') renderTasks();
    else if (S.campaign) renderCampaign();
    restoreScroll(keep);
    if (focusKey) {
      const el = app.querySelector('[data-fkey="' + CSS.escape(focusKey) + '"]');
      if (el) el.focus({ preventScroll: true });
    }
  }

  function saveScroll() {
    const out = {};
    for (const el of app.querySelectorAll('[data-scroll]')) {
      out[el.dataset.scroll] = {
        left: el.scrollLeft, top: el.scrollTop,
        bottom: el.scrollHeight - el.scrollTop - el.clientHeight < 8,
      };
    }
    return out;
  }

  function restoreScroll(keep) {
    for (const el of app.querySelectorAll('[data-scroll]')) {
      const k = keep[el.dataset.scroll];
      if (el.dataset.stick === 'bottom' && (!k || k.bottom)) { el.scrollTop = el.scrollHeight; continue; }
      if (!k) {
        // A pipeline shown for the first time scrolls to the current step.
        const here = el.dataset.scroll === 'lane' && el.querySelector('.col.current');
        if (here && here.offsetLeft + here.offsetWidth > el.clientWidth) el.scrollLeft = here.offsetLeft - 40;
        continue;
      }
      el.scrollLeft = k.left;
      el.scrollTop = k.top;
    }
  }

  // Keeps the open state of a <details> across renders.
  function details(key, openByDefault, attrs, summary, body) {
    const isOpen = S.open.has(key) || (openByDefault && !S.closed.has(key));
    const el = h('details', Object.assign({ open: isOpen }, attrs), summary, body);
    el.addEventListener('toggle', () => {
      if (el.open) { S.open.add(key); S.closed.delete(key); } else { S.closed.add(key); S.open.delete(key); }
    });
    return el;
  }

  // Tabs between the campaigns and the tasks, shown once the folder has a
  // task queue.
  function listTabs(current) {
    if (!S.tasks) return null;
    const n = (list) => list ? ' (' + list.length + ')' : '';
    return h('nav', { class: 'tabs' },
      h('a', { href: '#/', 'aria-current': current === 'list' ? 'page' : false, text: t('page.list.title') + n(S.list) }),
      h('a', { href: '#/tasks', 'aria-current': current === 'tasks' ? 'page' : false, text: t('page.tasks.title') + n(S.tasks) }));
  }

  const TASK_MARK = {
    queued: 'notrun', cancelled: 'notrun', running: 'running', waiting: 'asking',
    done: 'passed', failed: 'failed', halted: 'halted', error: 'error',
  };

  function renderTasks() {
    document.title = t('page.tasks.title') + ' · ' + t('page.brand');
    if (!S.tasks) {
      app.replaceChildren(h('h1', { text: t('page.tasks.title') }),
        h('p', { class: 'empty' }, tn('page.tasks.no_queue', { command: h('code', { text: 'hetman task add' }) }))); // i18n: keep (a command)
      return;
    }
    if (S.tasks.length === 0) {
      app.replaceChildren(listTabs('tasks'), h('p', { class: 'empty', text: t('page.tasks.empty') }));
      return;
    }
    const rows = S.tasks.map((task) => {
      const go = task.campaign ? () => { location.hash = href(task.campaign); } : null;
      const st = TASK_MARK[task.state] || task.state;
      return h('tr', { onclick: go, class: go ? '' : 'still' },
        h('td', { class: 'id', text: '#' + task.id }),
        h('td', { class: 'flow' }, task.kind, task.key ? h('div', { class: 'sub', text: task.key }) : null),
        h('td', { class: 'state' }, h('span', { class: 'badge st-' + st }, mark(st), h('span', { class: 'txt', text: tk('page.tasks.state.', task.state) }))),
        h('td', { class: 'id' }, task.campaign ? h('a', { href: href(task.campaign), 'data-fkey': 'task-' + task.id, onclick: (e) => e.stopPropagation(), text: '#' + task.campaign }) : '—'),
        h('td', { class: 'num started' }, liveDur(task.added)),
        h('td', { class: 'waits', text: task.waits || '' }));
    });
    const cols = ['task', 'kind', 'state', 'campaign', 'age', 'waits'];
    app.replaceChildren(listTabs('tasks'),
      h('table', { class: 'list tasks' },
        h('thead', null, h('tr', null, cols.map((c) => h('th', { text: t('page.tasks.col.' + c) })))),
        h('tbody', null, rows)));
  }

  function renderList() {
    document.title = t('page.brand');
    if (!S.list) return;
    if (S.list.length === 0) {
      app.replaceChildren(listTabs('list') || h('h1', { text: t('page.list.title') }),
        h('p', { class: 'empty' }, tn('page.list.empty', { command: h('code', { text: 'hetman run' }) }))); // i18n: keep (a command)
      return;
    }
    const rows = S.list.map((k) => {
      const go = () => { location.hash = href(k.id); };
      return h('tr', { onclick: go },
        h('td', { class: 'id' }, h('a', { href: href(k.id), 'data-fkey': 'row-' + k.id, onclick: (e) => e.stopPropagation(), text: '#' + k.id })),
        h('td', { class: 'flow', text: k.flow }),
        h('td', { class: 'state' }, stateBadge(k.state), k.haltRequested ? [' ', h('span', { class: 'tag', text: t('page.halt_requested') })] : null),
        h('td', { class: 'num started', text: fmtTime(k.started) }),
        h('td', { class: 'num dur' }, liveDur(k.started, k.ended)),
        h('td', { class: 'marks' }, miniPipe(k)));
    });
    app.replaceChildren(listTabs('list') || h('h1', { text: t('page.list.title') }),
      h('table', { class: 'list' },
        h('thead', null, h('tr', null, ['campaign', 'flow', 'state', 'started', 'duration', 'steps'].map((c) => h('th', { text: t('page.list.col.' + c) })))),
        h('tbody', null, rows)));
  }

  function miniPipe(k) {
    return h('span', { class: 'minipipe', 'aria-label': t('page.list.steps_label') }, (k.marks || []).map((m) =>
      h('span', { class: 'mp' + (m.current ? ' current' : '') + (m.side ? ' side' : ''), title: m.node + ': ' + statusText(m.status) + (m.attempts > 1 ? ' ×' + m.attempts : '') },
        mark(m.status, 'small'))));
  }

  function position(c) {
    if (c.end) return null;
    const list = c.nodes[c.node] || [];
    const last = list[list.length - 1];
    const node = h('b', { text: c.node });
    if (c.state === 'running' && last && last.status === 'running') {
      return tn('page.pos.running', { node: node, attempt: last.n });
    }
    if (c.state === 'halted') return tn('page.pos.halted', { node: node });
    if (c.state === 'asking') return tn('page.pos.asking', { node: node });
    if (c.state === 'waiting') {
      return tn('page.pos.waiting', { node: node, at: fmtTime(c.waitUntil),
        left: h('span', { 'data-until': c.waitUntil, text: fmtDur(ms(c.waitUntil) - Date.now()) }) });
    }
    if (c.state === 'limit') return tn('page.pos.limit', { node: node });
    if (c.state === 'offline') return tn('page.pos.offline', { node: node });
    if (c.state === 'interrupted') return tn('page.pos.interrupted', { node: node });
    if (c.lockWait) return tn('page.pos.lock', { node: node, lock: h('code', { text: c.lockWait }), holder: c.lockHolder });
    return tn('page.pos.at', { node: node });
  }

  function renderCampaign() {
    const c = S.campaign;
    const r = S.route;
    document.title = '#' + c.id + ' ' + c.flow + ' · ' + tk('page.state.', c.state) + ' · ' + t('page.brand');
    // A child campaign is halted and resumed with the campaign a person started.
    const canHalt = !c.parent && (c.state === 'running' || c.state === 'interrupted' || c.state === 'asking') && !c.haltRequested;
    const canResume = !c.parent && (c.state === 'halted' || c.state === 'error' || c.state === 'interrupted' || c.state === 'limit' ||
      c.state === 'offline' || c.haltRequested || (c.state === 'asking' && !c.driven));
    const pos = position(c);
    const out = [
      h('nav', { class: 'crumbs' }, h('a', { href: '#/', text: '← ' + t('page.all_campaigns') }),
        c.parent ? [' · ', tn('page.child.runs_this', {
          parent: h('a', { href: href(c.parent), text: t('page.child.parent', { id: c.parent }) }),
          path: h('code', { text: c.path }) }),
        c.top !== c.parent ? [' ', tn('page.child.part_of', { top: h('a', { href: href(c.top), text: t('page.child.campaign', { id: c.top }) }) })] : null] : null),
      h('div', { class: 'head' },
        h('h1', null, t('page.campaign.title', { id: c.id }) + ' ', h('span', { class: 'flow', text: c.flow })),
        stateBadge(c.state),
        c.haltRequested ? h('span', { class: 'tag', text: t('page.halt_requested_long') }) : null,
        h('div', { class: 'controls' },
          h('button', { class: 'btn', 'data-fkey': 'halt', disabled: !canHalt, title: t('page.halt.title'), onclick: () => control('halt') },
            sv('svg', { viewBox: '0 0 16 16', 'aria-hidden': 'true' }, sv('rect', { x: 3.5, y: 3, width: 3.2, height: 10, rx: 0.8 }), sv('rect', { x: 9.3, y: 3, width: 3.2, height: 10, rx: 0.8 })),
            t('page.halt')),
          h('button', { class: 'btn primary', 'data-fkey': 'resume', disabled: !canResume, title: t('page.resume.title'), onclick: () => control('resume') },
            sv('svg', { viewBox: '0 0 16 16', 'aria-hidden': 'true' }, sv('path', { d: 'M4.5 2.8v10.4L13 8z' })),
            t('page.resume')))),
      h('div', { class: 'facts' },
        h('span', null, tn('page.fact.started', { time: h('b', { text: fmtTime(c.started) }) })),
        h('span', null, tn('page.fact.duration', { time: h('b', null, liveDur(c.started, c.ended)) })),
        c.language ? h('span', { title: t('page.fact.language_title') }, tn('page.fact.language', {
          language: h('b', { text: c.language.english ? c.language.english + ' (' + c.language.native + ')' : c.language.code }),
          code: c.language.english ? ' ' + c.language.code : '' })) : null,
        pos ? h('span', null, pos) : null),
      c.error ? h('div', { class: 'problem' }, h('b', { text: t('page.error_state') + ' ' }), c.error, '\n' + t('page.error_fix')) : null,
      waitNote(c),
      c.ordersProblem ? h('div', { class: 'problem soft', text: c.ordersProblem }) : null,
      h('nav', { class: 'tabs' }, ['pipeline', 'timeline', 'chronicle', 'words'].map((x) =>
        h('a', { href: href(c.id, x), 'aria-current': r.tab === x ? 'page' : false, text: t('page.tab.' + x) }))),
    ];
    if (r.tab === 'timeline') out.push(timeline(c));
    else if (r.tab === 'words') out.push(words(c));
    else if (r.tab === 'chronicle') out.push(chronicle());
    else out.push(pipeline(c), legend());
    if (r.tab === 'pipeline' && r.node) out.push(...card(c, r.node));
    app.replaceChildren(...out.filter(Boolean));
  }

  // Explains a campaign that waits because of an agent step.
  function waitNote(c) {
    if (c.ask && !c.end && c.ask.campaign && c.ask.campaign !== c.id) {
      // A child asks: the parent links to the card that answers it.
      const where = tn('page.ask.child_node', { node: h('code', { text: c.ask.node }), path: h('code', { text: c.ask.path }), id: c.ask.campaign });
      return h('div', { class: 'problem ask' }, h('b', { text: t('page.ask.waiting') + ' ' }),
        tn('page.ask.reason.' + c.ask.reason, { node: where }), ' ',
        tn('page.ask.answer_card', { link: h('a', { href: href(c.ask.campaign, 'pipeline', c.ask.node), text: t('page.ask.answer_card_link') }),
          command: h('code', { text: c.ask.command }) }));
    }
    if (c.ask && !c.end) {
      const open = S.route.node === c.ask.node;
      const node = h('code', { text: c.ask.node });
      const command = h('code', { text: c.ask.command });
      return h('div', { class: 'problem ask' }, h('b', { text: t('page.ask.waiting') + ' ' }),
        tn('page.ask.reason.' + c.ask.reason, { node: node }), ' ',
        open ? tn('page.ask.answer_below', { command: command })
          : tn('page.ask.answer_card', { link: h('a', { href: href(c.id, 'pipeline', c.ask.node), text: t('page.ask.answer_card_link') }), command: command }),
        c.driven ? '' : ' ' + t('page.ask.undriven'));
    }
    if (!c.wait || c.end) return null;
    const node = h('code', { text: c.node });
    if (c.state === 'limit') {
      return h('div', { class: 'problem soft' }, h('b', { text: t('page.wait.limit') + ' ' }), tn('page.wait.limit_text', { node: node }));
    }
    if (c.state === 'offline') {
      return h('div', { class: 'problem soft' }, h('b', { text: t('page.wait.offline') + ' ' }), tn('page.wait.offline_text', { node: node }));
    }
    if (c.waitUntil) {
      return h('div', { class: 'problem info' }, h('b', { text: t('page.wait.reset') + ' ' }),
        tn('page.wait.reset_text', { node: node, at: fmtTime(c.waitUntil) }),
        c.state === 'interrupted' ? ' ' + t('page.wait.undriven') : '');
    }
    return null;
  }

  async function control(action) {
    const c = S.campaign;
    try {
      const res = await fetch('/api/campaigns/' + c.id + '/' + action, {
        method: 'POST', headers: { 'X-Hetman-Control': '1' },
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error || res.statusText);
      toast(tk('page.toast.' + action + '.', body.result));
    } catch (e) {
      toast(t('page.toast.' + action + '_failed', { error: e.message }), true);
    }
  }

  let toastTimer = 0;
  function toast(text, bad) {
    const el = document.getElementById('toast');
    el.textContent = text;
    el.className = 'toast show' + (bad ? ' bad' : '');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.className = 'toast'; }, 4000);
  }

  // ---- pipeline ----------------------------------------------------------

  function lastAttempt(c, node) {
    const list = c.nodes[node] || [];
    return list[list.length - 1];
  }

  function pipeline(c) {
    const track = h('div', { class: 'track' });
    track.append(h('div', { class: 'cap start', text: t('page.pipeline.start') }), link(true, ''));
    c.columns.forEach((col, i) => {
      if (i > 0) {
        const prev = c.columns[i - 1];
        const main = (prev.exits || []).find((x) => x.main);
        track.append(link(main && main.taken > 0, main ? main.outcome : ''));
      }
      track.append(column(c, col, false));
    });
    const last = c.columns[c.columns.length - 1];
    if (last) {
      const main = (last.exits || []).find((x) => x.main);
      track.append(link(!!c.end, main ? main.outcome : ''));
    }
    const end = c.end || 'done';
    track.append(h('div', { class: 'cap st-' + end + (c.end ? ' lit' : '') }, c.end ? mark(end === 'done' ? 'done' : 'failed') : null, end));
    return h('div', { class: 'lane', 'data-scroll': 'lane', role: 'region', 'aria-label': t('page.tab.pipeline'), tabindex: 0 }, track);
  }

  function link(on, label) {
    return h('div', { class: 'link' + (on ? ' on' : ''), 'aria-hidden': 'true' }, label ? h('span', { class: 'lbl', text: label }) : null);
  }

  function column(c, col, side) {
    const cls = ['col', 'st-' + col.status];
    if (col.current) cls.push('current');
    if (!col.known) cls.push('unknown');
    if (side) cls.push('side', col.attempts > 0 || col.current ? 'ran' : 'idle');
    const last = lastAttempt(c, col.node);
    const head = h('div', { class: 'col-head' },
      h('span', { class: 'name', title: col.node, text: col.node }),
      h('span', { class: 'tag', text: col.known ? col.type : t('page.not_in_orders') }));
    const tiles = h('div', { class: 'tiles' }, col.tiles.map((t, i) => tile(c, col, t, i, last)));
    const exits = (col.exits || []).filter((x) => !x.main).map((x) =>
      h('div', { class: 'exit' + (x.taken ? ' taken' : ''), title: x.taken ? t('page.exit.taken', { n: x.taken }) : t('page.exit.not_taken') },
        x.outcome, h('span', { class: 'arrow', text: '→' }), x.to, x.taken ? ' ×' + x.taken : ''));
    const el = h('section', { class: cls.join(' '), 'aria-label': col.node + ': ' + statusText(col.status) },
      col.current && !side ? h('span', { class: 'here', text: t('page.now') }) : null,
      col.current && side ? h('span', { class: 'tag', text: t('page.now') }) : null,
      head, tiles,
      exits.length ? h('div', { class: 'exits' }, exits) : null);
    if (col.side && col.side.length) {
      el.append(h('div', { class: 'sidebox' }, h('div', { class: 'hint', text: t('page.off_main_line') }), col.side.map((s) => column(c, s, true))));
    }
    return el;
  }

  function tile(c, col, t, i, last) {
    let label, tag = null;
    const sub = [];
    if (t.item || t.child) {
      // A child campaign of a segment or for-each step; it opens the child.
      label = t.item || tt('page.child.campaign', { id: t.child });
      if (t.item && t.child) tag = h('span', { class: 'tag', text: '#' + t.child });
      if (t.outcome) sub.push(t.outcome);
      else if (t.node) sub.push(t.node);
      const note = t.note ? tt(t.note, t.args) : '';
      if (note) sub.push(note);
      else if (!t.child) sub.push(statusText(t.status));
      return h('button', {
        class: 'tile st-' + t.status, type: 'button', 'data-fkey': 'tile-' + col.node + '-' + i,
        title: col.node + ' / ' + label + ': ' + statusText(t.status) + (note ? ' (' + note + ')' : ''),
        onclick: () => { location.hash = t.child ? href(t.child) : href(c.id, 'pipeline', col.node); },
      },
      mark(t.status),
      h('span', { class: 'label', text: label }),
      h('span', { class: 'right' }, t.attempts > 1 ? h('span', { class: 'count', title: tt('page.attempts', { n: t.attempts }), text: '×' + t.attempts }) : null),
      h('span', { class: 'sub' }, tag, sub.map((x) => h('span', { class: 'part' }, x))));
    }
    if (t.check) {
      label = t.check;
      const run = last && col.status !== 'waiting' && last.checks && last.checks.find((r) => r.check === t.check);
      if (t.soft) tag = h('span', { class: 'tag', text: tt('page.check.soft') });
      if (run && run.outcome && run.outcome !== 'ok') sub.push(run.outcome);
      else sub.push(statusText(t.status));
      if (run && run.finished) sub.push(fmtDur(run.ms));
      else if (run && run.started && t.status === 'running') sub.push(liveDur(run.started));
    } else {
      label = statusText(t.status);
      if (last && col.status !== 'waiting') {
        if (t.outcome && t.outcome !== 'ok' && t.outcome !== 'pass') sub.push(t.outcome);
        if (last.finished) sub.push(fmtDur(ms(last.finished) - ms(last.started)));
        else if (t.status === 'running') sub.push(liveDur(last.started));
      } else if (col.status === 'asking') {
        sub.push(tt('page.tile.needs_answer'));
      } else if (col.status === 'waiting') {
        if (col.current && c.lockWait) sub.push(tt('page.tile.lock', { lock: c.lockWait, holder: c.lockHolder }));
        else if (col.current && c.wait === 'limit' && c.waitUntil) sub.push(tt('page.tile.limit_again', { at: fmtTime(c.waitUntil) }));
        else if (col.current && c.wait) sub.push(tt(c.wait === 'limit' ? 'page.tile.limit' : 'page.tile.offline'));
        else sub.push(tt(c.state === 'halted' ? 'page.tile.halted_here' : 'page.tile.next'));
      }
    }
    const attempts = t.check ? t.attempts : col.attempts;
    return h('button', {
      class: 'tile st-' + t.status, type: 'button', 'data-fkey': 'tile-' + col.node + '-' + i,
      title: col.node + (t.check ? ' / ' + t.check : '') + ': ' + statusText(t.status),
      onclick: () => { location.hash = href(c.id, 'pipeline', col.node); },
    },
    mark(t.status),
    h('span', { class: 'label', text: label }),
    h('span', { class: 'right' }, attempts > 1 ? h('span', { class: 'count', title: tt('page.attempts', { n: attempts }), text: '×' + attempts }) : null),
    sub.length || tag ? h('span', { class: 'sub' }, tag, sub.map((x) => h('span', { class: 'part' }, x))) : null);
  }

  function legend() {
    return h('div', { class: 'legend', 'aria-label': t('page.legend') },
      ['passed', 'running', 'waiting', 'asking', 'failed', 'warning', 'notrun', 'interrupted'].map((s) =>
        h('span', null, mark(s, 'small'), statusText(s))));
  }

  // ---- step card ---------------------------------------------------------

  function findColumn(c, node) {
    for (const col of c.columns) {
      if (col.node === node) return col;
      for (const s of col.side || []) if (s.node === node) return s;
    }
    return null;
  }

  function closeCard() { location.hash = href(S.campaign.id, 'pipeline'); }

  function card(c, node) {
    const col = findColumn(c, node);
    const attempts = (c.nodes[node] || []).slice().reverse();
    const status = col ? col.status : attempts.length ? attempts[0].status : 'notrun';
    const body = h('div', { class: 'card-body', 'data-scroll': 'card' });
    if (col && col.exits && col.exits.length) {
      body.append(h('h3', { text: t('page.card.transitions') }), h('div', { class: 'routes' }, col.exits.map((x) =>
        h('span', { class: 'tag' }, x.outcome + ' → ' + x.to + (x.taken ? ' ' + t('page.card.taken', { n: x.taken }) : '')))));
    }
    body.append(h('h3', { text: attempts.length > 1 ? t('page.card.attempts', { n: attempts.length }) : t('page.card.attempt') }));
    if (!attempts.length) body.append(h('p', { class: 'empty', text: t('page.card.not_run') }));
    attempts.forEach((a, i) => body.append(attemptView(c, node, a, i === 0)));
    const panel = h('aside', { class: 'card', role: 'dialog', 'aria-modal': 'true', 'aria-label': t('page.card.label', { node: node }), tabindex: -1, onkeydown: (e) => { if (e.key === 'Escape') closeCard(); } },
      h('div', { class: 'card-head' },
        mark(status, 'large'),
        h('h2', { text: node }),
        col && col.known ? h('span', { class: 'tag', text: col.type }) : h('span', { class: 'tag', text: t('page.not_in_orders') }),
        col && col.executor ? h('span', { class: 'tag', title: t('page.card.executor'), text: col.executor }) : null,
        col && col.model ? h('span', { class: 'tag', title: t('page.card.model'), text: col.model }) : null,
        col && col.effort ? h('span', { class: 'tag', title: t('page.card.effort'), text: t('page.card.effort_value', { effort: col.effort }) }) : null,
        h('button', { class: 'close', type: 'button', 'aria-label': t('page.card.close'), 'data-fkey': 'card-close', onclick: closeCard, text: '×' })),
      body);
    setTimeout(() => { if (!panel.contains(document.activeElement)) panel.focus({ preventScroll: true }); }, 0);
    return [h('div', { class: 'scrim', onclick: closeCard }), panel];
  }

  function attemptView(c, node, a, newest) {
    const key = node + '#' + a.n;
    const facts = h('dl', { class: 'kv' },
      kv(t('page.kv.status'), [mark(a.status, 'small'), ' ', statusText(a.status)]),
      a.outcome ? kv(t('page.kv.outcome'), h('code', { text: a.outcome })) : null,
      kv(t('page.kv.started'), fmtTime(a.started, true)),
      a.finished ? kv(t('page.kv.finished'), fmtTime(a.finished, true)) : null,
      kv(t('page.kv.duration'), a.finished ? fmtDur(a.ms || ms(a.finished) - ms(a.started)) : a.status === 'running' ? liveDur(a.started) : '—'),
      a.exit !== undefined ? kv(t('page.kv.exit'), String(a.exit)) : null,
      a.executor ? kv(t('page.kv.executor'), h('code', { text: a.executor })) : null,
      a.model ? kv(t('page.kv.model'), h('code', { text: a.model })) : null,
      a.effort ? kv(t('page.kv.effort'), h('code', { text: a.effort })) : null,
      a.sessionIn ? kv(t('page.kv.continued'), tn('page.kv.continued_session', { session: h('code', { text: a.sessionIn }) })) : null,
      a.session ? kv(t('page.kv.session'), h('code', { text: a.session })) : null,
      a.failure ? kv(t('page.kv.why_failed'), [h('code', { text: a.failure }), ' — ', I.texts['page.failure.' + a.failure] !== undefined ? t('page.failure.' + a.failure) : '']) : null,
      a.retryAt ? kv(t('page.kv.retry_at'), fmtTime(a.retryAt, true)) : null,
      a.error ? kv(t(a.failure ? 'page.kv.details' : 'page.kv.error'), h('span', { class: 'err', text: a.error })) : null,
      a.transition ? kv(t('page.kv.went_to'), h('code', { text: a.transition })) : null);
    const body = h('div', { class: 'attempt-body' }, facts);
    for (const cl of a.cleanup || []) {
      body.append(h('div', { class: 'cleanup' },
        h('b', { text: t('page.cleanup.title') + ' ' }), '(' + fmtTime(cl.at, true) + '): ',
        cl.pid ? t(cl.check ? 'page.cleanup.process_check' : 'page.cleanup.process', { pid: cl.pid, check: cl.check }) +
          (cl.ident ? t('page.cleanup.started', { ident: cl.ident }) : '') + ': ' : '',
        h('code', { text: cl.outcome }), ' — ', I.texts['page.cleanup.' + cl.outcome] !== undefined ? t('page.cleanup.' + cl.outcome) : ''));
    }
    if (a.checks && a.checks.length) {
      body.append(h('h3', { text: t('page.card.checks') }));
      const table = h('table', { class: 'checks' });
      for (const r of a.checks) {
        const ckey = key + '/' + r.check;
        table.append(h('tr', null,
          h('td', null, mark(r.status)),
          h('td', null, h('b', { text: r.check }), ' ', r.soft ? h('span', { class: 'tag', text: t('page.check.soft') }) : h('span', { class: 'tag', text: t('page.check.required') }),
            r.error ? h('div', { class: 'err', text: r.error }) : null,
            (r.log || r.result || (r.artifacts && r.artifacts.length)) ?
              details(ckey, r.status === 'running' || (newest && (r.status === 'failed' || r.status === 'warning')), { class: 'sub-details' },
                h('summary', { text: t('page.card.log_files') }),
                h('div', null,
                  r.log ? logView(c.id, r.log, r.status === 'running') : null,
                  r.result ? fileBlock(c.id, t('page.card.result_file'), r.result) : null,
                  artifacts(c.id, r.artifacts))) : null),
          h('td', { class: 'n' }, statusText(r.status), r.outcome && r.outcome !== 'ok' ? ' · ' + r.outcome : ''),
          h('td', { class: 'n' }, r.finished ? fmtDur(r.ms) : r.status === 'running' && r.started ? liveDur(r.started) : ''),
          h('td', { class: 'n' }, r.exit !== undefined ? t('page.card.exit', { code: r.exit }) : '')));
      }
      body.append(table);
    }
    if (a.ask) body.append(...askBlock(c, a.ask));
    if (a.prompt) body.append(fileBlock(c.id, t('page.card.prompt'), a.prompt));
    if (a.log) {
      body.append(h('h3', { text: t(a.status === 'running' ? 'page.card.log_live' : 'page.card.log') }), logView(c.id, a.log, a.status === 'running'));
    }
    if (a.stderr) {
      body.append(details(key + '/stderr', false, { class: 'sub-details' }, h('summary', { text: t('page.card.stderr') }),
        logView(c.id, a.stderr, a.status === 'running')));
    }
    if (a.output) {
      body.append(h('h3', { text: t('page.card.output') }), h('pre', { class: 'log', text: JSON.stringify(a.output, null, 2) }));
    } else if (a.result) {
      body.append(fileBlock(c.id, t(a.executor ? 'page.card.rejected_output' : 'page.card.result_file'), a.result));
    }
    if (a.artifacts && a.artifacts.length) body.append(h('h3', { text: t('page.card.artifacts') }), artifacts(c.id, a.artifacts));
    return details(key, newest, { class: 'attempt' },
      h('summary', null, mark(a.status), h('b', { text: t('page.card.attempt_n', { n: a.n }) }), a.outcome ? h('code', { text: a.outcome }) : h('span', { text: statusText(a.status) }),
        h('span', { class: 'when', text: fmtTime(a.started) })),
      body);
  }

  function kv(k, v) { return [h('dt', { text: k }), h('dd', null, v)]; }

  function fileURL(id, path) { return '/api/campaigns/' + id + '/file?path=' + encodeURIComponent(path); }

  function artifacts(id, list) {
    if (!list || !list.length) return null;
    return h('ul', { class: 'files' }, list.map((name) => {
      const local = !/^([a-zA-Z]:)?[\\/]/.test(name) && !name.split(/[\\/]/).includes('..');
      return h('li', null, local ? h('a', { href: fileURL(id, 'work/' + name), target: '_blank', rel: 'noopener', text: name }) : h('code', { text: name }));
    }));
  }

  function fileBlock(id, title, path, url) {
    url = url || fileURL(id, path);
    const key = 'file:' + id + ':' + url;
    let v = S.logs.get(key);
    if (!v) {
      const pre = h('pre', { class: 'log', text: t('page.file.loading') });
      v = { el: h('div', null, h('h3', null, title + ' ', h('a', { href: url, target: '_blank', rel: 'noopener', text: path })), pre) };
      S.logs.set(key, v);
      fetch(url).then((r) => r.ok ? r.text() : r.text().then((t) => Promise.reject(new Error(t.trim() || r.statusText))))
        .then((x) => { pre.textContent = x; }, (e) => { pre.textContent = t('page.file.cannot_read', { error: e.message }); });
    }
    return v.el;
  }

  // ---- questions to a person ---------------------------------------------

  function showURL(id, seq, i) { return '/api/campaigns/' + id + '/show?ask=' + seq + '&i=' + i; }

  // The questions of one round: the files to look at, then the form while
  // the question is open, or the answers once given.
  function askBlock(c, q) {
    const open = c.ask && c.ask.seq === q.seq;
    const title = t('page.askblock.' + (q.reason === 'dialogue' || q.reason === 'human' ? q.reason : 'gate'),
      { round: q.round, seq: q.seq, at: fmtTime(q.at) });
    const out = [h('h3', { class: open ? 'ask-title' : '' }, mark(open ? 'asking' : 'passed', 'small'), ' ', title)];
    (q.show || []).forEach((p, i) => out.push(fileBlock(c.id, t('page.askblock.look_at'), p, showURL(c.id, q.seq, i))));
    out.push(open ? answerForm(c, q) : answered(q));
    return out;
  }

  function answered(q) {
    if (!q.answeredAt) return h('p', { class: 'empty', text: t('page.answers.none') });
    const byID = {};
    for (const a of q.answers || []) byID[a.id] = a;
    const rows = (q.questions || []).map((x) => {
      const a = byID[x.id] || {};
      return kv(x.question, [h('b', { class: a.value === 'no' ? 'err' : '', text: a.value || '—' }), a.comment ? [' — ', a.comment] : null]);
    });
    if (q.reason === 'human' && q.outcome) rows.push(kv(t('page.kv.outcome'), h('code', { text: q.outcome })));
    rows.push(kv(t('page.answers.answered'), [q.by ? [tn('page.answers.by', { by: h('b', { text: q.by }) }), ' '] : null,
      q.via ? '(' + t(q.via === 'page' ? 'page.answers.via_page' : 'page.answers.via_command') + ') ' : '', t('page.answers.at', { at: fmtTime(q.answeredAt, true) })]));
    return h('dl', { class: 'kv answers' }, rows);
  }

  function savedBy() {
    try { return localStorage.getItem('hetman.by') || ''; } catch (e) { return ''; }
  }

  function answerForm(c, q) {
    let st = S.forms.get(q.seq);
    if (!st) {
      st = { values: {}, comments: {}, outcome: '', by: savedBy(), error: '', busy: false };
      S.forms.set(q.seq, st);
    }
    const name = (id) => 'a-' + q.seq + '-' + id;
    const radio = (group, value, label, checked, set) => h('label', { class: 'opt' },
      h('input', { type: 'radio', name: group, value: value, checked: checked, 'data-fkey': group + '=' + value, onchange: () => set(value) }), ' ', label);
    const items = (q.questions || []).map((x, i) => {
      const set = (v) => { st.values[x.id] = v; };
      let input;
      if (x.kind === 'text') {
        input = h('textarea', { rows: 3, name: name(x.id), 'data-fkey': name(x.id), oninput: (e) => set(e.target.value) });
        input.value = st.values[x.id] || '';
      } else {
        const opts = x.kind === 'choice' ? x.options : ['yes', 'no'];
        input = h('div', { class: 'opts', role: 'radiogroup' }, opts.map((o) => radio(name(x.id), o, x.kind === 'choice' ? o : t('page.form.' + o), st.values[x.id] === o, set)));
      }
      let comment = null;
      if (x.kind !== 'text') {
        comment = h('input', { type: 'text', class: 'comment', placeholder: t('page.form.comment'), 'aria-label': t('page.form.comment_on', { id: x.id }),
          'data-fkey': name(x.id) + '/comment', oninput: (e) => { st.comments[x.id] = e.target.value; } });
        comment.value = st.comments[x.id] || '';
      }
      return h('fieldset', { class: 'q' }, h('legend', null, (i + 1) + '. ' + x.question, ' ', h('span', { class: 'tag', title: t('page.form.question_id'), text: x.id })),
        h('div', { class: 'hint', text: tk('page.form.kind.', x.kind) }), input, comment);
    });
    if ((q.outcomes || []).length > 1) {
      items.push(h('fieldset', { class: 'q' }, h('legend', { text: t('page.form.outcome') }),
        h('div', { class: 'opts', role: 'radiogroup' }, q.outcomes.map((o) =>
          radio('o-' + q.seq, o.name, [h('code', { text: o.name }), o.to ? ' → ' + o.to : ''], st.outcome === o.name, (v) => { st.outcome = v; })))));
    } else if ((q.outcomes || []).length === 1) {
      items.push(h('p', { class: 'hint' }, tn('page.form.goes_on', { outcome: h('code', { text: q.outcomes[0].name }) })));
    }
    const by = h('input', { type: 'text', class: 'by', placeholder: t('page.form.your_name'), 'aria-label': t('page.form.answered_by_label'), 'data-fkey': 'by-' + q.seq,
      oninput: (e) => { st.by = e.target.value; } });
    by.value = st.by;
    return h('form', { class: 'answer', novalidate: true, onsubmit: (e) => { e.preventDefault(); submitAnswer(c, q, st); } },
      items,
      h('div', { class: 'send' },
        h('label', null, t('page.form.answered_by') + ' ', by),
        h('button', { class: 'btn primary', type: 'submit', 'data-fkey': 'send-' + q.seq, disabled: st.busy, text: t(st.busy ? 'page.form.recording' : 'page.form.record') })),
      st.error ? h('div', { class: 'err form-err', role: 'alert', text: st.error }) : null,
      h('div', { class: 'cmd' }, tn('page.form.terminal', { command: h('code', { text: q.command }) }),
        h('div', null, tn('page.form.terminal_flags', { command: h('code', { text: q.example }) }))));
  }

  async function submitAnswer(c, q, st) {
    st.busy = true;
    st.error = '';
    render();
    try { localStorage.setItem('hetman.by', st.by); } catch (e) { /* not kept */ }
    try {
      const res = await fetch('/api/campaigns/' + c.id + '/answer?lang=' + encodeURIComponent(I.lang), {
        method: 'POST', headers: { 'X-Hetman-Control': '1', 'Content-Type': 'application/json' },
        body: JSON.stringify({ ask: q.seq, values: st.values, comments: st.comments, outcome: st.outcome, by: st.by }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.problem ? t('page.form.nothing_recorded', { problem: body.problem }) : body.error || res.statusText);
      S.forms.delete(q.seq);
      toast(I.texts['page.toast.answer.' + body.result] !== undefined ? t('page.toast.answer.' + body.result) : t('page.toast.answer.other'));
    } catch (e) {
      st.error = e.message;
    }
    st.busy = false;
    render();
  }

  // A log viewer. While its step runs it follows the file through the log
  // event stream; when the step ends the whole file is read once more so
  // that nothing written at the very end is missed.
  function logView(id, path, live) {
    const key = 'log:' + id + ':' + path;
    let v = S.logs.get(key);
    if (!v) {
      v = { pre: h('pre', { class: 'log', 'data-scroll': key, 'data-stick': 'bottom' }), es: null, live: false, loaded: false };
      S.logs.set(key, v);
    }
    if (live && !v.es) {
      v.pre.textContent = '';
      v.pre.classList.add('live');
      const es = new EventSource('/api/campaigns/' + id + '/log?path=' + encodeURIComponent(path));
      v.es = es;
      es.addEventListener('skipped', (ev) => {
        v.pre.append(h('span', { class: 'note', text: '… ' + t('page.log.skipped', { n: JSON.parse(ev.data).bytes }) + '\n' }));
      });
      es.addEventListener('reset', () => { v.pre.textContent = ''; });
      es.addEventListener('chunk', (ev) => {
        const atBottom = v.pre.scrollHeight - v.pre.scrollTop - v.pre.clientHeight < 8;
        v.pre.append(JSON.parse(ev.data).text);
        if (atBottom) v.pre.scrollTop = v.pre.scrollHeight;
      });
    } else if (!live && (v.es || !v.loaded)) {
      if (v.es) { v.es.close(); v.es = null; }
      v.pre.classList.remove('live');
      v.loaded = true;
      fetch(fileURL(id, path)).then((r) => r.ok ? r.text() : r.status === 404 ? '' : Promise.reject(new Error(r.statusText)))
        .then((x) => {
          v.pre.textContent = x;
          if (!x) v.pre.append(h('span', { class: 'note', text: t('page.log.empty') }));
        }, (e) => { v.pre.textContent = t('page.log.cannot_read', { error: e.message }); });
    }
    return v.pre;
  }

  function closeLogs() {
    for (const v of S.logs.values()) if (v.es) v.es.close();
    S.logs.clear();
  }

  // ---- timeline ----------------------------------------------------------

  function timeline(c) {
    const now = Date.now();
    const runs = [];
    for (const [node, list] of Object.entries(c.nodes)) {
      for (const a of list) runs.push({ node: node, a: a });
    }
    runs.sort((x, y) => ms(x.a.started) - ms(y.a.started));
    if (!runs.length) return h('div', { class: 'timeline' }, h('p', { class: 'empty', text: t('page.timeline.empty') }));
    const t0 = ms(c.started);
    const campaignEnd = c.ended ? ms(c.ended) : now;
    const rows = [];
    runs.forEach((r, i) => {
      const a = r.a;
      const next = runs[i + 1];
      const end = a.finished ? ms(a.finished) : a.status === 'running' ? now : next ? ms(next.a.started) : campaignEnd;
      rows.push({ label: r.node, n: a.n, node: r.node, status: a.status, from: ms(a.started), to: end, check: false });
      for (const ck of a.checks || []) {
        if (!ck.started && ck.status !== 'running') continue;
        const from = ck.started ? ms(ck.started) : previousEnd(a, ck, ms(a.started));
        const to = ck.finished ? ms(ck.finished) : end;
        rows.push({ label: ck.check, node: r.node, status: ck.status, from: from, to: to, check: true });
      }
    });
    const t1 = Math.max(campaignEnd, ...rows.map((r) => r.to), t0 + 1000);
    const span = t1 - t0;
    const pct = (t) => (100 * (t - t0) / span).toFixed(3) + '%';
    const step = tickStep(span);
    const ticks = [];
    for (let t = 0; t <= span; t += step) ticks.push(t);
    const axis = h('div', { class: 'tl-row tl-axis' }, h('div', { class: 'tl-label', text: '' }),
      h('div', { class: 'tl-track' }, ticks.map((t) => {
        const el = h('span', { class: 'tl-tick', text: fmtTick(t) });
        el.style.left = pct(t0 + t);
        return el;
      })));
    const body = rows.map((r) => {
      const bar = h('span', { class: 'tl-bar st-' + r.status, title: r.label + ': ' + statusText(r.status) + ', ' + fmtDur(r.to - r.from) });
      bar.style.left = pct(r.from);
      bar.style.width = 'calc(' + (100 * Math.max(0, r.to - r.from) / span).toFixed(3) + '% + 0px)';
      const grid = ticks.map((t) => { const g = h('span', { class: 'tl-grid' }); g.style.left = pct(t0 + t); return g; });
      const label = r.check ? h('div', { class: 'tl-label' }, mark(r.status, 'small'), h('span', { text: r.label }))
        : h('div', { class: 'tl-label' }, mark(r.status, 'small'),
          h('button', { type: 'button', onclick: () => { location.hash = href(c.id, 'pipeline', r.node); }, text: r.label + ' #' + r.n }));
      return h('div', { class: 'tl-row' + (r.check ? ' check' : '') }, label, h('div', { class: 'tl-track' }, grid, bar));
    });
    const out = h('div', { class: 'tl' }, axis, body);
    if (!c.ended) {
      const line = h('div', { class: 'tl-row' }, h('div', { class: 'tl-label', text: '' }), h('div', { class: 'tl-track' }));
      const nowMark = h('span', { class: 'tl-now', title: t('page.now') });
      nowMark.style.left = pct(now);
      line.lastChild.append(nowMark);
      out.append(line);
    }
    return h('div', { class: 'timeline', 'data-scroll': 'timeline' }, out);
  }

  function previousEnd(a, ck, fallback) {
    let end = fallback;
    for (const other of a.checks || []) {
      if (other === ck) break;
      if (other.finished) end = ms(other.finished);
    }
    return end;
  }

  function tickStep(span) {
    const steps = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21600, 43200, 86400].map((s) => s * 1000);
    for (const s of steps) if (span / s <= 8) return s;
    return steps[steps.length - 1];
  }

  function fmtTick(d) {
    if (d === 0) return '0';
    const s = Math.round(d / 1000);
    if (s < 60) return t('page.tick.sec', { s: s });
    if (s < 3600) return s % 60 ? t('page.tick.min_sec', { m: Math.floor(s / 60), s: s % 60 }) : t('page.tick.min', { m: Math.floor(s / 60) });
    const m = Math.floor(s / 60) % 60;
    return m ? t('page.tick.hour_min', { h: Math.floor(s / 3600), m: m }) : t('page.tick.hour', { h: Math.floor(s / 3600) });
  }

  // ---- in words ----------------------------------------------------------

  // The campaign told in sentences built by the server from the chronicle.
  function words(c) {
    return h('div', { class: 'words' }, ...(c.words || []).map((s) =>
      h('section', null, h('h3', { text: s.title }), ...s.lines.map((l) => h('p', { text: l })))));
  }

  // ---- chronicle ---------------------------------------------------------

  function chronicle() {
    const rows = S.events.map((e) => {
      const det = [];
      if (e.to) det.push('to=' + e.to);
      if (e.soft) det.push('soft');
      if (e.exit !== undefined) det.push('exit=' + e.exit);
      if (e.ms) det.push('ms=' + e.ms);
      if (e.pid) det.push('pid=' + e.pid);
      if (e.ident) det.push('started=' + e.ident);
      if (e.log) det.push('log=' + e.log);
      if (e.artifacts && e.artifacts.length) det.push('artifacts=' + e.artifacts.join(','));
      if (e.executor) det.push('executor=' + e.executor);
      if (e.model) det.push('model=' + e.model);
      if (e.effort) det.push('effort=' + e.effort);
      if (e.prompt) det.push('prompt=' + e.prompt);
      if (e.session) det.push('session=' + e.session);
      if (e.failure) det.push('failure=' + e.failure);
      if (e.retryAt) det.push('retry_at=' + fmtTime(e.retryAt, true));
      if (e.input) det.push('input=' + JSON.stringify(e.input));
      if (e.language) det.push('language=' + e.language);
      if (e.output) det.push('output=' + JSON.stringify(e.output));
      if (e.reason) det.push('reason=' + e.reason);
      if (e.round) det.push('round=' + e.round);
      if (e.questions) det.push('questions=' + e.questions.map((x) => x.id).join(','));
      if (e.show) det.push('show=' + e.show.join(','));
      if (e.outcomes) det.push('outcomes=' + e.outcomes.join(','));
      if (e.ask) det.push('ask=' + e.ask);
      if (e.answers) det.push('answers=' + e.answers.map((a) => a.id + '=' + a.value + (a.comment ? ' (' + a.comment + ')' : '')).join('; '));
      if (e.by) det.push('by=' + e.by);
      if (e.via) det.push('via=' + e.via);
      if (e.error) det.push('error=' + JSON.stringify(e.error));
      return h('tr', { class: e.seq >= S.freshFrom ? 'fresh' : '' },
        h('td', { class: 'n', text: e.seq }),
        h('td', { class: 'n', text: fmtTime(e.at, true) }),
        h('td', { class: 'kind', text: e.kind }),
        h('td', { text: e.node || '' }),
        h('td', { text: e.check || '' }),
        h('td', { class: 'n', text: e.attempt || '' }),
        h('td', { text: e.outcome || '' }),
        h('td', { class: 'det', text: det.join(' ') }));
    });
    S.freshFrom = Infinity;
    return h('div', { class: 'chron', 'data-scroll': 'chronicle', 'data-stick': 'bottom' },
      h('table', null,
        h('thead', null, h('tr', null, ['seq', 'time', 'event', 'node', 'check', 'attempt', 'outcome', 'details'].map((x) => h('th', { text: t('page.chronicle.col.' + x) })))),
        h('tbody', null, rows)));
  }

  // ---- clock -------------------------------------------------------------

  setInterval(() => {
    const now = Date.now();
    for (const el of document.querySelectorAll('[data-since]')) {
      el.textContent = fmtDur(now - ms(el.dataset.since));
    }
    for (const el of document.querySelectorAll('[data-until]')) {
      el.textContent = fmtDur(Math.max(0, ms(el.dataset.until) - now));
    }
    const c = S.campaign;
    if (S.route.view === 'campaign' && S.route.tab === 'timeline' && c && !c.ended) render();
  }, 1000);

  window.addEventListener('hashchange', onRoute);
  loadTexts(savedLang()).catch(() => { /* English keys are shown */ }).then(onRoute);
})();
