// godl web interface. Plain script, no framework and no build step:
// everything it does goes through the daemon's /api routes, and live
// updates arrive over one Server-Sent Events feed.
'use strict';

const $ = (id) => document.getElementById(id);
const el = (tag, props = {}, ...kids) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') e.className = v;
    else if (k === 'text') e.textContent = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null && v !== false) e.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids) if (kid != null) e.append(kid);
  return e;
};

// ---------- multi-select ----------
//
// Every list with checkboxes works the same way: tick a box or tap
// the row, and shift-click to tick (or untick) everything between the
// last row touched and this one. The pointer's shift state is noted on
// pointerdown, because a click forwarded from a <label> to its checkbox
// doesn't reliably carry modifier keys.

let shiftHeld = false;
document.addEventListener('pointerdown', (e) => { shiftHeld = e.shiftKey; }, true);
document.addEventListener('keydown', (e) => { if (e.key === ' ' || e.key === 'Enter') shiftHeld = e.shiftKey; }, true);

// selectRange applies on to every key between anchor and key in order.
// It returns false when there's no usable anchor, so the caller treats
// the click as a single toggle.
function selectRange(order, anchor, key, on, set) {
  const a = order.indexOf(anchor), b = order.indexOf(key);
  if (a < 0 || b < 0) return false;
  for (let i = Math.min(a, b); i <= Math.max(a, b); i++) set(order[i], on);
  return true;
}

// rowTap turns a tap anywhere on a row into a tick, except on the row's
// own controls (buttons, links, inputs, the details panel).
function rowTap(e) {
  return !e.target.closest('button, a, input, select, label, .details');
}

// ---------- formatting ----------

function bytes(n) {
  if (!(n > 0)) return '0 B';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + ' ' + u[i];
}
const speed = (n) => (n > 0 ? bytes(n) + '/s' : '');
function clock(s) {
  if (!(s >= 0) || !isFinite(s)) return '0:00';
  s = Math.floor(s);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  const mm = h ? String(m).padStart(2, '0') : String(m);
  return (h ? h + ':' : '') + mm + ':' + String(sec).padStart(2, '0');
}
const eta = (s) => (s >= 0 ? clock(s) : '');
const base = (p) => (p || '').replace(/[\\/]+$/, '').split(/[\\/]/).pop();
const langName = (code) => {
  if (!code || code === 'und') return '';
  try { return new Intl.DisplayNames([navigator.language || 'en'], { type: 'language' }).of(code) || code; } catch { return code; }
};

// ---------- API ----------

async function api(method, path, body, raw) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    if (raw) { opts.body = body; } else { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json'; }
  }
  let resp;
  try {
    resp = await fetch(path, opts);
  } catch {
    throw new Error("Can't reach godl. Is its background daemon still running?");
  }
  if (resp.status === 401) {
    banner('This page needs signing in again. Run "godl web" in a terminal to open a fresh link.', true);
    throw new Error('Not signed in.');
  }
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || resp.statusText);
  return data;
}

function banner(text, isErr) {
  const b = $('banner');
  b.textContent = text || '';
  b.classList.toggle('err', !!isErr);
  b.hidden = !text;
}

function say(id, text, kind) {
  const m = $(id);
  m.textContent = text || '';
  m.classList.toggle('err', kind === 'err');
  m.classList.toggle('ok', kind === 'ok');
}

// ---------- state ----------

const state = {
  info: null,        // /api/state
  jobs: new Map(),   // id -> job, in feed order
  order: [],
  selected: new Set(),
  rows: new Map(),   // id -> row elements
};

// ---------- views ----------

const views = ['downloads', 'new', 'remote', 'share', 'settings'];
function showView() {
  let v = location.hash.replace('#', '');
  if (!views.includes(v)) v = 'downloads';
  for (const name of views) $('view-' + name).hidden = name !== v;
  document.querySelectorAll('.tabs a').forEach((a) => a.classList.toggle('on', a.dataset.view === v));
  if (v === 'settings') fillSettings();
  if (v === 'remote') renderConnections();
}
window.addEventListener('hashchange', showView);

// ---------- live feed ----------

let feed = null;
function connectFeed() {
  if (feed) return;
  feed = new EventSource('api/events');
  feed.onmessage = (e) => {
    banner('');
    const data = JSON.parse(e.data);
    applyJobs(data.jobs || []);
    applyShare(data.share);
  };
  feed.onerror = () => banner('Lost the connection to godl — reconnecting…', true);
}
function disconnectFeed() {
  if (feed) { feed.close(); feed = null; }
}
// A hidden tab doesn't need updates; closing the feed means the daemon
// stops producing them (it only works while someone is watching).
document.addEventListener('visibilitychange', () => (document.hidden ? disconnectFeed() : connectFeed()));

// ---------- downloads ----------

const ACTIVE = new Set(['active', 'queued', 'seeding']);

function jobName(j) {
  if (j.type === 'webdav') return base(j.source.split(':').slice(1).join(':')) || j.source;
  const out = base(j.output);
  if (j.type === 'url' && out) return out;
  if (j.type === 'torrent') {
    const m = /[?&]dn=([^&]+)/.exec(j.source);
    if (m) { try { return decodeURIComponent(m[1].replace(/\+/g, ' ')); } catch { /* fall through */ } }
    if (!j.source.startsWith('magnet:')) return base(j.source).replace(/\.torrent$/, '');
  }
  return j.source;
}

function playable(j) {
  return j.status === 'completed' || j.status === 'seeding' || (j.type === 'torrent' && j.status === 'active' && j.total > 0);
}

function applyJobs(list) {
  const seen = new Set();
  state.order = list.map((j) => j.id);
  for (const j of list) { state.jobs.set(j.id, j); seen.add(j.id); }
  for (const id of [...state.jobs.keys()]) {
    if (!seen.has(id)) { state.jobs.delete(id); state.selected.delete(id); }
  }
  renderJobs();
  renderSummary();
}

function renderSummary() {
  let active = 0, down = 0, up = 0;
  for (const j of state.jobs.values()) {
    if (ACTIVE.has(j.status)) active++;
    down += j.speed || 0;
    up += j.upload || 0;
  }
  const parts = [state.jobs.size + ' download' + (state.jobs.size === 1 ? '' : 's')];
  if (active) parts.push(active + ' running');
  if (down) parts.push('↓ ' + speed(down));
  if (up) parts.push('↑ ' + speed(up));
  $('summary').textContent = parts.join(' · ');
}

// ---------- sorting ----------
//
// Remembered per browser (a convenience, so it's fine if storage is
// unavailable). Each order starts in the direction usually wanted —
// biggest, fastest, furthest along first; the arrow button flips it.

const prefs = (() => { try { return JSON.parse(localStorage.getItem('godl-sort') || '{}'); } catch { return {}; } })();
function savePrefs() { try { localStorage.setItem('godl-sort', JSON.stringify(prefs)); } catch { /* not kept */ } }
const sortState = { jobs: prefs.jobs || 'newest', jobsRev: !!prefs.jobsRev, entries: prefs.entries || 'name', entriesRev: !!prefs.entriesRev };

const STATUS_RANK = { active: 0, queued: 1, seeding: 2, paused: 3, failed: 4, canceled: 5, completed: 6 };
const fraction = (j) => (j.total > 0 ? j.done / j.total : (j.status === 'completed' ? 1 : 0));
const byText = (a, b) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' });
const JOB_ORDER = {
  newest: (a, b) => b.created - a.created,
  name: (a, b) => byText(jobName(a), jobName(b)),
  status: (a, b) => STATUS_RANK[a.status] - STATUS_RANK[b.status],
  progress: (a, b) => fraction(b) - fraction(a),
  size: (a, b) => b.total - a.total,
  speed: (a, b) => (b.speed || 0) - (a.speed || 0),
  // No estimate sorts after every known one.
  eta: (a, b) => ((a.eta < 0) - (b.eta < 0)) || (a.eta - b.eta),
};

