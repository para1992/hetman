// Watchtower controls that change state: starting a campaign, retry and
// rewind, and the flow editor. Every request carries the control header,
// which the server requires on everything that changes state. Nothing is
// written to the orders file before the person has seen the change and
// confirmed it, and nothing is sent to a model unless they press the button.
'use strict';

(function () {
  const W = window.Watchtower;
  const { h, t, tn, tk } = W;
  const S = W.S;

  const C = {
    flows: null,       // the orders files of the folder
    loading: false,
    start: null,       // the start form while open
    flow: null,        // the flow shown in the editor
    flowError: '',
    forms: new Map(),  // editor form values by node and operation
    change: null,      // the change shown for confirmation
    busy: false,
    error: '',
    sentence: { text: '', busy: false, reply: null, error: '', answer: null },
  };

  const HEADERS = { 'X-Hetman-Control': '1', 'Content-Type': 'application/json' };

  async function post(path, body) {
    const res = await fetch(path + (path.includes('?') ? '&' : '?') + 'lang=' + encodeURIComponent(W.lang()), {
      method: 'POST', headers: HEADERS, body: JSON.stringify(body || {}),
    });
    const data = await res.json().catch(() => ({}));
    return { ok: res.ok, status: res.status, data: data };
  }

  function stored(key) {
    try { return localStorage.getItem(key) || ''; } catch (e) { return ''; }
  }

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (e) { /* not kept */ }
  }

  function flowPath(file) { return '/api/flows/' + encodeURIComponent(file); }
  function flowHref(file, node) { return '#/flow/' + encodeURIComponent(file) + (node ? '/' + encodeURIComponent(node) : ''); }

  async function loadFlows() {
    if (C.loading) return;
    C.loading = true;
    try {
      const res = await fetch('/api/flows');
      C.flows = await res.json();
    } catch (e) {
      C.flows = [];
    }
    C.loading = false;
    W.render();
  }

  function langLabel(f) {
    if (f.languageProblem) return h('span', { class: 'err', text: f.languageProblem });
    if (!f.language) return t('page.start.no_language');
    return f.language.english ? f.language.english + ' (' + f.language.native + ') ' + f.language.code : f.language.code;
  }

  // ---- the campaign list: start a campaign, open the editor --------------

  function listTop() {
    if (!C.flows) {
      loadFlows();
      return null;
    }
    const usable = C.flows.filter((f) => !f.problem);
    const bar = h('div', { class: 'ctl-bar' },
      h('button', { class: 'btn primary', type: 'button', 'data-fkey': 'start-open', disabled: !usable.length,
        onclick: () => { C.start = C.start ? null : newStart(usable[0]); W.render(); } }, t('page.start.button')),
      C.flows.map((f) => h('a', { class: 'btn', href: flowHref(f.file), title: t('page.flow.edit_title', { file: f.file }) },
        t('page.flow.edit', { file: f.file }))));
    return h('div', null, bar, C.start ? startForm(usable) : null);
  }

  function newStart(f) {
    const file = f ? f.file : '';
    return { file: file, input: stored('hetman.input.' + file), error: '', problems: [], busy: false };
  }

  function startForm(usable) {
    const st = C.start;
    const f = usable.find((x) => x.file === st.file) || usable[0];
    if (!f) return null;
    const pick = h('select', { 'data-fkey': 'start-file', onchange: (e) => { C.start = newStart(usable.find((x) => x.file === e.target.value)); W.render(); } },
      usable.map((x) => h('option', { value: x.file, selected: x.file === f.file }, x.file + ' · ' + x.flow)));
    const input = h('textarea', { rows: 6, class: 'mono', 'data-fkey': 'start-input', placeholder: t('page.start.input_placeholder'),
      'aria-label': t('page.start.input'), oninput: (e) => { st.input = e.target.value; } });
    input.value = st.input;
    return h('form', { class: 'answer ctl-panel', novalidate: true, onsubmit: (e) => { e.preventDefault(); startCampaign(f); } },
      h('fieldset', { class: 'q' }, h('legend', { text: t('page.start.title') }),
        h('label', { class: 'row' }, t('page.start.flow') + ' ', pick),
        h('div', { class: 'hint' }, t('page.start.language') + ' ', h('b', null, langLabel(f))),
        h('label', { class: 'hint', text: t('page.start.input') }), input,
        h('div', { class: 'hint', text: t('page.start.input_hint') })),
      st.error ? h('div', { class: 'err form-err', role: 'alert', text: st.error }) : null,
      st.problems.length ? h('div', { class: 'err form-err', role: 'alert' }, h('b', { text: t('page.start.problems') }), '\n' + st.problems.join('\n')) : null,
      h('div', { class: 'send' },
        h('button', { class: 'btn', type: 'button', onclick: () => { C.start = null; W.render(); } }, t('page.cancel')),
        h('button', { class: 'btn primary', type: 'submit', 'data-fkey': 'start-go', disabled: st.busy || !!f.languageProblem },
          t(st.busy ? 'page.start.starting' : 'page.start.go'))));
  }

  // parseInput checks the run input before it is sent: empty, or one JSON
  // object.
  function parseInput(text) {
    if (!text.trim()) return { value: null };
    let v;
    try { v = JSON.parse(text); } catch (e) { return { error: t('page.start.not_json', { error: e.message }) }; }
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return { error: t('page.start.not_object') };
    return { value: v };
  }

  async function startCampaign(f) {
    const st = C.start;
    st.error = '';
    st.problems = [];
    const parsed = parseInput(st.input);
    if (parsed.error) { st.error = parsed.error; W.render(); return; }
    store('hetman.input.' + f.file, st.input);
    st.busy = true;
    W.render();
    try {
      const r = await post(flowPath(f.file) + '/start', parsed.value ? { input: parsed.value } : {});
      if (r.ok) {
        C.start = null;
        W.toast(t('page.start.started', { id: r.data.id }));
        location.hash = W.href(r.data.id);
        return;
      }
      if (r.data.problems) st.problems = r.data.problems;
      else st.error = r.data.error || String(r.status);
    } catch (e) {
      st.error = e.message;
    }
    st.busy = false;
    W.render();
  }

  // ---- retry and rewind ---------------------------------------------------

  function savedBy() { return stored('hetman.by'); }

  async function again(c, action, node) {
    try {
      const r = await post('/api/campaigns/' + c.id + '/' + action, { node: node || '', by: savedBy() });
      if (!r.ok) throw new Error(r.data.error || String(r.status));
      W.toast(t('page.toast.' + action + '_done', { node: r.data.node }));
    } catch (e) {
      W.toast(t('page.toast.' + action + '_failed', { error: e.message }), true);
    }
  }

  function campaignButtons(c) {
    const can = !!c.retry && !c.driven;
    return h('button', { class: 'btn', 'data-fkey': 'retry', disabled: !can,
      title: c.retry ? t('page.retry.title', { node: c.retry }) : t('page.retry.none'), onclick: () => again(c, 'retry') },
    t('page.retry'));
  }

  function cardTop(c, node) {
    const out = [];
    const returns = (c.returns || []).filter((x) => x.node === node);
    for (const x of returns) {
      out.push(h('div', { class: 'cleanup' }, mark('waiting'), ' ', tn('page.return.' + x.kind, {
        who: h('b', { text: x.by || t('page.return.who') }), at: W.fmtTime(x.at, true),
        via: x.via ? ' ' + t(x.via === 'page' ? 'page.answers.via_page' : 'page.answers.via_command') : '' })));
    }
    if ((c.rewind || []).includes(node)) {
      out.push(h('div', { class: 'ctl-rewind' },
        h('button', { class: 'btn', type: 'button', 'data-fkey': 'rewind-' + node, disabled: c.driven,
          title: t('page.rewind.title', { node: node }),
          onclick: () => { if (window.confirm(t('page.rewind.confirm', { node: node }))) again(c, 'rewind', node); } },
        t('page.rewind', { node: node })),
        c.driven ? h('span', { class: 'hint', text: ' ' + t('page.rewind.driven') }) : null));
    }
    return out;
  }

  function mark(s) { return W.mark(s, 'small'); }

  function columnNote(c, col) {
    if (c.flowView) return null;
    const notes = [];
    if (col.current && col.maxAttempts && col.used) {
      notes.push(h('span', { class: 'ctl-note', text: t('page.col.budget', { used: col.used, max: col.maxAttempts }) }));
    }
    if (col.again) notes.push(h('span', { class: 'ctl-note', title: t('page.col.again_title'), text: t('page.col.again') }));
    return notes.length ? notes : null;
  }

  // ---- the flow editor ----------------------------------------------------

  async function openFlow(file) {
    C.flow = null;
    C.flowError = '';
    C.change = null;
    C.error = '';
    C.sentence = { text: '', busy: false, reply: null, error: '', answer: null };
    await reloadFlow(file);
  }

  async function reloadFlow(file) {
    try {
      const res = await fetch(flowPath(file) + '?lang=' + encodeURIComponent(W.lang()));
      const d = await res.json();
      if (!res.ok) throw new Error(d.error || String(res.status));
      C.flow = d;
    } catch (e) {
      C.flowError = e.message;
    }
    W.render();
  }

  function renderFlow() {
    const f = C.flow;
    const r = S.route;
    document.title = r.file + ' · ' + t('page.brand');
    const out = [h('nav', { class: 'crumbs' }, h('a', { href: '#/', text: '← ' + t('page.all_campaigns') }))];
    if (!f) {
      out.push(h('p', { class: 'empty', text: C.flowError ? t('page.flow.cannot_read', { error: C.flowError }) : t('page.loading') }));
      W.app.replaceChildren(...out);
      return;
    }
    out.push(h('div', { class: 'head' },
      h('h1', null, t('page.flow.title') + ' ', h('span', { class: 'flow', text: f.file })),
      f.flow ? h('span', { class: 'tag', text: f.flow }) : null),
    h('div', { class: 'facts' },
      h('span', null, tn('page.start.language_fact', { language: h('b', null, langLabel(f)) })),
      h('span', { text: t('page.flow.hint') })));
    if (f.problem) out.push(h('div', { class: 'problem', text: f.problem }));
    if (f.active && f.active.length) out.push(h('div', { class: 'problem info', text: t('page.flow.active', { n: f.active.length, ids: f.active.map((x) => '#' + x).join(', ') }) }));
    if (f.scribe) out.push(sentenceLine(f));
    if (C.change && !r.node) out.push(changeBox(f));
    if (C.error && !r.node) out.push(h('div', { class: 'err form-err', role: 'alert', text: C.error }));
    if (f.columns && f.columns.length) {
      const pseudo = { columns: f.columns, nodes: {}, end: '', flowView: true, nodeHref: (n) => flowHref(f.file, n) };
      out.push(W.pipeline(pseudo));
      if (r.node) out.push(...editorCard(f, r.node));
    }
    W.app.replaceChildren(...out.filter(Boolean));
  }

  // ---- a sentence to Scribe -----------------------------------------------

  function sentenceLine(f) {
    const st = C.sentence;
    const input = h('input', { type: 'text', 'data-fkey': 'sentence', placeholder: t('page.scribe.placeholder'), 'aria-label': t('page.scribe.label'),
      oninput: (e) => { st.text = e.target.value; } });
    input.value = st.text;
    const out = [h('form', { class: 'answer ctl-sentence', novalidate: true, onsubmit: (e) => { e.preventDefault(); sendSentence(f); } },
      h('div', { class: 'send' }, h('label', { class: 'grow' }, t('page.scribe.label') + ' ', input),
        h('button', { class: 'btn primary', type: 'submit', 'data-fkey': 'sentence-go', disabled: st.busy || !st.text.trim() && !st.busy },
          t(st.busy ? 'page.scribe.asking' : 'page.scribe.ask'))),
      h('div', { class: 'hint', text: t('page.scribe.hint') }))];
    if (st.error) out.push(h('div', { class: 'err form-err', role: 'alert', text: st.error }));
    const rep = st.reply;
    if (rep && rep.kind === 'unclear') {
      out.push(h('div', { class: 'ctl-panel' }, h('b', { text: t('page.scribe.unclear') }),
        h('ol', null, rep.choices.map((ch, i) => h('li', null,
          h('button', { class: 'btn', type: 'button', 'data-fkey': 'choice-' + i, onclick: () => choose(f, ch) }, ch.question),
          h('div', { class: 'hint', text: ch.words }))))));
    }
    if (st.answer) {
      out.push(h('div', { class: 'ctl-panel' }, h('b', { text: st.answer.words }), h('pre', { class: 'log', text: st.answer.text })));
    }
    return h('div', null, out);
  }

  async function sendSentence(f) {
    const st = C.sentence;
    if (!st.text.trim()) return;
    st.busy = true;
    st.error = '';
    st.reply = null;
    st.answer = null;
    C.change = null;
    W.render();
    try {
      const r = await post(flowPath(f.file) + '/sentence', { sentence: st.text });
      if (!r.ok) {
        st.error = r.data.invalid ? t('page.scribe.invalid', { error: r.data.error }) : t('page.scribe.failed', { error: r.data.error || String(r.status) });
      } else if (r.data.kind === 'operation') {
        C.change = r.data.change;
      } else if (r.data.kind === 'query') {
        st.answer = { words: r.data.words, text: r.data.text };
      } else {
        st.reply = r.data;
      }
    } catch (e) {
      st.error = e.message;
    }
    st.busy = false;
    W.render();
  }

  function choose(f, ch) {
    C.sentence.reply = null;
    if (ch.op) preview(f, ch.op, '');
    else { C.sentence.answer = { words: ch.words, text: ch.text }; W.render(); }
  }

  // ---- previews and confirmation ------------------------------------------

  async function preview(f, op, runLine) {
    C.busy = true;
    C.error = '';
    C.change = null;
    W.render();
    try {
      const r = await post(flowPath(f.file) + '/preview', { op: op, runLine: runLine || '' });
      if (!r.ok) throw new Error(r.data.error || String(r.status));
      C.change = r.data;
    } catch (e) {
      C.error = e.message;
    }
    C.busy = false;
    W.render();
  }

  async function confirm(f) {
    const ch = C.change;
    C.busy = true;
    C.error = '';
    W.render();
    try {
      const r = await post(flowPath(f.file) + '/apply', { op: ch.op, base: ch.base });
      if (r.status === 409 && r.data.stale) throw new Error(t('page.edit.stale'));
      if (!r.ok) {
        if (r.data.change) C.change = r.data.change;
        throw new Error(r.data.error || String(r.status));
      }
      C.change = null;
      W.toast(t(r.data.result === 'written' ? 'page.edit.written' : 'page.edit.unchanged', { file: f.file }));
      C.forms.clear();
      await reloadFlow(f.file);
    } catch (e) {
      C.error = t('page.edit.not_written', { error: e.message });
    }
    C.busy = false;
    W.render();
  }

  function changeBox(f) {
    const ch = C.change;
    const writable = !ch.error && ch.changed && !(ch.problems && ch.problems.length) && !ch.refused;
    return h('div', { class: 'ctl-change', role: 'region', 'aria-label': t('page.edit.change') },
      h('h3', { text: t('page.edit.change') }),
      h('p', null, h('b', { text: ch.words + '.' })),
      ch.error ? h('div', { class: 'err form-err', text: t('page.edit.cannot', { error: ch.error }) }) : null,
      !ch.error && !ch.changed ? h('p', { class: 'hint', text: t('page.edit.already') }) : null,
      ch.diff ? h('pre', { class: 'log diff' }, ch.diff.split('\n').map((l) =>
        h('span', { class: l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : '' }, l + '\n'))) : null,
      (ch.notes || []).map((n) => h('p', { class: 'hint', text: n })),
      (ch.warnings || []).length ? h('div', { class: 'problem soft' }, h('b', { text: t('page.edit.warnings') + ' ' }), ch.warnings.join('\n')) : null,
      (ch.problems || []).length ? h('div', { class: 'problem' }, h('b', { text: t('page.edit.problems') + '\n' }), ch.problems.join('\n')) : null,
      ch.refused ? h('div', { class: 'problem' }, h('b', { text: t('page.edit.refused') + ' ' }), ch.refused) : null,
      (ch.active || []).length ? h('div', { class: 'problem info', text: t('page.flow.active', { n: ch.active.length, ids: ch.active.map((x) => '#' + x).join(', ') }) }) : null,
      C.error ? h('div', { class: 'err form-err', role: 'alert', text: C.error }) : null,
      h('div', { class: 'send' },
        h('button', { class: 'btn', type: 'button', 'data-fkey': 'change-cancel', onclick: () => { C.change = null; C.error = ''; W.render(); } }, t('page.cancel')),
        h('button', { class: 'btn primary', type: 'button', 'data-fkey': 'change-confirm', disabled: !writable || C.busy, onclick: () => confirm(f) },
          t(C.busy ? 'page.edit.writing' : 'page.edit.confirm'))));
  }

  // ---- the operations of a node -------------------------------------------

  // field kinds: text, select (options), list (comma separated), number,
  // check (a checkbox) and outcome (text with the node's outcomes offered).
  function operations(f, n) {
    const names = f.nodes.map((x) => x.id);
    const others = names.filter((x) => x !== n.id);
    const checks = f.checks.map((x) => x.name);
    const types = ['script', 'gate', 'agent', 'human'];
    const ops = [];
    const nodeFields = (v) => {
      const out = [{ name: 'type', kind: 'select', options: types, value: 'script' }, { name: 'node', kind: 'text' }];
      const type = v.type || 'script';
      if (type === 'script' || type === 'gate') out.push({ name: 'run', kind: 'text', mono: true });
      if (type === 'gate') out.push({ name: 'checks', kind: 'multi', options: checks });
      if (type === 'agent') {
        out.push({ name: 'executor', kind: 'select', options: f.executors }, { name: 'model', kind: 'text' },
          { name: 'outcomes', kind: 'list' }, { name: 'starters', kind: 'check', value: true });
      }
      if (type === 'human') out.push({ name: 'outcomes', kind: 'list' });
      return out;
    };
    const nodeOp = (v, extra) => {
      const op = Object.assign({ op: 'add', type: v.type || 'script', node: v.node || '' }, extra);
      if ((op.type === 'gate') && (v.checks || []).length) op.checks = v.checks;
      if (op.type === 'agent') {
        if (v.executor) op.executor = v.executor;
        if (v.model) op.model = v.model;
        if (v.starters !== false) op.starters = true;
      }
      if ((op.type === 'agent' || op.type === 'human') && v.outcomes) op.outcomes = split(v.outcomes);
      return op;
    };
    const runOf = (v) => (v.type === 'script' || v.type === 'gate' || !v.type) ? v.run || '' : '';
    ops.push({ key: 'add_after', fields: (v) => [...nodeFields(v), { name: 'via', kind: 'select', options: [''].concat(n.outcomes) }],
      op: (v) => { const x = nodeOp(v, { after: n.id }); if (v.via) x.via = v.via; return x; }, run: runOf });
    ops.push({ key: 'add_before', fields: nodeFields, op: (v) => nodeOp(v, { before: n.id }), run: runOf });
    if (checks.length) {
      ops.push({ key: 'add_gate', fields: () => [{ name: 'node', kind: 'text' }, { name: 'checks', kind: 'multi', options: checks }],
        op: (v) => ({ op: 'add', type: 'gate', node: v.node || '', after: n.id, checks: v.checks || [] }) });
    }
    ops.push({ key: 'remove', fields: () => [], op: () => ({ op: 'remove', node: n.id }) });
    ops.push({ key: 'rename', fields: () => [{ name: 'name', kind: 'text', value: n.id }], op: (v) => ({ op: 'rename', node: n.id, name: v.name || '' }) });
    ops.push({ key: 'link', fields: () => [{ name: 'outcome', kind: 'outcome', options: n.outcomes, value: n.outcomes[0] },
      { name: 'target', kind: 'select', options: others.concat(['done', 'failed']) }],
    op: (v) => ({ op: 'link', node: n.id, outcome: v.outcome || '', target: v.target || '' }) });
    if (Object.keys(n.on || {}).length) {
      ops.push({ key: 'unlink', fields: () => [{ name: 'outcome', kind: 'select', options: Object.keys(n.on) }],
        op: (v) => ({ op: 'unlink', node: n.id, outcome: v.outcome || '' }) });
    }
    if (!(n.type === 'gate' && (n.checks || []).length) && n.type !== 'human') {
      ops.push({ key: 'timeout', fields: () => [{ name: 'value', kind: 'text', value: n.timeout || '' }],
        op: (v) => (v.value || '').trim() ? { op: 'set', node: n.id, field: 'timeout', value: v.value.trim() } : { op: 'unset', node: n.id, field: 'timeout' } });
    }
    if (n.type === 'agent') {
      const agentFields = ['executor', 'model', 'effort', 'attempts', 'dialogue', 'idle_timeout', 'tools', 'prompt', 'schema'];
      ops.push({ key: 'agent', fields: (v) => [{ name: 'field', kind: 'select', options: agentFields, value: 'model' },
        (v.field || 'model') === 'executor' ? { name: 'value', kind: 'select', options: f.executors, value: n.executor } : { name: 'value', kind: 'text', value: current(n, v.field || 'model') }],
      op: (v) => {
        const field = v.field || 'model';
        const value = (v.value === undefined ? current(n, field) : v.value).trim();
        if (!value && f.fields.unset.includes(field)) return { op: 'unset', node: n.id, field: field };
        return { op: 'set', node: n.id, field: field, value: value };
      } });
    }
    if (n.type === 'gate' || n.type === 'human') {
      ops.push({ key: 'ask', fields: (v) => {
        const out = [{ name: 'id', kind: 'text' }, { name: 'question', kind: 'text' }];
        if (n.type === 'human') {
          out.push({ name: 'kind', kind: 'select', options: ['yesno', 'choice', 'text'], value: 'yesno' });
          if (v.kind === 'choice') out.push({ name: 'options', kind: 'list' });
        }
        return out;
      }, op: (v) => {
        const op = { op: 'ask', node: n.id, id: v.id || '', question: v.question || '' };
        if (v.kind && v.kind !== 'yesno') op.kind = v.kind;
        if (v.kind === 'choice' && v.options) op.options = split(v.options);
        return op;
      } });
    }
    if (n.type === 'gate' && (n.checks || []).length) {
      const missing = checks.filter((x) => !n.checks.includes(x));
      if (missing.length) {
        ops.push({ key: 'gate_add', fields: () => [{ name: 'check', kind: 'select', options: missing }, { name: 'at', kind: 'number' }],
          op: (v) => { const op = { op: 'gate-add', node: n.id, check: v.check || missing[0] }; if (Number(v.at) > 0) op.at = Number(v.at); return op; } });
      }
      ops.push({ key: 'gate_remove', fields: () => [{ name: 'check', kind: 'select', options: n.checks }],
        op: (v) => ({ op: 'gate-remove', node: n.id, check: v.check || n.checks[0] }) });
      ops.push({ key: 'soft', fields: () => [{ name: 'check', kind: 'select', options: n.checks }, { name: 'soft', kind: 'select', options: ['soft', 'required'] }],
        op: (v) => ({ op: 'check', name: v.check || n.checks[0], soft: (v.soft || 'soft') === 'soft' }) });
    }
    return ops;
  }

  function current(n, field) {
    switch (field) {
      case 'executor': return n.executor || '';
      case 'model': return n.model || '';
      case 'effort': return n.effort || '';
      case 'attempts': return n.attempts ? String(n.attempts) : '';
      default: return '';
    }
  }

  function split(s) { return String(s).split(/[,\s]+/).filter(Boolean); }

  function editorCard(f, id) {
    const n = f.nodes.find((x) => x.id === id);
    const close = () => { location.hash = flowHref(f.file); };
    const body = h('div', { class: 'card-body', 'data-scroll': 'card' });
    if (!n) {
      body.append(h('p', { class: 'empty', text: t('page.flow.no_node', { node: id }) }));
    } else {
      const facts = h('dl', { class: 'kv' },
        W.kv(t('page.kv.type'), h('code', { text: n.type })),
        Object.keys(n.on || {}).length ? W.kv(t('page.card.transitions'), Object.entries(n.on).map(([o, to]) => h('span', { class: 'tag', text: o + ' → ' + to }))) : null,
        n.run ? W.kv(t('page.kv.run'), h('code', { text: n.run.join(' ') })) : null,
        n.checks ? W.kv(t('page.card.checks'), n.checks.map((x) => h('span', { class: 'tag', text: x + (soft(f, x) ? ' · ' + t('page.check.soft') : '') }))) : null,
        n.timeout ? W.kv(t('page.kv.timeout'), n.timeout) : null,
        n.executor ? W.kv(t('page.kv.executor'), h('code', { text: n.executor })) : null,
        n.model ? W.kv(t('page.kv.model'), h('code', { text: n.model })) : null,
        n.effort ? W.kv(t('page.kv.effort'), h('code', { text: n.effort })) : null,
        n.attempts ? W.kv(t('page.kv.attempts'), String(n.attempts)) : null,
        n.ask ? W.kv(t('page.kv.questions'), n.ask.join(', ')) : null);
      body.append(facts);
      if (C.change) body.append(changeBox(f));
      else if (C.error) body.append(h('div', { class: 'err form-err', role: 'alert', text: C.error }));
      body.append(h('h3', { text: t('page.edit.operations') }));
      for (const op of operations(f, n)) body.append(opForm(f, n, op));
    }
    const panel = h('aside', { class: 'card', role: 'dialog', 'aria-modal': 'true', 'aria-label': t('page.card.label', { node: id }), tabindex: -1,
      onkeydown: (e) => { if (e.key === 'Escape') close(); } },
    h('div', { class: 'card-head' }, h('h2', { text: id }), n ? h('span', { class: 'tag', text: n.type }) : null,
      h('button', { class: 'close', type: 'button', 'aria-label': t('page.card.close'), 'data-fkey': 'card-close', onclick: close, text: '×' })),
    body);
    setTimeout(() => { if (!panel.contains(document.activeElement)) panel.focus({ preventScroll: true }); }, 0);
    return [h('div', { class: 'scrim', onclick: close }), panel];
  }

  function soft(f, name) { const c = f.checks.find((x) => x.name === name); return c && c.soft; }

  function opForm(f, n, spec) {
    const key = n.id + '/' + spec.key;
    let v = C.forms.get(key);
    if (!v) { v = {}; C.forms.set(key, v); }
    const fields = spec.fields(v);
    for (const fd of fields) if (v[fd.name] === undefined && fd.value !== undefined) v[fd.name] = fd.value;
    const items = fields.map((fd) => field(key, v, fd));
    return W.details('op:' + key, false, { class: 'attempt ctl-op' },
      h('summary', null, h('b', { text: t('page.op.' + spec.key) })),
      h('form', { class: 'answer attempt-body', novalidate: true, onsubmit: (e) => {
        e.preventDefault();
        preview(f, spec.op(v), spec.run ? spec.run(v) : '');
      } },
      items,
      h('div', { class: 'send' }, h('span', { class: 'hint', text: t('page.edit.nothing_yet') }),
        h('button', { class: 'btn primary', type: 'submit', 'data-fkey': 'show-' + key, disabled: C.busy }, t('page.edit.show')))));
  }

  function field(key, v, fd) {
    const fkey = key + '/' + fd.name;
    const label = t('page.field.' + fd.name);
    const rerender = () => W.render();
    let input;
    switch (fd.kind) {
      case 'select':
        input = h('select', { 'data-fkey': fkey, 'aria-label': label, onchange: (e) => { v[fd.name] = e.target.value; rerender(); } },
          fd.options.map((o) => h('option', { value: o, selected: (v[fd.name] || '') === o }, o === '' ? t('page.field.main_outcome') : tk('page.field.value.', o))));
        if (v[fd.name] === undefined && fd.options.length) v[fd.name] = fd.options[0];
        break;
      case 'multi':
        input = h('div', { class: 'opts' }, fd.options.map((o) => h('label', { class: 'opt' },
          h('input', { type: 'checkbox', 'data-fkey': fkey + '=' + o, checked: (v[fd.name] || []).includes(o),
            onchange: (e) => {
              const set = new Set(v[fd.name] || []);
              if (e.target.checked) set.add(o); else set.delete(o);
              v[fd.name] = fd.options.filter((x) => set.has(x));
            } }), ' ', o)));
        break;
      case 'check':
        input = h('input', { type: 'checkbox', 'data-fkey': fkey, checked: v[fd.name] !== false, onchange: (e) => { v[fd.name] = e.target.checked; } });
        break;
      case 'outcome': {
        const list = 'dl-' + fkey.replace(/[^a-zA-Z0-9_-]/g, '_');
        input = h('span', null, h('input', { type: 'text', list: list, 'data-fkey': fkey, 'aria-label': label, oninput: (e) => { v[fd.name] = e.target.value; } }),
          h('datalist', { id: list }, fd.options.map((o) => h('option', { value: o }))));
        input.firstChild.value = v[fd.name] || '';
        break;
      }
      default:
        input = h('input', { type: fd.kind === 'number' ? 'number' : 'text', class: fd.mono ? 'mono' : '', 'data-fkey': fkey, 'aria-label': label,
          oninput: (e) => { v[fd.name] = e.target.value; } });
        input.value = v[fd.name] || '';
    }
    return h('label', { class: 'row' }, h('span', { class: 'lbl', text: label }), input,
      I18N_HINTS.includes(fd.name) ? h('span', { class: 'hint', text: t('page.field.hint.' + fd.name) }) : null);
  }

  const I18N_HINTS = ['run', 'outcomes', 'via', 'value', 'at', 'starters', 'options'];

  Object.assign(W.hooks, { listTop, campaignButtons, cardTop, columnNote, openFlow, renderFlow });
})();
