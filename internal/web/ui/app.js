'use strict';

// ---------- helpers ----------
const $view = document.getElementById('view');

async function api(method, path, body, raw) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    if (raw) { opt.body = body; opt.headers['Content-Type'] = 'text/plain'; }
    else { opt.body = JSON.stringify(body); opt.headers['Content-Type'] = 'application/json'; }
  }
  const r = await fetch(path, opt);
  const text = await r.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error((data && data.error) || text || r.statusText);
  return data;
}
const get = p => api('GET', p);

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'html') el.innerHTML = v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat(Infinity)) {
    if (k === undefined || k === null || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
}
const svgNS = 'http://www.w3.org/2000/svg';
function s(tag, attrs) { const el = document.createElementNS(svgNS, tag); for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v); return el; }

function toast(msg, err) {
  const d = h('div', { class: err ? 'err' : '' }, msg);
  document.getElementById('toast').append(d);
  setTimeout(() => d.remove(), err ? 6000 : 3000);
}
async function act(fn, ok) {
  try { const r = await fn(); if (ok) toast(typeof ok === 'function' ? ok(r) : ok); return r; }
  catch (e) { toast(e.message, true); throw e; }
}
function copy(text) { navigator.clipboard?.writeText(text).then(() => toast('Copied')); }
const fmtTime = d => new Date(d).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const fmtDate = d => new Date(d).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
function fmtBytes(b) { const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0; while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; } return b.toFixed(i ? 1 : 0) + ' ' + u[i]; }
function fmtKbps(k) { return k >= 1000 ? (k / 1000).toFixed(1) + ' Mbit/s' : Math.round(k) + ' kbit/s'; }
function fmtDur(sec) { sec = Math.floor(sec); const h_ = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60); return h_ ? `${h_}h ${m}m` : m ? `${m}m ${sec % 60}s` : `${sec}s`; }
function fmtBER(b) { return b < 0 ? '–' : b === 0 ? '0' : b.toExponential(1); }
function field(label, input) { return h('label', { class: 'f' }, label, input); }
function input(attrs) { return h('input', attrs); }
function select(opts, value, attrs) {
  return h('select', attrs || {}, opts.map(o => { const [v, t] = Array.isArray(o) ? o : [o, o]; return h('option', { value: v, selected: String(v) === String(value) }, t); }));
}
const levelClass = pct => pct < 0 ? '' : pct < 35 ? 'bad' : pct < 55 ? 'warn' : pct < 70 ? 'ok' : 'good';
const qualityClass = { excellent: 'b-good', good: 'b-ok', fair: 'b-warn', poor: 'b-bad', 'no-lock': 'b-bad', idle: '' };
const stateBadge = st => h('span', { class: 'badge ' + ({ streaming: 'b-good', tuning: 'b-accent', nosignal: 'b-bad' }[st] || '') }, st === 'nosignal' ? 'no signal' : st);

let timer = null;
function every(ms, fn) { clearInterval(timer); fn(); timer = setInterval(fn, ms); }

// ---------- router ----------
const routes = {};
async function route() {
  clearInterval(timer);
  const name = (location.hash || '#dashboard').slice(1).split('/')[0];
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('active', a.getAttribute('href') === '#' + name));
  $view.replaceChildren();
  try { await (routes[name] || routes.dashboard)(); }
  catch (e) { $view.append(h('div', { class: 'card' }, h('b', {}, 'Error: '), e.message)); }
}
window.addEventListener('hashchange', route);

// ---------- dashboard ----------
function meter(label, pct, main, unit, sub) {
  const cls = levelClass(pct);
  return h('div', { class: 'meter' },
    h('div', { class: 'lbl' }, h('span', {}, label), h('span', {}, sub || '')),
    h('div', { class: 'val' }, main, unit ? h('small', {}, unit) : null),
    h('div', { class: 'bar' }, h('i', { style: `width:${Math.max(0, Math.min(100, pct))}%;background:var(--${cls || 'idle'})` })));
}

