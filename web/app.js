// Pathau web UI. The same page runs on this PC (admin, full control) and on phones (passcode).
'use strict';
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
function h(tag, props = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('aria-') || k.startsWith('data-') || k === 'role') e.setAttribute(k, v);
    else e[k] = v;
  }
  e.append(...kids.filter((k) => k != null && k !== false));
  return e;
}
function ic(name, cls = '') {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  s.setAttribute('class', `ic ${cls}`); s.setAttribute('aria-hidden', 'true');
  const u = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  u.setAttribute('href', `#i-${name}`); s.append(u);
  return s;
}
const btn = (label, icon, onclick, cls = '') => h('button', { type: 'button', class: `btn ${cls}`, onclick }, icon && ic(icon), label);
const iconBtn = (label, icon, onclick, cls = '') => h('button', {
  type: 'button', class: `icon-btn ${cls}`, 'aria-label': label, title: label,
  onclick: (e) => { e.stopPropagation(); onclick(e); },
}, ic(icon));
const enc = encodeURIComponent;
const HDR = { 'X-Pathau': '1' };
let me = {};

const prefs = (() => { try { return JSON.parse(localStorage.getItem('pathau') || '{}'); } catch { return {}; } })();
const savePrefs = () => { try { localStorage.setItem('pathau', JSON.stringify(prefs)); } catch { /* private mode */ } };

async function api(url, body) {
  const r = await fetch(url, body === undefined ? { headers: HDR }
    : { method: 'POST', headers: { ...HDR, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const t = await r.text();
  if (!r.ok) throw new Error(t.trim() || r.statusText);
  try { return JSON.parse(t); } catch { return t; }
}

function fmt(n) {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < 4) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i];
}
function when(d) {
  d = new Date(d);
  const today = new Date().toDateString() === d.toDateString();
  return today ? d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : d.toLocaleDateString([], { day: 'numeric', month: 'short', year: 'numeric' });
}

let toastTimer;
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.hidden = false;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => (t.hidden = true), 3500);
}

async function copy(text) {
  if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(text);
  else { // phones load us over plain http on the LAN, where the Clipboard API is off
    const ta = h('textarea', { value: text }); document.body.append(ta); ta.select();
    document.execCommand('copy'); ta.remove();
  }
  toast('Copied');
}