function sortJobs(list) {
  const cmp = JOB_ORDER[sortState.jobs] || JOB_ORDER.newest;
  const pos = new Map(state.order.map((id, i) => [id, i]));
  // Ties keep newest first, so equal rows don't swap places on every update.
  return list.slice().sort((a, b) => (sortState.jobsRev ? cmp(b, a) : cmp(a, b)) || (pos.get(b.id) - pos.get(a.id)));
}

function showSort() {
  $('job-sort').value = sortState.jobs;
  $('job-sort-dir').textContent = sortState.jobsRev ? '↑' : '↓';
  $('job-sort-dir').title = sortState.jobsRev ? 'Reversed — click for the usual order' : 'Click to reverse';
  document.querySelectorAll('.th-sort').forEach((b) => {
    const on = b.dataset.sort === sortState.jobs;
    b.classList.toggle('on', on);
    b.dataset.dir = on ? (sortState.jobsRev ? '↑' : '↓') : '';
  });
  $('browse-sort').value = sortState.entries;
  $('browse-sort-dir').textContent = sortState.entriesRev ? '↑' : '↓';
}

function setJobSort(by, rev) {
  sortState.jobs = by;
  sortState.jobsRev = rev;
  Object.assign(prefs, { jobs: by, jobsRev: rev });
  savePrefs();
  showSort();
  renderJobs();
}

$('job-sort').addEventListener('change', (e) => setJobSort(e.target.value, false));
$('job-sort-dir').addEventListener('click', () => setJobSort(sortState.jobs, !sortState.jobsRev));
document.querySelectorAll('.th-sort').forEach((b) => b.addEventListener('click', () => {
  // A second click on the same column reverses it.
  setJobSort(b.dataset.sort, b.dataset.sort === sortState.jobs ? !sortState.jobsRev : false);
}));

function filtered() {
  const q = $('job-filter').value.trim().toLowerCase();
  const st = $('job-state').value;
  return sortJobs(state.order.map((id) => state.jobs.get(id)).filter((j) => {
    if (st && j.status !== st) return false;
    if (!q) return true;
    return (jobName(j) + ' ' + j.source + ' ' + j.output).toLowerCase().includes(q);
  }));
}

function makeRow(j) {
  const check = el('input', { type: 'checkbox', 'aria-label': 'Select' });
  check.addEventListener('click', () => selectJob(j.id, check.checked));
  const name = el('b'), src = el('small'), err = el('small', { class: 'j-err' });
  const chip = el('span', { class: 'chip' });
  const fill = el('i'), bar = el('div', { class: 'bar' }, fill), pct = el('small');
  const spd = el('td', { class: 'num' }), left = el('td', { class: 'num' });
  const more = el('button', { type: 'button', class: 'linkish', text: 'Details', 'aria-expanded': 'false' });
  const play = el('button', { type: 'button', text: 'Play' });
  play.addEventListener('click', () => openPlayer({ kind: 'job', job: j.id }, jobName(state.jobs.get(j.id)), j.id));
  const toggle = el('button', { type: 'button' });
  toggle.addEventListener('click', () => {
    const cur = state.jobs.get(j.id);
    const act = ACTIVE.has(cur.status) ? (cur.status === 'seeding' ? 'cancel' : 'pause') : (cur.status === 'completed' ? null : (cur.status === 'paused' ? 'resume' : 'retry'));
    if (act) jobAction([j.id], act);
  });
  const tr = el('tr', {},
    el('td', { class: 'c-check' }, check),
    el('td', { class: 'j-name' }, name, src, err, more),
    el('td', {}, chip),
    el('td', { class: 'prog' }, bar, pct),
    spd, left,
    el('td', { class: 'j-acts' }, play, ' ', toggle));
  const detailBody = el('div', { class: 'details' });
  const detail = el('tr', { class: 'detail' }, el('td', { colspan: '7' }, detailBody));
  tr.addEventListener('click', (e) => { if (rowTap(e)) selectJob(j.id, !state.selected.has(j.id)); });
  const row = { tr, check, name, src, err, chip, bar, fill, pct, spd, left, play, toggle, more, detail, detailBody, open: false, last: {} };
  more.addEventListener('click', () => {
    row.open = !row.open;
    more.setAttribute('aria-expanded', String(row.open));
    more.textContent = row.open ? 'Hide details' : 'Details';
    if (row.open) { renderDetails(row, j.id); loadDetails(row, j.id); }
    renderJobs();
  });
  return row;
}

// ---------- job details ----------
//
// A download's full source and destination, and for one made of many
// files (a torrent, a WebDAV folder) every file with its own progress.
// Fetched only while a row is open, refreshed every couple of seconds.

async function loadDetails(row, id) {
  try {
    row.files = await api('GET', 'api/jobs/' + encodeURIComponent(id) + '/details');
  } catch (err) {
    row.files = { files: [], note: err.message };
  }
  if (row.open) renderDetails(row, id);
}

function renderDetails(row, id) {
  const j = state.jobs.get(id);
  if (!j) return;
  if (row.choosing) { renderChooser(row, id); return; }
  const info = el('dl', { class: 'kv' },
    el('dt', { text: 'Source' }), el('dd', { text: j.source }),
    el('dt', { text: 'Saved to' }), el('dd', { text: j.output || '—' }),
    el('dt', { text: 'Started' }), el('dd', { text: new Date(j.created * 1000).toLocaleString() }));
  const kids = [info];
  const data = row.files;
  if (!data) {
    kids.push(el('p', { class: 'hint', text: 'Loading the file list…' }));
  } else {
    const files = data.files || [];
    const done = files.filter((f) => f.length > 0 && f.done >= f.length).length;
    const skipped = files.filter((f) => f.skipped).length;
    const total = files.reduce((n, f) => n + (f.skipped ? 0 : Math.max(0, f.length)), 0);
    const parts = [files.length + ' file' + (files.length === 1 ? '' : 's')];
    if (total) parts.push(bytes(total) + (skipped ? ' selected' : ''));
    if (files.length > 1) parts.push(done + ' finished');
    if (skipped) parts.push(skipped + ' skipped');
    const summary = el('p', { class: 'fsum' }, parts.join(' · '));
    // A running torrent's list is its whole list: files are ticked on and
    // off right in it, as it downloads. Otherwise the choice is made from
    // the torrent's metadata in a separate list.
    const live = j.type === 'torrent' && data.live && files.length > 0;
    if (!live) row.pick = null;
    const pick = live ? (row.pick = row.pick || new Map()) : null;
    const wants = (f) => (pick.has(f.index) ? pick.get(f.index) : !f.skipped);
    const setWant = (f, v) => { if (v === !f.skipped) pick.delete(f.index); else pick.set(f.index, v); };
    if (live) files.forEach((f) => { if (pick.has(f.index)) setWant(f, pick.get(f.index)); }); // drop changes that already took
    if (live) {
      summary.append(' ',
        el('button', { type: 'button', class: 'linkish', text: 'Tick all', onclick: () => { files.forEach((f) => setWant(f, true)); renderDetails(row, id); } }), ' ',
        el('button', { type: 'button', class: 'linkish', text: 'Untick all', onclick: () => { files.forEach((f) => setWant(f, false)); renderDetails(row, id); } }));
    } else if (j.type === 'torrent') {
      summary.append(' ', el('button', { type: 'button', class: 'linkish', text: 'Choose files…', onclick: () => startChoosing(row, id) }));
    }
    kids.push(summary);
    if (data.note) kids.push(el('p', { class: 'hint', text: data.note }));
    if (live && pick.size) kids.push(pickBar(row, id, files, wants));
    if (files.length) {
      kids.push(el('div', { class: 'ftable' }, el('table', {},
        el('thead', {}, el('tr', {}, live ? el('th', { class: 'c-check' }) : null, el('th', { text: 'File' }), el('th', { text: 'Progress' }), el('th', { class: 'num', text: 'Size' }))),
        el('tbody', {}, ...files.map((f, i) => {
          let tick = null;
          if (live) {
            tick = el('input', { type: 'checkbox', checked: wants(f), 'aria-label': 'Download ' + f.path });
            tick.addEventListener('click', () => {
              if (shiftHeld && row.pickAnchor != null) selectRange(files.map((_, k) => k), row.pickAnchor, i, tick.checked, (k, v) => setWant(files[k], v));
              setWant(f, tick.checked);
              row.pickAnchor = i;
              shiftHeld = false;
              renderDetails(row, id);
            });
          }
          f.done = f.done || 0; // left out of the JSON when nothing has arrived yet
          const known = f.length > 0;
          const p = known ? Math.min(100, (f.done / f.length) * 100) : 0;
          const fill = el('i');
          fill.style.width = p.toFixed(1) + '%';
          const fs = f.skipped ? 'skipped' : (known && f.done >= f.length ? 'done' : (f.done > 0 ? 'part' : 'none'));
          return el('tr', { class: 'f-' + fs + (live && pick.has(f.index) ? ' f-changed' : '') },
            live ? el('td', { class: 'c-check' }, tick) : null,
            el('td', { class: 'fname', text: f.path }),
            el('td', { class: 'fprog' }, f.skipped ? el('span', { class: 'hint', text: 'skipped' })
              : !known ? el('span', { class: 'hint', text: 'so far' })
              : el('div', { class: 'fbar' }, el('div', { class: 'bar st-' + (fs === 'done' ? 'completed' : 'active') }, fill), el('small', { text: known ? p.toFixed(p < 10 && p > 0 ? 1 : 0) + '%' : '' }))),
            el('td', { class: 'num', text: known ? (fs === 'done' || f.skipped ? bytes(f.length) : bytes(f.done) + ' / ' + bytes(f.length)) : bytes(f.done) }));
        })))));
    }
  }
  // Keep the file list where it was scrolled to across the refreshes.
  const old = row.detailBody.querySelector('.ftable');
  const top = old ? old.scrollTop : 0;
  row.detailBody.replaceChildren(...kids);
  const now = row.detailBody.querySelector('.ftable');
  if (now && top) now.scrollTop = top;
}

