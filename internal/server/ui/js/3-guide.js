/* Guide: a programme grid, search, and where the guide data comes from. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  function showEvent(e, chName) {
    DH.modal(e.title, h('div', {},
      h('p', { class: 'dh-muted' }, (chName ? chName + ' · ' : '') + DH.fmtDay(e.start) + ' – ' + DH.fmtTime(e.stop)),
      e.subtitle ? h('p', {}, h('b', {}, e.subtitle)) : null,
      e.episodeText ? h('p', { class: 'dh-small' }, e.episodeText) : null,
      e.description ? h('p', {}, e.description) : h('p', { class: 'dh-muted' }, 'No description.'),
      e.categories && e.categories.length ? h('p', { class: 'dh-small dh-muted' }, e.categories.join(', ')) : null,
      e.rating ? h('p', { class: 'dh-small dh-muted' }, 'Rating ' + e.rating) : null,
      DH.L('pro') ? h('p', { class: 'dh-small dh-muted' }, 'Source: ' + (e.source === 'eit' ? 'broadcast' : e.source)) : null));
  }

  DH.page('guide', {
    title: 'Guide', order: 30,
    render: function (view, ctx) {
      var hours = DH.store.get('guideHours', 6), offset = 0;
      var grid = h('div', {}), results = h('div', {}), status = h('div', {});
      var search = h('input', { type: 'search', placeholder: 'Search programmes', style: { minWidth: '220px' } });
      var timer;
      search.addEventListener('input', function () {
        clearTimeout(timer);
        timer = setTimeout(function () {
          var q = search.value.trim();
          if (!q) { fill(results); return; }
          DH.get('guide/search?q=' + DH.enc(q)).then(function (evs) {
            fill(results, DH.card(h('h3', {}, evs.length + ' result' + (evs.length === 1 ? '' : 's')), evs.length ? h('div', { class: 'dh-table-wrap' }, h('table', { class: 'dh-t' }, h('tbody', {},
              evs.map(function (e) {
                return h('tr', { style: { cursor: 'pointer' }, onclick: function () { showEvent(e); } },
                  h('td', { class: 'num' }, DH.fmtDay(e.start)), h('td', {}, h('b', {}, e.title), e.subtitle ? h('div', { class: 'dh-small dh-muted' }, e.subtitle) : null));
              })))) : null));
          });
        }, 300);
      });
      function load() {
        var from = new Date(Math.floor(Date.now() / 1800000) * 1800000 + offset * 3600000);
        return DH.get('guide?hours=' + hours + '&from=' + DH.enc(from.toISOString())).then(function (g) { drawGrid(g); }, function (e) { fill(grid, DH.errorBox(e)); });
      }
      function drawGrid(g) {
        var from = new Date(g.from).getTime(), to = new Date(g.to).getTime(), span = to - from, now = Date.now();
        if (!g.channels.length) { fill(grid, DH.card(h('p', { class: 'dh-muted' }, 'No channels yet.'))); return; }
        var head = h('div', { class: 'dh-g-head' }, h('div', { class: 'dh-g-ch' }, ''), h('div', { class: 'dh-g-line', style: { height: '24px' } },
          Array.from({ length: hours * 2 }, function (_, i) {
            var t = from + i * 1800000;
            return h('span', { style: { position: 'absolute', left: (i * 1800000 / span * 100) + '%', paddingLeft: '4px' } }, DH.fmtTime(t));
          })));
        var rows = g.channels.map(function (c) {
          var line = h('div', { class: 'dh-g-line' });
          c.events.forEach(function (e) {
            var s = Math.max(new Date(e.start).getTime(), from), en = Math.min(new Date(e.stop).getTime(), to);
            if (en <= s) return;
            var isNow = new Date(e.start).getTime() <= now && new Date(e.stop).getTime() > now;
            line.appendChild(h('div', { class: 'dh-g-ev' + (isNow ? ' now' : ''), title: e.title, onclick: function () { showEvent(e, c.guideNumber + ' ' + c.name); },
              style: { left: ((s - from) / span * 100) + '%', width: 'calc(' + ((en - s) / span * 100) + '% - 2px)' } },
              e.title, h('small', {}, DH.fmtTime(e.start))));
          });
          if (!c.events.length) line.appendChild(h('div', { class: 'dh-small dh-muted', style: { padding: '12px 8px' } }, 'No guide data'));
          if (now > from && now < to) line.appendChild(h('div', { class: 'dh-g-now', style: { left: ((now - from) / span * 100) + '%' } }));
          return h('div', { class: 'dh-g-row' }, h('div', { class: 'dh-g-ch' }, h('b', {}, c.guideNumber), h('span', { class: 'dh-small' }, c.name)), line);
        });
        var inner = h('div', { class: 'dh-guide-in' }, head, rows);
        fill(grid, h('div', { class: 'dh-card', style: { padding: '0' } }, h('div', { class: 'dh-guide' }, inner)));
      }
      function loadStatus() {
        if (!DH.L('advanced')) return;
        DH.get('guide/status').then(function (s) {
          var ota = s.ota, x = s.xmltv;
          fill(status, DH.card(h('div', { class: 'dh-between' }, h('h3', { style: { margin: 0 } }, 'Guide sources', DH.lvlTag('advanced')),
            h('div', { class: 'dh-row' },
              h('button', { class: 'dh-btn sm', onclick: function () { DH.act(DH.post('guide/grab'), 'Refreshing the guide in the background').then(loadStatus); } }, 'Refresh now'),
              DH.L('pro') ? h('button', { class: 'dh-btn sm red', onclick: function () {
                if (DH.confirm('Delete all guide data and collect it again?')) DH.act(DH.del('guide'), 'Guide cleared').then(load);
              } }, 'Clear guide') : null)),
            h('p', {}, 'Broadcast guide: ', ota.enabled ? h('span', {}, 'on', ota.grabbing ? ' · collecting now' : '', !DH.isZeroTime(ota.lastRun) ? ' · last collected ' + DH.fmtDay(ota.lastRun) : '') : h('b', {}, 'off (Settings)')),
            h('p', {}, 'XMLTV: ', x.channels ? x.info : 'no sources', x.lastError ? h('span', { style: { color: 'var(--bad)' } }, ' · ' + x.lastError) : '', ' ',
              h('button', { class: 'dh-link', onclick: function () { ctx.go('settings'); } }, 'Change')),
            h('div', { class: 'dh-table-wrap' }, h('table', { class: 'dh-t' }, h('thead', {}, h('tr', {}, h('th', {}, 'Channel'), h('th', {}, 'Source'), h('th', {}, 'Guide until'))),
              h('tbody', {}, s.channels.map(function (c) {
                var none = DH.isZeroTime(c.until) || new Date(c.until) < new Date();
                return h('tr', {}, h('td', {}, c.guideNumber + ' ' + c.name), h('td', { class: 'dh-small' }, c.source === 'xmltv' ? 'XMLTV' : 'Broadcast'),
                  h('td', { class: 'dh-small' }, none ? h('span', { style: { color: 'var(--warn)' } }, 'nothing yet') : DH.fmtDay(c.until)));
              }))))));
        });
      }
      var hoursSel = DH.select([[3, '3 hours'], [6, '6 hours'], [12, '12 hours'], [24, '24 hours']], hours, function (v) { hours = Number(v); DH.store.set('guideHours', hours); load(); });
      fill(view, h('div', { class: 'dh-between', style: { marginBottom: '12px' } }, search,
        h('div', { class: 'dh-row' },
          h('button', { class: 'dh-btn sm', onclick: function () { offset -= hours; load(); } }, '◀ Earlier'),
          h('button', { class: 'dh-btn sm', onclick: function () { offset = 0; load(); } }, 'Now'),
          h('button', { class: 'dh-btn sm', onclick: function () { offset += hours; load(); } }, 'Later ▶'), hoursSel)),
        results, grid, status);
      loadStatus();
      ctx.every(60000, load);
      return load();
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