// ---------- navigation ----------
const PAGES = ['home', 'files', 'text', 'devices', 'settings'];
function go() {
  let page = location.hash.slice(1) || 'home';
  if (!PAGES.includes(page) || (!me.admin && ['devices', 'settings'].includes(page))) page = 'home';
  for (const s of $$('.page')) s.hidden = s.dataset.page !== page;
  for (const a of $$('.nav a')) {
    if (a.dataset.page === page) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  if (page === 'files') $('#files-badge').hidden = true;
  if (page === 'text') $('#text-badge').hidden = true;
  $('#content').focus({ preventScroll: true });
  window.scrollTo(0, 0);
}
window.addEventListener('hashchange', go);
const onPage = (p) => !$(`.page[data-page="${p}"]`).hidden;
function bump(id) { const b = $(id); b.textContent = (+b.textContent || 0) + 1; b.hidden = false; }

// ---------- transfers ----------
const rows = {}; // transfer id -> row, so pause/cancel from the other side shows here too
function row(label, total, icon = 'upload') {
  $('#no-xfers').hidden = true;
  const fill = h('i'), info = h('span', { class: 'info' }), acts = h('span', { class: 'acts' });
  const el = h('li', { class: 'xfer' }, ic(icon), h('span', { class: 'name', title: label, textContent: label }), acts, h('div', { class: 'bar' }, fill), info);
  const list = $('#xfers'); list.prepend(el);
  while (list.children.length > 50) list.lastChild.remove();
  let t0 = Date.now(), base = 0, last = 0;
  const r = {
    id: Math.random().toString(36).slice(2, 10), xid: '', paused: false, canceled: false, xhr: null,
    update(done, tot = total, note = '') {
      last = done;
      fill.style.width = tot > 0 ? `${(done * 100) / tot}%` : '30%';
      if (r.paused || r.canceled) return;
      const speed = (done - base) / Math.max((Date.now() - t0) / 1000, 0.001);
      info.textContent = (tot > 0 ? `${Math.floor((done * 100) / tot)}% · ${fmt(done)} of ${fmt(tot)}` : fmt(done)) + ` · ${fmt(speed)}/s` + (note ? ` · ${note}` : '');
    },
    status(msg) { info.textContent = msg; },
    done(msg) { acts.replaceChildren(); delete rows[r.xid]; el.classList.add('ok'); fill.style.width = '100%'; info.textContent = msg; el.firstChild.replaceWith(ic('check')); },
    fail(msg) { acts.replaceChildren(); delete rows[r.xid]; el.classList.remove('paused'); el.classList.add('bad'); info.textContent = msg; el.firstChild.replaceWith(ic('alert')); },
    action(label, icon, fn) { acts.append(iconBtn(label, icon, fn)); },
    track(xid) { delete rows[r.xid]; r.xid = xid; rows[xid] = r; },
    // mark shows a pause/resume/cancel made here or on the other device
    mark(action) {
      if (action === 'cancel') { r.canceled = true; info.textContent = 'Canceling…'; acts.replaceChildren(); r.xhr?.abort(); return; }
      r.paused = action === 'pause';
      el.classList.toggle('paused', r.paused);
      pauseBtn.replaceChildren(ic(r.paused ? 'play' : 'pause'));
      pauseBtn.setAttribute('aria-label', r.paused ? 'Resume' : 'Pause'); pauseBtn.title = r.paused ? 'Resume' : 'Pause';
      if (r.paused) info.textContent = `Paused · ${total > 0 ? Math.floor((last * 100) / total) + '%' : fmt(last)}`;
      else { t0 = Date.now(); base = last; info.textContent = 'Resuming…'; }
    },
  };
  const send = (action) => r.xid && api('/api/xfer', { id: r.xid, action }).then(() => r.mark(action), (e) => toast(e.message));
  const pauseBtn = iconBtn('Pause', 'pause', () => send(r.paused ? 'resume' : 'pause'));
  r.controls = () => acts.replaceChildren(pauseBtn, iconBtn('Cancel', 'x', () => send('cancel'), 'danger'));
  return r;
}

// XHR, not fetch: fetch can't report upload progress.
function put(url, file, onProgress, r) {
  return new Promise((res, rej) => {
    const x = new XMLHttpRequest();
    if (r) r.xhr = x;
    x.open('PUT', url); x.setRequestHeader('X-Pathau', '1');
    x.upload.onprogress = (e) => onProgress(e.loaded);
    x.onload = () => (x.status < 300 ? res(x.responseText) : rej(new Error(x.responseText.trim() || x.status)));
    x.onerror = x.onabort = () => rej(new Error('Connection lost'));
    x.send(file);
  });
}

const total = (items) => items.reduce((s, i) => s + i.file.size, 0);
const what = (items) => (items.length === 1 ? items[0].path : `${items.length} files`);

// uploadItems sends files one by one; urlFor(item, xid) gives each file's URL.
async function uploadItems(items, urlFor, label) {
  if (!items.length) return;
  const tot = total(items), r = row(label, tot);
  r.controls();
  let sent = 0, failed = 0;
  for (const [i, it] of items.entries()) {
    if (r.canceled) break;
    r.track(`${r.id}-${i}`);
    try { await put(urlFor(it, r.xid, r.paused), it.file, (d) => r.update(sent + d, tot, items.length > 1 ? it.path : ''), r); }
    catch (e) { if (!r.canceled) { failed++; toast(`${it.path}: ${e.message}`); } }
    sent += it.file.size;
  }
  if (r.canceled) r.fail('Canceled');
  else failed ? r.fail(`${failed} of ${items.length} failed`) : r.done(`Sent · ${fmt(sent)}`);
}
// paused=1 starts the next file of a paused batch already paused
const xq = (xid, paused) => `&xid=${xid}${paused ? '&paused=1' : ''}`;
// phone -> this PC
const sendToPC = (items) => uploadItems(items, (it, xid, p) => `/api/upload?name=${enc(it.path)}${xq(xid, p)}`, `${what(items)} → ${me.name}`);
// this PC -> phones (outbox)
const sendToPhones = (items) => uploadItems(items, (it, xid, p) => `/api/outbox?name=${enc(it.path)}${xq(xid, p)}`, `${what(items)} → phones`)
  .then(() => items.length && toast('Ready on your phones: open Pathau there and tap Files'));

// this PC -> another PC. The other side must Accept first; each file is SHA-256 checked there.
async function sendToPeer(p, items) {
  if (!items.length) return;
  const tot = total(items), r = row(`${what(items)} → ${p.name}`, tot, 'send');
  r.status(`Waiting for ${p.name} to accept…`);
  let token;
  try {
    ({ token } = await api('/api/send/offer', { peer: p.addr, files: items.map((i) => ({ path: i.path, size: i.file.size })) }));
  } catch (e) { return r.fail(e.message === 'declined' ? `${p.name} declined` : e.message); }
  r.controls();
  let sent = 0, failed = 0;
  for (const [i, it] of items.entries()) {
    let err;
    for (let attempt = 0; attempt < 3 && !r.canceled; attempt++) { // damaged copies are deleted on the other side, so just resend
      r.track(`${r.id}-${i}-${attempt}`);
      const q = { peer: p.addr, token, path: it.path, xid: r.xid };
      if (r.paused) q.paused = '1';
      try {
        await put('/api/send/file?' + new URLSearchParams(q), it.file, (d) => r.update(sent + d, tot, items.length > 1 ? it.path : ''), r);
        err = null; break;
      } catch (e) { err = e; if (/Canceled/.test(e.message)) r.canceled = true; }
    }
    if (r.canceled) break;
    if (err) { failed++; toast(`${it.path}: ${err.message}`); }
    sent += it.file.size;
  }
  if (r.canceled) r.fail('Canceled');
  else failed ? r.fail(`${failed} of ${items.length} failed`) : r.done(`Sent · ${fmt(sent)} · verified`);
}

// Incoming progress, pushed by this PC's server
const rx = {};
function onRx(m) {
  let r = rx[m.id];
  if (!r) { r = rx[m.id] = row(`${m.name} ← ${m.from}`, m.total, 'download'); r.track(m.id); r.controls(); }
  if (!m.state) return r.update(m.done, m.total);
  delete rx[m.id];
  if (m.state !== 'done') return r.fail(m.state);
  r.done(`Saved · ${fmt(m.done)}`);
  const name = m.name.split('/').pop();
  r.action('Preview', 'image', () => view('recv', [{ name, sub: m.name }], 0));
  r.action('Show in folder', 'folder', () => openFile('recv', m.name, true));
  if (files.src === 'recv') loadFiles();
  if (!onPage('files')) bump('#files-badge');
}
// a transfer was paused/resumed/canceled (possibly from the phone that's sending)
const onXfer = (m) => rows[m.id]?.mark(m.action);

function pick(dir) {
  return new Promise((res) => {
    const i = h('input', { type: 'file', multiple: true });
    if (dir) i.webkitdirectory = true;
    i.onchange = () => res([...i.files].map((f) => ({ file: f, path: f.webkitRelativePath || f.name })));
    i.click();
  });
}

// Dropped files and folders (folders keep their structure)
async function dropped(dt) {
  const out = [];
  const walk = async (entry, prefix) => {
    if (entry.isFile) {
      out.push({ file: await new Promise((res, rej) => entry.file(res, rej)), path: prefix + entry.name });
    } else if (entry.isDirectory) {
      const rd = entry.createReader();
      for (let batch; (batch = await new Promise((res, rej) => rd.readEntries(res, rej))).length;)
        for (const e of batch) await walk(e, prefix + entry.name + '/');
    }
  };
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
  if (!entries.length) return [...dt.files].map((f) => ({ file: f, path: f.name }));
  for (const e of entries) await walk(e, '');
  return out;
}

function dropZone(el, onItems) {
  el.addEventListener('dragover', (e) => { e.preventDefault(); el.classList.add('over'); });
  el.addEventListener('dragleave', (e) => { if (!el.contains(e.relatedTarget)) el.classList.remove('over'); });
  el.addEventListener('drop', async (e) => { e.preventDefault(); el.classList.remove('over'); onItems(await dropped(e.dataTransfer)); });
}

// ---------- files ----------
const files = { src: null, p: '', list: [], cap: 300, sources: [] };
const KIND = [
  ['img', 'image', /\.(jpe?g|png|gif|webp|bmp|avif)$/i], ['vid', 'film', /\.(mp4|webm|mov|m4v|mkv|avi)$/i],
  ['aud', 'music', /\.(mp3|m4a|wav|ogg|aac|flac)$/i], ['zip', 'archive', /\.(zip|rar|7z|tar|gz)$/i],
];
const kind = (name) => KIND.find(([, , re]) => re.test(name)) || ['file', 'files'];
const fileURL = (src, p, inline) => `/api/dl?s=${src}&p=${enc(p)}${inline ? '&inline=1' : ''}`;
const playable = (name) => /\.(jpe?g|png|gif|webp|bmp|avif|mp4|webm|mov|m4v|mp3|m4a|wav|ogg|aac|flac)$/i.test(name);
const openFile = (s, p, reveal) => api('/api/open-file', { s, p, reveal }).catch((e) => toast(e.message));

async function loadSources() {
  const shares = await api('/api/shares');
  files.sources = [
    ...(me.admin
      ? [{ id: 'recv', name: 'Received', icon: 'inbox' }, { id: 'out', name: 'Sent to phones', icon: 'send' }]
      : [{ id: 'out', name: 'From PC', icon: 'download' }]),
    ...shares.map((s) => ({ id: String(s.i), name: s.name, icon: 'folder', path: s.path, share: true })),
  ];
  if (!files.sources.some((s) => s.id === files.src)) { files.src = files.sources[0].id; files.p = ''; }
  $('#sources').replaceChildren(...files.sources.map((s) => h('button', {
    type: 'button', class: 'src', role: 'tab', 'data-id': s.id, 'aria-selected': String(s.id === files.src), onclick: () => openSrc(s.id),
  }, ic(s.icon), s.name)), ...(me.admin ? [h('button', { type: 'button', class: 'src', onclick: addShare }, ic('plus'), 'Share a folder')] : []));
}

function openSrc(id, p = '') {
  files.src = id; files.p = p;
  for (const b of $$('#sources [role=tab]')) b.setAttribute('aria-selected', String(b.dataset.id === id));
  loadFiles();
}

async function loadFiles() {
  const { src, p } = files;
  let list = [];
  try { list = await api(`/api/ls?s=${src}&p=${enc(p)}`); } catch (e) { toast(e.message); }
  if (src !== files.src || p !== files.p) return; // user moved on
  files.list = list; files.cap = 300;
  renderFiles();
}

function sortedFiles() {
  const [key, dir] = (prefs.sort || 'date-desc').split('-');
  const q = $('#q').value.trim().toLowerCase();
  const cmp = {
    name: (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' }),
    size: (a, b) => a.size - b.size,
    date: (a, b) => Date.parse(a.mod) - Date.parse(b.mod),
  }[key];
  return files.list.filter((e) => !q || e.name.toLowerCase().includes(q))
    .sort((a, b) => (b.dir - a.dir) || (dir === 'desc' ? cmp(b, a) : cmp(a, b)));
}

function thumb(e, sub, src = files.src) {
  const [k, icon] = e.dir ? ['dir', 'folder'] : kind(e.name);
  return k === 'img' ? h('img', { class: 'thumb', src: fileURL(src, sub, true), loading: 'lazy', alt: '' })
    : h('span', { class: `ficon ${k}` }, ic(icon));
}

function itemActions(e, sub) {
  const src = files.src;
  if (!me.admin) {
    return [h('a', { class: 'icon-btn', href: fileURL(src, sub), download: e.dir ? `${e.name}.zip` : e.name, 'aria-label': `Download ${e.name}`, title: 'Download', onclick: (ev) => ev.stopPropagation() }, ic('download'))];
  }
  if (src === 'out') {
    return [iconBtn('Stop sending', 'trash', () => api('/api/outbox/remove', { p: sub }).then(loadFiles), 'danger')];
  }
  return [
    !e.dir && iconBtn('Open', 'open', () => openFile(src, sub)),
    iconBtn('Show in folder', 'folder', () => openFile(src, sub, true)),
  ].filter(Boolean);
}

function renderFiles() {
  const { src, p } = files, srcInfo = files.sources.find((s) => s.id === src) || {};
  // breadcrumbs
  const parts = p ? p.split('/') : []; // at the top level the selected tab already says where we are
  $('#crumbs').replaceChildren(...(parts.length ? [srcInfo.name, ...parts] : []).flatMap((part, i) => [
    i ? ic('right', 'sm') : null,
    h('button', { type: 'button', textContent: part, onclick: () => { files.p = parts.slice(0, i).join('/'); loadFiles(); } }),
  ]).filter(Boolean));

  // what this location is, plus its actions
  const bar = [];
  if (me.admin && src === 'recv') bar.push(h('span', { class: 'grow', textContent: `Files from phones and PCs are saved in ${me.saveDir || '…'}` }), btn('Show in folder', 'folder', () => openFile('recv', p), 'sm'), btn('Change…', null, () => (location.hash = 'settings'), 'sm'));
  if (me.admin && src === 'out') bar.push(h('span', { class: 'grow', textContent: 'Every connected phone can download these.' }), btn('Add files', 'plus', async () => sendToPhones(await pick(false)).then(loadFiles), 'sm primary'), files.list.length && btn('Remove all', 'trash', () => confirm('Stop sending all of these files to phones?') && api('/api/outbox/remove', { p: '' }).then(loadFiles), 'sm danger'));
  if (me.admin && srcInfo.share) bar.push(h('span', { class: 'grow', textContent: `${srcInfo.path} · phones can browse and download, but never change or delete` }), btn('Show in folder', 'folder', () => openFile(src, p), 'sm'), btn('Stop sharing', null, () => confirm(`Stop sharing ${srcInfo.name}? Your files stay where they are.`) && api('/api/shares/remove', { i: +src }).then(() => loadSources()).then(loadFiles), 'sm danger'));
  if (!me.admin && files.list.length) bar.push(h('span', { class: 'grow' }), h('a', { class: 'btn sm', href: fileURL(src, p), download: '' }, ic('download'), 'Download all (.zip)'));
  $('#src-bar').replaceChildren(...bar.filter(Boolean));
  $('#src-bar').hidden = !bar.length;

  const list = sortedFiles(), shown = list.slice(0, files.cap);
  const box = $('#file-view');
  if (!list.length) {
    const q = $('#q').value.trim();
    const msg = q ? `Nothing matches "${q}".`
      : src === 'recv' ? 'Nothing received yet. Scan the QR code on Home with your phone and send something.'
        : src === 'out' ? (me.admin ? 'Drop files on Home, or click "Add files", to send them to your phones.' : 'Nothing here yet. Files your PC sends will show up here.')
          : 'This folder is empty.';
    return box.replaceChildren(h('div', { class: 'file-empty' }, ic(q ? 'search' : 'folder'), h('p', { textContent: msg })));
  }
  const media = list.filter((e) => !e.dir && playable(e.name)).map((e) => ({ name: e.name, sub: p ? `${p}/${e.name}` : e.name }));
  const open = (e, sub) => {
    if (e.dir) { files.p = sub; return loadFiles(); }
    const i = media.findIndex((m) => m.sub === sub);
    i >= 0 ? view(src, media, i) : view(src, [{ name: e.name, sub }], 0);
  };

  let view_;
  if ((prefs.view || 'grid') === 'grid') {
    view_ = h('div', { class: 'fgrid' }, ...shown.map((e) => {
      const sub = p ? `${p}/${e.name}` : e.name;
      return h('button', { type: 'button', class: 'tile', title: e.name, onclick: () => open(e, sub) },
        thumb(e, sub), h('span', { class: 'nm', textContent: e.name }),
        h('small', { class: 'sub', textContent: e.dir ? 'Folder' : `${fmt(e.size)} · ${when(e.mod)}` }));
    }));
  } else {
    const [key, dir] = (prefs.sort || 'date-desc').split('-');
    const head = (label, k, cls = '') => h('th', { class: cls, 'aria-sort': key === k ? (dir === 'asc' ? 'ascending' : 'descending') : 'none' },
      h('button', { type: 'button', onclick: () => setSort(`${k}-${key === k && dir === 'asc' ? 'desc' : 'asc'}`) }, label, key === k ? (dir === 'asc' ? ' ↑' : ' ↓') : ''));
    view_ = h('table', { class: 'flist' },
      h('thead', {}, h('tr', {}, head('Name', 'name'), head('Size', 'size', 'num'), head('Modified', 'date', 'num c-date'), h('th', { class: 'acts' }))),
      h('tbody', {}, ...shown.map((e) => {
        const sub = p ? `${p}/${e.name}` : e.name;
        return h('tr', { onclick: () => open(e, sub) },
          h('td', {}, h('div', { class: 'c-name' }, thumb(e, sub), h('span', { title: e.name, textContent: e.name }))),
          h('td', { class: 'num', textContent: e.dir ? '—' : fmt(e.size) }),
          h('td', { class: 'num c-date', textContent: when(e.mod) }),
          h('td', { class: 'acts' }, ...itemActions(e, sub)));
      })));
  }
  const more = list.length > shown.length
    ? btn(`Show all ${list.length} items`, null, () => { files.cap = Infinity; renderFiles(); }, 'block') : null;
  box.replaceChildren(view_, ...(more ? [more] : []));
}

function setSort(v) { prefs.sort = v; savePrefs(); $('#sort').value = v; renderFiles(); }
function setView(v) {
  prefs.view = v; savePrefs();
  $('#view-grid').setAttribute('aria-pressed', String(v === 'grid'));
  $('#view-list').setAttribute('aria-pressed', String(v === 'list'));
  if (files.src) renderFiles();
}
$('#sort').onchange = (e) => setSort(e.target.value);
$('#view-grid').onclick = () => setView('grid');
$('#view-list').onclick = () => setView('list');
let qTimer;
$('#q').oninput = () => { clearTimeout(qTimer); qTimer = setTimeout(renderFiles, 150); };

async function addShare() {
  try { await api('/api/shares/add', { path: '' }); await loadSources(); } catch (e) { toast(e.message); }
}

// ---------- viewer ----------
function dialog(title, body, actions = []) {
  $('#viewer-name').textContent = title;
  $('#viewer-body').replaceChildren(...[].concat(body));
  $('#viewer-actions').replaceChildren(...actions.filter(Boolean));
  if (!$('#viewer').open) $('#viewer').showModal();
}

// view previews items[i] ({name, sub}) of source src, with prev/next through items.
function view(src, items, i) {
  const { name, sub } = items[i];
  const url = fileURL(src, sub, true), [k] = kind(name);
  const media = k === 'img' ? h('img', { src: url, alt: name })
    : k === 'vid' ? h('video', { src: url, controls: true, autoplay: true, playsInline: true })
      : k === 'aud' ? h('audio', { src: url, controls: true, autoplay: true })
        : h('div', { class: 'no-prev' }, ic('files', 'lg'), h('p', { textContent: 'No preview for this type of file.' }));
  const nav = items.length > 1 ? [
    h('button', { type: 'button', class: 'nav-arrow prev', 'aria-label': 'Previous', onclick: () => view(src, items, (i - 1 + items.length) % items.length) }, ic('left')),
    h('button', { type: 'button', class: 'nav-arrow next', 'aria-label': 'Next', onclick: () => view(src, items, (i + 1) % items.length) }, ic('right')),
  ] : [];
  const actions = !me.admin
    ? [h('a', { class: 'btn sm primary', href: fileURL(src, sub), download: name }, ic('download'), 'Download')]
    : src === 'out' ? []
      : [btn('Open', 'open', () => openFile(src, sub), 'sm primary'), btn('Show in folder', 'folder', () => openFile(src, sub, true), 'sm')];
  dialog(items.length > 1 ? `${name}  ·  ${i + 1} of ${items.length}` : name, [media, ...nav], actions);
  $('#viewer').onkeydown = (e) => {
    if (items.length > 1 && e.key === 'ArrowRight') view(src, items, (i + 1) % items.length);
    if (items.length > 1 && e.key === 'ArrowLeft') view(src, items, (i - 1 + items.length) % items.length);
  };
}
$('#viewer-close').onclick = () => $('#viewer').close();
$('#viewer').addEventListener('click', (e) => { if (e.target.id === 'viewer') e.target.close(); }); // click outside
$('#viewer').addEventListener('close', () => { $('#viewer-body').replaceChildren(); $('#viewer').onkeydown = null; }); // stops media

// ---------- text ----------
let lastSent = '';
function addText(m, live) {
  $('#no-texts').hidden = true;
  const mine = m.text === lastSent;
  const li = h('li', { class: live && !mine ? 'new' : '' },
    h('div', { class: 'grow' },
      h('div', { class: 'meta', textContent: `${m.from} · ${when(m.time)}` }),
      h('div', { class: `body${/^\s*[\w-]{3,12}\s*$/.test(m.text) && /\d/.test(m.text) ? ' code' : ''}`, textContent: m.text }),
      m.code && h('div', { class: 'code-chip', title: 'Code found in this message' }, m.code, iconBtn('Copy code', 'copy', () => copy(m.code)))),
    iconBtn('Copy', 'copy', () => copy(m.text)));
  $('#texts').prepend(li);
  if (live && !mine) {
    if (!onPage('text')) bump('#text-badge');
    if (!me.admin) toast('New text from your PC: open Text to copy it');
  }
}

$('#text-form').onsubmit = async (e) => {
  e.preventDefault();
  const text = $('#text-in').value, sel = $('#text-to');
  if (!text.trim()) return;
  lastSent = text;
  try {
    if (me.admin && sel.value) await api('/api/send/text', { peer: sel.value, to: sel.selectedOptions[0].text, text });
    else await api('/api/text', { text });
    $('#text-in').value = '';
    toast(me.admin ? 'Sent' : `Sent to ${me.name}`);
  } catch (err) { toast(err.message); }
};
$('#paste').onclick = async () => {
  try {
    if (me.admin) { $('#text-in').value = (await api('/api/clipboard')).text; $('#text-in').focus(); return; }
    $('#text-in').value = await navigator.clipboard.readText(); $('#text-form').requestSubmit();
  } catch (e) { toast(e.message); }
};
// Phone: pasting into the box sends right away, so an OTP copied on the phone lands on the PC in one step.
$('#text-in').addEventListener('paste', () => { if (!me.admin) setTimeout(() => $('#text-form').requestSubmit(), 50); });

// ---------- this PC (admin) ----------
let last = {};
const changed = (k, v) => { const j = JSON.stringify(v); if (last[k] === j) return false; last[k] = j; return true; };
const device = (ua) => (/iPhone/.test(ua) ? ['iPhone', 'phone'] : /iPad/.test(ua) ? ['iPad', 'phone'] : /Android/.test(ua) ? ['Android phone', 'phone']
  : /Windows/.test(ua) ? ['Windows PC', 'laptop'] : /Mac/.test(ua) ? ['Mac', 'laptop'] : ['Device', 'devices']);

function peerCard(p) {
  const li = h('li', { class: 'peer' },
    h('div', { class: 'peer-id' }, ic('laptop'), h('div', { class: 'grow' }, h('b', { textContent: p.name }), h('small', { class: 'sub', textContent: `${p.addr} · or drop files here` }))),
    h('div', { class: 'row' },
      btn('Send files', 'files', async () => sendToPeer(p, await pick(false)), 'sm'),
      btn('Send folder', 'folder', async () => sendToPeer(p, await pick(true)), 'sm')));
  dropZone(li, (items) => sendToPeer(p, items));
  return li;
}

async function refresh() {
  let s;
  try { s = await api('/api/state'); } catch { return; }
  for (const el of $$('.pc-name')) el.textContent = s.name;
  me.name = s.name;

  if (changed('qr', [s.ips, s.passcode])) {
    const sel = $('#ip'), cur = sel.value;
    sel.replaceChildren(...s.ips.map((ip) => h('option', { value: ip, textContent: ip })));
    if (s.ips.includes(cur)) sel.value = cur;
    showQR(s);
  }
  if (changed('peers', s.peers)) {
    for (const id of ['#peers', '#peers-2']) $(id).replaceChildren(...s.peers.map(peerCard));
    $('#no-peers').hidden = s.peers.length > 0;
    const sel = $('#text-to'), cur = sel.value;
    sel.replaceChildren(h('option', { value: '', textContent: 'To phones' }), ...s.peers.map((p) => h('option', { value: p.addr, textContent: `To ${p.name}` })));
    sel.value = cur;
  }
  const pending = s.devices.filter((d) => d.pending), paired = s.devices.filter((d) => !d.pending);
  if (changed('banners', [s.offers, pending])) {
    $('#offers').replaceChildren(
      ...pending.map((d) => {
        const answer = (allow) => api('/api/devices/answer', { id: d.id, allow }).then(refresh);
        return h('div', { class: 'card offer', role: 'alert' }, ic(device(d.ua)[1], 'lg'),
          h('div', { class: 'grow' }, h('b', { textContent: `Allow ${device(d.ua)[0]} (${d.ip}) to connect?` }),
            h('span', { class: 'sub', textContent: 'It will stay paired until you remove it. Only allow your own devices.' })),
          btn('Deny', null, () => answer(false)), btn('Allow', 'check', () => answer(true), 'primary'));
      }),
      ...s.offers.map((o) => {
        const answer = (accept) => api('/api/offer/answer', { id: o.id, accept }).then(refresh);
        return h('div', { class: 'card offer', role: 'alert' }, ic('download', 'lg'),
          h('div', { class: 'grow' }, h('b', { textContent: `${o.from} wants to send you ${o.count === 1 ? 'a file' : `${o.count} files`}` }), h('span', { class: 'sub', textContent: `${fmt(o.total)} · saved to ${s.saveDir}` })),
          btn('Decline', null, () => answer(false)), btn('Accept', 'check', () => answer(true), 'primary'));
      }));
    const n = s.offers.length + pending.length;
    document.title = n ? `(${n}) Pathau` : 'Pathau';
  }
  if (changed('devices', paired)) {
    $('#no-devices').hidden = paired.length > 0;
    $('#devices').replaceChildren(...paired.map((d) => {
      const [label, icon] = d.kind === 'shortcut' ? ['iPhone Shortcut', 'phone'] : device(d.ua);
      return h('li', {}, ic(icon), h('div', { class: 'grow' }, h('b', { textContent: d.name }),
        h('small', { class: 'sub', textContent: ` · ${label}${d.ip ? ` · ${d.ip}` : ''} · last seen ${d.seen.startsWith('0001') ? 'never' : when(d.seen)}` })),
      btn('Rename', null, () => {
        const name = prompt('Name this device', d.name);
        if (name) api('/api/devices/rename', { id: d.id, name }).then(refresh, (e) => toast(e.message));
      }, 'sm'),
      btn('Remove', null, () => confirm(`Remove ${d.name}? It will need to pair again.`) && api('/api/devices/remove', { id: d.id }).then(refresh), 'sm danger'));
    }));
  }
  if (changed('autostart', [s.canAutostart, s.autostart])) {
    $('#autostart-row').hidden = !s.canAutostart;
    $('#autostart').checked = s.autostart;
  }
  me.url = $('#url').textContent;
  $('#version').textContent = s.version === 'dev' ? '(dev build)' : `v${s.version}`;
  if (changed('shares', s.shares)) loadSources();
  if (changed('saveDir', s.saveDir)) { me.saveDir = s.saveDir; if (files.src === 'recv') loadFiles(); }
  if (!last.settings) {
    last.settings = true;
    const f = $('#settings');
    f.name.value = s.name; f.passcode.value = s.fixedPass ? s.passcode : ''; f.saveDir.value = s.saveDir; f.autoCopy.checked = s.autoCopy;
  }
}

function showQR(s) {
  const ip = $('#ip').value || s.ips[0];
  if (!ip) { $('#url').textContent = 'Not connected to Wi-Fi'; return; }
  $('#qr').src = `/api/qr?ip=${ip}&v=${enc(s.passcode)}`;
  $('#url').textContent = `http://${ip}:${s.port}`;
  $('#pass').textContent = s.passcode;
}
$('#ip').onchange = () => { delete last.qr; refresh(); };
$('#qr-btn').onclick = () => dialog('Scan with your phone camera', h('img', { src: $('#qr').src, alt: 'QR code', class: 'qr-big' }));
$('#copy-url').onclick = () => copy($('#url').textContent);
$('#out-files').onclick = async () => sendToPhones(await pick(false));
$('#out-folder').onclick = async () => sendToPhones(await pick(true));
dropZone($('#out-drop'), sendToPhones);
$('#out-link').onclick = () => openSrc('out');

$('#connect-form').onsubmit = async (e) => {
  e.preventDefault();
  try { const p = await api('/api/connect', { addr: $('#connect-ip').value }); toast(`Connected to ${p.name}`); $('#connect-ip').value = ''; refresh(); }
  catch (err) { toast(err.message); }
};
$('#settings').onsubmit = async (e) => {
  e.preventDefault();
  const f = e.target;
  try {
    await api('/api/settings', { name: f.name.value, passcode: f.passcode.value, saveDir: f.saveDir.value, autoCopy: f.autoCopy.checked });
    toast('Settings saved'); delete last.qr; refresh();
  } catch (err) { toast(err.message); }
};
$('#pick-save').onclick = async () => {
  const r = await api('/api/pick-save-dir', {}).catch((e) => toast(e.message));
  if (r?.path) { $('#settings').saveDir.value = r.path; toast(`Received files will be saved in ${r.path}`); refresh(); }
};
$('#open-save').onclick = () => openFile('recv', '');
$('#quit').onclick = () => confirm('Quit Pathau? Phones and computers will disconnect.') &&
  api('/api/quit', {}).then(() => document.body.replaceChildren(h('main', { class: 'login' }, h('p', { textContent: 'Pathau has stopped. You can close this tab.' }))));

$('#autostart').onchange = (e) => api('/api/autostart', { on: e.target.checked })
  .then(() => toast(e.target.checked ? 'Pathau will start with Windows' : "Pathau won't start with Windows"), (err) => toast(err.message));

$('#make-key').onclick = async () => {
  const { key } = await api('/api/devices/shortcut', {});
  const code = (t) => h('code', { textContent: t });
  $('#shortcut-setup').replaceChildren(
    h('p', { class: 'sub', textContent: 'Your Shortcut key. It is shown only this once: copy it now.' }),
    h('div', { class: 'keybox' }, h('span', { class: 'grow', textContent: key }), iconBtn('Copy key', 'copy', () => copy(key))),
    h('ol', { class: 'steps-box' },
      h('li', {}, 'On the iPhone open ', h('b', { textContent: 'Shortcuts' }), ' → ', h('b', { textContent: 'Automation' }), ' → ', h('b', { textContent: '+' }), ' → ', h('b', { textContent: 'Message' }), '. Set ', h('b', { textContent: 'Message Contains' }), ' to ', code('OTP'), ' and choose ', h('b', { textContent: 'Run Immediately' }), '.'),
      h('li', {}, 'Add the action ', h('b', { textContent: 'Get Contents of URL' }), ' with URL ', code(`${me.url}/api/text`), ', Method ', code('POST'), '.'),
      h('li', {}, 'Under Headers add ', code('Authorization'), ' = ', code(`Bearer ${key}`), '.'),
      h('li', {}, 'Under Request Body choose ', h('b', { textContent: 'JSON' }), ', add the key ', code('text'), ' and set its value to ', h('b', { textContent: 'Shortcut Input' }), '.'),
      h('li', {}, "Done. Test it by texting yourself 'OTP 1234': the code lands on this PC's clipboard.")),
    h('p', { class: 'hint', textContent: 'Tip: for any copied text, make a Shortcut with "Get Clipboard" then the same "Get Contents of URL" (value: Clipboard), and run it with Back Tap: Settings → Accessibility → Touch → Back Tap. To revoke, remove "iPhone Shortcut" from Paired phones.' }));
};

// ---------- phone ----------
$('#phone-files').onchange = (e) => { sendToPC([...e.target.files].map((f) => ({ file: f, path: f.name }))); e.target.value = ''; };
dropZone($('#phone-drop'), sendToPC);
$('#from-pc-all').onclick = () => openSrc('out');

async function loadFromPC() {
  const list = await api('/api/ls?s=out').catch(() => []);
  list.sort((a, b) => Date.parse(b.mod) - Date.parse(a.mod));
  $('#from-pc').hidden = !list.length;
  $('#from-pc-list').replaceChildren(...list.slice(0, 5).map((e) => h('li', {},
    thumb(e, e.name, 'out'), h('div', { class: 'grow' }, h('b', { textContent: e.name, style: 'display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap' }), h('small', { class: 'sub', textContent: e.dir ? 'Folder' : fmt(e.size) })),
    h('a', { class: 'icon-btn', href: fileURL('out', e.name), download: e.dir ? `${e.name}.zip` : e.name, 'aria-label': `Download ${e.name}` }, ic('download')))));
}

$('#login-form').onsubmit = async (e) => {
  e.preventDefault();
  try { await api('/api/login', { code: $('#code').value }); location.reload(); } catch (err) { toast(err.message); }
};

// ---------- add to home screen (phones) ----------
// A real "Install app" needs HTTPS, which home-network addresses can't get, so we guide the
// browser's own "Add to Home Screen". On iPhone that opens full screen like an app.
let installEvt;
window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); installEvt = e; $('#install-btn').hidden = false; });
function setupInstall() {
  const standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone;
  let st = {};
  try { st = JSON.parse(localStorage.getItem('pathau-install') || '{}'); } catch { /* private mode */ }
  if (me.admin || standalone || st.done || Date.now() - (st.later || 0) < 7 * 864e5) return;
  const ios = /iPhone|iPad/.test(navigator.userAgent);
  $('#install-how').textContent = ios
    ? 'Tap Share at the bottom of Safari, then "Add to Home Screen". Pathau then opens full screen, like an app.'
    : 'Tap ⋮ at the top right of Chrome, then "Add to Home screen". Next time just tap the Pathau icon.';
  $('#install').hidden = false;
  const save = (v) => { try { localStorage.setItem('pathau-install', JSON.stringify(v)); } catch { /* ignore */ } $('#install').hidden = true; };
  $('#install-done').onclick = () => save({ done: true });
  $('#install-later').onclick = () => save({ later: Date.now() });
  $('#install-btn').onclick = async () => { installEvt?.prompt(); if ((await installEvt?.userChoice)?.outcome === 'accepted') save({ done: true }); };
}