// pickBar holds the changes ticked in a running torrent's file list
// until they're applied, so a run of clicks becomes one switch.
function pickBar(row, id, files, wants) {
  const on = files.filter(wants);
  const size = on.reduce((n, f) => n + Math.max(0, f.length), 0);
  const msg = el('span', { class: 'msg' });
  const apply = el('button', { type: 'button', class: 'primary', text: 'Apply', disabled: on.length === 0 });
  apply.addEventListener('click', async () => {
    apply.disabled = true;
    say2(msg, 'Applying…');
    const want = [];
    for (const f of files) want[f.index] = wants(f);
    try {
      await api('POST', 'api/jobs/' + encodeURIComponent(id) + '/select', { torrent_files: selectionSpec(Array.from(want, Boolean)) });
      row.pick = null;
      loadDetails(row, id);
    } catch (err) { say2(msg, err.message, 'err'); apply.disabled = false; }
  });
  const n = row.pick.size;
  return el('div', { class: 'pickbar' },
    el('span', { text: n + ' change' + (n === 1 ? '' : 's') + ' · will download ' + on.length + ' of ' + files.length + ' files (' + bytes(size) + ')' }),
    on.length === 0 ? el('span', { class: 'msg err', text: 'Tick at least one file.' }) : null,
    el('span', { class: 'grow' }),
    apply,
    el('button', { type: 'button', text: 'Undo', onclick: () => { row.pick = null; renderDetails(row, id); } }),
    msg);
}

// ---------- choosing a torrent's files after it was added ----------

async function startChoosing(row, id) {
  row.choosing = { loading: true, files: [], err: '' };
  renderDetails(row, id);
  try {
    const r = await api('GET', 'api/jobs/' + encodeURIComponent(id) + '/torrent-choice');
    if (!row.choosing) return;
    row.choosing = { loading: false, files: r.files.map((f) => ({ ...f, on: !f.skipped })), err: '' };
  } catch (err) {
    if (!row.choosing) return;
    row.choosing = { loading: false, files: [], err: err.message };
  }
  renderDetails(row, id);
}

function renderChooser(row, id) {
  const c = row.choosing;
  const close = () => { row.choosing = null; renderDetails(row, id); loadDetails(row, id); };
  if (c.loading) {
    const j = state.jobs.get(id);
    const slow = j && j.status !== 'active' && j.source.startsWith('magnet:');
    row.detailBody.replaceChildren(el('p', { class: 'hint', text: slow ? 'Getting the file list from peers (can take up to a minute)…' : 'Loading the file list…' }),
      el('button', { type: 'button', text: 'Cancel', onclick: close }));
    return;
  }
  if (c.err) {
    row.detailBody.replaceChildren(el('p', { class: 'msg err', text: 'Couldn\u2019t list the files: ' + c.err }),
      el('button', { type: 'button', text: 'Back', onclick: close }));
    return;
  }
  const count = el('strong');
  const apply = el('button', { type: 'button', class: 'primary' });
  const msg = el('span', { class: 'msg' });
  const refresh = () => {
    const on = c.files.filter((f) => f.on);
    const size = on.reduce((n, f) => n + f.length, 0);
    count.textContent = on.length + ' of ' + c.files.length + ' files selected · ' + bytes(size);
    apply.textContent = 'Download ' + on.length + ' file' + (on.length === 1 ? '' : 's');
    apply.disabled = on.length === 0;
  };
  let anchor = null;
  const boxes = c.files.map((f, i) => {
    const box = el('input', { type: 'checkbox', checked: f.on });
    box.addEventListener('click', () => {
      if (shiftHeld && anchor !== null) selectRange(c.files.map((_, k) => k), anchor, i, box.checked, (k, v) => { c.files[k].on = v; boxes[k].checked = v; });
      f.on = box.checked;
      anchor = i;
      shiftHeld = false;
      refresh();
    });
    return box;
  });
  const setAll = (v) => { c.files.forEach((f, k) => { f.on = v; boxes[k].checked = v; }); refresh(); };
  apply.addEventListener('click', async () => {
    apply.disabled = true;
    say2(msg, 'Applying…');
    try {
      await api('POST', 'api/jobs/' + encodeURIComponent(id) + '/select', { torrent_files: selectionSpec(c.files.map((f) => f.on)) });
      close();
    } catch (err) { say2(msg, err.message, 'err'); apply.disabled = false; }
  });
  const list = el('ul', {}, ...c.files.map((f, i) => el('li', {}, el('label', {}, boxes[i],
    el('span', { text: f.path }),
    el('em', { text: f.done > 0 && f.done < f.length ? bytes(f.done) + ' / ' + bytes(f.length) : bytes(f.length) })))));
  const j = state.jobs.get(id);
  const note = j && (j.status === 'completed' || j.status === 'seeding')
    ? 'This torrent has finished: applying starts it again to fetch any files you add. Files already on disk aren\u2019t downloaded twice.'
    : 'Unticked files stop downloading; what\u2019s already on disk of them is kept.';
  row.detailBody.replaceChildren(el('div', { class: 'filepick' },
    el('div', { class: 'row' }, count, el('span', { class: 'grow' }),
      el('button', { type: 'button', text: 'All', onclick: () => setAll(true) }),
      el('button', { type: 'button', text: 'None', onclick: () => setAll(false) })),
    list,
    el('p', { class: 'hint', text: note }),
    el('div', { class: 'row' }, apply, el('button', { type: 'button', text: 'Cancel', onclick: close }), msg)));
  refresh();
}

function say2(node, text, kind) {
  node.textContent = text || '';
  node.classList.toggle('err', kind === 'err');
}

// selectionSpec writes a choice as --files: runs of files as ranges
// ("1-40,43"), or "" for every file.
function selectionSpec(on) {
  if (on.every(Boolean)) return '';
  const parts = [];
  for (let i = 0; i < on.length; i++) {
    if (!on[i]) continue;
    let k = i;
    while (k + 1 < on.length && on[k + 1]) k++;
    parts.push(i === k ? String(i + 1) : (i + 1) + '-' + (k + 1));
    i = k;
  }
  return parts.join(',');
}

