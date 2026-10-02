/* dvbhub web UI core: helpers shared by every page. The UI is mounted with
   DvbHubUI.mount(element, host), either standalone or inside Jellyfin, where
   host.api sends requests through the Jellyfin plugin. */
(function (DH) {
  'use strict';
  DH.pages = DH.pages || {};
  // Pages register themselves: DH.page('home', {title, order, render(view, ctx)}).
  DH.page = function (id, def) { def.id = id; DH.pages[id] = def; };

  // ---------- DOM ----------
  function h(tag, attrs) {
    var el = document.createElement(tag);
    attrs = attrs || {};
    Object.keys(attrs).forEach(function (k) {
      var v = attrs[k];
      if (v === undefined || v === null || v === false) return;
      if (k.indexOf('on') === 0 && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'class') el.className = v;
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
      else el.setAttribute(k, v === true ? '' : v);
    });
    for (var i = 2; i < arguments.length; i++) add(el, arguments[i]);
    return el;
  }
  function add(el, k) {
    if (k === undefined || k === null || k === false) return;
    if (Array.isArray(k)) { k.forEach(function (x) { add(el, x); }); return; }
    el.appendChild(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  function fill(el) {
    while (el.firstChild) el.removeChild(el.firstChild);
    for (var i = 1; i < arguments.length; i++) add(el, arguments[i]);
    return el;
  }
  var NS = 'http://www.w3.org/2000/svg';
  function svg(tag, attrs) {
    var el = document.createElementNS(NS, tag);
    Object.keys(attrs || {}).forEach(function (k) { el.setAttribute(k, attrs[k]); });
    for (var i = 2; i < arguments.length; i++) if (arguments[i]) el.appendChild(arguments[i]);
    return el;
  }
  DH.h = h; DH.fill = fill; DH.svg = svg; DH.add = add;

  // ---------- formatting ----------
  DH.fmtKbps = function (k) { return k >= 1000 ? (k / 1000).toFixed(1) + ' Mbit/s' : Math.round(k || 0) + ' kbit/s'; };
  DH.fmtBytes = function (b) { var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0; while (b >= 1024 && i < 4) { b /= 1024; i++; } return b.toFixed(i ? 1 : 0) + ' ' + u[i]; };
  DH.fmtTime = function (d) { return new Date(d).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }); };
  DH.fmtDay = function (d) { return new Date(d).toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit' }); };
  DH.fmtDur = function (secs) {
    secs = Math.max(0, Math.round(secs));
    if (secs < 60) return secs + ' s';
    if (secs < 3600) return Math.floor(secs / 60) + ' min';
    return Math.floor(secs / 3600) + ' h ' + Math.floor(secs % 3600 / 60) + ' min';
  };
  DH.since = function (d) { return DH.fmtDur((Date.now() - new Date(d).getTime()) / 1000); };
  DH.mhz = function (khz) { return (khz / 1000).toFixed(khz % 1000 ? 3 : 0).replace(/\.?0+$/, '') + ' MHz'; };
  DH.isZeroTime = function (t) { return !t || String(t).indexOf('0001-01-01') === 0; };

  // ---------- levels ----------
  // One set of settings; the level only decides what is shown.
  var LEVELS = ['simple', 'advanced', 'pro'];
  DH.LEVELS = LEVELS;
  DH.level = 'simple';
  DH.L = function (min) { return LEVELS.indexOf(DH.level) >= LEVELS.indexOf(min || 'simple'); };
  DH.lvlTag = function (min) { return min && min !== 'simple' && DH.L(min) ? h('span', { class: 'dh-lvl', title: 'Shown at the ' + min + ' level' }, min === 'pro' ? 'PRO' : 'ADV') : null; };

  // ---------- storage ----------
  DH.store = {
    get: function (k, d) { try { var v = localStorage.getItem('dvbhub.' + k); return v === null ? d : JSON.parse(v); } catch (e) { return d; } },
    set: function (k, v) { try { localStorage.setItem('dvbhub.' + k, JSON.stringify(v)); } catch (e) { } }
  };

  // ---------- API ----------
  DH.api = function (method, path, body) { return DH.host.api(method, path, body); };
  DH.get = function (path) { return DH.api('GET', path); };
  DH.post = function (path, body) { return DH.api('POST', path, body === undefined ? {} : body); };
  DH.put = function (path, body) { return DH.api('PUT', path, body); };
  DH.del = function (path) { return DH.api('DELETE', path); };
  DH.enc = encodeURIComponent;

  // Runs a promise and reports success or failure in a toast.
  DH.act = function (p, okMsg) {
    return Promise.resolve(p).then(function (r) {
      if (okMsg) DH.toast(typeof okMsg === 'function' ? okMsg(r) : okMsg);
      return r;
    }, function (e) { DH.toast(e.message || String(e), true); throw e; });
  };

  var toastTimer;
  DH.toast = function (msg, bad) {
    var old = document.querySelector('.dh-toast');
    if (old) old.remove();
    var t = h('div', { class: 'dh-toast' + (bad ? ' bad' : ''), role: 'status' }, msg);
    document.body.appendChild(t);
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.remove(); }, bad ? 6000 : 3000);
  };

  DH.confirm = function (msg) { return window.confirm(msg); };

  // A dialog. content is a node or a function(close) returning one.
  DH.modal = function (title, content) {
    var bg = h('div', { class: 'dh-modal-bg' });
    function close() { bg.remove(); document.removeEventListener('keydown', esc); }
    function esc(e) { if (e.key === 'Escape') close(); }
    var body = typeof content === 'function' ? content(close) : content;
    var root = h('div', { class: 'dh' + (DH.host && DH.host.embedded ? ' dh-embed' : '') + (DH.light ? ' dh-light' : ''), style: { background: 'transparent', minHeight: '0', width: '100%', maxWidth: '760px' } },
      h('div', { class: 'dh-card dh-modal', style: { background: DH.host && DH.host.embedded ? (DH.host.modalBg || '#202020') : 'var(--panel)' } },
        h('div', { class: 'dh-between', style: { marginBottom: '10px' } }, h('h2', { style: { margin: 0 } }, title),
          h('button', { class: 'dh-btn sm', onclick: close, 'aria-label': 'Close' }, '✕')),
        body));
    bg.appendChild(root);
    bg.addEventListener('click', function (e) { if (e.target === bg) close(); });
    document.addEventListener('keydown', esc);
    document.body.appendChild(bg);
    return close;
  };

  DH.copyBox = function (text) {
    return h('div', { class: 'dh-copy' }, h('code', {}, text),
      h('button', { class: 'dh-btn sm', onclick: function () {
        var done = function () { DH.toast('Copied'); };
        if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(done, function () { fallback(); });
        else fallback();
        function fallback() {
          var ta = h('textarea', { style: { position: 'fixed', opacity: '0' } }); ta.value = text;
          document.body.appendChild(ta); ta.select();
          try { document.execCommand('copy'); done(); } catch (e) { DH.toast('Select the text and copy it', true); }
          ta.remove();
        }
      } }, 'Copy'));
  };

  // ---------- signal ----------
  DH.barColor = function (n) { return n <= 1 ? 'var(--bad)' : n === 2 ? 'var(--warn)' : n === 3 ? 'var(--ok)' : 'var(--good)'; };
  // Phone-style bars for a signal snapshot {bars, locked, snrDb, strengthPct, quality, live, at}.
  DH.bars = function (snap, opts) {
    opts = opts || {};
    var size = opts.size || 16;
    var n = snap ? snap.bars : -1;
    var s = svg('svg', { viewBox: '0 0 20 16', width: size * 1.25, height: size, 'aria-hidden': 'true' });
    for (var i = 0; i < 4; i++) s.appendChild(svg('rect', { x: i * 5, y: 12 - i * 4, width: 3.6, height: 4 + i * 4, rx: 1, fill: snap && i < n ? DH.barColor(n) : 'var(--line)' }));
    var label = !snap ? '–' : snap.snrDb != null ? snap.snrDb.toFixed(1) + ' dB' : snap.locked ? (snap.quality || 'locked') : 'no lock';
    var title = !snap ? 'Not measured yet' : [snap.locked ? 'Locked' : 'No lock', snap.strengthPct >= 0 ? 'strength ' + Math.round(snap.strengthPct) + '%' : '',
      snap.live ? 'live now' : (snap.at ? 'measured ' + DH.fmtDay(snap.at) : '')].filter(Boolean).join(' · ');
    return h('span', { class: 'dh-bars', title: title }, s, opts.noLabel ? null : h('span', { class: 'dh-small' + (snap && snap.live ? '' : ' dh-muted') }, label),
      snap && snap.live ? h('span', { class: 'dh-live', title: 'live' }) : null);
  };
  DH.signalColor = function (pct) { return pct < 0 ? 'var(--line)' : pct < 35 ? 'var(--bad)' : pct < 55 ? 'var(--warn)' : pct < 70 ? 'var(--ok)' : 'var(--good)'; };
  DH.score = function (sg) { return sg.snrDb != null ? sg.snrDb * 100 / 30 : (sg.snrPct >= 0 ? sg.snrPct : sg.strengthPct); };

  var STATE = {
    idle: ['Idle', ''], tuning: ['Tuning…', 'acc'], streaming: ['Receiving', 'good'], nosignal: ['No signal', 'bad'],
    ok: ['OK', 'good'], nosignal_scan: ['Nothing here', ''], fail: ['Failed', 'bad'], queued: ['Waiting', 'acc'], scanning: ['Scanning…', 'acc'],
    new: ['Not scanned', ''], running: ['Running', 'acc'], failed: ['Failed', 'bad'], working: ['Working', 'good'],
    'no-adapter': ['No TV adapter', 'bad'], 'no-driver': ['No driver', 'bad']
  };
  DH.badge = function (state, text) {
    var s = STATE[state] || [state, ''];
    return h('span', { class: 'dh-badge ' + s[1] }, text || s[0]);
  };
  DH.scanBadge = function (st) {
    if (!st) return DH.badge('new');
    if (st.status === 'nosignal') return DH.badge('nosignal_scan');
    var b = DH.badge(st.status);
    if (st.error) b.title = st.error;
    return b;
  };

  // ---------- forms ----------
  // DH.form(obj, fields) renders fields bound to a copy of obj and returns
  // {el, changes()} where changes() holds only fields the user changed.
  // Field: {key, label, type: text|number|select|bool|textarea|password, options:[[v,label]], lvl, help, wide, min, max, step, placeholder, section}
  DH.form = function (obj, fields) {
    var values = {}, el = h('div', { class: 'dh-form' }), inputs = {};
    fields.forEach(function (f) {
      if (f.lvl && !DH.L(f.lvl)) return;
      if (f.section) { el.appendChild(h('div', { class: 'dh-section' }, f.section)); return; }
      if (f.show && !f.show(obj)) return;
      var v = get(obj, f.key), input;
      switch (f.type) {
        case 'bool':
          input = h('input', { type: 'checkbox', checked: !!v });
          break;
        case 'select':
          input = h('select', {}, (typeof f.options === 'function' ? f.options(obj) : f.options).map(function (o) {
            var ov = Array.isArray(o) ? o[0] : o, ol = Array.isArray(o) ? o[1] : o;
            return h('option', { value: String(ov), selected: String(ov) === String(v) }, ol);
          }));
          break;
        case 'textarea':
          input = h('textarea', { placeholder: f.placeholder }); input.value = v == null ? '' : v;
          break;
        default:
          input = h('input', { type: f.type === 'number' ? 'number' : f.type === 'password' ? 'password' : 'text', value: v == null ? '' : String(v),
            min: f.min, max: f.max, step: f.step, placeholder: f.placeholder, autocomplete: 'off', spellcheck: 'false' });
      }
      inputs[f.key] = input;
      input.addEventListener(f.type === 'bool' || f.type === 'select' ? 'change' : 'input', function () {
        var nv = f.type === 'bool' ? input.checked : f.type === 'number' ? (input.value === '' ? 0 : Number(input.value)) : input.value;
        if (f.type === 'select' && typeof v === 'number') nv = Number(nv);
        values[f.key] = nv;
        if (f.onchange) f.onchange(nv, values);
      });
      var label = h('span', {}, f.label, DH.lvlTag(f.lvl));
      el.appendChild(f.type === 'bool'
        ? h('label', { class: 'dh-field dh-check' + (f.wide ? ' dh-wide' : '') }, input, h('span', {}, f.label, DH.lvlTag(f.lvl), f.help ? h('small', { class: 'dh-muted', style: { display: 'block', fontWeight: 400 } }, f.help) : null))
        : h('label', { class: 'dh-field' + (f.wide ? ' dh-wide' : '') }, label, input, f.help ? h('small', {}, f.help) : null));
    });
    return {
      el: el, inputs: inputs,
      changes: function () { var out = {}; Object.keys(values).forEach(function (k) { set(out, k, values[k]); }); return out; },
      changed: function () { return Object.keys(values).length > 0; }
    };
  };
  function get(o, path) { return path.split('.').reduce(function (a, k) { return a == null ? undefined : a[k]; }, o); }
  function set(o, path, v) {
    var parts = path.split('.'), last = parts.pop();
    parts.forEach(function (k) { o = o[k] = o[k] || {}; });
    o[last] = v;
  }
  DH.getPath = get;

  DH.field = function (label, input, help) {
    return h('label', { class: 'dh-field' }, h('span', {}, label), input, help ? h('small', {}, help) : null);
  };
  DH.select = function (options, value, onchange) {
    var s = h('select', { onchange: onchange ? function () { onchange(s.value); } : null }, options.map(function (o) {
      var ov = Array.isArray(o) ? o[0] : o, ol = Array.isArray(o) ? o[1] : o;
      return h('option', { value: String(ov), selected: String(ov) === String(value) }, ol);
    }));
    return s;
  };
  DH.toggle = function (checked, onchange, title) {
    var i = h('input', { type: 'checkbox', checked: checked, title: title });
    i.addEventListener('change', function () { onchange(i.checked, i); });
    return i;
  };

  // patch refills el only when sig differs from what it last showed, so
  // sections that didn't change keep their buttons, focus and selection.
  DH.patch = function (el, sig, build) {
    var key = typeof sig === 'string' ? sig : JSON.stringify(sig);
    if (el._dhSig === key) return el;
    el._dhSig = key;
    return fill(el, build());
  };

  DH.loading = function () { return h('div', { class: 'dh-muted', style: { padding: '20px 0' } }, 'Loading…'); };
  DH.errorBox = function (e) { return h('div', { class: 'dh-alert bad' }, e && e.message ? e.message : String(e)); };
  DH.card = function () { var c = h('div', { class: 'dh-card' }); for (var i = 0; i < arguments.length; i++) add(c, arguments[i]); return c; };

  DH.sparkline = function (values, opts) {
    opts = opts || {};
    var w = 300, hgt = 46, max = opts.max || Math.max.apply(null, values.concat([1])), min = opts.min || 0;
    var s = svg('svg', { class: 'dh-spark', viewBox: '0 0 ' + w + ' ' + hgt, preserveAspectRatio: 'none' });
    if (values.length < 2) return s;
    var pts = values.map(function (v, i) {
      var x = i * w / (values.length - 1), y = hgt - 2 - (Math.max(min, Math.min(max, v)) - min) / (max - min || 1) * (hgt - 4);
      return x.toFixed(1) + ',' + y.toFixed(1);
    });
    s.appendChild(svg('polyline', { points: pts.join(' '), fill: 'none', stroke: opts.color || 'var(--accent)', 'stroke-width': 2, 'vector-effect': 'non-scaling-stroke' }));
    return s;
  };
})(window.DvbHubUI = window.DvbHubUI || {});
