/* App shell: header, navigation, level switch, page lifecycle. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  var app = null;

  // A host for the standalone web UI: calls /api/ on this server.
  DH.standaloneHost = function () {
    return {
      embedded: false,
      api: function (method, path, body) {
        var opts = { method: method, headers: {}, credentials: 'same-origin' };
        if (body !== undefined && method !== 'GET') {
          if (typeof body === 'string') { opts.body = body; opts.headers['Content-Type'] = 'text/plain'; }
          else { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json'; }
        }
        return fetch('api/' + path, opts).then(function (r) {
          return r.text().then(function (t) {
            var d = null;
            try { d = t ? JSON.parse(t) : null; } catch (e) { d = t; }
            if (!r.ok) throw new Error((d && d.error) || t || r.statusText);
            return d;
          });
        });
      }
    };
  };

  DH.mount = function (root, host) {
    if (app) app.destroy();
    DH.host = host;
    var wrap = h('div', { class: 'dh-wrap' });
    var dh = h('div', { class: 'dh' + (host.embedded ? ' dh-embed' : '') }, wrap);
    fill(root, dh);
    var nav = h('nav', { class: 'dh-nav', 'aria-label': 'Sections' });
    var seg = h('div', { class: 'dh-seg', role: 'group', 'aria-label': 'Detail level' });
    var view = h('main', {});
    var timers = [], cleanup = null, current = null, arg = null, alive = true;
    app = {
      status: null,
      destroy: function () { alive = false; stopTimers(); if (cleanup) try { cleanup(); } catch (e) { } },
      go: go
    };
    wrap.appendChild(h('header', { class: 'dh-top' },
      host.embedded ? null : h('div', { class: 'dh-logo' }, logo(), 'dvbhub'),
      h('span', { class: 'dh-sp' }), seg));
    wrap.appendChild(nav);
    wrap.appendChild(view);

    function stopTimers() { timers.forEach(clearInterval); timers = []; }
    var ctx = {
      every: function (ms, fn) { var t = setInterval(function () { if (alive && document.contains(view)) fn(); }, ms); timers.push(t); return t; },
      go: function (p, a) { go(p, a); },
      app: app,
      refreshStatus: refreshStatus
    };

    function renderSeg() {
      fill(seg, DH.LEVELS.map(function (l) {
        return h('button', { class: l === DH.level ? 'on' : '', onclick: function () {
          DH.level = l; DH.store.set('level', l); renderSeg(); renderNav(); go(current, arg);
        }, title: { simple: 'The essentials', advanced: 'More settings', pro: 'Every setting' }[l] }, l[0].toUpperCase() + l.slice(1));
      }));
    }
    function pagesList() {
      return Object.keys(DH.pages).map(function (k) { return DH.pages[k]; })
        .filter(function (p) { return !p.lvl || DH.L(p.lvl); })
        .sort(function (a, b) { return (a.order || 50) - (b.order || 50); });
    }
    function renderNav() {
      var st = app.status, list = pagesList();
      var dots = list.map(function (p) { return p.attention && st ? p.attention(st) : ''; });
      DH.patch(nav, [current, DH.level, list.map(function (p) { return p.id; }), dots], function () {
        return list.map(function (p, i) {
          return h('button', { class: p.id === current ? 'on' : '', onclick: function () { go(p.id); } }, p.title, dots[i] ? h('span', { class: 'dh-dot', title: dots[i] }) : null);
        });
      });
    }
    function go(id, a) {
      if (!DH.pages[id] || (DH.pages[id].lvl && !DH.L(DH.pages[id].lvl))) id = 'home';
      stopTimers();
      if (cleanup) { try { cleanup(); } catch (e) { } cleanup = null; }
      current = id; arg = a;
      if (!host.embedded) { try { history.replaceState(null, '', '#' + id); } catch (e) { } }
      renderNav();
      fill(view, DH.loading());
      var page = DH.pages[id];
      try {
        var r = page.render(view, ctx, a);
        Promise.resolve(r).then(function (c) { if (typeof c === 'function') cleanup = c; }, function (e) { fill(view, DH.errorBox(e)); });
      } catch (e) { fill(view, DH.errorBox(e)); }
      ctx.every(15000, refreshStatus);
    }
    function refreshStatus() {
      return DH.get('status').then(function (st) { app.status = st; renderNav(); return st; }, function () { });
    }

    var start = host.embedded ? 'home' : (location.hash || '').slice(1) || 'home';
    return refreshStatus().then(function (st) {
      DH.level = DH.store.get('level', st && st.uiLevel) || 'simple';
      if (DH.LEVELS.indexOf(DH.level) < 0) DH.level = 'simple';
      renderSeg();
      go(start);
    }, function (e) {
      fill(view, DH.errorBox(e));
    }).then(function () {
      if (!app.status) {
        renderSeg();
        fill(view, DH.errorBox(new Error('Can\'t reach dvbhub. ' + (host.embedded ? 'Check the connection settings above.' : 'Is it running?'))));
      }
      return app;
    });
  };

  function logo() {
    return DH.svg('svg', { viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' },
      DH.svg('rect', { x: 2, y: 7, width: 20, height: 14, rx: 2 }), DH.svg('path', { d: 'M17 2l-5 5-5-5' }));
  }
})(window.DvbHubUI = window.DvbHubUI || {});