setInterval(() => {
  if (document.hidden) return;
  for (const [id, row] of state.rows) {
    if (!row.open || row.choosing) continue;
    const j = state.jobs.get(id);
    // A finished job's files don't change; no need to keep asking.
    if (j && row.files && (j.status === 'completed' || j.status === 'failed' || j.status === 'canceled') && row.filesFinal === j.status) continue;
    if (j) row.filesFinal = j.status;
    loadDetails(row, id);
  }
}, 2000);

// set changes a text or class only when it differs, so a 500 ms feed
// over a long list touches almost nothing in the page.
function set(row, key, value, apply) {
  if (row.last[key] === value) return;
  row.last[key] = value;
  apply(value);
}

function updateRow(row, j) {
  set(row, 'name', jobName(j), (v) => (row.name.textContent = v));
  set(row, 'src', j.type + ' · ' + j.source, (v) => { row.src.textContent = v; row.src.title = j.source; });
  set(row, 'err', j.status === 'failed' ? j.error || '' : '', (v) => { row.err.textContent = v; row.err.hidden = !v; });
  set(row, 'status', j.status, (v) => {
    row.chip.textContent = v;
    row.chip.className = 'chip st-' + v;
    row.bar.className = 'bar st-' + v;
  });
  const known = j.total > 0;
  const p = known ? Math.min(100, (j.done / j.total) * 100) : 0;
  set(row, 'unknown', !known && j.status === 'active', (v) => row.bar.classList.toggle('unknown', v));
  set(row, 'pct', known ? p.toFixed(1) : '', (v) => (row.fill.style.width = (v || 0) + '%'));
  let detail = known ? bytes(j.done) + ' of ' + bytes(j.total) + ' · ' + p.toFixed(1) + '%' : (j.done ? bytes(j.done) : '');
  if (j.status === 'seeding') detail = 'ratio ' + (j.ratio || 0).toFixed(2);
  set(row, 'detail', detail, (v) => (row.pct.textContent = v));
  set(row, 'speed', j.status === 'seeding' ? '↑ ' + speed(j.upload) : speed(j.speed), (v) => (row.spd.textContent = v));
  set(row, 'eta', j.status === 'active' ? eta(j.eta) : '', (v) => (row.left.textContent = v));
  set(row, 'play', playable(j), (v) => (row.play.hidden = !v));
  const label = { active: 'Pause', queued: 'Pause', seeding: 'Stop seeding', paused: 'Resume', failed: 'Retry', canceled: 'Retry' }[j.status] || '';
  set(row, 'toggle', label, (v) => { row.toggle.textContent = v; row.toggle.hidden = !v; });
  const sel = state.selected.has(j.id);
  set(row, 'sel', sel, (v) => { row.check.checked = v; row.tr.classList.toggle('sel', v); });
}

function renderJobs() {
  const body = $('job-rows');
  const list = filtered();
  const keep = new Set(list.map((j) => j.id));
  for (const [id, row] of state.rows) {
    if (!state.jobs.has(id)) { row.tr.remove(); row.detail.remove(); state.rows.delete(id); }
    else if (!keep.has(id)) { row.tr.remove(); row.detail.remove(); }
  }
  let prev = null;
  for (const j of list) {
    let row = state.rows.get(j.id);
    if (!row) { row = makeRow(j); state.rows.set(j.id, row); }
    updateRow(row, j);
    const want = prev ? prev.nextSibling : body.firstChild;
    if (want !== row.tr) body.insertBefore(row.tr, want);
    prev = row.tr;
    if (row.open) {
      if (row.tr.nextSibling !== row.detail) body.insertBefore(row.detail, row.tr.nextSibling);
      prev = row.detail;
    } else if (row.detail.parentNode) {
      row.detail.remove();
    }
  }
  $('jobs-empty').hidden = state.jobs.size > 0;
  $('jobs-nomatch').hidden = state.jobs.size === 0 || list.length > 0;
  renderSelection();
}

let jobAnchor = null;

function selectJob(id, on) {
  const set = (k, v) => (v ? state.selected.add(k) : state.selected.delete(k));
  if (!(shiftHeld && selectRange(filtered().map((j) => j.id), jobAnchor, id, on, set))) set(id, on);
  jobAnchor = id;
  shiftHeld = false;
  renderSelection();
}

function setAllJobs(on) {
  for (const j of filtered()) on ? state.selected.add(j.id) : state.selected.delete(j.id);
  if (!on) state.selected.clear();
  renderSelection();
}

function renderSelection() {
  const n = state.selected.size;
  const visible = filtered();
  const allOn = visible.length > 0 && visible.every((j) => state.selected.has(j.id));
  $('sel-count').textContent = n + ' selected';
  $('selbar').hidden = n === 0;
  if (n === 0) $('confirm').hidden = true;
  document.body.classList.toggle('has-selbar', n > 0);
  $('sel-all').hidden = allOn;
  $('sel-all').textContent = 'Select all ' + visible.length;
  $('sel-start').hidden = n > 0 || visible.length < 2;
  $('check-all').checked = allOn;
  $('check-all').indeterminate = n > 0 && !allOn;
  for (const [id, row] of state.rows) {
    const s = state.selected.has(id);
    set(row, 'sel', s, (v) => { row.check.checked = v; row.tr.classList.toggle('sel', v); });
  }
}

$('job-filter').addEventListener('input', renderJobs);
$('job-state').addEventListener('change', renderJobs);
$('check-all').addEventListener('change', (e) => setAllJobs(e.target.checked));
$('sel-start').addEventListener('click', () => setAllJobs(true));
$('sel-all').addEventListener('click', () => setAllJobs(true));
$('sel-clear').addEventListener('click', () => setAllJobs(false));

let pendingConfirm = null;
function confirmThen(text, fn) {
  $('confirm-text').textContent = text;
  $('confirm').hidden = false;
  pendingConfirm = fn;
  $('confirm-no').focus();
}
$('confirm-yes').addEventListener('click', () => { $('confirm').hidden = true; const f = pendingConfirm; pendingConfirm = null; if (f) f(); });
$('confirm-no').addEventListener('click', () => { $('confirm').hidden = true; pendingConfirm = null; });

async function jobAction(ids, action, purge) {
  try {
    const r = await api('POST', 'api/jobs/action', { ids, action, purge: !!purge });
    if (r.failed && r.failed.length) banner(r.failed.length + ' could not be changed: ' + r.failed[0].error, true);
    // Done with that selection, as the dashboard is after a bulk action.
    if (ids.length > 1 || action === 'remove') ids.forEach((id) => state.selected.delete(id));
    renderSelection();
  } catch (e) { banner(e.message, true); }
}

$('bulk').addEventListener('click', (e) => {
  const act = e.target.dataset && e.target.dataset.action;
  if (!act) return;
  const ids = [...state.selected];
  const n = ids.length + ' download' + (ids.length === 1 ? '' : 's');
  if (act === 'remove') confirmThen('Remove ' + n + ' from the list? The files stay on disk.', () => jobAction(ids, 'remove'));
  else if (act === 'purge') confirmThen('Remove ' + n + ' and delete the downloaded files?', () => jobAction(ids, 'remove', true));
  else jobAction(ids, act);
});

// ---------- new download ----------

let kind = 'url';
const kindText = {
  url: { hint: 'Any direct http(s) address. Several links, one per line, each become their own download.', links: 'Links, one per line', output: 'Save to (a file for one link, a folder for several)', submit: 'Start download' },
  social: { hint: 'YouTube and the many other sites yt-dlp supports.', links: 'Links, one per line', output: 'Save to folder', submit: 'Start download' },
  torrent: { hint: 'A magnet link or the path of a .torrent file on the godl machine — or upload one.', links: 'Magnet links or .torrent paths, one per line', output: 'Save to folder', submit: 'Start download' },
  watch: { hint: 'Plays the video here in the page without saving it.', links: 'Link to watch', output: '', submit: 'Watch' },
};

