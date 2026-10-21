'use strict';

// ---------- helpers ----------

const $app = document.getElementById('app');
const $nav = document.getElementById('topnav');
let META = null;

// h('div.cls', {attrs}, children...) builds DOM without innerHTML.
function h(sel, attrs, ...kids) {
  const [tag, ...cls] = sel.split('.');
  const el = document.createElement(tag || 'div');
  if (cls.length) el.className = cls.join(' ');
  if (attrs && (typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs))) {
    kids.unshift(attrs);
    attrs = null;
  }
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className += ' ' + v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked' || k === 'selected' || k === 'disabled') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  const add = (k) => {
    if (k === null || k === undefined || k === false) return;
    if (Array.isArray(k)) k.forEach(add);
    else el.append(k instanceof Node ? k : String(k));
  };
  kids.forEach(add);
  return el;
}

function toast(msg) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { t.hidden = true; }, 4000);
}

async function api(method, path, body) {
  const opts = { method, headers: {}, credentials: 'same-origin' };
  if (body instanceof FormData) opts.body = body;
  else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let r = await fetch(path, opts);
  // Behind idlegate (see README), a request that finds the server stopped
  // outside the gate gets a 502 and the next one starts it again. Reads are
  // safe to repeat, so retry those once instead of showing the error.
  if (r.status === 502 && method === 'GET') r = await fetch(path, opts);
  if (r.status === 401 && path !== '/api/login') {
    showLogin();
    throw new Error('unauthorized');
  }
  if (r.status === 204) return null;
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    const msg = data.error || r.statusText;
    toast(msg);
    const err = new Error(msg);
    err.shown = true;
    throw err;
  }
  return data;
}

// API errors are already shown as a toast; don't report them again as
// uncaught from the event handlers that await api().
window.addEventListener('unhandledrejection', (e) => {
  if (e.reason && (e.reason.shown || e.reason.message === 'unauthorized')) e.preventDefault();
});

function select(options, value, onchange, attrs) {
  return h('select', Object.assign({ onchange: (e) => onchange(e.target.value) }, attrs || {}),
    options.map(([v, label]) => h('option', { value: v, selected: String(v) === String(value) }, label)));
}

function fmtDate(s) {
  if (!s) return '';
  const d = new Date(s);
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}
function fmtDateTime(s) {
  if (!s) return '';
  return new Date(s).toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

const PRIO_ICON = { Highest: '⇈', High: '↑', Medium: '=', Low: '↓', Lowest: '⇊' };
const typeIcon = (t) => h('span.type.' + t, { title: t }, t[0]);
const prioIcon = (p) => h('span.prio.' + p, { title: p }, PRIO_ICON[p] || '');

function statusPill(project, id) {
  const st = project.statuses.find((s) => s.id === id);
  return st ? h('span.pill.' + st.category, st.name) : h('span.pill', '?');
}
// Small line icons for the issue fields, drawn as SVG (h() makes HTML
// elements only). Each is a list of [tag, attributes] on a 24x24 grid.
const ICONS = {
  key: [['path', { d: 'M10 3 8 21M16 3l-2 18M4 8h17M3 16h17' }]],
  release: [['path', { d: 'M20.6 13.4 13.4 20.6a2 2 0 0 1-2.8 0L3 13V3h10l7.6 7.6a2 2 0 0 1 0 2.8z' }], ['circle', { cx: 7.5, cy: 7.5, r: 1.5 }]],
  board: [['rect', { x: 3, y: 4, width: 18, height: 16, rx: 2 }], ['path', { d: 'M9 4v16M15 4v16' }]],
  backlog: [['path', { d: 'M4 6h16M4 12h16M4 18h10' }]],
  epic: [['path', { d: 'M13 2 4 14h7l-1 8 9-12h-7z' }]],
  created: [['rect', { x: 3, y: 5, width: 18, height: 16, rx: 2 }], ['path', { d: 'M3 10h18M8 3v4M16 3v4' }]],
  updated: [['circle', { cx: 12, cy: 12, r: 9 }], ['path', { d: 'M12 7v5l3 2' }]],
  resolved: [['circle', { cx: 12, cy: 12, r: 9 }], ['path', { d: 'm8 12 3 3 5-6' }]],
};
function icon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  for (const [k, v] of Object.entries({ viewBox: '0 0 24 24', width: 16, height: 16, fill: 'none', stroke: 'currentColor',
    'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', class: 'ico', 'aria-hidden': 'true' })) svg.setAttribute(k, v);
  for (const [tag, attrs] of ICONS[name]) {
    const el = document.createElementNS(ns, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    svg.append(el);
  }
  return svg;
}

const isDone = (project, issue) => (project.statuses.find((s) => s.id === issue.status) || {}).category === 'done';

// ---------- markdown (small, safe subset) ----------

function esc(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function emphasis(s) {
  s = s.replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>');
  return s.replace(/(^|[\s(])_([^_]+)_(?=[\s).,!?:;]|$)/g, '$1<em>$2</em>');
}

// Code spans and links are swapped for placeholders as soon as they are
// built, so the later patterns never run over generated HTML: an autolink
// matched inside a link's href would otherwise close the attribute.
function inline(s) {
  const held = [];
  const hold = (html) => { held.push(html); return '\u0000' + (held.length - 1) + '\u0000'; };
  s = esc(s.replace(/\u0000/g, ''));
  s = s.replace(/`([^`]+)`/g, (_, c) => hold(`<code>${c}</code>`));
  s = s.replace(/\[([^\]]+)\]\(([^)\s\u0000]+)\)/g, (m, text, url) =>
    /^(https?:|mailto:|\/|#)/i.test(url) ? hold(`<a href="${url}" target="_blank" rel="noopener">${emphasis(text)}</a>`) : m);
  s = s.replace(/(^|[\s(])(https?:\/\/[^\s<)\u0000]+)/g, (_, pre, url) =>
    pre + hold(`<a href="${url}" target="_blank" rel="noopener">${url}</a>`));
  s = emphasis(s);
  // A link's text can hold a code span, so restore until none are left.
  while (s.includes('\u0000')) s = s.replace(/\u0000(\d+)\u0000/g, (_, n) => held[n]);
  return s;
}

function markdown(src) {
  const lines = (src || '').replace(/\r/g, '').split('\n');
  let out = '';
  let para = [];
  const lists = []; // stack of 'ul' / 'ol'
  const flush = () => {
    if (para.length) out += '<p>' + para.map(inline).join('<br>') + '</p>';
    para = [];
  };
  const closeLists = (depth) => { while (lists.length > depth) out += `</li></${lists.pop()}>`; };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.startsWith('```')) {
      flush(); closeLists(0);
      const code = [];
      for (i++; i < lines.length && !lines[i].startsWith('```'); i++) code.push(lines[i]);
      out += '<pre><code>' + esc(code.join('\n')) + '</code></pre>';
      continue;
    }
    let m;
    if ((m = line.match(/^(#{1,6})\s+(.*)$/))) {
      flush(); closeLists(0);
      const n = Math.min(m[1].length + 1, 4);
      out += `<h${n}>${inline(m[2])}</h${n}>`;
    } else if (/^\s*(---|\*\*\*)\s*$/.test(line)) {
      flush(); closeLists(0);
      out += '<hr>';
    } else if ((m = line.match(/^(\s*)([-*]|\d+\.)\s+(.*)$/))) {
      flush();
      const depth = Math.floor(m[1].length / 2) + 1;
      const kind = /\d/.test(m[2]) ? 'ol' : 'ul';
      if (lists.length > depth) closeLists(depth);
      if (lists.length === depth && lists[depth - 1] !== kind) closeLists(depth - 1);
      if (lists.length === depth) out += '</li><li>';
      while (lists.length < depth) { lists.push(kind); out += `<${kind}><li>`; }
      out += inline(m[3]);
    } else if (line.trim() === '') {
      flush(); closeLists(0);
    } else if (lists.length) {
      out += ' ' + inline(line.trim());
    } else {
      para.push(line);
    }
  }
  flush(); closeLists(0);
  const el = h('div.md');
  el.innerHTML = out; // built from escaped text only
  return el;
}

// ---------- shared data ----------

async function loadProject(key) {
  const [project, releases, issues] = await Promise.all([
    api('GET', `/api/projects/${key}`),
    api('GET', `/api/projects/${key}/releases`),
    api('GET', `/api/projects/${key}/issues`),
  ]);
  const byId = new Map(issues.map((i) => [i.id, i]));
  const epics = issues.filter((i) => i.type === 'Epic');
  return { project, releases, issues, byId, epics, relById: new Map(releases.map((r) => [r.id, r])) };
}

async function reloadIssues(ctx) {
  ctx.issues = await api('GET', `/api/projects/${ctx.project.key}/issues`);
  ctx.byId = new Map(ctx.issues.map((i) => [i.id, i]));
  ctx.epics = ctx.issues.filter((i) => i.type === 'Epic');
}

function epicChip(ctx, issue) {
  const e = issue.epic && ctx.byId.get(issue.epic);
  return e ? h('span.chip', { title: e.summary }, e.summary) : null;
}

function projectHeader(ctx, tab) {
  const k = ctx.project.key;
  const t = (name, label) => h('a', { href: `#/p/${k}/${name}`, class: tab === name ? 'on' : '' }, label);
  return [
    h('h1', ctx.project.name, ' ', h('span.muted.small', k)),
    h('div.tabs', t('backlog', 'Backlog'), t('board', 'Board'), t('releases', 'Releases'), t('settings', 'Settings'),
      h('span.spacer'), h('button.primary', { onclick: () => createIssueDialog(ctx, { board: tab === 'board' }) }, 'Create', h('span.wide', ' issue'))),
  ];
}

// ---------- popup menu ----------

// popMenu shows a list of actions under anchor. items: { label, icon,
// on (marks the current choice), onclick } or '-' for a separator; falsy
// items are skipped. Clicking the anchor again closes it.
function popMenu(anchor, items) {
  const again = popMenu.anchor === anchor;
  closeMenu();
  if (again) return;
  const m = h('div.menu', { role: 'menu' }, items.filter(Boolean).map((it) => (it === '-' ? h('div.sep')
    : h('button', {
      class: it.on ? 'on' : '',
      onclick: (e) => { e.stopPropagation(); closeMenu(); it.onclick(); },
    }, it.icon || null, it.label))));
  // In a modal dialog (the top layer), the menu has to be inside it to show.
  (anchor.closest('dialog') || document.body).append(m);
  const r = anchor.getBoundingClientRect();
  m.style.left = Math.max(8, Math.min(r.left, innerWidth - m.offsetWidth - 8)) + 'px';
  m.style.top = (r.bottom + m.offsetHeight + 4 > innerHeight ? Math.max(8, r.top - m.offsetHeight - 4) : r.bottom + 4) + 'px';
  popMenu.el = m;
  popMenu.anchor = anchor;
}

function closeMenu() {
  if (popMenu.el) popMenu.el.remove();
  popMenu.el = popMenu.anchor = null;
}

document.addEventListener('mousedown', (e) => {
  const m = popMenu.el;
  if (m && !m.contains(e.target) && !popMenu.anchor.contains(e.target)) closeMenu();
}, true);
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && popMenu.el) { e.preventDefault(); closeMenu(); } }, true);
window.addEventListener('resize', closeMenu);
document.addEventListener('scroll', (e) => { if (popMenu.el && !popMenu.el.contains(e.target)) closeMenu(); }, true);

