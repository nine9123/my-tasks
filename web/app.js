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
      h('span.spacer'), h('button.primary', { onclick: () => createIssueDialog(ctx) }, 'Create issue')),
  ];
}

function issueRow(ctx, issue, extra, hideRelease) {
  return h('div.row', Object.assign({ onclick: () => { location.hash = `#/i/${issue.key}`; } }, extra || {}),
    typeIcon(issue.type),
    h('span.key', { class: isDone(ctx.project, issue) ? 'done' : '' }, issue.key),
    h('span.sum', issue.summary),
    epicChip(ctx, issue),
    !hideRelease && issue.release && ctx.relById.get(issue.release) ? h('span.rel', ctx.relById.get(issue.release).name) : null,
    statusPill(ctx.project, issue.status),
    prioIcon(issue.priority));
}

// ---------- create issue ----------

function createIssueDialog(ctx, preset) {
  preset = preset || {};
  const f = {
    type: preset.type || 'Task', summary: '', description: '', priority: 'Medium',
    release: preset.release || 0, epic: preset.epic || '',
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
        h('label', 'Description'), h('textarea', { oninput: (e) => { f.description = e.target.value; } })),
      h('div.actions',
        h('button', { type: 'button', onclick: () => dlg.close() }, 'Cancel'),
        h('button.primary', { type: 'submit' }, 'Create'))));
  async function submit(e) {
    e.preventDefault();
    const body = { type: f.type, summary: f.summary, description: f.description, priority: f.priority, release: f.release };
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
    h('a', { href: '#/import' }, 'Import from Jira'),
    h('a', { href: '#', onclick: async (e) => { e.preventDefault(); await api('POST', '/api/logout'); showLogin(); } }, 'Sign out'));
}

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

// Drag and drop in the backlog: rows reorder, sections set the release.
let dragKey = null;