function setKind(k) {
  kind = k;
  document.querySelectorAll('#new-kind button').forEach((b) => b.setAttribute('aria-selected', String(b.dataset.kind === k)));
  const t = kindText[k];
  $('new-hint').textContent = t.hint;
  $('links-label').textContent = t.links;
  $('output-label').textContent = t.output;
  $('new-submit').textContent = t.submit;
  $('new-submit').disabled = false;
  if (k === 'torrent') loadTorrentFiles(false);
  $('new-links').rows = k === 'watch' ? 1 : 4;
  $('f-quality').hidden = !(k === 'social' || k === 'watch');
  $('f-watch-cookies').hidden = k !== 'watch';
  $('torrent-extra').hidden = k !== 'torrent';
  $('dl-fields').hidden = k === 'watch';
  document.querySelectorAll('#adv > div').forEach((d) => {
    d.hidden = !d.className.split(' ').includes('kind-' + k);
  });
  say('new-msg', '');
}
document.querySelectorAll('#new-kind button').forEach((b) => b.addEventListener('click', () => setKind(b.dataset.kind)));

const lines = (text) => text.split('\n').map((s) => s.trim()).filter((s) => s && !s.startsWith('#'));

// The torrent file picker. As soon as one torrent is entered (pasted,
// typed, or uploaded) its file list is fetched and shown, every file
// ticked; the user can untick some, clear them all, or tick them all
// back. `picked` is the list on show and the source it belongs to.
let picked = null;
let pickSeq = 0; // the latest request; older answers are ignored

const isTorrentSource = (s) => s.startsWith('magnet:') || /\.torrent$/i.test(s);

async function loadTorrentFiles(force) {
  const srcs = lines($('new-links').value);
  if (srcs.length !== 1 || !isTorrentSource(srcs[0])) {
    picked = null;
    $('torrent-pick').hidden = true;
    say('torrent-status', srcs.length > 1 ? 'Several torrents: each downloads every file. Add them one at a time to choose files.' : '');
    return;
  }
  const source = srcs[0];
  if (!force && picked && picked.source === source) return;
  const seq = ++pickSeq;
  picked = null;
  $('torrent-pick').hidden = true;
  say('torrent-status', source.startsWith('magnet:') ? 'Getting the file list from peers (can take up to a minute)…' : 'Reading the torrent…');
  try {
    const r = await api('POST', 'api/torrent/files', { source });
    if (seq !== pickSeq || lines($('new-links').value)[0] !== source) return;
    picked = { source, files: r.files, name: r.name };
    $('torrent-files').replaceChildren(...r.files.map((f) => el('li', {}, el('label', {},
      el('input', { type: 'checkbox', checked: true, value: String(f.index + 1), 'data-size': String(f.length) }),
      el('span', { text: f.path }), el('em', { text: bytes(f.length) })))));
    $('torrent-pick').hidden = r.files.length < 2;
    say('torrent-status', r.files.length < 2 ? r.name + ' is a single file.' : '');
    updatePickSummary();
  } catch (err) {
    if (seq === pickSeq) say('torrent-status', 'Couldn\u2019t list the files (' + err.message + '). Starting downloads every file.', 'err');
  }
}

function updatePickSummary() {
  if (!picked) return;
  const boxes = [...$('torrent-files').querySelectorAll('input')];
  const on = boxes.filter((c) => c.checked);
  const size = on.reduce((n, c) => n + +c.dataset.size, 0);
  $('torrent-name').textContent = picked.name + ' — ' + on.length + ' of ' + boxes.length + ' files selected · ' + bytes(size);
  $('new-submit').textContent = on.length === boxes.length ? 'Start download' : 'Start download (' + on.length + ' file' + (on.length === 1 ? '' : 's') + ')';
  $('new-submit').disabled = on.length === 0;
}

$('torrent-file').addEventListener('change', async (e) => {
  const f = e.target.files[0];
  if (!f) return;
  say('torrent-status', 'Uploading…');
  try {
    const r = await api('POST', 'api/torrent/upload?name=' + encodeURIComponent(f.name), f, true);
    const cur = lines($('new-links').value);
    cur.push(r.path);
    $('new-links').value = cur.join('\n');
    say('torrent-status', 'Added ' + f.name + '.', 'ok');
    loadTorrentFiles(false);
  } catch (err) { say('torrent-status', err.message, 'err'); }
  e.target.value = '';
});

$('torrent-list').addEventListener('click', () => loadTorrentFiles(true));
let fileAnchor = null;
$('torrent-files').addEventListener('click', (e) => {
  const box = e.target.closest('input[type=checkbox]');
  if (!box) return;
  const boxes = [...$('torrent-files').querySelectorAll('input')];
  if (shiftHeld && fileAnchor) selectRange(boxes, fileAnchor, box, box.checked, (b, v) => (b.checked = v));
  fileAnchor = box;
  shiftHeld = false;
  updatePickSummary();
});
$('torrent-all').addEventListener('click', () => { $('torrent-files').querySelectorAll('input').forEach((c) => (c.checked = true)); updatePickSummary(); });
$('torrent-none').addEventListener('click', () => { $('torrent-files').querySelectorAll('input').forEach((c) => (c.checked = false)); updatePickSummary(); });

let pickTimer = null;
$('new-links').addEventListener('input', () => {
  if (kind !== 'torrent') return;
  // Wait for typing or pasting to settle before asking.
  clearTimeout(pickTimer);
  pickTimer = setTimeout(() => loadTorrentFiles(false), 600);
});

// torrentSelection is the --files value for the choice on show: runs of
// files as ranges ("1-40,43"), or "" for every file.
function torrentSelection() {
  if (!picked || $('torrent-pick').hidden) return '';
  const boxes = [...$('torrent-files').querySelectorAll('input')];
  const on = boxes.map((c) => c.checked);
  if (on.every(Boolean)) return '';
  if (!on.some(Boolean)) throw new Error('Pick at least one file (All ticks them all).');
  return selectionSpec(on);
}

$('new-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const links = lines($('new-links').value);
  if (!links.length) { say('new-msg', 'Paste a link first.', 'err'); return; }
  if (kind === 'watch') {
    const quality = { '1080p': 1080, '720p': 720, '480p': 480, worst: 360 }[$('new-preset').value] || 0;
    const cookies = $('watch-cookies').value.trim();
    Object.assign(prefs, { watchCookies: cookies });
    savePrefs();
    openPlayer({ kind: 'link', link: links[0], quality, cookies }, links[0]);
    return;
  }
  let body;
  try {
    body = {
      type: kind, links,
      output: $('new-output').value.trim(), rate: $('new-rate').value.trim(),
    };
    if (kind === 'url') Object.assign(body, { concurrency: +$('new-conc').value || 4, sha256: $('new-sha').value.trim() });
    if (kind === 'social') Object.assign(body, { preset: $('new-preset').value, format: $('new-format').value.trim(), cookies_from_browser: $('new-browser').value.trim() });
    if (kind === 'url' || kind === 'social') Object.assign(body, { headers: lines($('new-headers').value), cookie: $('new-cookie').value.trim(), cookies_file: $('new-cookies-file').value.trim() });
    if (kind === 'torrent') Object.assign(body, { torrent_files: torrentSelection(), seed_ratio: +$('new-seed-ratio').value || 0, seed_time: $('new-seed-time').value.trim() });
  } catch (err) { say('new-msg', err.message, 'err'); return; }

  $('new-submit').disabled = true;
  say('new-msg', 'Starting…');
  try {
    const r = await api('POST', 'api/jobs', body);
    const ok = r.started.length, bad = r.failed.length;
    if (bad) say('new-msg', (ok ? ok + ' started; ' : '') + bad + ' failed: ' + r.failed[0].error, 'err');
    else {
      say('new-msg', ok === 1 ? 'Started.' : ok + ' downloads started.', 'ok');
      $('new-links').value = '';
      picked = null;
      $('torrent-pick').hidden = true;
      say('torrent-status', '');
    }
  } catch (err) { say('new-msg', err.message, 'err'); }
  $('new-submit').disabled = false;
  if (kind === 'torrent') updatePickSummary();
});