function sparkline(hist) {
  const W = 300, H = 56;
  const svg = s('svg', { class: 'spark', viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none' });
  if (!hist || hist.length < 2) return svg;
  const n = hist.length, x = i => (W * (i + (300 - n))) / 299;
  hist.forEach((p, i) => { if (!p.locked) svg.append(s('rect', { class: 'gap', x: x(i) - 0.5, y: 0, width: W / 299 + 1, height: H })); });
  const snrPct = p => p.snrIsDb ? p.snr * 100 / 30 : p.snr;
  const line = (f, cls) => svg.append(s('polyline', { class: cls, fill: 'none', 'stroke-width': 1.6, 'vector-effect': 'non-scaling-stroke',
    points: hist.map((p, i) => `${x(i).toFixed(1)},${(H - 2 - Math.max(0, Math.min(100, f(p))) * (H - 4) / 100).toFixed(1)}`).join(' ') }));
  line(p => p.strength, 'str');
  line(snrPct, 'snr');
  return svg;
}

function tunerCard(t) {
  const sig = t.signal || {};
  const q = t.state === 'idle' ? 'idle' : quality(t);
  const snrDb = sig.snrDb, dbm = sig.strengthDbm;
  const snrPct = snrDb != null ? snrDb * 100 / 30 : sig.snrPct;
  const card = h('div', { class: 'card' },
    h('div', { class: 'tuner-head' },
      h('div', {},
        h('div', { class: 'name' }, t.config.name || t.name, ' ', h('span', { class: 'muted small mono' }, t.key)),
        h('div', { class: 'small muted' }, t.state === 'idle' ? (t.config.enabled ? 'Idle' : 'Disabled') : t.mux, ' ', t.state !== 'idle' ? stateBadge(t.state) : null)),
      h('span', { class: 'badge quality ' + (qualityClass[q] || '') }, q.replace('-', ' '))));
  if (t.state === 'idle') {
    card.append(h('div', { class: 'small muted' }, (t.delsys || []).join(' · ') || ''));
    return card;
  }
  card.append(h('div', { class: 'meters' },
    meter('Signal strength', sig.strengthPct, sig.strengthPct < 0 ? '–' : Math.round(sig.strengthPct), sig.strengthPct < 0 ? '' : '%', dbm != null ? dbm.toFixed(1) + ' dBm' : ''),
    meter('Signal quality (SNR)', snrPct, snrDb != null ? snrDb.toFixed(1) : (sig.snrPct < 0 ? '–' : Math.round(sig.snrPct)), snrDb != null ? 'dB' : (sig.snrPct < 0 ? '' : '%'), sig.source || '')));
  card.append(h('div', { class: 'stats' },
    h('div', { class: 'stat' }, h('div', { class: 'k' }, 'Bit error rate'), h('div', { class: 'v' }, fmtBER(sig.ber))),
    h('div', { class: 'stat' }, h('div', { class: 'k' }, 'Uncorrected'), h('div', { class: 'v', style: sig.uncDelta > 0 ? 'color:var(--bad)' : '' }, sig.unc ?? 0)),
    h('div', { class: 'stat' }, h('div', { class: 'k' }, 'Stream errors'), h('div', { class: 'v', style: t.ccErrors > 0 ? 'color:var(--warn)' : '' }, t.ccErrors + t.teiErrors)),
    h('div', { class: 'stat' }, h('div', { class: 'k' }, 'Mux bitrate'), h('div', { class: 'v' }, fmtKbps(t.kbps)))));
  card.append(sparkline(t.history), h('div', { class: 'legend' },
    h('span', {}, h('i', { style: 'background:var(--accent)' }), 'SNR'),
    h('span', {}, h('i', { style: 'background:var(--muted)' }), 'Strength'),
    h('span', {}, h('i', { style: 'background:var(--bad);height:8px;opacity:.4' }), 'No lock'),
    h('span', { class: 'spacer' }), h('span', {}, 'last 5 min')));
  if (t.state === 'nosignal' || t.state === 'tuning' && t.outageSeconds > 0) {
    card.append(h('div', { class: 'outage' }, `Signal lost for ${fmtDur(t.outageSeconds)} — clients are kept connected, re-tuned ${t.retunes}×. `, t.lastError || ''));
  }
  if (t.subscriptions?.length) {
    card.append(h('div', { class: 'subs' }, t.subscriptions.map(sub => {
      const tx = sub.transcode;
      return h('div', { class: 'sub' },
        h('span', { class: 'badge b-accent' }, sub.weight >= 500 ? 'DVR' : sub.weight >= 100 ? 'Live' : sub.weight >= 10 ? 'Scan' : 'EPG'),
        h('div', {},
          h('div', {}, h('b', {}, sub.service || sub.name), ' → ', sub.client || sub.name),
          h('div', { class: 'tx' }, sub.profile || 'raw', ' · ', fmtBytes(sub.bytesOut), ' · ', fmtDur((Date.now() - new Date(sub.started)) / 1000),
            sub.failovers ? ` · ${sub.failovers} failover(s)` : '', sub.dropped ? ` · ${sub.dropped} pkts dropped` : '',
            tx ? ` · ${tx.encoder}${tx.hwDecode ? '+NVDEC' : ''} ${fmtKbps(tx.outKbps)} (target ${fmtKbps(tx.targetKbps)}) ${tx.fps ? tx.fps.toFixed(0) + ' fps' : ''} ${tx.speed || ''}${tx.restarts ? ' · restarts ' + tx.restarts : ''}` : '')),
        h('span', { class: 'spacer' }),
        h('button', { class: 'sm danger', title: 'Stop this subscription', onclick: () => act(() => api('DELETE', '/api/subscriptions/' + sub.id), 'Stopped') }, '✕'));
    })));
  }
  if (t.virtual) {
    card.append(h('div', { class: 'row', style: 'margin-top:10px' }, h('span', { class: 'small muted' }, 'Test recovery:'),
      h('button', { class: 'sm', onclick: () => act(() => api('POST', '/api/tuners-drop/' + t.key + '?seconds=5'), 'Simulating 5 s signal drop') }, 'Drop 5 s'),
      h('button', { class: 'sm', onclick: () => act(() => api('POST', '/api/tuners-drop/' + t.key + '?seconds=20'), 'Simulating 20 s signal drop') }, 'Drop 20 s')));
  }
  return card;
}

function quality(t) {
  const sg = t.signal || {};
  if (!sg.locked) return 'no-lock';
  const snr = sg.snrDb != null ? sg.snrDb * 100 / 30 : sg.snrPct;
  if (sg.uncDelta > 0 || sg.ber > 1e-3) return 'poor';
  if (snr >= 0 && snr < 40) return 'fair';
  if (snr >= 0 && snr < 60) return 'good';
  return 'excellent';
}

function gpuCard(sys) {
  const tc = sys.transcode;
  const card = h('div', { class: 'card' }, h('h2', {}, 'Transcoding hardware'));
  card.append(h('div', { class: 'small' }, tc.ffmpegOk ? tc.version : h('span', { style: 'color:var(--bad)' }, 'ffmpeg not found (' + tc.ffmpeg + ')')));
  card.append(h('div', { class: 'row', style: 'margin:8px 0' }, (tc.encoders || []).map(e => h('span', { class: 'badge ' + (e.includes('nvenc') ? 'b-good' : '') }, e)),
    tc.cuda ? h('span', { class: 'badge b-good' }, 'CUDA decode') : h('span', { class: 'badge' }, 'no CUDA'),
    tc.cudaFilters ? h('span', { class: 'badge b-good' }, 'CUDA scale/deinterlace') : null));
  if (tc.gpus?.length) {
    tc.gpus.forEach(g => card.append(h('div', { style: 'margin-top:10px' },
      h('div', {}, h('b', {}, g.name), h('span', { class: 'muted small' }, ` · ${g.tempC}°C · ${g.encoderSessions} encode session(s)`)),
      h('div', { class: 'meters', style: 'margin-top:6px' },
        meter('Encoder', 100 - g.encUtil, Math.round(g.encUtil), '%'),
        meter('Decoder', 100 - g.decUtil, Math.round(g.decUtil), '%')),
      h('div', { class: 'small muted', style: 'margin-top:4px' }, `GPU ${g.util}% · memory ${Math.round(g.memUsedMb)} / ${Math.round(g.memTotalMb)} MB`))));
  } else {
    card.append(h('div', { class: 'small muted' }, tc.smiError || 'No NVIDIA GPU detected'));
  }
  return card;
}

routes.dashboard = async () => {
  const head = h('div', { class: 'row' }, h('h1', {}, 'Dashboard'), h('span', { class: 'spacer' }), h('span', { class: 'small muted', id: 'dash-sum' }));
  const tunersEl = h('div', { class: 'grid' });
  const side = h('div', { class: 'grid', style: 'margin-top:16px' });
  $view.append(head, tunersEl, side);
  let sysAt = 0, sys = null;
  every(1000, async () => {
    try {
      const st = await get('/api/status');
      if (Date.now() - sysAt > 5000) { sys = await get('/api/system'); sysAt = Date.now(); }
      if (!st.tuners?.length) tunersEl.replaceChildren(h('div', { class: 'card empty' }, 'No tuners found. Plug in a DVB adapter and use Tuners → Rediscover, or enable virtual tuners in Settings for testing.'));
      else tunersEl.replaceChildren(...st.tuners.map(tunerCard));
      const c = st.counts;
      document.getElementById('dash-sum').textContent =
        `${c.channels} channels · ${c.services} services · ${c.muxes} muxes · ${st.epg.events} EPG events` +
        (st.scan.active?.length || st.scan.queued?.length ? ` · scanning ${st.scan.active.length}, queued ${st.scan.queued.length}` : '');
      side.replaceChildren(gpuCard(sys));
    } catch (e) { /* keep last view on transient errors */ }
  });
};

// ---------- tuners ----------
routes.tuners = async () => {
  const [tuners, nets] = await Promise.all([get('/api/tuners'), get('/api/networks')]);
  $view.append(h('div', { class: 'row' }, h('h1', {}, 'Tuners'), h('span', { class: 'spacer' }),
    h('button', { onclick: () => act(() => api('POST', '/api/tuners-rediscover'), r => `${r?.length || 0} hardware frontend(s)`).then(route) }, 'Rediscover hardware')));
  if (!tuners?.length) $view.append(h('div', { class: 'card empty' }, 'No tuners.'));
  for (const t of tuners || []) {
    const c = t.config;
    const name = input({ value: c.name });
    const en = input({ type: 'checkbox', checked: c.enabled });
    const prio = input({ type: 'number', value: c.priority, style: 'width:80px' });
    const to = input({ type: 'number', value: c.tuneTimeout || 5, style: 'width:80px' });
    const netBoxes = nets.filter(n => (n.type === 'virtual') === t.virtual).map(n => {
      const cb = input({ type: 'checkbox', checked: (c.networks || []).includes(n.id), 'data-id': n.id });
      return h('label', { class: 'row small' }, cb, n.name);
    });
    const satNets = nets.filter(n => n.type === 'dvbs' && !t.virtual);
    const sat = satNets.map(n => {
      const cur = (c.sat || {})[n.id] || { lnb: 'universal', lofLow: 9750000, lofHigh: 10600000, switch: 11700000, diseqcPort: 0 };
      const lnb = select([['universal', 'Universal (9750/10600)'], ['single', 'Single LOF'], ['circular', 'Circular (10750)'], ['none', 'None (IF = frequency)']], cur.lnb);
      const lo = input({ type: 'number', value: cur.lofLow / 1000, style: 'width:100px' });
      const hi = input({ type: 'number', value: cur.lofHigh / 1000, style: 'width:100px' });
      const sw = input({ type: 'number', value: cur.switch / 1000, style: 'width:100px' });
      const dq = select([[0, 'None'], [1, 'Port A'], [2, 'Port B'], [3, 'Port C'], [4, 'Port D']], cur.diseqcPort);
      lnb.onchange = () => { if (lnb.value === 'circular') { lo.value = 10750; } };
      return { id: n.id, el: h('div', { class: 'form', style: 'margin-top:8px' }, h('b', {}, n.name), field('LNB', lnb), field('LOF low (MHz)', lo), field('LOF high (MHz)', hi), field('Switch (MHz)', sw), field('DiSEqC 1.0', dq)),
        val: () => ({ lnb: lnb.value, lofLow: +lo.value * 1000, lofHigh: +hi.value * 1000, switch: +sw.value * 1000, diseqcPort: +dq.value }) };
    });
    const save = async () => {
      const body = { ...c, name: name.value, enabled: en.checked, priority: +prio.value, tuneTimeout: +to.value,
        networks: netBoxes.map(l => l.querySelector('input')).filter(i => i.checked).map(i => i.dataset.id), sat: {} };
      sat.forEach(x => body.sat[x.id] = x.val());
      await act(() => api('PUT', '/api/tuners/' + t.key, body), 'Saved');
    };
    $view.append(h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h2', {}, t.name), h('span', { class: 'mono muted' }, t.key), (t.delsys || []).map(d => h('span', { class: 'badge' }, d))),
      h('div', { class: 'form' }, field('Name', name), field('Priority (higher first)', prio), field('Tune timeout (s)', to), h('label', { class: 'row' }, en, 'Enabled')),
      h('div', { style: 'margin-top:12px' }, h('div', { class: 'small muted' }, 'Networks (none ticked = any compatible network)'), h('div', { class: 'row' }, netBoxes.length ? netBoxes : h('span', { class: 'small muted' }, 'No compatible networks yet'))),
      sat.map(x => x.el),
      h('div', { class: 'row', style: 'margin-top:12px' }, h('button', { class: 'primary', onclick: save }, 'Save'))));
  }
};

// ---------- networks & muxes ----------
const delsysFor = { dvbt: ['DVB-T', 'DVB-T2'], dvbc: ['DVB-C'], dvbs: ['DVB-S', 'DVB-S2'], atsc: ['ATSC'], virtual: ['VIRTUAL'] };
const scanBadge = st => h('span', { class: 'badge ' + ({ ok: 'b-good', fail: 'b-bad', scanning: 'b-accent', pending: 'b-warn' }[st] || '') }, st || 'new');

routes.networks = async () => {
  const nets = await get('/api/networks');
  $view.append(h('h1', {}, 'Networks & muxes'));
  const nName = input({ placeholder: 'e.g. Freeview' });
  const nType = select([['dvbt', 'DVB-T/T2 (terrestrial)'], ['dvbc', 'DVB-C (cable)'], ['dvbs', 'DVB-S/S2 (satellite)'], ['atsc', 'ATSC'], ['virtual', 'Virtual (TS files, testing)']], 'dvbt');
  const nDisc = input({ type: 'checkbox', checked: true });
  $view.append(h('div', { class: 'card' }, h('h2', {}, 'Add network'),
    h('div', { class: 'form' }, field('Name', nName), field('Type', nType), h('label', { class: 'row' }, nDisc, 'Discover muxes from NIT'),
      h('button', { class: 'primary', onclick: () => act(() => api('POST', '/api/networks', { name: nName.value, type: nType.value, discoverMuxes: nDisc.checked }), 'Network added').then(route) }, 'Add'))));
  for (const n of nets || []) $view.append(await networkCard(n));
  if (!nets?.length) $view.append(h('div', { class: 'card empty' }, 'Add a network, then import a scan table or add muxes manually.'));
};

async function networkCard(n) {
  const muxes = await get('/api/muxes?network=' + n.id);
  const card = h('div', { class: 'card' });
  card.append(h('div', { class: 'row' }, h('h2', {}, n.name), h('span', { class: 'badge' }, n.type), n.networkId ? h('span', { class: 'muted small' }, 'NID ' + n.networkId) : null,
    h('span', { class: 'muted small' }, `${n.muxes} muxes · ${n.services} services`), h('span', { class: 'spacer' }),
    h('button', { onclick: () => act(() => api('POST', `/api/networks/${n.id}/scan`), r => `Queued ${r.queued} muxes`) }, 'Scan all'),
    h('button', { class: 'danger', onclick: () => confirm(`Delete ${n.name} with its muxes, services and channel mappings?`) && act(() => api('DELETE', '/api/networks/' + n.id), 'Deleted').then(route) }, 'Delete')));

  // import scan table
  if (n.type !== 'virtual') {
    const ta = h('textarea', { placeholder: '[CHANNEL]\n\tDELIVERY_SYSTEM = DVBT2\n\tFREQUENCY = 506000000\n\tBANDWIDTH_HZ = 8000000\n\t...\n\nor legacy lines:  T 506000000 8MHz AUTO AUTO QAM64 8k 1/32 NONE' });
    const fileSel = select([['', '— load a dtv-scan-tables file —']]);
    get('/api/scanfiles').then(files => (files || []).filter(f => f.includes('/' + ({ dvbt: 'dvb-t', dvbc: 'dvb-c', dvbs: 'dvb-s', atsc: 'atsc' }[n.type]) + '/')).forEach(f => fileSel.append(h('option', { value: f }, f.split('/').slice(-1)[0]))));
    fileSel.onchange = async () => { if (fileSel.value) ta.value = await fetch('/api/scanfile?path=' + encodeURIComponent(fileSel.value)).then(r => r.text()); };
    card.append(h('details', { style: 'margin:12px 0' }, h('summary', {}, 'Import initial tuning data'),
      h('div', { style: 'margin-top:8px' }, h('div', { class: 'row', style: 'margin-bottom:8px' }, fileSel), ta,
        h('button', { class: 'primary', style: 'margin-top:8px', onclick: () => act(() => api('POST', `/api/networks/${n.id}/import`, ta.value, true), r => `Parsed ${r.parsed}, added ${r.added} muxes (scanning)`).then(route) }, 'Import & scan'))));
  }

  // add mux
  const ds = delsysFor[n.type] || ['DVB-T'];
  const f = {
    delsys: select(ds, ds[ds.length - 1]), freq: input({ type: 'number', step: 'any', placeholder: n.type === 'dvbs' ? 'MHz e.g. 10847' : 'MHz e.g. 506' }),
    bw: select([[8000000, '8 MHz'], [7000000, '7 MHz'], [6000000, '6 MHz'], [5000000, '5 MHz'], [1712000, '1.7 MHz']], 8000000),
    sr: input({ type: 'number', placeholder: 'kS/s e.g. 23000' }), mod: select(['AUTO', 'QPSK', '8PSK', '16APSK', '32APSK', 'QAM/16', 'QAM/32', 'QAM/64', 'QAM/128', 'QAM/256', '8VSB'], 'AUTO'),
    fec: select(['AUTO', '1/2', '2/3', '3/4', '3/5', '4/5', '5/6', '7/8', '8/9', '9/10'], 'AUTO'), pol: select(['H', 'V', 'L', 'R'], 'H'),
    plp: input({ type: 'number', value: -1, style: 'width:80px' }), file: input({ placeholder: '/path/to/capture.ts' }),
  };
  const addMux = () => {
    const t = { delsys: f.delsys.value, frequency: Math.round(parseFloat(f.freq.value || 0) * 1000), bandwidth: +f.bw.value, symbolRate: (+f.sr.value || 0) * 1000,
      modulation: f.mod.value, fec: f.fec.value, polarization: n.type === 'dvbs' ? f.pol.value : '', streamId: +f.plp.value, rolloff: 'AUTO', pilot: 'AUTO' };
    return act(() => api('POST', '/api/muxes', { networkId: n.id, tuning: t, file: f.file.value, enabled: true }), 'Mux added, scanning').then(route);
  };
  const addForm = n.type === 'virtual'
    ? h('div', { class: 'form' }, field('Transport stream file', f.file), h('button', { class: 'primary', onclick: addMux }, 'Add mux'))
    : h('div', { class: 'form' }, field('System', f.delsys), field('Frequency', f.freq),
      n.type === 'dvbt' ? field('Bandwidth', f.bw) : field('Symbol rate', f.sr),
      n.type === 'dvbs' ? field('Polarisation', f.pol) : null, field('Modulation', f.mod), field('FEC', f.fec),
      n.type !== 'dvbc' ? field('PLP / stream id', f.plp) : null, h('button', { class: 'primary', onclick: addMux }, 'Add mux'));
  card.append(h('details', { style: 'margin:12px 0' }, h('summary', {}, 'Add mux manually'), h('div', { style: 'margin-top:8px' }, addForm)));

  if (muxes?.length) {
    card.append(h('div', { class: 'tablewrap' }, h('table', {},
      h('tr', {}, h('th', {}, 'Mux'), h('th', {}, 'TSID/ONID'), h('th', {}, 'Services'), h('th', {}, 'Scan'), h('th', {}, 'Last scan'), h('th', {}, '')),
      muxes.map(m => h('tr', {},
        h('td', {}, m.label, m.enabled ? null : h('span', { class: 'badge', style: 'margin-left:6px' }, 'disabled')),
        h('td', { class: 'mono' }, m.tsid ? `${m.tsid} / ${m.onid}` : '–'),
        h('td', {}, m.services),
        h('td', {}, scanBadge(m.scanStatus), m.scanError ? h('div', { class: 'small muted' }, m.scanError) : null),
        h('td', { class: 'small muted' }, m.lastScan && !m.lastScan.startsWith('0001') ? fmtDate(m.lastScan) : '–'),
        h('td', { class: 'row' },
          h('button', { class: 'sm', onclick: () => act(() => api('POST', `/api/muxes/${m.id}/scan`), 'Scan queued') }, 'Scan'),
          h('button', { class: 'sm', onclick: () => act(() => api('PUT', '/api/muxes/' + m.id, { ...m, enabled: !m.enabled }), m.enabled ? 'Disabled' : 'Enabled').then(route) }, m.enabled ? 'Disable' : 'Enable'),
          h('button', { class: 'sm danger', onclick: () => confirm('Delete this mux and its services?') && act(() => api('DELETE', '/api/muxes/' + m.id), 'Deleted').then(route) }, '✕')))))));
  }
  return card;
}

// ---------- services ----------
routes.services = async () => {
  const svcs = await get('/api/services') || [];
  const q = input({ placeholder: 'Filter…' });
  const kind = select([['', 'All kinds'], ['tv', 'TV'], ['radio', 'Radio'], ['other', 'Other']], '');
  const unmapped = input({ type: 'checkbox' });
  const optRadio = input({ type: 'checkbox' }), optFTA = input({ type: 'checkbox', checked: true }), optMerge = input({ type: 'checkbox', checked: true });
  const tbody = h('tbody');
  const selected = new Set();
  const render = () => {
    const f = q.value.toLowerCase();
    tbody.replaceChildren(...svcs.filter(sv => (!f || (sv.name + sv.provider + sv.mux).toLowerCase().includes(f)) && (!kind.value || sv.kind === kind.value) && (!unmapped.checked || !sv.mappedTo?.length))
      .map(sv => h('tr', {},
        h('td', {}, input({ type: 'checkbox', checked: selected.has(sv.id), onchange: e => e.target.checked ? selected.add(sv.id) : selected.delete(sv.id) })),
        h('td', {}, h('b', {}, sv.name), h('div', { class: 'small muted' }, sv.provider)),
        h('td', {}, h('span', { class: 'badge' }, sv.kind), sv.scrambled ? h('span', { class: 'badge b-warn', style: 'margin-left:4px' }, 'scrambled') : null),
        h('td', {}, sv.lcn || ''),
        h('td', { class: 'small' }, sv.mux, h('div', { class: 'muted' }, sv.network)),
        h('td', { class: 'small mono' }, (sv.streams || []).filter(x => x.kind).map(x => x.kind + (x.lang ? '/' + x.lang : '')).join(' ')),
        h('td', { class: 'small' }, (sv.mappedTo || []).join(', ') || h('span', { class: 'muted' }, '—')),
        h('td', {}, input({ type: 'checkbox', checked: sv.enabled, title: 'Enabled', onchange: e => act(() => api('PUT', '/api/services/' + sv.id, { enabled: e.target.checked })) })),
        h('td', {}, h('a', { href: '/stream/service/' + sv.id, target: '_blank', class: 'small' }, 'stream')))));
  };
  [q, kind, unmapped].forEach(el => el.addEventListener('input', render));
  const map = ids => act(() => api('POST', '/api/map', { serviceIds: ids, includeRadio: optRadio.checked, skipScrambled: optFTA.checked, mergeByName: optMerge.checked }),
    r => `Created ${r.created} channels, added ${r.merged} failover services`).then(route);
  $view.append(h('h1', {}, 'Services'),
    h('div', { class: 'card' }, h('div', { class: 'row' }, q, kind, h('label', { class: 'row small' }, unmapped, 'Unmapped only'), h('span', { class: 'spacer' }),
      h('label', { class: 'row small' }, optRadio, 'Include radio'), h('label', { class: 'row small' }, optFTA, 'Skip scrambled'),
      h('label', { class: 'row small', title: 'A service with the same name as an existing channel is added to that channel as a backup source' }, optMerge, 'Same name → failover'),
      h('button', { onclick: () => map([...selected]) }, 'Map selected'), h('button', { class: 'primary', onclick: () => map([]) }, 'Map all'))),
    h('div', { class: 'card tight tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}), h('th', {}, 'Service'), h('th', {}, 'Kind'), h('th', {}, 'LCN'), h('th', {}, 'Mux'), h('th', {}, 'Streams'), h('th', {}, 'Channel'), h('th', {}, 'On'), h('th', {}))), tbody)));
  render();
  if (!svcs.length) tbody.append(h('tr', {}, h('td', { colspan: 9, class: 'empty' }, 'No services yet — scan a mux first.')));
};

// ---------- channels ----------
routes.channels = async () => {
  const [chans, profs, xm, svcs] = await Promise.all([get('/api/channels'), get('/api/profiles'), get('/api/epg/xmltv'), get('/api/services')]);
  const dl = h('datalist', { id: 'xmltv-ids' }, (xm.channels || []).map(c => h('option', { value: c.id }, (c.names || []).join(' / '))));
  const svcName = Object.fromEntries((svcs || []).map(s_ => [s_.id, `${s_.name} @ ${s_.mux}`]));
  $view.append(dl, h('div', { class: 'row' }, h('h1', {}, 'Channels'), h('span', { class: 'spacer' }),
    h('button', { onclick: () => act(() => api('POST', '/api/epg/automap'), r => `Matched ${r.mapped} channels to XMLTV`).then(route) }, 'Auto-match XMLTV guide')));
  const tbody = h('tbody');
  for (const c of chans || []) {
    const num = input({ type: 'number', value: c.number, style: 'width:70px' });
    const name = input({ value: c.name, style: 'width:180px' });
    const en = input({ type: 'checkbox', checked: c.enabled });
    const epgId = input({ value: c.epgId || '', list: 'xmltv-ids', placeholder: 'OTA (EIT)', style: 'width:150px' });
    const prof = select([['', 'Default']].concat((profs || []).map(p => [p.id, p.name])), c.profile || '');
    const icon = input({ value: c.icon || '', placeholder: 'https://…/logo.png', style: 'width:150px' });
    let services = [...c.services];
    const svcList = h('div', { class: 'small' });
    const renderSvcs = () => svcList.replaceChildren(...services.map((id, i) => h('div', { class: 'row', style: 'gap:4px' },
      h('span', { class: 'badge' + (i === 0 ? ' b-accent' : '') }, i === 0 ? 'primary' : 'backup ' + i), svcName[id] || id,
      i > 0 ? h('button', { class: 'sm', title: 'Move up', onclick: () => { [services[i - 1], services[i]] = [services[i], services[i - 1]]; renderSvcs(); } }, '↑') : null,
      h('button', { class: 'sm danger', onclick: () => { services.splice(i, 1); renderSvcs(); } }, '✕'))),
      h('select', { class: 'small', onchange: e => { if (e.target.value) { services.push(e.target.value); renderSvcs(); } } },
        h('option', { value: '' }, '+ add backup service…'), (svcs || []).filter(x => !services.includes(x.id)).map(x => h('option', { value: x.id }, `${x.name} @ ${x.mux}`))));
    renderSvcs();
    const save = () => act(() => api('PUT', '/api/channels/' + c.id, { ...c, number: +num.value, name: name.value, enabled: en.checked, epgId: epgId.value.trim(), profile: prof.value, icon: icon.value.trim(), services }), 'Saved');
    tbody.append(h('tr', {},
      h('td', {}, num), h('td', {}, name, c.now ? h('div', { class: 'small muted' }, 'Now: ', c.now.title) : null), h('td', {}, en),
      h('td', {}, svcList), h('td', {}, epgId), h('td', {}, prof), h('td', {}, icon),
      h('td', {}, h('div', { class: 'row', style: 'gap:4px' },
        h('button', { class: 'sm primary', onclick: save }, 'Save'),
        h('button', { class: 'sm', title: c.streamUrl, onclick: () => copy(c.streamUrl) }, 'URL'),
        h('button', { class: 'sm danger', onclick: () => confirm('Delete channel ' + c.name + '?') && act(() => api('DELETE', '/api/channels/' + c.id), 'Deleted').then(route) }, '✕')))));
  }
  $view.append(h('div', { class: 'card tight tablewrap' }, h('table', {},
    h('thead', {}, h('tr', {}, ['#', 'Name', 'On', 'Services (failover order)', 'XMLTV id', 'Profile', 'Icon', ''].map(t => h('th', {}, t)))), tbody)));
  if (!chans?.length) tbody.append(h('tr', {}, h('td', { colspan: 8, class: 'empty' }, 'No channels yet — map services on the Services page.')));
};

// ---------- guide ----------
routes.guide = async () => {
  const hours = 6, pxPerMin = 5;
  const data = await get('/api/epg/grid?hours=' + hours);
  const from = new Date(data.from).getTime();
  const width = hours * 60 * pxPerMin;
  const xs = t => (new Date(t).getTime() - from) / 60000 * pxPerMin;
  $view.append(h('div', { class: 'row' }, h('h1', {}, 'Guide'), h('span', { class: 'spacer' }),
    h('button', { onclick: () => act(() => api('POST', '/api/epg/xmltv/refresh'), 'XMLTV refresh started') }, 'Refresh XMLTV')));
  const g = h('div', { class: 'card tight guide' });
  const times = h('div', { class: 'g-times' }, h('div', { class: 'g-ch small muted' }, ''), h('div', { class: 'g-track', style: `width:${width}px;height:26px` },
    Array.from({ length: hours * 2 }, (_, i) => h('div', { class: 'small muted', style: `position:absolute;left:${i * 30 * pxPerMin + 6}px;top:4px` }, fmtTime(from + i * 1800000)))));
  g.append(times);
  const nowX = xs(Date.now());
  for (const c of data.channels || []) {
    const track = h('div', { class: 'g-track', style: `width:${width}px` }, h('div', { class: 'g-now', style: `left:${nowX}px` }));
    for (const e of c.events || []) {
      const l = Math.max(0, xs(e.start)), r = Math.min(width, xs(e.stop));
      if (r <= l) continue;
      const isNow = new Date(e.start) <= Date.now() && new Date(e.stop) > Date.now();
      track.append(h('div', { class: 'g-ev' + (isNow ? ' now' : ''), style: `left:${l}px;width:${r - l - 2}px`, title: `${e.title}\n${fmtTime(e.start)}–${fmtTime(e.stop)}\n${e.subtitle || ''}\n${e.description || ''}` },
        h('b', {}, e.title), h('span', { class: 'muted' }, `${fmtTime(e.start)} ${e.episodeText || e.subtitle || ''}`)));
    }
    g.append(h('div', { class: 'g-row' }, h('div', { class: 'g-ch' }, h('b', {}, c.number, ' '), c.name, h('div', { class: 'small muted' }, c.source)), track));
  }
  if (!data.channels?.length) g.append(h('div', { class: 'empty' }, 'No channels.'));
  $view.append(g);
};

// ---------- transcoding profiles ----------
routes.profiles = async () => {
  const [profs, sys, set] = await Promise.all([get('/api/profiles'), get('/api/system'), get('/api/settings')]);
  const base = location.origin;
  $view.append(h('h1', {}, 'Transcoding profiles'), gpuCard(sys));
  const editor = h('div');
  const edit = p => {
    p = p || { id: '', name: '', videoCodec: 'h264_nvenc', hwDecode: true, preset: 'p4', rateControl: 'vbr', bitrate: 6000, maxrate: 9000, bufsize: 12000, cq: 23, height: 1080, fps: 0, deinterlace: true, audioCodec: 'aac', audioBitrate: 192, audioChannels: 2, gpu: 0, extra: '' };
    const f = {
      id: input({ value: p.id, disabled: !!p.id, placeholder: 'nvenc-1080p' }), name: input({ value: p.name }),
      vc: select([['copy', 'Copy (no video transcode)'], ['h264_nvenc', 'H.264 — NVENC'], ['hevc_nvenc', 'HEVC — NVENC'], ['av1_nvenc', 'AV1 — NVENC (RTX 40+)'], ['libx264', 'H.264 — CPU x264'], ['libx265', 'HEVC — CPU x265']], p.videoCodec),
      hw: input({ type: 'checkbox', checked: p.hwDecode }), preset: input({ value: p.preset, placeholder: 'p1 (fast) … p7 (quality)' }),
      rc: select([['vbr', 'VBR (target + peak)'], ['cbr', 'CBR (constant)'], ['cq', 'Constant quality']], p.rateControl || 'vbr'),
      br: input({ type: 'number', value: p.bitrate }), mr: input({ type: 'number', value: p.maxrate }), bs: input({ type: 'number', value: p.bufsize }), cq: input({ type: 'number', value: p.cq }),
      height: select([[0, 'Source'], [2160, '2160p'], [1080, '1080p'], [720, '720p'], [576, '576p'], [480, '480p'], [360, '360p']], p.height),
      fps: select([[0, 'Source'], [25, '25'], [30, '30'], [50, '50'], [60, '60']], p.fps), dei: input({ type: 'checkbox', checked: p.deinterlace }),
      ac: select([['copy', 'Copy'], ['aac', 'AAC'], ['ac3', 'AC-3'], ['mp2', 'MP2']], p.audioCodec), ab: input({ type: 'number', value: p.audioBitrate }),
      ach: select([[0, 'Source'], [2, 'Stereo'], [6, '5.1']], p.audioChannels), gpu: input({ type: 'number', value: p.gpu, min: 0 }), extra: input({ value: p.extra || '', placeholder: 'e.g. -tune hq -bf 3' }),
    };
    const save = () => act(() => api('PUT', '/api/profiles/' + encodeURIComponent(f.id.value.trim()), {
      name: f.name.value, videoCodec: f.vc.value, hwDecode: f.hw.checked, preset: f.preset.value, rateControl: f.rc.value, bitrate: +f.br.value, maxrate: +f.mr.value, bufsize: +f.bs.value, cq: +f.cq.value,
      height: +f.height.value, fps: +f.fps.value, deinterlace: f.dei.checked, audioCodec: f.ac.value, audioBitrate: +f.ab.value, audioChannels: +f.ach.value, gpu: +f.gpu.value, extra: f.extra.value,
    }), 'Profile saved').then(route);
    editor.replaceChildren(h('div', { class: 'card' }, h('h2', {}, p.id ? 'Edit ' + p.name : 'New profile'),
      h('div', { class: 'form' }, field('Id', f.id), field('Name', f.name), field('Video encoder', f.vc), h('label', { class: 'row' }, f.hw, 'NVDEC hardware decode'), field('Preset', f.preset),
        field('Rate control', f.rc), field('Target bitrate (kbit/s)', f.br), field('Max bitrate (kbit/s)', f.mr), field('VBV buffer (kbit)', f.bs), field('CQ / CRF (quality mode)', f.cq),
        field('Resolution', f.height), field('Frame rate', f.fps), h('label', { class: 'row' }, f.dei, 'Deinterlace'),
        field('Audio codec', f.ac), field('Audio bitrate (kbit/s)', f.ab), field('Audio channels', f.ach), field('GPU index', f.gpu), field('Extra ffmpeg output args', f.extra)),
      h('div', { class: 'row', style: 'margin-top:12px' }, h('button', { class: 'primary', onclick: save }, 'Save profile'), h('button', { onclick: () => editor.replaceChildren() }, 'Cancel'))));
    editor.scrollIntoView({ behavior: 'smooth' });
  };
  $view.append(h('div', { class: 'row', style: 'margin-bottom:12px' }, h('span', { class: 'spacer' }), h('button', { class: 'primary', onclick: () => edit() }, 'New profile')), editor);
  for (const p of profs || []) {
    const lineup = p.id === set.hdhrProfile ? base : `${base}/p/${p.id}`;
    $view.append(h('div', { class: 'card' },
      h('div', { class: 'row' }, h('h2', {}, p.name), p.id === set.hdhrProfile ? h('span', { class: 'badge b-accent' }, 'default lineup') : null,
        p.passthrough ? h('span', { class: 'badge' }, 'no transcoding') : h('span', { class: 'badge ' + (p.videoCodec.includes('nvenc') ? 'b-good' : '') }, p.videoCodec),
        !p.passthrough && p.videoCodec !== 'copy' ? h('span', { class: 'muted small' }, `${p.rateControl.toUpperCase()} ${p.rateControl === 'cq' ? 'q' + p.cq : fmtKbps(p.bitrate)}${p.maxrate && p.rateControl === 'vbr' ? ' (peak ' + fmtKbps(p.maxrate) + ')' : ''} · ${p.height ? p.height + 'p' : 'source res'}${p.hwDecode ? ' · NVDEC' : ''}`) : null,
        h('span', { class: 'spacer' }),
        h('button', { class: 'sm', onclick: () => edit(p) }, 'Edit'),
        p.id !== set.hdhrProfile ? h('button', { class: 'sm danger', onclick: () => confirm('Delete profile?') && act(() => api('DELETE', '/api/profiles/' + p.id), 'Deleted').then(route) }, '✕') : null),
      h('div', { class: 'small', style: 'margin-top:8px' }, 'HDHomeRun tuner URL for Jellyfin: ', h('span', { class: 'mono copy', onclick: () => copy(lineup) }, lineup), ' · M3U: ',
        h('span', { class: 'mono copy', onclick: () => copy(lineup + '/playlist.m3u') }, lineup + '/playlist.m3u')),
      p.command ? h('details', { style: 'margin-top:8px' }, h('summary', { class: 'small muted' }, 'ffmpeg command'), h('div', { class: 'mono small', style: 'word-break:break-all;margin-top:6px' }, p.command)) : null));
  }
};

// ---------- settings ----------
routes.settings = async () => {
  const [set, profs, xm] = await Promise.all([get('/api/settings'), get('/api/profiles'), get('/api/epg/xmltv')]);
  const f = {
    name: input({ value: set.serverName }), base: input({ value: set.baseUrl, placeholder: 'auto (from request)' }), ssdp: input({ type: 'checkbox', checked: set.ssdp }),
    prof: select((profs || []).map(p => [p.id, p.name]), set.hdhrProfile), eit: input({ type: 'checkbox', checked: set.eit }),
    xh: input({ type: 'number', value: set.xmltvHours }), ffmpeg: input({ value: set.ffmpeg }), vt: input({ type: 'number', value: set.virtualTuners, min: 0, max: 8 }),
  };
  let sources = [...(set.xmltv || [])];
  const srcEl = h('div');
  const renderSrc = () => srcEl.replaceChildren(...sources.map((x, i) => h('div', { class: 'row', style: 'margin-bottom:6px' },
    input({ type: 'checkbox', checked: x.enabled, onchange: e => x.enabled = e.target.checked }),
    input({ value: x.url, style: 'flex:1;min-width:260px', placeholder: 'https://…/guide.xml.gz or /path/guide.xml', oninput: e => x.url = e.target.value }),
    h('button', { class: 'sm danger', onclick: () => { sources.splice(i, 1); renderSrc(); } }, '✕'))),
    h('button', { class: 'sm', onclick: () => { sources.push({ url: '', enabled: true }); renderSrc(); } }, '+ Add XMLTV source'));
  renderSrc();
  const save = () => act(() => api('PUT', '/api/settings', { ...set, serverName: f.name.value, baseUrl: f.base.value.trim(), ssdp: f.ssdp.checked, hdhrProfile: f.prof.value,
    eit: f.eit.checked, xmltvHours: +f.xh.value, ffmpeg: f.ffmpeg.value.trim(), virtualTuners: +f.vt.value, xmltv: sources.filter(x => x.url.trim()) }), 'Settings saved');
  const origin = set.baseUrl || location.origin;
  const st = xm.status || {};
  $view.append(h('h1', {}, 'Settings'),
    h('div', { class: 'card' }, h('h2', {}, 'Connect Jellyfin'),
      h('ol', { class: 'small' },
        h('li', {}, 'Jellyfin → Dashboard → Live TV → Tuner Devices → Add → ', h('b', {}, 'HD Homerun'), ', URL ', h('span', { class: 'mono copy', onclick: () => copy(origin) }, origin), '. Tuner count and channels are read automatically.'),
        h('li', {}, 'TV Guide Data Providers → Add → ', h('b', {}, 'XMLTV'), ', file/URL ', h('span', { class: 'mono copy', onclick: () => copy(origin + '/xmltv.xml') }, origin + '/xmltv.xml'), '. Channels match by number.'),
        h('li', {}, 'Recording is done by Jellyfin\'s own DVR (Live TV → Recording settings). Streams survive signal drops, so short dropouts do not end a recording.'),
        h('li', {}, 'Want a GPU-transcoded variant? Add a second HD Homerun tuner using the per-profile URL on the Transcoding page.'),
        h('li', {}, 'Alternatively use the M3U tuner type with ', h('span', { class: 'mono copy', onclick: () => copy(origin + '/playlist.m3u') }, origin + '/playlist.m3u'), '.'))),
    h('div', { class: 'card' }, h('h2', {}, 'Server'),
      h('div', { class: 'form' }, field('Server name', f.name), field('Advertised base URL', f.base), field('Default lineup profile', f.prof),
        field('ffmpeg binary', f.ffmpeg), field('Virtual test tuners', f.vt), h('label', { class: 'row' }, f.ssdp, 'SSDP discovery (restart to apply)')),
      h('div', { class: 'small muted', style: 'margin-top:8px' }, 'HDHomeRun device id: ', h('span', { class: 'mono' }, set.deviceId))),
    h('div', { class: 'card' }, h('h2', {}, 'Guide data'),
      h('div', { class: 'form' }, h('label', { class: 'row' }, f.eit, 'Collect over-the-air EPG (EIT)'), field('XMLTV refresh (hours)', f.xh)),
      h('div', { style: 'margin-top:12px' }, srcEl),
      h('div', { class: 'small muted', style: 'margin-top:8px' }, st.lastRun && !st.lastRun.startsWith('0001') ? `Last import ${fmtDate(st.lastRun)}: ${st.info}` : 'Not imported yet', st.lastError ? h('div', { style: 'color:var(--bad)' }, st.lastError) : null),
      h('div', { class: 'small muted' }, 'Channels with an XMLTV id use XMLTV; the rest use over-the-air EIT.')),
    h('button', { class: 'primary', onclick: save }, 'Save settings'));
};

route();