async function pageBacklog(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const st = pageBacklog.state[key] || (pageBacklog.state[key] = { q: '', type: '', epic: '', showDone: false });
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
    for (const i of visible) {
      const g = groups.find((x) => x.release.id === i.release);
      if (g) g.items.push(i); else if (!i.release || !st.showDone) none.items.push(i);
    }
    groups.push(none);

    const epicsBox = h('details.section',
      h('summary.head', h('b', `Epics (${ctx.epics.length})`)),
      ctx.epics.map((e) => {
        const kids = ctx.issues.filter((i) => i.epic === e.id);
        const done = kids.filter((i) => isDone(ctx.project, i)).length;
        return h('div.row', { onclick: () => { location.hash = `#/i/${e.key}`; } },
          typeIcon('Epic'), h('span.key', e.key), h('span.sum', e.summary),
          h('span.muted.small', `${done}/${kids.length} done`), statusPill(ctx.project, e.status));
      }),
      h('div.quick', h('button.link', { onclick: () => createIssueDialog(ctx, { type: 'Epic' }) }, '+ Create epic')));

    body.replaceChildren(epicsBox, ...groups.map((g) => section(g)));
  };

  // Index of a release's section in body (the epics box comes first).
  const groupIndex = (relId) => {
    const shown = ctx.releases.filter((r) => st.showDone || !r.released);
    const k = shown.findIndex((r) => r.id === relId);
    return 1 + (k < 0 ? shown.length : k);
  };

  const section = (g) => {
    const rel = g.release;
    const relId = rel ? rel.id : 0;
    const quick = h('input', { type: 'text', placeholder: '+ What needs to be done? (Enter)' });
    const quickType = select(META.types.filter((t) => t !== 'Epic').map((t) => [t, t]), 'Task', () => {});
    quick.addEventListener('keydown', async (e) => {
      if (e.key !== 'Enter' || !quick.value.trim()) return;
      const data = { summary: quick.value, type: quickType.value, release: relId };
      if (st.epic) data.epic = ctx.byId.get(+st.epic).key;
      const issue = await api('POST', `/api/projects/${key}/issues`, data);
      ctx.issues.push(issue);
      ctx.byId.set(issue.id, issue);
      draw();
      // draw() rebuilt the section: keep typing in the new input.
      const again = body.querySelectorAll('.section')[groupIndex(relId)];
      if (again) again.querySelector('.quick input').focus();
    });
    const sec = h('div.section',
      h('div.head',
        h('b', rel ? rel.name : 'Backlog'),
        h('span.muted.small', `${g.items.length} issues`),
        rel && rel.releaseDate ? h('span.muted.small', 'release ' + fmtDate(rel.releaseDate)) : null,
        rel && rel.released ? h('span.pill.done', 'released') : null),
      g.items.length ? null : h('div.empty', 'Nothing here. Drag issues in, or add one below.'),
      g.items.map((i) => {
        const row = issueRow(ctx, i, { draggable: 'true' }, !!rel);
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
          await moveIssue(dragKey, relId, after ? { after: i.key } : { before: i.key });
        });
        return row;
      }),
      h('div.quick', quickType, quick));
    sec.addEventListener('dragover', (e) => { if (dragKey) { e.preventDefault(); sec.classList.add('drop'); } });
    sec.addEventListener('dragleave', (e) => { if (!sec.contains(e.relatedTarget)) sec.classList.remove('drop'); });
    sec.addEventListener('drop', async (e) => {
      e.preventDefault();
      sec.classList.remove('drop');
      const last = g.items[g.items.length - 1];
      await moveIssue(dragKey, relId, last && last.key !== dragKey ? { after: last.key } : null);
    });
    return sec;
  };

  async function moveIssue(k, relId, rank) {
    const issue = ctx.issues.find((i) => i.key === k);
    dragKey = null;
    if (!issue) return;
    // Open issues of a released (hidden) release are drawn under Backlog:
    // reordering them there must not clear their release.
    const drawnIn = ctx.releases.some((r) => r.id === issue.release && (st.showDone || !r.released)) ? issue.release : 0;
    if (drawnIn !== relId) Object.assign(issue, await api('PATCH', `/api/issues/${k}`, { release: relId }));
    if (rank) {
      const r = await api('POST', `/api/issues/${k}/rank`, rank);
      issue.rank = r.rank;
      // Respacing may have moved others too: reload when that happened.
      ctx.issues = await api('GET', `/api/projects/${key}/issues`);
      ctx.byId = new Map(ctx.issues.map((i) => [i.id, i]));
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

async function pageBoard(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const st = pageBoard.state[key] || (pageBoard.state[key] = { release: 'open', epic: '', q: '' });
  const board = h('div.board');
  const DONE_LIMIT = 30;

  const draw = () => {
    const q = st.q.toLowerCase();
    const rel = (i) => ctx.relById.get(i.release);
    const visible = ctx.issues.filter((i) => {
      if (i.type === 'Epic') return false;
      if (st.epic && String(i.epic) !== st.epic) return false;
      if (q && !(i.key + ' ' + i.summary).toLowerCase().includes(q)) return false;
      if (st.release === 'open') return rel(i) ? !rel(i).released : !isDone(ctx.project, i);
      if (st.release === 'all') return true;
      if (st.release === 'none') return !i.release;
      return String(i.release) === st.release;
    });
    board.replaceChildren(...ctx.project.statuses.map((s) => {
      let items = visible.filter((i) => i.status === s.id);
      let more = 0;
      if (s.category === 'done') {
        items.sort((a, b) => (b.resolved || '').localeCompare(a.resolved || ''));
        more = Math.max(0, items.length - DONE_LIMIT);
        items = items.slice(0, DONE_LIMIT);
      }
      const col = h('div.col',
        h('div.head', `${s.name} `, h('span', String(items.length + more))),
        items.map((i) => {
          const card = h('div.card', { draggable: 'true', onclick: () => { location.hash = `#/i/${i.key}`; } },
            h('div', i.summary),
            h('div.meta', typeIcon(i.type), prioIcon(i.priority), epicChip(ctx, i),
              rel(i) ? h('span.rel', rel(i).name) : null, h('span.key', i.key)));
          card.addEventListener('dragstart', (e) => { dragKey = i.key; e.dataTransfer.setData('text/plain', i.key); });
          return card;
        }),
        more ? h('div.muted.small', `${more} older not shown`) : null);
      col.addEventListener('dragover', (e) => { if (dragKey) { e.preventDefault(); col.classList.add('drop'); } });
      col.addEventListener('dragleave', (e) => { if (!col.contains(e.relatedTarget)) col.classList.remove('drop'); });
      col.addEventListener('drop', async (e) => {
        e.preventDefault();
        col.classList.remove('drop');
        const issue = ctx.issues.find((i) => i.key === dragKey);
        dragKey = null;
        if (!issue || issue.status === s.id) return;
        Object.assign(issue, await api('PATCH', `/api/issues/${issue.key}`, { status: s.id }));
        draw();
      });
      return col;
    }));
  };

  const relOpts = [['open', 'Unreleased work'], ['all', 'Everything'], ['none', 'No release'],
    ...ctx.releases.map((r) => [String(r.id), r.name + (r.released ? ' (released)' : '')])];
  const epicOpts = [['', 'All epics'], ...ctx.epics.map((e) => [String(e.id), e.summary])];
  $app.replaceChildren(...projectHeader(ctx, 'board'),
    h('div.toolbar',
      h('input', { type: 'text', placeholder: 'Filter…', value: st.q, oninput: (e) => { st.q = e.target.value; draw(); } }),
      select(relOpts, st.release, (v) => { st.release = v; draw(); }),
      select(epicOpts, st.epic, (v) => { st.epic = v; draw(); })),
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

async function pageReleases(key) {
  const ctx = await loadProject(key);
  topNav(await api('GET', '/api/projects'));
  const open = new Set(pageReleases.open[key] || (pageReleases.open[key] = new Set()));

  const row = (r) => {
    const items = ctx.issues.filter((i) => i.release === r.id && i.type !== 'Epic');
    const cat = (i) => (ctx.project.statuses.find((s) => s.id === i.status) || {}).category;
    const done = items.filter((i) => cat(i) === 'done').length;
    const prog = items.filter((i) => cat(i) === 'inprogress').length;
    const pct = (n) => (items.length ? (100 * n) / items.length : 0) + '%';
    const isOpen = open.has(r.id);
    return [
      h('div.release',
        h('div',
          h('a', { href: '#', onclick: (e) => { e.preventDefault(); if (isOpen) open.delete(r.id); else open.add(r.id); pageReleases.open[key] = open; draw(); } },
            h('b', (isOpen ? '▾ ' : '▸ ') + r.name)), ' ',
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
      isOpen ? h('div', { style: 'padding: 0 0 8px 20px' }, items.length ? items.map((i) => issueRow(ctx, i, null, true)) : h('div.empty', 'No issues.')) : null,
    ];
  };

  const list = h('div');
  const draw = () => {
    const unreleased = ctx.releases.filter((r) => !r.released);
    const released = ctx.releases.filter((r) => r.released);
    list.replaceChildren(
      h('div.section', h('div.head', h('b', 'Unreleased')), unreleased.length ? unreleased.map(row) : h('div.empty', 'No unreleased releases.')),
      released.length ? h('details.section', h('summary.head', h('b', `Released (${released.length})`)), released.map(row)) : '');
  };
  $app.replaceChildren(...projectHeader(ctx, 'releases'),
    h('div.toolbar', h('button', { onclick: () => releaseDialog(ctx) }, '+ New release')),
    list);
  draw();
}
pageReleases.open = {};

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

async function pageIssue(key) {
  const issue = await api('GET', `/api/issues/${key}`);
  const pkey = key.replace(/-\d+$/, '');
  const [ctx, comments, links, projects] = await Promise.all([
    loadProject(pkey), api('GET', `/api/issues/${key}/comments`), api('GET', `/api/issues/${key}/links`), api('GET', '/api/projects')]);
  topNav(projects);
  const p = ctx.project;
  const save = async (patch) => {
    Object.assign(issue, await api('PATCH', `/api/issues/${key}`, patch));
    route();
  };

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
        h('button.link.small.danger', { onclick: async () => { if (confirm('Delete this comment?')) { await api('DELETE', `/api/comments/${c.id}`); route(); } } }, 'Delete')),
      markdown(c.body));
    const edit = () => {
      const ta = h('textarea', { style: 'min-height:80px', value: c.body });
      box.replaceChildren(ta, h('div.form', { style: 'margin-top:6px' },
        h('button.primary', { onclick: async () => { await api('PATCH', `/api/comments/${c.id}`, { body: ta.value }); route(); } }, 'Save'),
        h('button', { onclick: view }, 'Cancel')));
    };
    view();
    return box;
  };
  const newComment = h('textarea', { style: 'min-height:70px', placeholder: 'Add a comment…' });

  const linkForm = { type: 'Blocks:0', issue: '' };
  const linkOpts = [];
  for (const lt of META.linkTypes) {
    linkOpts.push([lt.name + ':0', lt.outward]);
    if (lt.inward !== lt.outward) linkOpts.push([lt.name + ':1', lt.inward]);
  }

  const children = issue.type === 'Epic' ? ctx.issues.filter((i) => i.epic === issue.id) : [];
  const epic = issue.epic && ctx.byId.get(issue.epic);
  const relOpts = [[0, '— none —'], ...ctx.releases.filter((r) => !r.released || r.id === issue.release).map((r) => [r.id, r.name])];
  const epicOpts = [['', '— none —'], ...ctx.epics.map((e) => [e.key, `${e.key} ${e.summary}`])];

  $app.replaceChildren(
    h('div.muted.small', { style: 'margin-bottom:6px' },
      h('a', { href: `#/p/${p.key}/backlog` }, p.name), ' / ',
      epic ? [h('a', { href: `#/i/${epic.key}` }, epic.key), ' / '] : null, issue.key),
    h('div.issue',
      h('div',
        h('input.summary', { type: 'text', value: issue.summary, onchange: (e) => save({ summary: e.target.value }) }),
        h('h2', 'Description'), descBox,
        issue.type === 'Epic' ? [
          h('h2', `Issues in this epic (${children.length})`),
          h('div.section', children.length ? children.map((i) => issueRow(ctx, i)) : h('div.empty', 'None yet.'),
            h('div.quick', h('button.link', { onclick: () => createIssueDialog(ctx, { epic: issue.key, onCreated: () => route() }) }, '+ Create issue in epic'))),
        ] : null,
        h('h2', 'Links'),
        h('div.links',
          links.length ? links.map((l) => h('div.row',
            h('span.muted.small', { style: 'min-width:110px' }, l.label),
            h('a', { href: `#/i/${l.key}` }, l.key), h('span.sum', l.summary),
            l.status ? h('span.pill' + (l.done ? '.done' : ''), l.status) : null,
            h('button.link.danger', { title: 'Remove link', onclick: async () => { await api('DELETE', `/api/links/${l.id}`); route(); } }, '✕')))
            : h('div.muted.small', 'No links.'),
          h('form.form', {
            style: 'margin-top:8px',
            onsubmit: async (e) => {
              e.preventDefault();
              const [type, inward] = linkForm.type.split(':');
              await api('POST', `/api/issues/${key}/links`, { type, issue: linkForm.issue, inward: inward === '1' });
              route();
            },
          },
          select(linkOpts, linkForm.type, (v) => { linkForm.type = v; }),
          h('input', { type: 'text', placeholder: `${p.key}-123`, size: 10, oninput: (e) => { linkForm.issue = e.target.value; } }),
          h('button', { type: 'submit' }, 'Link'))),
        h('h2', `Comments (${comments.length})`),
        comments.map(commentEl),
        newComment,
        h('div.form', { style: 'margin-top:6px' },
          h('button.primary', { onclick: async () => { if (!newComment.value.trim()) return; await api('POST', `/api/issues/${key}/comments`, { body: newComment.value }); route(); } }, 'Comment'))),
      h('div.panel',
        h('div.fields',
          h('label', 'Key'), h('b', issue.key),
          h('label', 'Status'), select(p.statuses.map((s) => [s.id, s.name]), issue.status, (v) => save({ status: +v })),
          h('label', 'Type'), select(META.types.map((t) => [t, t]), issue.type, (v) => save({ type: v })),
          h('label', 'Priority'), select(META.priorities.map((x) => [x, x]), issue.priority, (v) => save({ priority: v })),
          h('label', 'Release'), select(relOpts, issue.release, (v) => save({ release: +v })),
          issue.type !== 'Epic' ? [h('label', 'Epic'), select(epicOpts, epic ? epic.key : '', (v) => save({ epic: v }))] : null,
          h('label.muted', 'Created'), h('span.small', fmtDateTime(issue.created)),
          h('label.muted', 'Updated'), h('span.small', fmtDateTime(issue.updated)),
          issue.resolved ? [h('label.muted', 'Resolved'), h('span.small', fmtDateTime(issue.resolved))] : null),
        h('div', { style: 'margin-top:16px' },
          h('button.danger', {
            onclick: async () => {
              if (!confirm(`Delete ${issue.key}? This also deletes its comments and links.`)) return;
              await api('DELETE', `/api/issues/${key}`);
              location.hash = `#/p/${p.key}/backlog`;
            },
          }, 'Delete issue')))));
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