// ---------- remote storage ----------

const browse = { conn: null, path: '/', entries: [], sel: new Set() };

function renderConnections() {
  const conns = (state.info && state.info.connections) || [];
  $('conn-empty').hidden = conns.length > 0;
  $('conn-list').replaceChildren(...conns.map((c) => el('li', {},
    el('b', { text: c.name }), el('small', { text: c.url + (c.username ? ' · ' + c.username : '') }),
    el('button', { type: 'button', class: 'primary', text: 'Browse', onclick: () => openBrowser(c.name, '/') }),
    el('button', { type: 'button', class: 'danger', text: 'Remove', onclick: async (e) => {
      if (e.target.dataset.armed !== '1') { e.target.dataset.armed = '1'; e.target.textContent = 'Remove — sure?'; return; }
      try { state.info.connections = await api('DELETE', 'api/connections/' + encodeURIComponent(c.name)); renderConnections(); } catch (err) { banner(err.message, true); }
    } }))));
}

$('conn-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    state.info.connections = await api('POST', 'api/connections', {
      name: $('conn-name').value.trim(), url: $('conn-url').value.trim(), username: $('conn-user').value.trim(),
      password: $('conn-pass').value, insecure: $('conn-insecure').checked,
    });
    say('conn-msg', 'Saved.', 'ok');
    $('conn-form').reset();
    $('conn-add').open = false;
    renderConnections();
  } catch (err) { say('conn-msg', err.message, 'err'); }
});

async function openBrowser(conn, path) {
  browse.conn = conn;
  browse.sel.clear();
  $('conn-list-wrap').hidden = true;
  $('browser').hidden = false;
  $('browse-filter').value = '';
  say('browse-msg', 'Loading…');
  $('entries').replaceChildren();
  try {
    const r = await api('GET', 'api/webdav/list?conn=' + encodeURIComponent(conn) + '&path=' + encodeURIComponent(path));
    browse.path = r.path;
    browse.entries = r.entries;
    say('browse-msg', '');
    renderBrowser();
  } catch (err) { say('browse-msg', err.message, 'err'); }
}

function renderBrowser() {
  const parts = browse.path.split('/').filter(Boolean);
  const crumbs = [el('button', { type: 'button', text: browse.conn + ':/', onclick: () => openBrowser(browse.conn, '/') })];
  parts.forEach((p, i) => crumbs.push(el('button', { type: 'button', text: p + '/', onclick: () => openBrowser(browse.conn, '/' + parts.slice(0, i + 1).join('/') + '/') })));
  $('crumbs').replaceChildren(...crumbs);
  const q = $('browse-filter').value.trim().toLowerCase();
  const list = sortEntries(browse.entries.filter((e) => !q || e.name.toLowerCase().includes(q)));
  $('entries').replaceChildren(...list.map((e) => {
    const box = el('input', { type: 'checkbox', 'aria-label': 'Select ' + e.name, checked: browse.sel.has(e.path) });
    box.addEventListener('click', () => selectEntry(e.path, box.checked));
    const name = e.dir
      ? el('span', { class: 'e-name' }, el('button', { type: 'button', text: e.name + '/', onclick: () => openBrowser(browse.conn, e.path) }))
      : el('span', { class: 'e-name', text: e.name });
    return el('li', { class: browse.sel.has(e.path) ? 'sel' : '', onclick: (ev) => { if (rowTap(ev)) selectEntry(e.path, !browse.sel.has(e.path)); } }, box, name,
      el('em', { class: 'e-date', text: e.modified ? new Date(e.modified * 1000).toLocaleDateString() : '' }),
      e.dir ? null : el('em', { text: e.size >= 0 ? bytes(e.size) : '' }),
      e.dir ? null : el('button', { type: 'button', text: 'Play', onclick: () => openPlayer({ kind: 'webdav', conn: browse.conn, path: e.path }, e.name) }));
  }));
  if (!list.length) $('entries').append(el('li', { class: 'empty', text: q ? 'Nothing here matches.' : 'This folder is empty.' }));
  const n = browse.sel.size;
  const allOn = list.length > 0 && list.every((e) => browse.sel.has(e.path));
  $('browse-all').checked = allOn;
  $('browse-all').indeterminate = n > 0 && !allOn;
  $('browse-count').textContent = n ? n + ' selected' : '';
  $('browse-clear').hidden = n === 0;
  $('browse-dl-sel').textContent = n ? 'Download selected (' + n + ')' : 'Download selected';
  $('browse-dl-sel').disabled = n === 0;
}

let entryAnchor = null;

function visibleEntries() {
  const q = $('browse-filter').value.trim().toLowerCase();
  return sortEntries(browse.entries.filter((e) => !q || e.name.toLowerCase().includes(q)));
}

function selectEntry(path, on) {
  const set = (k, v) => (v ? browse.sel.add(k) : browse.sel.delete(k));
  if (!(shiftHeld && selectRange(visibleEntries().map((e) => e.path), entryAnchor, path, on, set))) set(path, on);
  entryAnchor = path;
  shiftHeld = false;
  renderBrowser();
}

$('browse-all').addEventListener('change', (e) => {
  for (const en of visibleEntries()) e.target.checked ? browse.sel.add(en.path) : browse.sel.delete(en.path);
  renderBrowser();
});
$('browse-clear').addEventListener('click', () => { browse.sel.clear(); renderBrowser(); });

// Folders always come before files, whatever the order.
const ENTRY_ORDER = {
  name: () => 0,
  size: (a, b) => b.size - a.size,
  modified: (a, b) => (b.modified || 0) - (a.modified || 0),
};
function sortEntries(list) {
  const cmp = ENTRY_ORDER[sortState.entries] || ENTRY_ORDER.name;
  return list.slice().sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    const r = cmp(a, b) || byText(a.name, b.name);
    return sortState.entriesRev ? -r : r;
  });
}
function setEntrySort(by, rev) {
  sortState.entries = by;
  sortState.entriesRev = rev;
  Object.assign(prefs, { entries: by, entriesRev: rev });
  savePrefs();
  showSort();
  renderBrowser();
}
$('browse-sort').addEventListener('change', (e) => setEntrySort(e.target.value, false));
$('browse-sort-dir').addEventListener('click', () => setEntrySort(sortState.entries, !sortState.entriesRev));

$('browse-filter').addEventListener('input', renderBrowser);
$('browse-back').addEventListener('click', () => { $('browser').hidden = true; $('conn-list-wrap').hidden = false; });

async function webdavDownload(paths) {
  if (!paths.length) { say('browse-msg', 'Tick some files or folders first.', 'err'); return; }
  try {
    const r = await api('POST', 'api/webdav/download', { conn: browse.conn, paths, output: $('browse-output').value.trim() });
    if (r.failed.length) say('browse-msg', r.failed.length + ' failed: ' + r.failed[0].error, 'err');
    else say('browse-msg', r.started.length + ' download' + (r.started.length === 1 ? '' : 's') + ' started, into ' + r.output + '.', 'ok');
    browse.sel.clear();
    renderBrowser();
  } catch (err) { say('browse-msg', err.message, 'err'); }
}
$('browse-dl-sel').addEventListener('click', () => webdavDownload([...browse.sel]));
$('browse-dl-all').addEventListener('click', () => webdavDownload([browse.path]));

// ---------- share a folder ----------

function applyShare(s) {
  $('share-running').hidden = !s;
  $('share-form').hidden = !!s;
  if (!s) return;
  $('share-root').textContent = s.root;
  $('share-urls').replaceChildren(...s.urls.map((u) => el('li', {}, el('a', { href: u, target: '_blank', rel: 'noopener', text: u }))));
  const notes = [];
  notes.push(s.allow_write ? 'Uploads and deletes are allowed.' : 'Read-only.');
  notes.push(s.username ? 'Sign in as “' + s.username + '”.' : 'No password.');
  if (s.tls) notes.push('Browsers will warn about the self-signed certificate.');
  $('share-note').textContent = notes.join(' ');
}