// ---------- inline edits on backlog rows and board cards ----------

// issueEditor returns a function that patches the issue and redraws.
function issueEditor(issue, redraw) {
  return async (patch) => {
    Object.assign(issue, await api('PATCH', `/api/issues/${issue.key}`, patch));
    redraw();
  };
}

// editInline swaps el for a text input. Enter or leaving the field saves,
// Escape puts el back unchanged. multiline: a text area that grows with the
// text and wraps it like el, for the summaries on board cards (a summary is
// still one line: Enter saves).
function editInline(el, value, save, multiline) {
  const input = multiline
    ? h('textarea.inline', { rows: 1, value, onclick: (e) => e.stopPropagation() })
    : h('input.inline', { type: 'text', value, onclick: (e) => e.stopPropagation() });
  const fit = () => { input.style.height = 'auto'; input.style.height = input.scrollHeight + 'px'; };
  if (multiline) input.addEventListener('input', fit);
  // Text in a draggable element can't be selected with the mouse.
  const drag = el.closest('[draggable=true]');
  if (drag) drag.setAttribute('draggable', 'false');
  let done = false;
  const restore = () => {
    input.replaceWith(el);
    if (drag) drag.setAttribute('draggable', 'true');
  };
  const finish = async (ok) => {
    if (done) return;
    done = true;
    const v = input.value.replace(/\s*\n\s*/g, ' ').trim();
    if (!ok || !v || v === value) { restore(); return; }
    try { await save(v); } catch (err) { restore(); throw err; }
  };
  input.addEventListener('keydown', (e) => {
    e.stopPropagation();
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
  el.replaceWith(input);
  if (multiline) fit();
  input.focus();
  input.select();
}

function typeButton(issue, save) {
  if (issue.type === 'Epic') return typeIcon('Epic');
  const b = h('button.icon', {
    title: `${issue.type}: change type`,
    onclick: (e) => {
      e.stopPropagation();
      popMenu(b, META.types.filter((t) => t !== 'Epic').map((t) => ({
        icon: typeIcon(t), label: t, on: t === issue.type, onclick: () => { if (t !== issue.type) save({ type: t }); },
      })));
    },
  }, typeIcon(issue.type));
  return b;
}

function prioButton(issue, save) {
  const b = h('button.icon', {
    title: `${issue.priority}: change priority`,
    onclick: (e) => {
      e.stopPropagation();
      popMenu(b, META.priorities.map((p) => ({
        icon: prioIcon(p), label: p, on: p === issue.priority, onclick: () => { if (p !== issue.priority) save({ priority: p }); },
      })));
    },
  }, prioIcon(issue.priority));
  return b;
}

function iconButton(label, title, onclick) {
  return h('button.icon.hover', { title, onclick: (e) => { e.stopPropagation(); onclick(e.currentTarget); } }, label);
}

// fieldPicker is a value in the issue's side panel: shown with its icon, a
// click opens a menu of the choices (items() returns popMenu items).
function fieldPicker(title, items, ...content) {
  const b = h('button.field', {
    type: 'button', title, onclick: (e) => { e.stopPropagation(); popMenu(b, items()); },
  }, h('span.fval', content), h('span.caret', '▾'));
  return b;
}

// issuePicker is a text field that searches issues as you type and lists
// them to pick one; Enter or a click picks the highlighted one. A key typed
// in full works without picking. exclude: a key not to offer.
function issuePicker(exclude, placeholder) {
  const input = h('input', { type: 'text', placeholder, autocomplete: 'off', 'aria-label': placeholder });
  const list = h('div.search-results.picker-results', { hidden: true });
  let hits = [];
  let sel = 0;
  let seq = 0;
  let timer = null;
  let picked = null;

  const show = () => {
    list.replaceChildren(...(hits.length ? hits.map((hit, k) => h('div.hit', {
      class: k === sel ? 'on' : '',
      onmousedown: (e) => e.preventDefault(), // keep the focus in the input
      onclick: () => pick(hit),
    }, typeIcon(hit.type), h('span.key', hit.key), h('span.sum', hit.summary),
    h('span.pill' + (hit.done ? '.done' : ''), hit.status))) : [h('div.empty', 'No issues found.')]));
    list.hidden = false;
    const on = list.querySelector('.hit.on');
    if (on) on.scrollIntoView({ block: 'nearest' });
  };
  const run = async () => {
    clearTimeout(timer);
    const q = input.value.trim();
    const n = ++seq;
    if (!q) { hits = []; list.hidden = true; return; }
    const r = await api('GET', '/api/search?q=' + encodeURIComponent(q));
    if (n !== seq) return; // a newer search is under way
    hits = r.filter((hit) => hit.key !== exclude);
    sel = 0;
    show();
  };
  const pick = (hit) => {
    picked = hit;
    seq++; // drop a search still under way
    input.value = `${hit.key} ${hit.summary}`;
    list.hidden = true;
  };

  input.addEventListener('input', () => { picked = null; clearTimeout(timer); timer = setTimeout(run, 150); });
  input.addEventListener('blur', () => { list.hidden = true; });
  input.addEventListener('focus', () => { if (!picked && hits.length && input.value.trim()) show(); });
  input.addEventListener('keydown', (e) => {
    if (list.hidden) return; // Enter submits the form
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!hits.length) return;
      sel = (sel + (e.key === 'ArrowDown' ? 1 : hits.length - 1)) % hits.length;
      show();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (hits[sel]) pick(hits[sel]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      list.hidden = true;
    }
  });
  return {
    el: h('div.picker', input, list),
    // The picked issue's key, else what was typed (a key).
    value: () => (picked ? picked.key : input.value.trim().toUpperCase()),
  };
}

// issueRow is one line in the backlog, a release or an epic. opts:
// attrs (extra attributes), hideRelease, and, for rows that can be edited
// in place, redraw (called after a change) and menu (a function returning
// more menu items).
function issueRow(ctx, issue, opts) {
  opts = opts || {};
  const sum = h('span.sum', issue.summary);
  const save = opts.redraw && issueEditor(issue, opts.redraw);
  const editSummary = () => editInline(sum, issue.summary, (v) => save({ summary: v }));
  return h('div.row', Object.assign({ onclick: () => openIssue(issue.key) }, opts.attrs || {}),
    save ? typeButton(issue, save) : typeIcon(issue.type),
    h('span.key', { class: isDone(ctx.project, issue) ? 'done' : '' }, issue.key),
    sum,
    save ? iconButton('✎', 'Edit summary', editSummary) : null,
    epicChip(ctx, issue),
    !opts.hideRelease && issue.release && ctx.relById.get(issue.release) ? h('span.rel', ctx.relById.get(issue.release).name) : null,
    statusPill(ctx.project, issue.status),
    save ? prioButton(issue, save) : prioIcon(issue.priority),
    save ? iconButton('⋯', 'More actions', (b) => popMenu(b, [
      { label: 'Open', onclick: () => openIssue(issue.key) },
      { label: 'Edit summary', onclick: editSummary },
      ...(opts.menu ? ['-', ...opts.menu()] : []),
    ])) : null);
}

// ---------- create issue ----------

function createIssueDialog(ctx, preset) {
  preset = preset || {};
  const f = {
    type: preset.type || 'Task', summary: '', description: '', priority: 'Medium',
    release: preset.release || 0, epic: preset.epic || '', board: !!preset.board,
  };
  const relOpts = [[0, '— none —'], ...ctx.releases.filter((r) => !r.released).map((r) => [r.id, r.name])];
  const epicOpts = [['', '— none —'], ...ctx.epics.map((e) => [e.key, `${e.key} ${e.summary}`])];
  const sum = h('input', { type: 'text', style: 'width:100%', oninput: (e) => { f.summary = e.target.value; } });
  const dlg = h('dialog',
    h('h2', { style: 'margin-top:0' }, `Create issue in ${ctx.project.key}`),
    h('form', { method: 'dialog', onsubmit: submit },
      h('div.fields',
        h('label', 'Type'), select(META.types.map((t) => [t, t]), f.type, (v) => { f.type = v; }),
        h('label', 'Summary'), sum,
        h('label', 'Priority'), select(META.priorities.map((p) => [p, p]), f.priority, (v) => { f.priority = v; }),
        h('label', 'Release'), select(relOpts, f.release, (v) => { f.release = +v; }),
        h('label', 'Epic'), select(epicOpts, f.epic, (v) => { f.epic = v; }),
        h('label', 'Board'), h('label', h('input', { type: 'checkbox', checked: f.board, onchange: (e) => { f.board = e.target.checked; } }), ' Put it on the board'),
        h('label', 'Description'), h('textarea', { oninput: (e) => { f.description = e.target.value; } })),
      h('div.actions',
        h('button', { type: 'button', onclick: () => dlg.close() }, 'Cancel'),
        h('button.primary', { type: 'submit' }, 'Create'))));
  async function submit(e) {
    e.preventDefault();
    const body = { type: f.type, summary: f.summary, description: f.description, priority: f.priority, release: f.release, board: f.board };
    if (f.epic && f.type !== 'Epic') body.epic = f.epic;
    const issue = await api('POST', `/api/projects/${ctx.project.key}/issues`, body);
    dlg.close();
    if (preset.onCreated) preset.onCreated(issue);
    else route();
  }
  dlg.addEventListener('close', () => dlg.remove());
  document.body.append(dlg);
  dlg.showModal();
  sum.focus();
}

// ---------- pages ----------

function showLogin() {
  closeIssueModal();
  $nav.replaceChildren();
  const input = h('input', { type: 'password', placeholder: 'API key', autocomplete: 'current-password' });
  $app.replaceChildren(h('form.panel.login', {
    onsubmit: async (e) => {
      e.preventDefault();
      await api('POST', '/api/login', { key: input.value });
      route();
    },
  }, h('h1', 'Sign in'), input, h('button.primary', { type: 'submit' }, 'Sign in')));
  input.focus();
}

function topNav(projects) {
  $nav.replaceChildren(
    ...(projects || []).map((p) => h('a', { href: `#/p/${p.key}/backlog` }, p.key)),
    h('span.spacer'),
    searchBox(),
    h('a', { href: '#/import' }, 'Import from Jira'),
    h('a', { href: '#', onclick: async (e) => { e.preventDefault(); await api('POST', '/api/logout'); showLogin(); } }, 'Sign out'));
}

// ---------- search ----------

// searchBox is the issue search in the header, over all projects. It is
// built once and kept, so the query survives moving between pages.
function searchBox() {
  if (searchBox.el) return searchBox.el;
  const input = h('input.search', { type: 'search', placeholder: 'Search issues  /', autocomplete: 'off', 'aria-label': 'Search issues' });
  const list = h('div.search-results', { hidden: true });
  let hits = [];
  let sel = 0;
  let seq = 0;
  let shownQ = null;
  let timer = null;

  const show = () => {
    list.replaceChildren(...(hits.length ? hits.map((hit, k) => h('div.hit', {
      class: k === sel ? 'on' : '',
      onmousedown: (e) => e.preventDefault(), // keep the focus in the input
      onclick: () => pick(hit),
    }, typeIcon(hit.type), h('span.key', hit.key), h('span.sum', hit.summary),
    h('span.pill' + (hit.done ? '.done' : ''), hit.status))) : [h('div.empty', 'No issues found.')]));
    list.hidden = false;
  };
  const run = async () => {
    clearTimeout(timer);
    timer = null;
    const q = input.value.trim();
    const n = ++seq;
    if (!q) { hits = []; shownQ = q; list.hidden = true; return; }
    const r = await api('GET', '/api/search?q=' + encodeURIComponent(q));
    if (n !== seq) return; // a newer search is under way
    hits = r;
    sel = 0;
    shownQ = q;
    show();
  };
  const pick = (hit) => {
    list.hidden = true;
    input.blur();
    openIssue(hit.key);
  };

  input.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(run, 150); });
  input.addEventListener('focus', () => { if (input.value.trim() && shownQ === input.value.trim()) show(); });
  input.addEventListener('blur', () => { list.hidden = true; });
  input.addEventListener('keydown', async (e) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!hits.length) return;
      sel = (sel + (e.key === 'ArrowDown' ? 1 : hits.length - 1)) % hits.length;
      show();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (shownQ !== input.value.trim()) await run();
      if (hits[sel]) pick(hits[sel]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      if (!list.hidden) list.hidden = true; else input.blur();
    }
  });
  searchBox.hide = () => { list.hidden = true; input.blur(); };
  searchBox.input = input;
  searchBox.el = h('div.searchbox', input, list);
  return searchBox.el;
}

