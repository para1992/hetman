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

  const STATUS_TEXT = {
    passed: 'passed', running: 'running', waiting: 'waiting', failed: 'failed',
    warning: 'soft check failed', notrun: 'not run', interrupted: 'interrupted',
    limit: 'subscription limit reached', offline: 'connectivity lost', asking: 'waiting for a person',
  };
  const STATE_MARK = {
    running: 'running', halted: 'halted', done: 'passed', failed: 'failed',
    error: 'error', interrupted: 'interrupted', waiting: 'waiting', limit: 'limit', offline: 'offline', asking: 'asking',
  };
  const STATE_TEXT = {
    waiting: 'waiting for limit', limit: 'limit reached', offline: 'offline', asking: 'waiting for a person',
  };
  const REASON_TEXT = { gate: 'The gate asks', human: 'A person decides here', dialogue: 'The agent step asks' };
  const FAILURE_TEXT = {
    invalid_output: 'the output did not match the schema',
    transient: 'the adapter reported a transient error',
    permanent: 'the adapter reported a permanent error; no retry',
    crash: 'the adapter ended without a proper result',
    protocol: 'the adapter broke the protocol',
    timeout: 'the attempt ran past its timeout',
    idle_timeout: 'the adapter sent nothing for longer than the idle timeout',
    limit: 'the subscription limit was reached; the attempt does not count',
    offline: 'connectivity was lost; the attempt does not count',
  };
  const CLEANUP_TEXT = {
    gone: 'nothing of the old process tree was alive',
    killed: 'the old process tree was still alive and was killed',
    reused: 'the process id now belongs to an unrelated process, which was left alone',
    unverified: 'the start time of the old process could not be read, so it was left alone',
    unrecorded: 'the attempt’s process was never recorded; there was nothing to check',
  };

  function ms(t) { return t ? Date.parse(t) : NaN; }

  function fmtDur(d) {
    if (!isFinite(d) || d < 0) return '';
    if (d < 1000) return (d / 1000).toFixed(2) + 's';
    if (d < 60000) return (d / 1000).toFixed(1) + 's';
    const s = Math.floor(d / 1000);
    if (s < 3600) return Math.floor(s / 60) + 'm ' + String(s % 60).padStart(2, '0') + 's';
    return Math.floor(s / 3600) + 'h ' + String(Math.floor(s / 60) % 60).padStart(2, '0') + 'm';
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
    const label = STATUS_TEXT[status] || status;
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
    return h('span', { class: 'badge st-' + state }, mark(STATE_MARK[state] || state), h('span', { class: 'txt', text: STATE_TEXT[state] || state }));
  }

  // ---- state and routing -------------------------------------------------

  const S = {
    route: { view: '' }, // nothing shown yet
    stream: null,
    streamKey: '',
    list: null,
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
    if (prev.view !== S.route.view || prev.id !== S.route.id) {
      S.campaign = null;
      S.events = [];
      S.open.clear();
      S.closed.clear();
      closeLogs();
      app.replaceChildren(h('p', { class: 'empty', text: 'Loading…' }));
      connect();
    } else {
      if (prev.node !== S.route.node) closeLogs();
      render();
    }
  }

  function setLive(state, text) {
    const el = document.getElementById('live');
    el.dataset.state = state;
    el.querySelector('.live-text').textContent = text;
  }

  function connect() {
    if (S.stream) S.stream.close();
    const url = S.route.view === 'campaign' ? '/api/stream?campaign=' + S.route.id : '/api/stream';
    const es = new EventSource(url);
    S.stream = es;
    setLive('connecting', 'connecting');
    es.onopen = () => setLive('live', 'live');
    es.onerror = () => {
      if (es.readyState === EventSource.CLOSED) {
        setLive('lost', 'not connected');
        if (S.route.view === 'campaign' && !S.campaign) {
          app.replaceChildren(h('p', { class: 'empty' }, 'Campaign ' + S.route.id + ' was not found. ', h('a', { href: '#/', text: 'All campaigns' })));
        }
      } else {
        setLive('connecting', 'reconnecting');
      }
    };
    es.addEventListener('list', (ev) => { S.list = JSON.parse(ev.data); render(); });
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

  function renderList() {
    document.title = 'Watchtower';
    if (!S.list) return;
    if (S.list.length === 0) {
      app.replaceChildren(h('h1', { text: 'Campaigns' }),
        h('p', { class: 'empty' }, 'No campaigns yet. Start one with ', h('code', { text: 'hetman run' }), '.'));
      return;
    }
    const rows = S.list.map((k) => {
      const go = () => { location.hash = href(k.id); };
      return h('tr', { onclick: go },
        h('td', { class: 'id' }, h('a', { href: href(k.id), 'data-fkey': 'row-' + k.id, onclick: (e) => e.stopPropagation(), text: '#' + k.id })),
        h('td', { class: 'flow', text: k.flow }),
        h('td', { class: 'state' }, stateBadge(k.state), k.haltRequested ? [' ', h('span', { class: 'tag', text: 'halt requested' })] : null),
        h('td', { class: 'num started', text: fmtTime(k.started) }),
        h('td', { class: 'num dur' }, liveDur(k.started, k.ended)),
        h('td', { class: 'marks' }, miniPipe(k)));
    });
    app.replaceChildren(h('h1', { text: 'Campaigns' }),
      h('table', { class: 'list' },
        h('thead', null, h('tr', null, ['Campaign', 'Flow', 'State', 'Started', 'Duration', 'Steps'].map((t) => h('th', { text: t })))),
        h('tbody', null, rows)));
  }

  function miniPipe(k) {
    return h('span', { class: 'minipipe', 'aria-label': 'steps' }, (k.marks || []).map((m) =>
      h('span', { class: 'mp' + (m.current ? ' current' : '') + (m.side ? ' side' : ''), title: m.node + ': ' + (STATUS_TEXT[m.status] || m.status) + (m.attempts > 1 ? ' ×' + m.attempts : '') },
        mark(m.status, 'small'))));
  }

  function position(c) {
    if (c.end) return null;
    const list = c.nodes[c.node] || [];
    const last = list[list.length - 1];
    if (c.state === 'running' && last && last.status === 'running') {
      return ['running ', h('b', { text: c.node }), ' (attempt ' + last.n + ')'];
    }
    if (c.state === 'halted') return ['halted before ', h('b', { text: c.node })];
    if (c.state === 'asking') return ['waits at ', h('b', { text: c.node }), ' for a person to answer'];
    if (c.state === 'waiting') {
      return ['waiting at ', h('b', { text: c.node }), ' for the subscription limit; runs again at ' + fmtTime(c.waitUntil) + ' (in ',
        h('span', { 'data-until': c.waitUntil, text: fmtDur(ms(c.waitUntil) - Date.now()) }), ')'];
    }
    if (c.state === 'limit') return ['stopped at ', h('b', { text: c.node }), ': subscription limit reached'];
    if (c.state === 'offline') return ['stopped at ', h('b', { text: c.node }), ': connectivity lost'];
    if (c.state === 'interrupted') return ['stopped at ', h('b', { text: c.node }), ', no process drives it'];
    if (c.lockWait) return ['waits at ', h('b', { text: c.node }), ' for the lock ', h('code', { text: c.lockWait }), ', held by ' + c.lockHolder];
    return ['at ', h('b', { text: c.node })];
  }

  function renderCampaign() {
    const c = S.campaign;
    const r = S.route;
    document.title = '#' + c.id + ' ' + c.flow + ' · ' + c.state + ' · Watchtower';
    // A child campaign is halted and resumed with the campaign a person started.
    const canHalt = !c.parent && (c.state === 'running' || c.state === 'interrupted' || c.state === 'asking') && !c.haltRequested;
    const canResume = !c.parent && (c.state === 'halted' || c.state === 'error' || c.state === 'interrupted' || c.state === 'limit' ||
      c.state === 'offline' || c.haltRequested || (c.state === 'asking' && !c.driven));
    const pos = position(c);
    const out = [
      h('nav', { class: 'crumbs' }, h('a', { href: '#/', text: '← All campaigns' }),
        c.parent ? [' · ', h('a', { href: href(c.parent), text: '↑ campaign #' + c.parent }),
          ' runs this one at ', h('code', { text: c.path }),
          c.top !== c.parent ? [' (part of ', h('a', { href: href(c.top), text: 'campaign #' + c.top }), ')'] : null] : null),
      h('div', { class: 'head' },
        h('h1', null, 'Campaign #' + c.id + ' ', h('span', { class: 'flow', text: c.flow })),
        stateBadge(c.state),
        c.haltRequested ? h('span', { class: 'tag', text: 'halt requested: halts after the current step' }) : null,
        h('div', { class: 'controls' },
          h('button', { class: 'btn', 'data-fkey': 'halt', disabled: !canHalt, title: 'Halt after the current step', onclick: () => control('halt') },
            sv('svg', { viewBox: '0 0 16 16', 'aria-hidden': 'true' }, sv('rect', { x: 3.5, y: 3, width: 3.2, height: 10, rx: 0.8 }), sv('rect', { x: 9.3, y: 3, width: 3.2, height: 10, rx: 0.8 })),
            'Halt'),
          h('button', { class: 'btn primary', 'data-fkey': 'resume', disabled: !canResume, title: 'Continue from the recorded position', onclick: () => control('resume') },
            sv('svg', { viewBox: '0 0 16 16', 'aria-hidden': 'true' }, sv('path', { d: 'M4.5 2.8v10.4L13 8z' })),
            'Resume'))),
      h('div', { class: 'facts' },
        h('span', null, 'started ', h('b', { text: fmtTime(c.started) })),
        h('span', null, 'duration ', h('b', null, liveDur(c.started, c.ended))),
        c.language ? h('span', { title: 'Text for a person is written in this language' }, 'language ',
          h('b', { text: c.language.english ? c.language.english + ' (' + c.language.native + ')' : c.language.code }),
          c.language.english ? ' ' + c.language.code : '') : null,
        pos ? h('span', null, pos) : null),
      c.error ? h('div', { class: 'problem' }, h('b', { text: 'Stopped in the error state: ' }), c.error, '\nFix the cause, then resume.') : null,
      waitNote(c),
      c.ordersProblem ? h('div', { class: 'problem soft', text: c.ordersProblem }) : null,
      h('nav', { class: 'tabs' }, ['pipeline', 'timeline', 'chronicle', 'words'].map((t) =>
        h('a', { href: href(c.id, t), 'aria-current': r.tab === t ? 'page' : false, text: TAB_TEXT[t] }))),
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
      return h('div', { class: 'problem ask' }, h('b', { text: 'Waiting for a person. ' }),
        (REASON_TEXT[c.ask.reason] || 'The step asks') + ' at ', h('code', { text: c.ask.node }), ' in ', h('code', { text: c.ask.path }),
        ' (campaign #' + c.ask.campaign + '). ', h('a', { href: href(c.ask.campaign, 'pipeline', c.ask.node), text: 'Answer on its step card' }),
        ', or in a terminal with ', h('code', { text: c.ask.command }), '.');
    }
    if (c.ask && !c.end) {
      const open = S.route.node === c.ask.node;
      return h('div', { class: 'problem ask' }, h('b', { text: 'Waiting for a person. ' }),
        (REASON_TEXT[c.ask.reason] || 'The step asks') + ' at ', h('code', { text: c.ask.node }), '. ',
        open ? 'Answer below, or in a terminal with ' : h('a', { href: href(c.id, 'pipeline', c.ask.node), text: 'Answer on its step card' }),
        open ? h('code', { text: c.ask.command }) : ', or in a terminal with ', open ? '.' : h('code', { text: c.ask.command }),
        c.driven ? '' : ' No process drives the campaign now: an answer given here continues it from this server; after an answer in a terminal, resume it.');
    }
    if (!c.wait || c.end) return null;
    if (c.state === 'limit') {
      return h('div', { class: 'problem soft' }, h('b', { text: 'Subscription limit reached. ' }),
        'The adapter gave no time to retry, so the campaign waits for a person. Resume runs ', h('code', { text: c.node }), ' again; the lost attempt does not count.');
    }
    if (c.state === 'offline') {
      return h('div', { class: 'problem soft' }, h('b', { text: 'Connectivity lost. ' }),
        'The attempt of ', h('code', { text: c.node }), ' was lost and does not count. Resume runs the step again once the connection is back.');
    }
    if (c.waitUntil) {
      return h('div', { class: 'problem info' }, h('b', { text: 'Waiting for the subscription limit to reset. ' }),
        h('code', { text: c.node }), ' runs again on its own at ' + fmtTime(c.waitUntil) + '.',
        c.state === 'interrupted' ? ' No process drives the campaign now; resume it and it waits until then.' : '');
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
      const text = {
        already: 'A halt is already in force.',
        pending: 'The campaign halts after its current step.',
        halted: 'Halted.',
        driving: 'Resumed. This server now drives the campaign.',
        elsewhere: 'Resumed. The process that drives it continues.',
      }[body.result] || body.result;
      toast(text);
    } catch (e) {
      toast(action + ' failed: ' + e.message, true);
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
    track.append(h('div', { class: 'cap start', text: 'start' }), link(true, ''));
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
    return h('div', { class: 'lane', 'data-scroll': 'lane', role: 'region', 'aria-label': 'pipeline', tabindex: 0 }, track);
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
      h('span', { class: 'tag', text: col.known ? col.type : 'not in orders' }));
    const tiles = h('div', { class: 'tiles' }, col.tiles.map((t, i) => tile(c, col, t, i, last)));
    const exits = (col.exits || []).filter((x) => !x.main).map((x) =>
      h('div', { class: 'exit' + (x.taken ? ' taken' : ''), title: x.taken ? 'taken ' + x.taken + ' time(s)' : 'not taken' },
        x.outcome, h('span', { class: 'arrow', text: '→' }), x.to, x.taken ? ' ×' + x.taken : ''));
    const el = h('section', { class: cls.join(' '), 'aria-label': col.node + ': ' + (STATUS_TEXT[col.status] || col.status) },
      col.current && !side ? h('span', { class: 'here', text: 'now' }) : null,
      col.current && side ? h('span', { class: 'tag', text: 'now' }) : null,
      head, tiles,
      exits.length ? h('div', { class: 'exits' }, exits) : null);
    if (col.side && col.side.length) {
      el.append(h('div', { class: 'sidebox' }, h('div', { class: 'hint', text: 'off the main line' }), col.side.map((s) => column(c, s, true))));
    }
    return el;
  }

  function tile(c, col, t, i, last) {
    let label, tag = null;
    const sub = [];
    if (t.item || t.child) {
      // A child campaign of a segment or for-each step; it opens the child.
      label = t.item || 'campaign #' + t.child;
      if (t.item && t.child) tag = h('span', { class: 'tag', text: '#' + t.child });
      if (t.outcome) sub.push(t.outcome);
      else if (t.node) sub.push(t.node);
      if (t.note) sub.push(t.note);
      else if (!t.child) sub.push(STATUS_TEXT[t.status] || t.status);
      return h('button', {
        class: 'tile st-' + t.status, type: 'button', 'data-fkey': 'tile-' + col.node + '-' + i,
        title: col.node + ' / ' + label + ': ' + (STATUS_TEXT[t.status] || t.status) + (t.note ? ' (' + t.note + ')' : ''),
        onclick: () => { location.hash = t.child ? href(t.child) : href(c.id, 'pipeline', col.node); },
      },
      mark(t.status),
      h('span', { class: 'label', text: label }),
      h('span', { class: 'right' }, t.attempts > 1 ? h('span', { class: 'count', title: t.attempts + ' attempts', text: '×' + t.attempts }) : null),
      h('span', { class: 'sub' }, tag, sub.map((x) => h('span', { class: 'part' }, x))));
    }
    if (t.check) {
      label = t.check;
      const run = last && col.status !== 'waiting' && last.checks && last.checks.find((r) => r.check === t.check);
      if (t.soft) tag = h('span', { class: 'tag', text: 'soft' });
      if (run && run.outcome && run.outcome !== 'ok') sub.push(run.outcome);
      else sub.push(STATUS_TEXT[t.status] || t.status);
      if (run && run.finished) sub.push(fmtDur(run.ms));
      else if (run && run.started && t.status === 'running') sub.push(liveDur(run.started));
    } else {
      label = STATUS_TEXT[t.status] || t.status;
      if (last && col.status !== 'waiting') {
        if (t.outcome && t.outcome !== 'ok' && t.outcome !== 'pass') sub.push(t.outcome);
        if (last.finished) sub.push(fmtDur(ms(last.finished) - ms(last.started)));
        else if (t.status === 'running') sub.push(liveDur(last.started));
      } else if (col.status === 'asking') {
        sub.push('needs an answer');
      } else if (col.status === 'waiting') {
        if (col.current && c.lockWait) sub.push('waits for lock ' + c.lockWait + ' (held by ' + c.lockHolder + ')');
        else if (col.current && c.wait === 'limit' && c.waitUntil) sub.push('limit, again at ' + fmtTime(c.waitUntil));
        else if (col.current && c.wait) sub.push(c.wait === 'limit' ? 'limit reached' : 'offline');
        else sub.push(c.state === 'halted' ? 'halted here' : 'next');
      }
    }
    const attempts = t.check ? t.attempts : col.attempts;
    return h('button', {
      class: 'tile st-' + t.status, type: 'button', 'data-fkey': 'tile-' + col.node + '-' + i,
      title: col.node + (t.check ? ' / ' + t.check : '') + ': ' + (STATUS_TEXT[t.status] || t.status),
      onclick: () => { location.hash = href(c.id, 'pipeline', col.node); },
    },
    mark(t.status),
    h('span', { class: 'label', text: label }),
    h('span', { class: 'right' }, attempts > 1 ? h('span', { class: 'count', title: attempts + ' attempts', text: '×' + attempts }) : null),
    sub.length || tag ? h('span', { class: 'sub' }, tag, sub.map((x) => h('span', { class: 'part' }, x))) : null);
  }

  function legend() {
    return h('div', { class: 'legend', 'aria-label': 'legend' },
      ['passed', 'running', 'waiting', 'asking', 'failed', 'warning', 'notrun', 'interrupted'].map((s) =>
        h('span', null, mark(s, 'small'), STATUS_TEXT[s])));
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
      body.append(h('h3', { text: 'Transitions' }), h('div', { class: 'routes' }, col.exits.map((x) =>
        h('span', { class: 'tag' }, x.outcome + ' → ' + x.to + (x.taken ? ' (taken ×' + x.taken + ')' : '')))));
    }
    body.append(h('h3', { text: attempts.length > 1 ? attempts.length + ' attempts, newest first' : 'Attempt' }));
    if (!attempts.length) body.append(h('p', { class: 'empty', text: 'This step has not run in this campaign.' }));
    attempts.forEach((a, i) => body.append(attemptView(c, node, a, i === 0)));
    const panel = h('aside', { class: 'card', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'step ' + node, tabindex: -1, onkeydown: (e) => { if (e.key === 'Escape') closeCard(); } },
      h('div', { class: 'card-head' },
        mark(status, 'large'),
        h('h2', { text: node }),
        col && col.known ? h('span', { class: 'tag', text: col.type }) : h('span', { class: 'tag', text: 'not in orders' }),
        col && col.executor ? h('span', { class: 'tag', title: 'executor', text: col.executor }) : null,
        col && col.model ? h('span', { class: 'tag', title: 'model', text: col.model }) : null,
        col && col.effort ? h('span', { class: 'tag', title: 'effort', text: 'effort ' + col.effort }) : null,
        h('button', { class: 'close', type: 'button', 'aria-label': 'Close', 'data-fkey': 'card-close', onclick: closeCard, text: '×' })),
      body);
    setTimeout(() => { if (!panel.contains(document.activeElement)) panel.focus({ preventScroll: true }); }, 0);
    return [h('div', { class: 'scrim', onclick: closeCard }), panel];
  }

  function attemptView(c, node, a, newest) {
    const key = node + '#' + a.n;
    const facts = h('dl', { class: 'kv' },
      kv('Status', [mark(a.status, 'small'), ' ', STATUS_TEXT[a.status] || a.status]),
      a.outcome ? kv('Outcome', h('code', { text: a.outcome })) : null,
      kv('Started', fmtTime(a.started, true)),
      a.finished ? kv('Finished', fmtTime(a.finished, true)) : null,
      kv('Duration', a.finished ? fmtDur(a.ms || ms(a.finished) - ms(a.started)) : a.status === 'running' ? liveDur(a.started) : '—'),
      a.exit !== undefined ? kv('Exit code', String(a.exit)) : null,
      a.executor ? kv('Executor', h('code', { text: a.executor })) : null,
      a.model ? kv('Model', h('code', { text: a.model })) : null,
      a.effort ? kv('Effort', h('code', { text: a.effort })) : null,
      a.sessionIn ? kv('Continued', ['session ', h('code', { text: a.sessionIn })]) : null,
      a.session ? kv('Session', h('code', { text: a.session })) : null,
      a.failure ? kv('Why it failed', [h('code', { text: a.failure }), ' — ', FAILURE_TEXT[a.failure] || '']) : null,
      a.retryAt ? kv('Retry at', fmtTime(a.retryAt, true)) : null,
      a.error ? kv(a.failure ? 'Details' : 'Error', h('span', { class: 'err', text: a.error })) : null,
      a.transition ? kv('Went to', h('code', { text: a.transition })) : null);
    const body = h('div', { class: 'attempt-body' }, facts);
    for (const cl of a.cleanup || []) {
      body.append(h('div', { class: 'cleanup' },
        h('b', { text: 'Process cleanup ' }), '(' + fmtTime(cl.at, true) + '): ',
        cl.pid ? 'process ' + cl.pid + (cl.check ? ' of check ' + cl.check : '') + (cl.ident ? ', started ' + cl.ident : '') + ': ' : '',
        h('code', { text: cl.outcome }), ' — ', CLEANUP_TEXT[cl.outcome] || ''));
    }
    if (a.checks && a.checks.length) {
      body.append(h('h3', { text: 'Checks' }));
      const table = h('table', { class: 'checks' });
      for (const r of a.checks) {
        const ckey = key + '/' + r.check;
        table.append(h('tr', null,
          h('td', null, mark(r.status)),
          h('td', null, h('b', { text: r.check }), ' ', r.soft ? h('span', { class: 'tag', text: 'soft' }) : h('span', { class: 'tag', text: 'required' }),
            r.error ? h('div', { class: 'err', text: r.error }) : null,
            (r.log || r.result || (r.artifacts && r.artifacts.length)) ?
              details(ckey, r.status === 'running' || (newest && (r.status === 'failed' || r.status === 'warning')), { class: 'sub-details' },
                h('summary', { text: 'log and files' }),
                h('div', null,
                  r.log ? logView(c.id, r.log, r.status === 'running') : null,
                  r.result ? fileBlock(c.id, 'Result file', r.result) : null,
                  artifacts(c.id, r.artifacts))) : null),
          h('td', { class: 'n' }, STATUS_TEXT[r.status] || r.status, r.outcome && r.outcome !== 'ok' ? ' · ' + r.outcome : ''),
          h('td', { class: 'n' }, r.finished ? fmtDur(r.ms) : r.status === 'running' && r.started ? liveDur(r.started) : ''),
          h('td', { class: 'n' }, r.exit !== undefined ? 'exit ' + r.exit : '')));
      }
      body.append(table);
    }
    if (a.ask) body.append(...askBlock(c, a.ask));
    if (a.prompt) body.append(fileBlock(c.id, 'Prompt', a.prompt));
    if (a.log) {
      body.append(h('h3', { text: a.status === 'running' ? 'Log (live)' : 'Log' }), logView(c.id, a.log, a.status === 'running'));
    }
    if (a.stderr) {
      body.append(details(key + '/stderr', false, { class: 'sub-details' }, h('summary', { text: 'adapter diagnostics (standard error)' }),
        logView(c.id, a.stderr, a.status === 'running')));
    }
    if (a.output) {
      body.append(h('h3', { text: 'Output (validated)' }), h('pre', { class: 'log', text: JSON.stringify(a.output, null, 2) }));
    } else if (a.result) {
      body.append(fileBlock(c.id, a.executor ? 'Rejected output' : 'Result file', a.result));
    }
    if (a.artifacts && a.artifacts.length) body.append(h('h3', { text: 'Artifacts' }), artifacts(c.id, a.artifacts));
    return details(key, newest, { class: 'attempt' },
      h('summary', null, mark(a.status), h('b', { text: 'Attempt ' + a.n }), a.outcome ? h('code', { text: a.outcome }) : h('span', { text: STATUS_TEXT[a.status] || a.status }),
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
      const pre = h('pre', { class: 'log', text: 'loading…' });
      v = { el: h('div', null, h('h3', null, title + ' ', h('a', { href: url, target: '_blank', rel: 'noopener', text: path })), pre) };
      S.logs.set(key, v);
      fetch(url).then((r) => r.ok ? r.text() : r.text().then((t) => Promise.reject(new Error(t.trim() || r.statusText))))
        .then((t) => { pre.textContent = t; }, (e) => { pre.textContent = 'cannot read: ' + e.message; });
    }
    return v.el;
  }

  // ---- questions to a person ---------------------------------------------

  function showURL(id, seq, i) { return '/api/campaigns/' + id + '/show?ask=' + seq + '&i=' + i; }

  const KIND_TEXT = { yesno: 'yes or no', choice: 'one of the options', text: 'text' };

  // The questions of one round: the files to look at, then the form while
  // the question is open, or the answers once given.
  function askBlock(c, q) {
    const open = c.ask && c.ask.seq === q.seq;
    const title = (q.reason === 'dialogue' ? 'Questions of the agent, round ' + q.round : q.reason === 'human' ? 'Decision of a person' : 'Questions for a person') +
      ' (#' + q.seq + ', asked ' + fmtTime(q.at) + ')';
    const out = [h('h3', { class: open ? 'ask-title' : '' }, mark(open ? 'asking' : 'passed', 'small'), ' ', title)];
    (q.show || []).forEach((p, i) => out.push(fileBlock(c.id, 'Look at', p, showURL(c.id, q.seq, i))));
    out.push(open ? answerForm(c, q) : answered(q));
    return out;
  }

  function answered(q) {
    if (!q.answeredAt) return h('p', { class: 'empty', text: 'Not answered.' });
    const byID = {};
    for (const a of q.answers || []) byID[a.id] = a;
    const rows = (q.questions || []).map((x) => {
      const a = byID[x.id] || {};
      return kv(x.question, [h('b', { class: a.value === 'no' ? 'err' : '', text: a.value || '—' }), a.comment ? [' — ', a.comment] : null]);
    });
    if (q.reason === 'human' && q.outcome) rows.push(kv('Outcome', h('code', { text: q.outcome })));
    rows.push(kv('Answered', [q.by ? ['by ', h('b', { text: q.by }), ' '] : null, q.via ? '(' + (q.via === 'page' ? 'on this page' : 'on the command line') + ') ' : '', 'at ' + fmtTime(q.answeredAt, true)]));
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
        input = h('div', { class: 'opts', role: 'radiogroup' }, opts.map((o) => radio(name(x.id), o, o, st.values[x.id] === o, set)));
      }
      let comment = null;
      if (x.kind !== 'text') {
        comment = h('input', { type: 'text', class: 'comment', placeholder: 'comment (optional)', 'aria-label': 'comment on ' + x.id,
          'data-fkey': name(x.id) + '/comment', oninput: (e) => { st.comments[x.id] = e.target.value; } });
        comment.value = st.comments[x.id] || '';
      }
      return h('fieldset', { class: 'q' }, h('legend', null, (i + 1) + '. ' + x.question, ' ', h('span', { class: 'tag', title: 'question id', text: x.id })),
        h('div', { class: 'hint', text: KIND_TEXT[x.kind] || x.kind }), input, comment);
    });
    if ((q.outcomes || []).length > 1) {
      items.push(h('fieldset', { class: 'q' }, h('legend', { text: 'Outcome: where the flow goes next' }),
        h('div', { class: 'opts', role: 'radiogroup' }, q.outcomes.map((o) =>
          radio('o-' + q.seq, o.name, [h('code', { text: o.name }), o.to ? ' → ' + o.to : ''], st.outcome === o.name, (v) => { st.outcome = v; })))));
    } else if ((q.outcomes || []).length === 1) {
      items.push(h('p', { class: 'hint' }, 'The flow goes on with ', h('code', { text: q.outcomes[0].name }), '.'));
    }
    const by = h('input', { type: 'text', class: 'by', placeholder: 'your name (optional)', 'aria-label': 'answered by', 'data-fkey': 'by-' + q.seq,
      oninput: (e) => { st.by = e.target.value; } });
    by.value = st.by;
    return h('form', { class: 'answer', novalidate: true, onsubmit: (e) => { e.preventDefault(); submitAnswer(c, q, st); } },
      items,
      h('div', { class: 'send' },
        h('label', null, 'Answered by ', by),
        h('button', { class: 'btn primary', type: 'submit', 'data-fkey': 'send-' + q.seq, disabled: st.busy, text: st.busy ? 'Recording…' : 'Record answers' })),
      st.error ? h('div', { class: 'err form-err', role: 'alert', text: st.error }) : null,
      h('div', { class: 'cmd' }, 'The same from a terminal: ', h('code', { text: q.command }),
        h('div', null, 'or, without questions: ', h('code', { text: q.example }))));
  }

  async function submitAnswer(c, q, st) {
    st.busy = true;
    st.error = '';
    render();
    try { localStorage.setItem('hetman.by', st.by); } catch (e) { /* not kept */ }
    try {
      const res = await fetch('/api/campaigns/' + c.id + '/answer', {
        method: 'POST', headers: { 'X-Hetman-Control': '1', 'Content-Type': 'application/json' },
        body: JSON.stringify({ ask: q.seq, values: st.values, comments: st.comments, outcome: st.outcome, by: st.by }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error || res.statusText);
      S.forms.delete(q.seq);
      toast({
        driving: 'Answers recorded. This server now drives the campaign.',
        elsewhere: 'Answers recorded. The process that drives the campaign continues.',
        recorded: 'Answers recorded. The campaign is halted or stopped; resume it to continue.',
      }[body.result] || 'Answers recorded.');
    } catch (e) {
      st.error = e.message.replace(/^invalid answers: /, 'Nothing was recorded: ');
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
        v.pre.append(h('span', { class: 'note', text: '… ' + JSON.parse(ev.data).bytes + ' earlier bytes not shown\n' }));
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
        .then((t) => {
          v.pre.textContent = t;
          if (!t) v.pre.append(h('span', { class: 'note', text: '(empty)' }));
        }, (e) => { v.pre.textContent = 'cannot read the log: ' + e.message; });
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
    if (!runs.length) return h('div', { class: 'timeline' }, h('p', { class: 'empty', text: 'Nothing has run yet.' }));
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
      const bar = h('span', { class: 'tl-bar st-' + r.status, title: r.label + ': ' + (STATUS_TEXT[r.status] || r.status) + ', ' + fmtDur(r.to - r.from) });
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
      const nowMark = h('span', { class: 'tl-now', title: 'now' });
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

  function fmtTick(t) {
    if (t === 0) return '0';
    const s = Math.round(t / 1000);
    if (s < 60) return s + 's';
    if (s < 3600) return Math.floor(s / 60) + 'm' + (s % 60 ? ' ' + (s % 60) + 's' : '');
    return Math.floor(s / 3600) + 'h' + (Math.floor(s / 60) % 60 ? ' ' + (Math.floor(s / 60) % 60) + 'm' : '');
  }

  // ---- in words ----------------------------------------------------------

  const TAB_TEXT = { pipeline: 'Pipeline', timeline: 'Timeline', chronicle: 'Chronicle', words: 'In words' };

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
        h('thead', null, h('tr', null, ['#', 'Time', 'Event', 'Node', 'Check', 'Attempt', 'Outcome', 'Details'].map((t) => h('th', { text: t })))),
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
  onRoute();
})();