$('share-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    await api('POST', 'api/share', {
      dir: $('share-dir').value.trim(), port: +$('share-port').value || 8080,
      host: $('share-local').checked ? '127.0.0.1' : '0.0.0.0',
      username: $('share-user').value.trim(), password: $('share-pass').value,
      allow_write: $('share-write').checked, self_signed: $('share-tls').checked, insecure_no_auth: $('share-noauth').checked,
    });
    say('share-msg', '');
    $('share-pass').value = '';
  } catch (err) { say('share-msg', err.message, 'err'); }
});
$('share-stop').addEventListener('click', async () => {
  try { await api('DELETE', 'api/share'); } catch (err) { banner(err.message, true); }
});

// ---------- settings ----------

function fillSettings() {
  const s = state.info && state.info.settings;
  if (!s) return;
  $('s-max').value = s.max_concurrent;
  $('s-default-rate').value = s.default_rate_limit;
  $('s-global-rate').value = s.global_rate_limit;
  $('s-retry').checked = s.auto_retry;
  $('s-retry-max').value = s.auto_retry_max_attempts;
  $('s-notify').checked = s.notify_on_complete;
  $('s-web').checked = s.webui;
  $('s-web-port').value = s.webui_port;
  $('s-web-net').checked = s.webui_network;
  $('s-web-user').value = s.webui_username;
  $('s-web-pass').value = '';
  $('s-web-pass-state').textContent = s.webui_password_set ? '(set)' : '(not set)';
}

$('settings-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const body = {
    max_concurrent: +$('s-max').value || 0,
    default_rate_limit: $('s-default-rate').value.trim(),
    global_rate_limit: $('s-global-rate').value.trim(),
    auto_retry: $('s-retry').checked,
    auto_retry_max_attempts: +$('s-retry-max').value || 1,
    notify_on_complete: $('s-notify').checked,
    webui: $('s-web').checked,
    webui_port: +$('s-web-port').value || 0,
    webui_network: $('s-web-net').checked,
    webui_username: $('s-web-user').value.trim(),
    webui_password: $('s-web-pass').value,
  };
  const prev = state.info.settings;
  const moving = !body.webui || body.webui_port !== prev.webui_port || body.webui_network !== prev.webui_network;
  try {
    state.info.settings = await api('PUT', 'api/settings', body);
    fillSettings();
    if (moving) say('settings-msg', body.webui ? 'Saved. The web interface moved — open it again at its new address (run "godl web").' : 'Saved. The web interface is now off.', 'ok');
    else say('settings-msg', 'Saved.', 'ok');
  } catch (err) { say('settings-msg', err.message, 'err'); }
});

// ---------- player ----------
//
// Plays in the page. Files the browser reads as they are go straight
// to it (seeking is the browser's own). Everything else arrives as a
// stream godl repackages on the fly — picture copied, never re-encoded
// — which can't be seeked inside, so a seek or a change of audio
// language restarts it at the new position. `offset` is where the
// current stream starts in the film; the video element's own clock
// counts from there.

const P = {
  id: null, info: null, req: null, title: '', direct: false,
  offset: 0, audio: 0, subs: -1, cues: null, seeking: false,
};
const video = $('p-video');
let subTrack = null;

function playerWait(text) { $('p-wait').textContent = text || ''; $('p-wait').hidden = !text; }

async function openPlayer(req, title, jobID) {
  Object.assign(P, { id: null, info: null, req, title, direct: false, offset: 0, audio: 0, subs: -1, cues: null });
  $('p-title').textContent = title;
  $('p-fallback').hidden = true;
  $('player').hidden = false;
  document.body.style.overflow = 'hidden';
  $('p-file').hidden = true;
  if (jobID && !req.file) loadFileChoice(jobID);
  await startSession();
}

async function loadFileChoice(jobID) {
  try {
    const r = await api('GET', 'api/jobs/' + encodeURIComponent(jobID) + '/files');
    if (r.files.length < 2) return;
    const sel = $('p-file');
    sel.replaceChildren(...r.files.map((f) => el('option', { value: String(f.file), text: f.name + ' (' + bytes(f.size) + ')' })));
    sel.value = '';
    sel.hidden = false;
    sel.onchange = () => { P.req = Object.assign({}, P.req, { file: +sel.value }); P.title = sel.selectedOptions[0].textContent; startSession(); };
  } catch { /* the main file still plays */ }
}

async function startSession() {
  video.removeAttribute('src');
  video.load();
  $('p-play').textContent = 'Play';
  playerWait(P.req.kind === 'link' ? 'Looking the video up…' : 'Opening…');
  $('p-fallback').hidden = true;
  let r;
  try {
    r = await api('POST', 'api/play/open', P.req);
  } catch (err) {
    playerWait('');
    let msg = err.message;
    if (/sign in|not a bot|cookies/i.test(msg) && P.req.kind === 'link' && !P.req.cookies) {
      msg += ' — In New → Watch a link, enter the browser you\u2019re signed in with (for example firefox) and try again.';
    }
    showFallback(msg, false);
    return;
  }
  if ($('player').hidden) return;
  r.audio = r.audio || [];
  r.subs = r.subs || [];
  P.id = r.id;
  P.info = r;
  P.audio = 0;
  P.subs = -1;
  P.cues = null;

  const a = $('p-audio');
  a.replaceChildren(...r.audio.map((t) => el('option', { value: String(t.index), text: trackLabel(t, 'Track') })));
  a.parentElement.hidden = r.audio.length < 2;
  const s = $('p-subs');
  s.replaceChildren(el('option', { value: '-1', text: 'Off' }), ...r.subs.map((t) => el('option', { value: String(t.index), disabled: !t.text, text: trackLabel(t, 'Subtitles') + (t.text ? '' : ' (not supported)') })));
  s.parentElement.hidden = r.subs.length === 0;
  $('p-dur').textContent = clock(r.duration);

  const v = r.video;
  const decodable = v && v.mime && (video.canPlayType(v.mime) !== '' || (window.MediaSource && MediaSource.isTypeSupported(v.mime)));
  if (v && !decodable) {
    playerWait('');
    showFallback('This browser can’t play this video’s picture format (' + v.codec.toUpperCase() + '). godl doesn’t convert video, so play it in another app or download it:', true);
    return;
  }
  P.direct = r.direct;
  load(0);
}

function trackLabel(t, fallback) {
  const parts = [langName(t.language), t.title].filter(Boolean);
  const label = [...new Set(parts)].join(' — ') || fallback + ' ' + (t.index + 1);
  return label + (t.codec && t.codec !== 'aac' && t.codec !== 'webvtt' ? ' · ' + t.codec.toUpperCase() : '');
}

async function load(t) {
  const id = P.id;
  if (P.direct) {
    P.offset = 0;
    video.src = 'api/play/' + id + '/file';
    if (t) video.currentTime = t;
  } else {
    playerWait('Loading…');
    let start = 0;
    if (t > 0) {
      try { start = (await api('GET', 'api/play/' + id + '/seek?t=' + t.toFixed(2))).start; } catch { start = t; }
    }
    if (P.id !== id) return;
    P.offset = start;
    video.src = 'api/play/' + id + '/stream?audio=' + P.audio + '&t=' + start.toFixed(3);
  }
  applyCues();
  video.play().catch(() => {});
}

video.addEventListener('playing', () => playerWait(''));
video.addEventListener('waiting', () => { if (!$('p-fallback').hidden) return; playerWait('Loading…'); });
video.addEventListener('canplay', () => playerWait(''));
video.addEventListener('error', () => {
  if (!video.getAttribute('src') || $('player').hidden) return;
  playerWait('');
  showFallback('This browser couldn’t play this video. Play it in another app, or download it:', true);
});
video.addEventListener('play', () => ($('p-play').textContent = 'Pause'));
video.addEventListener('pause', () => ($('p-play').textContent = 'Play'));