function hideSearch() {
  if (searchBox.hide) searchBox.hide();
}

// "/" anywhere but in a field jumps to the search.
document.addEventListener('keydown', (e) => {
  if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey || modal.dlg || !searchBox.input || !searchBox.input.isConnected) return;
  const t = e.target;
  if (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return;
  e.preventDefault();
  searchBox.input.focus();
  searchBox.input.select();
});

async function pageProjects() {
  const projects = await api('GET', '/api/projects');
  topNav(projects);
  const f = { key: '', name: '' };
  $app.replaceChildren(
    h('h1', 'Projects'),
    projects.length ? h('div.cards', projects.map((p) => h('a.panel', { href: `#/p/${p.key}/backlog` },
      h('h3', p.name), h('div.muted.small', p.key), p.description ? h('div.small', p.description) : null)))
      : h('p.muted', 'No projects yet. Create one below, or ', h('a', { href: '#/import' }, 'import a Jira export'), '.'),
    h('h2', 'New project'),
    h('form.form', {
      onsubmit: async (e) => {
        e.preventDefault();
        const p = await api('POST', '/api/projects', { key: f.key, name: f.name });
        location.hash = `#/p/${p.key}/backlog`;
      },
    },
    h('input', { type: 'text', placeholder: 'KEY', size: 8, oninput: (e) => { f.key = e.target.value.toUpperCase(); } }),
    h('input', { type: 'text', placeholder: 'Name', oninput: (e) => { f.name = e.target.value; } }),
    h('button.primary', { type: 'submit' }, 'Create')));
}