// ---------- start ----------
(async () => {
  me = await api('/api/me');
  for (const el of $$('.pc-name')) el.textContent = me.name;
  if (!me.authed) {
    $('#login').hidden = false;
    if (me.pending) { // paired with the passcode, now waiting for Allow on the PC
      $('#login-form').hidden = true; $('#waiting').hidden = false;
      setInterval(async () => { if ((await api('/api/me').catch(() => ({}))).authed) location.reload(); }, 1500);
    }
    return;
  }
  document.body.classList.toggle('admin', me.admin);
  $('#app').hidden = false;
  if (/iPhone|iPad/.test(navigator.userAgent)) $('.ios-only').hidden = false;
  if (!me.admin && !navigator.clipboard?.readText) $('#paste').hidden = true; // plain-http pages can't read the phone clipboard
  $('#sort').value = prefs.sort || 'date-desc';
  setView(prefs.view || (me.admin ? 'grid' : 'list'));
  go();
  setupInstall();

  (await api('/api/texts')).forEach((m) => addText(m, false));
  const es = new EventSource('/api/events');
  es.addEventListener('text', (e) => addText(JSON.parse(e.data), true));
  es.addEventListener('rx', (e) => onRx(JSON.parse(e.data)));
  es.addEventListener('xfer', (e) => onXfer(JSON.parse(e.data)));
  es.addEventListener('outbox', (e) => {
    if (files.src === 'out') loadFiles();
    if (me.admin) return;
    loadFromPC();
    if (JSON.parse(e.data).name) { toast(`${me.name} sent you a file`); if (!onPage('files')) bump('#files-badge'); }
  });
  es.onopen = () => { $('.dot').classList.remove('off'); $('#me-status').textContent = 'Online'; $('#offline').hidden = true; };
  es.onerror = () => {
    $('.dot').classList.add('off'); $('#me-status').textContent = 'Reconnecting…';
    if (!me.admin) $('#offline').hidden = false;
    if (es.readyState === EventSource.CLOSED) location.reload(); // e.g. this phone was disconnected
  };

  await loadSources();
  loadFiles();
  if (me.admin) { refresh(); setInterval(refresh, 2000); } else loadFromPC();
})();