function now() { return P.offset + (video.currentTime || 0); }
function duration() { return (P.info && P.info.duration) || (isFinite(video.duration) ? video.duration : 0); }

video.addEventListener('timeupdate', () => {
  if (P.seeking) return;
  const d = duration();
  $('p-cur').textContent = clock(now());
  if (d) $('p-seek').value = String(Math.round((now() / d) * 1000));
});

$('p-seek').addEventListener('input', () => { P.seeking = true; $('p-cur').textContent = clock((+$('p-seek').value / 1000) * duration()); });
$('p-seek').addEventListener('change', () => { P.seeking = false; seekTo((+$('p-seek').value / 1000) * duration()); });

function seekTo(t) {
  const d = duration();
  t = Math.max(0, d ? Math.min(t, d - 0.5) : t);
  if (P.direct) { video.currentTime = t; return; }
  // Inside what's already arrived, the element can seek by itself.
  const rel = t - P.offset;
  const b = video.buffered;
  for (let i = 0; i < b.length; i++) {
    if (rel >= b.start(i) && rel <= b.end(i)) { video.currentTime = rel; return; }
  }
  load(t);
}

$('p-play').addEventListener('click', () => (video.paused ? video.play().catch(() => {}) : video.pause()));
$('p-audio').addEventListener('change', (e) => { P.audio = +e.target.value; load(now()); });
$('p-volume').addEventListener('input', (e) => (video.volume = +e.target.value));
$('p-full').addEventListener('click', () => {
  const stage = $('player');
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  else if (stage.requestFullscreen) stage.requestFullscreen().catch(() => {});
  else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen();
});
$('p-ext').addEventListener('click', () => showFallback('Play it in another app (VLC and similar), or download it:', true));
$('p-close').addEventListener('click', closePlayer);

function closePlayer() {
  $('player').hidden = true;
  document.body.style.overflow = '';
  video.pause();
  video.removeAttribute('src');
  video.load(); // drops the connection, which stops godl's ffmpeg
  P.id = null;
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
}

document.addEventListener('keydown', (e) => {
  if ($('player').hidden || e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;
  if (e.key === 'Escape') closePlayer();
  else if (e.key === ' ' || e.key === 'k') { e.preventDefault(); $('p-play').click(); }
  else if (e.key === 'ArrowRight') seekTo(now() + 10);
  else if (e.key === 'ArrowLeft') seekTo(now() - 10);
  else if (e.key === 'f') $('p-full').click();
});

// Subtitles: fetched once per track, timed against the whole film, and
// shifted onto the current stream's clock after every restart.
$('p-subs').addEventListener('change', async (e) => {
  P.subs = +e.target.value;
  P.cues = null;
  if (P.subs >= 0) {
    try {
      const resp = await fetch('api/play/' + P.id + '/subs?track=' + P.subs);
      if (!resp.ok) throw new Error((await resp.json().catch(() => ({}))).error || resp.statusText);
      P.cues = parseVTT(await resp.text());
    } catch (err) { banner('Subtitles: ' + err.message, true); P.subs = -1; e.target.value = '-1'; }
  }
  applyCues();
});

function parseVTT(text) {
  const ts = (s) => {
    const p = s.trim().split(':').map(parseFloat);
    return p.length === 3 ? p[0] * 3600 + p[1] * 60 + p[2] : p[0] * 60 + p[1];
  };
  const cues = [];
  for (const block of text.replace(/\r/g, '').split(/\n\n+/)) {
    const ls = block.split('\n');
    const i = ls.findIndex((l) => l.includes('-->'));
    if (i < 0) continue;
    const [a, b] = ls[i].split('-->');
    const text = ls.slice(i + 1).join('\n').trim();
    if (text) cues.push({ start: ts(a), end: ts(b.trim().split(/\s+/)[0]), text });
  }
  return cues;
}

function applyCues() {
  if (!subTrack) subTrack = video.addTextTrack('subtitles', 'Subtitles');
  if (subTrack.cues) for (const c of [...subTrack.cues]) subTrack.removeCue(c);
  if (!P.cues) { subTrack.mode = 'disabled'; return; }
  for (const c of P.cues) {
    if (c.end <= P.offset) continue;
    subTrack.addCue(new VTTCue(Math.max(0, c.start - P.offset), c.end - P.offset, c.text));
  }
  subTrack.mode = 'showing';
}

async function showFallback(reason, withLinks) {
  video.pause();
  $('p-reason').textContent = reason;
  const box = $('p-links');
  box.replaceChildren();
  $('p-fallback').hidden = false;
  const close = el('button', { type: 'button', text: 'Back to the player', onclick: () => ($('p-fallback').hidden = true) });
  if (!withLinks || !P.id) { box.append(close); return; }
  try {
    const r = await api('POST', 'api/play/' + P.id + '/link');
    const abs = (p) => location.origin + p;
    const row = (label, url, dl) => {
      const input = el('input', { type: 'text', readonly: true, value: url, 'aria-label': label });
      const copy = el('button', { type: 'button', text: 'Copy', onclick: async () => {
        if (await copyText(url)) copy.textContent = 'Copied';
        else { input.focus(); input.setSelectionRange(0, url.length); copy.textContent = 'Press Copy'; }
        setTimeout(() => (copy.textContent = 'Copy'), 1800);
      } });
      // On a phone, hand the link straight to the VLC app. It does
      // nothing if VLC isn't installed, so it's offered beside the
      // link, never instead of it.
      const vlc = !dl && isPhone() ? el('a', { href: 'vlc://' + url, text: 'Open in VLC' }) : null;
      return el('div', {}, el('div', { class: 'hint', text: label }),
        el('div', { class: 'p-link' }, input, copy, vlc, dl ? el('a', { href: url, download: '', text: 'Download' }) : null));
    };
    box.append(row(r.remote ? 'Stream link (all audio languages; paste into VLC → Open Network Stream)' : 'Stream link (paste into VLC → Open Network Stream)', abs(r.stream), false));
    if (r.download) box.append(row('Download link', abs(r.download), true));
    else box.append(el('button', { type: 'button', text: 'Download it with godl instead', onclick: () => saveLink() }));
    box.append(el('p', { class: 'hint', text: 'These links work for 12 hours, from any device that can reach this page.' }));
  } catch (err) { box.append(el('p', { text: err.message })); }
  box.append(close);
}

const isPhone = () => /iPhone|iPad|iPod|Android/i.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

// copyText puts text on the clipboard. The Clipboard API only exists on
// https or localhost, so a phone using the page over the home network
// (plain http) needs the older route: a selected, editable element and
// execCommand('copy'), which Safari on iOS accepts during a tap only
// when the selection is a real range on an editable node.
async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(text); return true; } catch { /* fall through */ }
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.top = '0';
  ta.style.left = '0';
  ta.style.opacity = '0';
  ta.style.fontSize = '16px'; // anything smaller makes iOS zoom the page
  ta.contentEditable = 'true';
  document.body.append(ta);
  const range = document.createRange();
  range.selectNodeContents(ta);
  const sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  ta.setSelectionRange(0, text.length);
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  sel.removeAllRanges();
  ta.remove();
  return ok;
}

async function saveLink() {
  try {
    await api('POST', 'api/jobs', { type: 'social', links: [P.req.link] });
    $('p-reason').textContent = 'Download started — it’s in your Downloads list.';
  } catch (err) { $('p-reason').textContent = err.message; }
}

// ---------- start ----------

async function init() {
  try {
    state.info = await api('GET', 'api/state');
  } catch (err) { banner(err.message, true); return; }
  $('version').textContent = state.info.version;
  $('new-preset').replaceChildren(...state.info.presets.map((p) => el('option', { value: p.name, text: p.name + ' — ' + p.description })));
  $('new-output').placeholder = state.info.downloads;
  $('browse-output').placeholder = state.info.downloads;
  $('share-dir').placeholder = state.info.downloads;
  applyShare(state.info.share);
  $('watch-cookies').value = prefs.watchCookies || '';
  showSort();
  setKind('url');
  showView();
  connectFeed();
}
init();