// The key of the issue being dragged, in the backlog and on the board.
let dragKey = null;

function endDrag() {
  dragKey = null;
  dropLine.remove();
  for (const el of document.querySelectorAll('.drop, .drop-before, .drop-after')) el.classList.remove('drop', 'drop-before', 'drop-after');
}
document.addEventListener('dragend', endDrag);

// Folding sections (details elements) remember whether they are open
// across redraws: state[name] holds it, def is the first value.
function fold(state, name, def, summary, ...kids) {
  if (state[name] === undefined) state[name] = def;
  const d = h('details.section', { open: state[name] }, summary, ...kids);
  d.addEventListener('toggle', () => { state[name] = d.open; });
  return d;
}

// Drag and drop in the backlog: rows reorder; dropping into a release's
// section sets the release and takes the issue off the board, dropping into
// the board section puts it on the board.
async function pageBacklog(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const st = pageBacklog.state[key] || (pageBacklog.state[key] = { q: '', type: '', epic: '', showDone: false, folds: {} });
  const body = h('div');

  const draw = () => {
    const q = st.q.toLowerCase();
    const visible = ctx.issues.filter((i) => i.type !== 'Epic'
      && (st.showDone || !isDone(ctx.project, i))
      && (!st.type || i.type === st.type)
      && (!st.epic || String(i.epic) === st.epic)
      && (!q || (i.key + ' ' + i.summary).toLowerCase().includes(q)));
    const groups = ctx.releases.filter((r) => st.showDone || !r.released).map((r) => ({ release: r, items: [] }));
    const none = { release: null, items: [] };
    const board = { board: true, items: [] };
    for (const i of visible) {
      if (i.board) { board.items.push(i); continue; }
      const g = groups.find((x) => x.release.id === i.release);
      if (g) g.items.push(i); else if (!i.release || !st.showDone) none.items.push(i);
    }
    groups.push(none);

    const epicsBox = fold(st.folds, 'epics', false,
      h('summary.head', h('b', `Epics (${ctx.epics.length})`)),
      ctx.epics.map((e) => {
        const kids = ctx.issues.filter((i) => i.epic === e.id);
        const done = kids.filter((i) => isDone(ctx.project, i)).length;
        return h('div.row', { onclick: () => openIssue(e.key) },
          typeIcon('Epic'), h('span.key', e.key), h('span.sum', e.summary),
          h('span.muted.small', `${done}/${kids.length} done`), statusPill(ctx.project, e.status));
      }),
      h('div.quick', h('button.link', { onclick: () => createIssueDialog(ctx, { type: 'Epic' }) }, '+ Create epic')));

    body.replaceChildren(epicsBox, section(board), ...groups.map((g) => section(g)));
  };

  // Where a drop into a section moves an issue.
  const targetOf = (g) => (g.board ? { board: true } : { release: g.release ? g.release.id : 0 });

  const section = (g) => {
    const rel = g.release;
    const relId = rel ? rel.id : 0;
    const target = targetOf(g);
    const rows = g.items.map((i, k) => {
      const row = issueRow(ctx, i, {
        attrs: { draggable: 'true' },
        hideRelease: !!rel,
        redraw: draw,
        menu: () => [
          i.board ? { label: 'Send to backlog', onclick: () => issueEditor(i, draw)({ board: false }) }
            : { label: 'Send to board', onclick: () => issueEditor(i, draw)({ board: true }) },
          k > 0 ? { label: 'Move to top', onclick: () => moveIssue(i.key, target, { before: g.items[0].key }) } : null,
          k < g.items.length - 1 ? { label: 'Move to bottom', onclick: () => moveIssue(i.key, target, { after: g.items[g.items.length - 1].key }) } : null,
        ],
      });
      row.addEventListener('dragstart', (e) => { dragKey = i.key; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', i.key); });
      row.addEventListener('dragover', (e) => {
        if (!dragKey || dragKey === i.key) return;
        e.preventDefault();
        e.stopPropagation();
        const after = e.offsetY > row.offsetHeight / 2;
        row.classList.toggle('drop-before', !after);
        row.classList.toggle('drop-after', after);
      });
      row.addEventListener('dragleave', () => row.classList.remove('drop-before', 'drop-after'));
      row.addEventListener('drop', async (e) => {
        e.preventDefault();
        e.stopPropagation();
        const after = row.classList.contains('drop-after');
        row.classList.remove('drop-before', 'drop-after');
        await moveIssue(dragKey, target, after ? { after: i.key } : { before: i.key });
      });
      return row;
    });

    let quick = null;
    if (!g.board) {
      const input = h('input', { type: 'text', placeholder: '+ What needs to be done? (Enter)' });
      const quickType = select(META.types.filter((t) => t !== 'Epic').map((t) => [t, t]), 'Task', () => {});
      input.addEventListener('keydown', async (e) => {
        if (e.key !== 'Enter' || !input.value.trim()) return;
        const data = { summary: input.value, type: quickType.value, release: relId };
        if (st.epic) data.epic = ctx.byId.get(+st.epic).key;
        const issue = await api('POST', `/api/projects/${key}/issues`, data);
        ctx.issues.push(issue);
        ctx.byId.set(issue.id, issue);
        draw();
        // draw() rebuilt the section: keep typing in the new input.
        const again = body.querySelector(`.section[data-rel="${relId}"] .quick input`);
        if (again) again.focus();
      });
      quick = h('div.quick', quickType, input);
    }

    const head = [
      h('b', g.board ? 'Board' : rel ? rel.name : 'Backlog'),
      h('span.muted.small', `${g.items.length} issues`),
      rel && rel.releaseDate ? h('span.muted.small', 'release ' + fmtDate(rel.releaseDate)) : null,
      rel && rel.released ? h('span.pill.done', 'released') : null,
    ];
    const empty = g.items.length ? null
      : h('div.empty', g.board ? 'Nothing on the board. Drag issues here, or use Send to board in their ⋯ menu.' : 'Nothing here. Drag issues in, or add one below.');
    const sec = g.board ? fold(st.folds, 'board', true, h('summary.head', head), empty, rows)
      : h('div.section', { 'data-rel': relId }, h('div.head', head), empty, rows, quick);
    sec.addEventListener('dragover', (e) => { if (dragKey) { e.preventDefault(); sec.classList.add('drop'); } });
    sec.addEventListener('dragleave', (e) => { if (!sec.contains(e.relatedTarget)) sec.classList.remove('drop'); });
    sec.addEventListener('drop', async (e) => {
      e.preventDefault();
      sec.classList.remove('drop');
      const last = g.items[g.items.length - 1];
      await moveIssue(dragKey, target, last && last.key !== dragKey ? { after: last.key } : null);
    });
    return sec;
  };

  // moveIssue puts an issue into a section (target from targetOf) and, with
  // rank, before or after another issue.
  async function moveIssue(k, target, rank) {
    const issue = ctx.issues.find((i) => i.key === k);
    dragKey = null;
    if (!issue) return;
    const patch = {};
    if (target.board) {
      if (!issue.board) patch.board = true;
    } else {
      if (issue.board) patch.board = false;
      // Open issues of a released (hidden) release are drawn under Backlog:
      // reordering them there must not clear their release.
      const drawnIn = ctx.releases.some((r) => r.id === issue.release && (st.showDone || !r.released)) ? issue.release : 0;
      if (drawnIn !== target.release) patch.release = target.release;
    }
    if (Object.keys(patch).length) Object.assign(issue, await api('PATCH', `/api/issues/${k}`, patch));
    if (rank) {
      await api('POST', `/api/issues/${k}/rank`, rank);
      // Respacing may have moved others too: reload.
      await reloadIssues(ctx);
    }
    draw();
  }

  const epicOpts = [['', 'All epics'], ...ctx.epics.map((e) => [String(e.id), e.summary])];
  $app.replaceChildren(...projectHeader(ctx, 'backlog'),
    h('div.toolbar',
      h('input', { type: 'text', placeholder: 'Filter…', value: st.q, oninput: (e) => { st.q = e.target.value; draw(); } }),
      select([['', 'All types'], ...META.types.filter((t) => t !== 'Epic').map((t) => [t, t])], st.type, (v) => { st.type = v; draw(); }),
      select(epicOpts, st.epic, (v) => { st.epic = v; draw(); }),
      h('label', h('input', { type: 'checkbox', checked: st.showDone, onchange: (e) => { st.showDone = e.target.checked; draw(); } }), 'Show done')),
    body);
  draw();
}
pageBacklog.state = {};

// The line showing where a card dragged on the board will land.
const dropLine = h('div.drop-line');

// boardTarget finds where a card dropped at height y in a column's list
// goes: before the first card whose middle is below y, else after the last.
function boardTarget(list, y) {
  const cards = [...list.children].filter((c) => c.classList.contains('card') && c.dataset.key !== dragKey);
  for (const c of cards) {
    const r = c.getBoundingClientRect();
    if (y < r.top + r.height / 2) return { el: c, rank: { before: c.dataset.key } };
  }
  const last = cards[cards.length - 1];
  return { el: null, rank: last ? { after: last.dataset.key } : null };
}

const byRank = (a, b) => a.rank - b.rank || a.number - b.number;

async function pageBoard(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const st = pageBoard.state[key] || (pageBoard.state[key] = { release: 'open', epic: '', q: '' });
  const board = h('div.board');
  const DONE_LIMIT = 30;
  // Like Jira's boards, the unreleased work shows done issues only for two
  // weeks after they were resolved.
  const DONE_DAYS = 14;
  const doneSince = new Date(Date.now() - DONE_DAYS * 864e5).toISOString();

  // moveCard puts an issue into a column and, with rank, before or after
  // another card.
  const moveCard = async (k, status, rank) => {
    const issue = ctx.issues.find((i) => i.key === k);
    if (!issue) return;
    if (issue.status !== status) Object.assign(issue, await api('PATCH', `/api/issues/${k}`, { status }));
    if (rank && (rank.before || rank.after) !== k) {
      await api('POST', `/api/issues/${k}/rank`, rank);
      await reloadIssues(ctx);
    }
    draw();
  };

  const card = (i, s, items) => {
    const save = issueEditor(i, draw);
    const rel = ctx.relById.get(i.release);
    const sum = h('div.csum', i.summary);
    const editSummary = () => editInline(sum, i.summary, (v) => save({ summary: v }), true);
    const k = items.indexOf(i);
    const c = h('div.card', { draggable: 'true', 'data-key': i.key, onclick: () => openIssue(i.key) },
      h('div.ctop', sum,
        iconButton('✎', 'Edit summary', editSummary),
        iconButton('⋯', 'More actions', (b) => popMenu(b, [
          { label: 'Open', onclick: () => openIssue(i.key) },
          { label: 'Edit summary', onclick: editSummary },
          '-',
          k > 0 ? { label: 'Move to top', onclick: () => moveCard(i.key, s.id, { before: items[0].key }) } : null,
          k < items.length - 1 ? { label: 'Move to bottom', onclick: () => moveCard(i.key, s.id, { after: items[items.length - 1].key }) } : null,
          { label: 'Send to backlog', onclick: () => save({ board: false }) },
        ]))),
      h('div.meta', typeButton(i, save), prioButton(i, save), epicChip(ctx, i),
        rel ? h('span.rel', rel.name) : null, h('span.key', i.key)));
    c.addEventListener('dragstart', (e) => { dragKey = i.key; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', i.key); });
    return c;
  };

  const draw = () => {
    const q = st.q.toLowerCase();
    const rel = (i) => ctx.relById.get(i.release);
    // Issues still in the backlog are not on the board.
    const oldDone = new Map(); // status id → done issues hidden for their age
    const visible = ctx.issues.filter((i) => {
      if (i.type === 'Epic' || !i.board) return false;
      if (st.epic === 'none' ? i.epic : st.epic && String(i.epic) !== st.epic) return false;
      if (q && !(i.key + ' ' + i.summary).toLowerCase().includes(q)) return false;
      if (st.release === 'open') {
        if (rel(i) && rel(i).released) return false;
        if (!isDone(ctx.project, i) || (i.resolved && new Date(i.resolved).toISOString() >= doneSince)) return true;
        oldDone.set(i.status, (oldDone.get(i.status) || 0) + 1);
        return false;
      }
      if (st.release === 'all') return true;
      if (st.release === 'none') return !i.release;
      return String(i.release) === st.release;
    });
    board.replaceChildren(...ctx.project.statuses.map((s) => {
      let items = visible.filter((i) => i.status === s.id);
      let more = 0;
      if (s.category === 'done') {
        // Only the most recently resolved, in board order like the rest.
        items.sort((a, b) => (b.resolved || '').localeCompare(a.resolved || ''));
        more = Math.max(0, items.length - DONE_LIMIT);
        items = items.slice(0, DONE_LIMIT).sort(byRank);
      }
      const list = h('div.clist', items.map((i) => card(i, s, items)));
      const col = h('div.col',
        h('div.head', `${s.name} `, h('span', String(items.length + more))),
        list,
        more ? h('div.muted.small', `${more} older not shown`) : null,
        oldDone.get(s.id)
          ? h('div.muted.small', `${oldDone.get(s.id)} done more than ${DONE_DAYS} days ago not shown: pick a release or Everything to see them.`) : null);
      col.addEventListener('dragover', (e) => {
        if (!dragKey) return;
        e.preventDefault();
        col.classList.add('drop');
        const t = boardTarget(list, e.clientY);
        if (t.el) { if (dropLine.nextSibling !== t.el) list.insertBefore(dropLine, t.el); } else if (list.lastChild !== dropLine) list.append(dropLine);
      });
      col.addEventListener('dragleave', (e) => {
        if (col.contains(e.relatedTarget)) return;
        col.classList.remove('drop');
        dropLine.remove();
      });
      col.addEventListener('drop', async (e) => {
        e.preventDefault();
        const k = dragKey;
        const t = boardTarget(list, e.clientY);
        endDrag();
        if (k) await moveCard(k, s.id, t.rank);
      });
      return col;
    }));
  };

  const relOpts = [['open', 'Unreleased work'], ['all', 'Everything'], ['none', 'No release'],
    ...ctx.releases.map((r) => [String(r.id), r.name + (r.released ? ' (released)' : '')])];
  const epicOpts = [['', 'All epics'], ['none', 'No epic'], ...ctx.epics.map((e) => [String(e.id), e.summary])];
  $app.replaceChildren(...projectHeader(ctx, 'board'),
    h('div.toolbar',
      h('input', { type: 'text', placeholder: 'Filter…', value: st.q, oninput: (e) => { st.q = e.target.value; draw(); } }),
      select(relOpts, st.release, (v) => { st.release = v; draw(); }),
      select(epicOpts, st.epic, (v) => { st.epic = v; draw(); }),
      h('span.muted.small', 'Issues in the backlog are not shown here: send them to the board from the backlog.')),
    board);
  draw();
}
pageBoard.state = {};

function releaseDialog(ctx, rel) {
  const f = Object.assign({ name: '', description: '', startDate: '', releaseDate: '', released: false }, rel || {});
  const dlg = h('dialog',
    h('h2', { style: 'margin-top:0' }, rel ? `Edit release ${rel.name}` : 'New release'),
    h('form', { method: 'dialog', onsubmit: submit },
      h('div.fields',
        h('label', 'Name'), h('input', { type: 'text', value: f.name, oninput: (e) => { f.name = e.target.value; } }),
        h('label', 'Start date'), h('input', { type: 'date', value: f.startDate, onchange: (e) => { f.startDate = e.target.value; } }),
        h('label', 'Release date'), h('input', { type: 'date', value: f.releaseDate, onchange: (e) => { f.releaseDate = e.target.value; } }),
        h('label', 'Released'), h('input', { type: 'checkbox', checked: f.released, onchange: (e) => { f.released = e.target.checked; } }),
        h('label', 'Description'), h('textarea', { style: 'min-height:80px', value: f.description, oninput: (e) => { f.description = e.target.value; } })),
      h('div.actions',
        h('button', { type: 'button', onclick: () => dlg.close() }, 'Cancel'),
        h('button.primary', { type: 'submit' }, 'Save'))));
  async function submit(e) {
    e.preventDefault();
    const body = { name: f.name, description: f.description, startDate: f.startDate, releaseDate: f.releaseDate, released: f.released };
    if (rel) await api('PATCH', `/api/releases/${rel.id}`, body);
    else await api('POST', `/api/projects/${ctx.project.key}/releases`, body);
    dlg.close();
    route();
  }
  dlg.addEventListener('close', () => dlg.remove());
  document.body.append(dlg);
  dlg.showModal();
}

// Each release folds out to its issues. Folding one changes only that
// release's list: redrawing the page would also rebuild the Released
// section around it, closing it again.
async function pageReleases(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const st = pageReleases.state[key] || (pageReleases.state[key] = { open: new Set(), folds: {} });

  const row = (r) => {
    const items = ctx.issues.filter((i) => i.release === r.id && i.type !== 'Epic');
    const cat = (i) => (ctx.project.statuses.find((s) => s.id === i.status) || {}).category;
    const done = items.filter((i) => cat(i) === 'done').length;
    const prog = items.filter((i) => cat(i) === 'inprogress').length;
    const pct = (n) => (items.length ? (100 * n) / items.length : 0) + '%';
    const title = h('b');
    const list = h('div.rel-issues');
    const show = () => {
      const isOpen = st.open.has(r.id);
      title.textContent = (isOpen ? '▾ ' : '▸ ') + r.name;
      list.hidden = !isOpen;
      list.replaceChildren(...(!isOpen ? [] : items.length ? items.map((i) => issueRow(ctx, i, { hideRelease: true })) : [h('div.empty', 'No issues.')]));
    };
    show();
    return h('div.relitem',
      h('div.release',
        h('div',
          h('a', { href: '#', onclick: (e) => { e.preventDefault(); if (st.open.has(r.id)) st.open.delete(r.id); else st.open.add(r.id); show(); } }, title), ' ',
          r.released ? h('span.pill.done', 'released') : h('span.pill', 'unreleased'),
          r.description ? h('div.muted.small', r.description) : null),
        h('div.small.muted',
          r.startDate ? h('div', 'start ' + fmtDate(r.startDate)) : null,
          r.releaseDate ? h('div', 'release ' + fmtDate(r.releaseDate)) : h('div', 'no date')),
        h('div',
          h('div.progress', h('div.d', { style: `width:${pct(done)}` }), h('div.p', { style: `width:${pct(prog)}` })),
          h('div.small.muted', `${done} of ${items.length} done, ${prog} in progress`)),
        h('div',
          h('button.link', { onclick: () => releaseDialog(ctx, r) }, 'Edit'),
          h('button.link', { onclick: async () => { await api('PATCH', `/api/releases/${r.id}`, { released: !r.released }); route(); } },
            r.released ? 'Unrelease' : 'Release'),
          h('button.link.danger', {
            onclick: async () => {
              if (!confirm(`Delete release ${r.name}? Its ${items.length} issues stay, without a release.`)) return;
              await api('DELETE', `/api/releases/${r.id}`);
              route();
            },
          }, 'Delete'))),
      list);
  };

  const unreleased = ctx.releases.filter((r) => !r.released);
  const released = ctx.releases.filter((r) => r.released);
  $app.replaceChildren(...projectHeader(ctx, 'releases'),
    h('div.toolbar', h('button', { onclick: () => releaseDialog(ctx) }, '+ New release')),
    h('div.section', h('div.head', h('b', 'Unreleased')), unreleased.length ? unreleased.map(row) : h('div.empty', 'No unreleased releases.')),
    released.length ? fold(st.folds, 'released', false, h('summary.head', h('b', `Released (${released.length})`)), released.map(row)) : '');
}
pageReleases.state = {};

async function pageSettings(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const p = ctx.project;
  const f = { name: p.name, description: p.description };
  let statuses = p.statuses.map((s) => Object.assign({}, s));
  const used = new Map();
  for (const i of ctx.issues) used.set(i.status, (used.get(i.status) || 0) + 1);

  const box = h('div.statuses');
  const cats = [['todo', 'To do'], ['inprogress', 'In progress'], ['done', 'Done']];
  const drawStatuses = () => {
    box.replaceChildren(...statuses.map((s, k) => h('div.srow',
      h('input', { type: 'text', value: s.name, oninput: (e) => { s.name = e.target.value; } }),
      select(cats, s.category, (v) => { s.category = v; }),
      h('button', { disabled: k === 0, onclick: () => { statuses.splice(k - 1, 0, statuses.splice(k, 1)[0]); drawStatuses(); } }, '↑'),
      h('button', { disabled: k === statuses.length - 1, onclick: () => { statuses.splice(k + 1, 0, statuses.splice(k, 1)[0]); drawStatuses(); } }, '↓'),
      h('button.danger', { disabled: used.get(s.id) > 0, title: used.get(s.id) ? `${used.get(s.id)} issues` : '', onclick: () => { statuses.splice(k, 1); drawStatuses(); } }, 'Remove'),
      h('span.muted.small', s.id ? `${used.get(s.id) || 0} issues` : 'new'))),
    h('div.form', { style: 'margin-top:8px' },
      h('button', { onclick: () => { statuses.push({ id: 0, name: 'New status', category: 'inprogress' }); drawStatuses(); } }, '+ Add status'),
      h('button.primary', {
        onclick: async () => {
          const r = await api('PUT', `/api/projects/${key}/statuses`, statuses);
          statuses = r.statuses.map((s) => Object.assign({}, s));
          drawStatuses();
          toast('Workflow saved');
        },
      }, 'Save workflow')));
  };
  drawStatuses();

  $app.replaceChildren(...projectHeader(ctx, 'settings'),
    h('div.panel',
      h('h2', { style: 'margin-top:0' }, 'Project'),
      h('div.fields', { style: 'max-width:600px' },
        h('label', 'Key'), h('span', p.key),
        h('label', 'Name'), h('input', { type: 'text', value: f.name, oninput: (e) => { f.name = e.target.value; } }),
        h('label', 'Description'), h('textarea', { style: 'min-height:60px', value: f.description, oninput: (e) => { f.description = e.target.value; } }),
        h('span'), h('div', h('button.primary', { onclick: async () => { await api('PATCH', `/api/projects/${key}`, f); route(); } }, 'Save'))),
      h('h2', 'Workflow'),
      h('p.muted.small', 'Statuses are the board columns, left to right. The category decides what counts as done. The first status is where new issues start.'),
      box,
      h('h2', 'Danger zone'),
      h('button.danger', {
        onclick: async () => {
          if (prompt(`This deletes ${p.key} with all ${ctx.issues.length} issues and its releases. Type ${p.key} to confirm.`) !== p.key) return;
          await api('DELETE', `/api/projects/${key}`);
          location.hash = '#/';
        },
      }, 'Delete project')));
}

// issueView builds the issue, for its own page or the issue modal. host
// says how: refresh() shows it again after a change, deleted(projectKey)
// is called after deleting it, and modal is true in the modal, where links
// to other issues open them in the modal too.
async function issueView(key, host) {
  const issue = await api('GET', `/api/issues/${key}`);
  const pkey = key.replace(/-\d+$/, '');
  const [ctx, comments, links] = await Promise.all([
    loadProject(pkey), api('GET', `/api/issues/${key}/comments`), api('GET', `/api/issues/${key}/links`)]);
  const p = ctx.project;
  const save = async (patch) => {
    Object.assign(issue, await api('PATCH', `/api/issues/${key}`, patch));
    host.refresh();
  };
  const issueLink = (k, text) => h('a', {
    href: `#/i/${k}`,
    onclick: (e) => {
      if (!host.modal || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      openIssue(k);
    },
  }, text);

  // Description: rendered, click to edit.
  const descBox = h('div');
  const showDesc = () => {
    descBox.replaceChildren(h('div.desc', { onclick: editDesc, title: 'Click to edit' },
      issue.description ? markdown(issue.description) : h('span.muted', 'Add a description…')));
  };
  const editDesc = () => {
    const ta = h('textarea', { value: issue.description });
    descBox.replaceChildren(ta, h('div.form', { style: 'margin-top:6px' },
      h('button.primary', { onclick: () => save({ description: ta.value }) }, 'Save'),
      h('button', { onclick: showDesc }, 'Cancel'),
      h('span.muted.small', 'Markdown: **bold**, _italic_, `code`, ``` blocks, - lists, # headings, [text](url)')));
    ta.focus();
  };
  showDesc();

  const commentEl = (c) => {
    const box = h('div.comment');
    const view = () => box.replaceChildren(
      h('div.when', fmtDateTime(c.created), c.updated !== c.created ? ' (edited)' : '', ' ',
        h('button.link.small', { onclick: edit }, 'Edit'),
        h('button.link.small.danger', { onclick: async () => { if (confirm('Delete this comment?')) { await api('DELETE', `/api/comments/${c.id}`); host.refresh(); } } }, 'Delete')),
      markdown(c.body));
    const edit = () => {
      const ta = h('textarea', { style: 'min-height:80px', value: c.body });
      box.replaceChildren(ta, h('div.form', { style: 'margin-top:6px' },
        h('button.primary', { onclick: async () => { await api('PATCH', `/api/comments/${c.id}`, { body: ta.value }); host.refresh(); } }, 'Save'),
        h('button', { onclick: view }, 'Cancel')));
    };
    view();
    return box;
  };
  const newComment = h('textarea', { style: 'min-height:70px', placeholder: 'Add a comment…' });

  const linkForm = { type: 'Blocks:0' };
  const linkPicker = issuePicker(issue.key, 'Search for an issue to link…');
  const linkOpts = [];
  for (const lt of META.linkTypes) {
    linkOpts.push([lt.name + ':0', lt.outward]);
    if (lt.inward !== lt.outward) linkOpts.push([lt.name + ':1', lt.inward]);
  }

  const children = issue.type === 'Epic' ? ctx.issues.filter((i) => i.epic === issue.id) : [];
  const epic = issue.epic && ctx.byId.get(issue.epic);
  const relOpts = [[0, '— none —'], ...ctx.releases.filter((r) => !r.released || r.id === issue.release).map((r) => [r.id, r.name])];
  const release = ctx.relById.get(issue.release);

  return h('div',
    h('div.muted.small', { style: 'margin-bottom:6px' },
      h('a', { href: `#/p/${p.key}/backlog` }, p.name), ' / ',
      epic ? [issueLink(epic.key, epic.key), ' / '] : null, issue.key),
    h('div.issue',
      h('div',
        h('input.summary', { type: 'text', value: issue.summary, onchange: (e) => save({ summary: e.target.value }) }),
        h('h2', 'Description'), descBox,
        issue.type === 'Epic' ? [
          h('h2', `Issues in this epic (${children.length})`),
          h('div.section', children.length ? children.map((i) => issueRow(ctx, i)) : h('div.empty', 'None yet.'),
            h('div.quick', h('button.link', { onclick: () => createIssueDialog(ctx, { epic: issue.key, onCreated: () => host.refresh() }) }, '+ Create issue in epic'))),
        ] : null,
        h('h2', 'Links'),
        h('div.links',
          links.length ? links.map((l) => h('div.row',
            h('span.muted.small', { style: 'min-width:110px' }, l.label),
            issueLink(l.key, l.key), h('span.sum', l.summary),
            l.status ? h('span.pill' + (l.done ? '.done' : ''), l.status) : null,
            h('button.link.danger', { title: 'Remove link', onclick: async () => { await api('DELETE', `/api/links/${l.id}`); host.refresh(); } }, '✕')))
            : h('div.muted.small', 'No links.'),
          h('form.form', {
            style: 'margin-top:8px',
            onsubmit: async (e) => {
              e.preventDefault();
              const [type, inward] = linkForm.type.split(':');
              const other = linkPicker.value();
              if (!other) return;
              await api('POST', `/api/issues/${key}/links`, { type, issue: other, inward: inward === '1' });
              host.refresh();
            },
          },
          select(linkOpts, linkForm.type, (v) => { linkForm.type = v; }),
          linkPicker.el,
          h('button', { type: 'submit' }, 'Link'))),
        h('h2', `Comments (${comments.length})`),
        comments.map(commentEl),
        newComment,
        h('div.form', { style: 'margin-top:6px' },
          h('button.primary', { onclick: async () => { if (!newComment.value.trim()) return; await api('POST', `/api/issues/${key}/comments`, { body: newComment.value }); host.refresh(); } }, 'Comment'))),
      h('div.panel',
        h('div.fields',
          h('label', 'Key'), h('b.fval', icon('key'), issue.key),
          h('label', 'Status'), fieldPicker('Change status', () => p.statuses.map((s) => ({
            icon: h('span.sdot.' + s.category), label: s.name, on: s.id === issue.status,
            onclick: () => { if (s.id !== issue.status) save({ status: s.id }); },
          })), statusPill(p, issue.status)),
          h('label', 'Type'), fieldPicker('Change type', () => META.types.map((t) => ({
            icon: typeIcon(t), label: t, on: t === issue.type, onclick: () => { if (t !== issue.type) save({ type: t }); },
          })), typeIcon(issue.type), issue.type),
          h('label', 'Priority'), fieldPicker('Change priority', () => META.priorities.map((x) => ({
            icon: prioIcon(x), label: x, on: x === issue.priority, onclick: () => { if (x !== issue.priority) save({ priority: x }); },
          })), prioIcon(issue.priority), issue.priority),
          h('label', 'Release'), fieldPicker('Change release', () => relOpts.map(([id, name]) => ({
            icon: icon('release'), label: name, on: id === issue.release, onclick: () => { if (id !== issue.release) save({ release: id }); },
          })), icon('release'), release ? h('span.ell', release.name) : h('span.muted', 'None')),
          issue.type !== 'Epic' ? [h('label', 'Board'), fieldPicker('Send to the board or the backlog', () => [
            { icon: icon('board'), label: 'On the board', on: issue.board, onclick: () => { if (!issue.board) save({ board: true }); } },
            { icon: icon('backlog'), label: 'In the backlog', on: !issue.board, onclick: () => { if (issue.board) save({ board: false }); } },
          ], icon(issue.board ? 'board' : 'backlog'), issue.board ? 'On the board' : 'In the backlog')] : null,
          issue.type !== 'Epic' ? [h('label', 'Epic'), fieldPicker('Change epic', () => [
            { icon: icon('epic'), label: '— none —', on: !epic, onclick: () => { if (epic) save({ epic: '' }); } },
            ...ctx.epics.map((e) => ({
              icon: typeIcon('Epic'), label: `${e.key} ${e.summary}`, on: epic === e, onclick: () => { if (epic !== e) save({ epic: e.key }); },
            }))], epic ? [typeIcon('Epic'), h('span.ell', epic.summary)] : [icon('epic'), h('span.muted', 'None')])] : null,
          h('label.muted', 'Created'), h('span.small.fval', icon('created'), fmtDateTime(issue.created)),
          h('label.muted', 'Updated'), h('span.small.fval', icon('updated'), fmtDateTime(issue.updated)),
          issue.resolved ? [h('label.muted', 'Resolved'), h('span.small.fval', icon('resolved'), fmtDateTime(issue.resolved))] : null),
        h('div', { style: 'margin-top:16px' },
          h('button.danger', {
            onclick: async () => {
              if (!confirm(`Delete ${issue.key}? This also deletes its comments and links.`)) return;
              await api('DELETE', `/api/issues/${key}`);
              host.deleted(p.key);
            },
          }, 'Delete issue')))));
}

async function pageIssue(key) {
  const host = { modal: false, refresh: route, deleted: (pkey) => { location.hash = `#/p/${pkey}/backlog`; } };
  const [view, projects] = await Promise.all([issueView(key, host), api('GET', '/api/projects')]);
  topNav(projects);
  $app.replaceChildren(view);
}

// ---------- issue modal ----------

// Issues opened from the backlog, board, releases or the search show in a
// modal over the page, so the page stays where it was. The modal shows one
// issue at a time; opening another (a link in it, a search result) swaps it.
const modal = { dlg: null, key: null, dirty: false };

function openIssue(key) {
  // On an issue's own page, issues open as pages.
  if (!modal.dlg && /^#\/i\//.test(location.hash)) {
    location.hash = `#/i/${key}`;
    return;
  }
  closeMenu();
  hideSearch();
  if (!modal.dlg) {
    const dlg = h('dialog.issue-modal');
    // Escape while typing only leaves the field, so an edit isn't lost.
    // (On keydown: Chrome may not let a cancel event be prevented.)
    dlg.addEventListener('keydown', (e) => {
      const a = e.target;
      if (e.key === 'Escape' && (a.tagName === 'TEXTAREA' || a.tagName === 'INPUT')) {
        e.preventDefault();
        a.blur();
      }
    });
    // A click on the backdrop (outside the content) closes it.
    let downOutside = false;
    dlg.addEventListener('mousedown', (e) => { downOutside = e.target === dlg; });
    dlg.addEventListener('click', (e) => { if (downOutside && e.target === dlg) dlg.close(); });
    dlg.addEventListener('close', () => {
      dlg.remove();
      modal.dlg = null;
      // Show changes made in the modal on the page below.
      if (modal.dirty) { modal.dirty = false; route(); }
    });
    document.body.append(dlg);
    dlg.showModal();
    modal.dlg = dlg;
  }
  modal.key = key;
  // The skeleton has the layout of the issue, so the modal doesn't jump
  // when it arrives.
  modal.dlg.replaceChildren(modalSkeleton());
  modal.dlg.scrollTop = 0;
  renderModal(true);
}

function modalSkeleton() {
  const bar = (w, cls) => h('div.skel' + (cls || ''), { style: `width:${w}` });
  return h('div.modal-inner', { 'aria-busy': 'true', 'aria-label': 'Loading…' },
    h('div.modal-bar', bar('90px'), h('span.spacer'), h('button.icon', { title: 'Close (Esc)', onclick: () => modal.dlg && modal.dlg.close() }, '✕')),
    bar('160px', '.crumb'),
    h('div.issue',
      h('div',
        bar('70%', '.title'),
        bar('110px', '.head'),
        bar('100%'), bar('96%'), bar('88%'), bar('60%'),
        bar('70px', '.head'),
        bar('50%'),
        bar('110px', '.head'),
        bar('100%', '.box')),
      h('div.panel',
        h('div.fields', ['Key', 'Status', 'Type', 'Priority', 'Release', 'Board', 'Epic', 'Created', 'Updated'].map((l, k) =>
          [h('label' + (k > 6 ? '.muted' : ''), l), bar(['60px', '90px', '80px', '90px', '70px', '120px', '140px', '150px', '150px'][k])])))));
}

function closeIssueModal() {
  modal.dirty = false;
  if (modal.dlg) modal.dlg.close();
}

async function renderModal(top) {
  const { dlg, key } = modal;
  const host = {
    modal: true,
    refresh: () => { modal.dirty = true; renderModal(false); },
    deleted: () => { modal.dirty = true; dlg.close(); },
  };
  let view;
  try {
    view = await issueView(key, host);
  } catch (err) {
    if (err.message === 'unauthorized') return;
    view = h('p', 'Error: ' + err.message);
  }
  // Closed or swapped for another issue meanwhile.
  if (modal.dlg !== dlg || modal.key !== key) return;
  const scroll = top ? 0 : dlg.scrollTop;
  dlg.replaceChildren(h('div.modal-inner',
    h('div.modal-bar',
      h('a', { href: `#/i/${key}`, target: '_blank', rel: 'noopener', title: 'Open on its own page, in a new tab' }, 'Open as page ↗'),
      h('span.spacer'),
      h('button.icon', { title: 'Close (Esc)', onclick: () => dlg.close() }, '✕')),
    view));
  dlg.scrollTop = scroll;
}

async function pageImport() {
  topNav(await api('GET', '/api/projects'));
  const input = h('input', { type: 'file', accept: '.xml,text/xml', multiple: true });
  const result = h('div');
  $app.replaceChildren(
    h('h1', 'Import from Jira'),
    h('div.panel',
      h('p', 'In Jira, search the issues (e.g. ', h('code', 'project = KEY ORDER BY created DESC'), '), then Export → ', h('b', 'Export XML'), '. ',
        'Jira exports at most 1000 issues per file: for more, export page by page and select all files here at once, so links and epics between files are kept.'),
      h('p.muted.small', 'Issues are matched by key: importing again updates them instead of adding copies. Projects, releases (fix versions) and missing statuses are created as needed. The backlog order is taken from Jira\'s rank. Kept: type, summary, description, status, priority, fix version, epic, comments, links, dates.'),
      h('form.form', {
        onsubmit: async (e) => {
          e.preventDefault();
          if (!input.files.length) return;
          const fd = new FormData();
          for (const f of input.files) fd.append('file', f);
          result.replaceChildren(h('p.muted', 'Importing…'));
          let r;
          try { r = await api('POST', '/api/import', fd); } catch (err) { result.replaceChildren(h('p', 'Failed: ' + err.message)); return; }
          result.replaceChildren(h('div.panel', { style: 'margin-top:14px' },
            h('h2', { style: 'margin-top:0' }, 'Done'),
            h('p', `${r.files} file(s): ${r.issuesCreated} issues created, ${r.issuesUpdated} updated, ${r.comments} comments, ${r.links} links.`),
            r.projectsCreated.length ? h('p', 'Projects created: ', r.projectsCreated.map((k) => h('a', { href: `#/p/${k}/backlog`, style: 'margin-right:8px' }, k))) : null,
            r.releasesCreated.length ? h('p', 'Releases created: ' + r.releasesCreated.join(', ')) : null,
            r.statusesAdded.length ? h('p', 'Statuses added: ' + r.statusesAdded.join(', ')) : null,
            r.warnings.length ? [h('h2', `Warnings (${r.warnings.length})`), h('ul.warnings', r.warnings.map((w) => h('li', w)))] : null));
          topNav(await api('GET', '/api/projects'));
        },
      }, input, h('button.primary', { type: 'submit' }, 'Import'))),
    result);
}

// ---------- router ----------

async function route() {
  closeIssueModal();
  closeMenu();
  try {
    // Inside the try: a malformed escape in the hash throws a URIError.
    const parts = location.hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
    if (!META) META = await api('GET', '/api/meta');
    if (parts[0] === 'p' && parts[1]) {
      const page = { backlog: pageBacklog, board: pageBoard, releases: pageReleases, settings: pageSettings }[parts[2] || 'backlog'];
      await (page || pageBacklog)(parts[1]);
    } else if (parts[0] === 'i' && parts[1]) {
      await pageIssue(parts[1]);
    } else if (parts[0] === 'import') {
      await pageImport();
    } else {
      await pageProjects();
    }
  } catch (err) {
    if (err.message !== 'unauthorized') {
      $app.replaceChildren(h('p', 'Error: ' + err.message, ' ', h('a', { href: '#/' }, 'Back to projects')));
    }
  }
}

window.addEventListener('hashchange', route);
route();
