/* Channels: what viewers see, with numbers, names, backups and profiles. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  function parseNumber(s) {
    var m = String(s).trim().match(/^(\d+)(?:[.\-](\d+))?$/);
    if (!m) return null;
    return { number: Number(m[1]), minor: m[2] ? Number(m[2]) : 0 };
  }

  DH.editChannel = function (c, done) {
    DH.modal(c ? 'Edit ' + c.name : 'New channel', function (close) {
      var box = h('div', {}, DH.loading());
      Promise.all([DH.get('profiles'), DH.L('advanced') ? DH.get('services') : Promise.resolve([]),
        DH.L('advanced') ? DH.get('xmltv/channels').catch(function () { return { channels: [] }; }) : Promise.resolve({ channels: [] })])
        .then(function (r) { fill(box, body(r[0], r[1], r[2].channels || [], close)); }, function (e) { fill(box, DH.errorBox(e)); });
      return box;
    });
    function body(profiles, services, xmltv, close) {
      c = c || { name: '', number: 0, minor: 0, enabled: true, services: [], profile: '', guideId: '', icon: '', radio: false };
      var num = h('input', { value: c.guideNumber || (c.number ? String(c.number) + (c.minor ? '.' + c.minor : '') : ''), placeholder: 'e.g. 7 or 7.1', inputmode: 'decimal' });
      var form = DH.form(c, [
        { key: 'name', label: 'Name' },
        { key: 'enabled', label: 'Show this channel', type: 'bool' },
        { key: 'profile', label: 'Stream quality', type: 'select', lvl: 'advanced', help: 'Overrides the default quality for this channel.',
          options: [['', 'Default']].concat(profiles.map(function (p) { return [p.id, p.name]; })) },
        { key: 'guideId', label: 'Guide source', type: 'select', lvl: 'advanced', help: 'Where this channel\'s programme guide comes from.',
          options: [['', 'From the broadcast']].concat(xmltv.map(function (x) { return [x.id, 'XMLTV: ' + ((x.names || [])[0] || x.id) + ' (' + x.id + ')']; }))
            .concat(c.guideId && !xmltv.some(function (x) { return x.id === c.guideId; }) ? [[c.guideId, 'XMLTV: ' + c.guideId]] : []) },
        { key: 'icon', label: 'Logo URL', lvl: 'advanced', placeholder: 'https://…' },
        { key: 'radio', label: 'Radio channel', type: 'bool', lvl: 'pro' }
      ]);
      var fields = form.el;
      fields.insertBefore(DH.field('Number', num, 'Shown in Jellyfin\'s guide.'), fields.children[1] || null);

      // Services: the first is used; the others are backups when it has no signal.
      var svcIDs = (c.services || []).slice();
      var svcBox = h('div', {});
      var byID = {};
      services.forEach(function (s) { byID[s.id] = s; });
      function drawServices() {
        var rows = svcIDs.map(function (id, i) {
          var s = byID[id] || { name: id, mux: '' };
          return h('tr', {}, h('td', {}, i === 0 ? DH.badge('ok', 'Main') : DH.badge('acc', 'Backup ' + i)),
            h('td', {}, s.name), h('td', { class: 'dh-small dh-muted' }, s.mux), h('td', {}, DH.bars(s.signal)),
            h('td', { style: { whiteSpace: 'nowrap' } },
              h('button', { class: 'dh-btn sm', disabled: i === 0, title: 'Move up', onclick: function () { svcIDs.splice(i - 1, 0, svcIDs.splice(i, 1)[0]); drawServices(); } }, '↑'), ' ',
              h('button', { class: 'dh-btn sm red', title: 'Remove', onclick: function () { svcIDs.splice(i, 1); drawServices(); } }, '✕')));
        });
        var free = services.filter(function (s) { return svcIDs.indexOf(s.id) < 0 && s.kind !== 'other'; });
        var pick = DH.select([['', 'Add a backup service…']].concat(free.map(function (s) {
          return [s.id, s.name + ' — ' + s.mux + (s.mappedTo.length ? ' (used by ' + s.mappedTo.join(', ') + ')' : '')];
        })), '', function (v) { if (v) { svcIDs.push(v); drawServices(); } });
        fill(svcBox, h('div', { class: 'dh-section' }, 'Services ', DH.lvlTag('advanced')),
          h('p', { class: 'dh-small dh-muted' }, 'The main service is used first. If it loses signal, dvbhub switches to a backup automatically.'),
          rows.length ? h('div', { class: 'dh-table-wrap' }, h('table', { class: 'dh-t' }, h('tbody', {}, rows))) : h('p', { class: 'dh-muted' }, 'No services.'),
          h('div', { style: { marginTop: '8px' } }, pick));
      }
      if (DH.L('advanced')) drawServices();

      function save() {
        var body = form.changes();
        var n = parseNumber(num.value);
        if (!n) { DH.toast('Enter a number like 7 or 7.1', true); return; }
        body.number = n.number; body.minor = n.minor;
        if (DH.L('advanced')) body.services = svcIDs;
        var req = c.id ? DH.put('channels/' + c.id, body) : DH.post('channels', Object.assign({ name: c.name, enabled: true, services: svcIDs }, body));
        DH.act(req, 'Saved').then(function () { close(); if (done) done(); });
      }
      return h('div', {}, fields, svcBox,
        DH.L('advanced') && c.streamUrl ? h('div', { style: { marginTop: '14px' } }, h('div', { class: 'dh-section' }, 'Stream address'), DH.copyBox(c.streamUrl)) : null,
        DH.L('pro') && c.id ? h('p', { class: 'dh-small dh-muted', style: { marginTop: '8px' } }, 'Channel id ', h('code', {}, c.id)) : null,
        h('div', { class: 'dh-row', style: { marginTop: '16px' } },
          h('button', { class: 'dh-btn pri', onclick: save }, 'Save'),
          h('button', { class: 'dh-btn', onclick: close }, 'Cancel'),
          h('span', { class: 'dh-sp' }),
          c.id && DH.L('advanced') ? h('button', { class: 'dh-btn red', onclick: function () {
            if (DH.confirm('Delete the channel "' + c.name + '"? Its services stay and can be mapped again.')) DH.act(DH.del('channels/' + c.id), 'Deleted').then(function () { close(); if (done) done(); });
          } }, 'Delete') : null));
    }
  };

  DH.page('channels', {
    title: 'Channels', order: 20,
    render: function (view, ctx) {
      var filter = '', selected = {}, channels = [], profiles = [];
      var list = h('div', {});
      var search = h('input', { type: 'search', placeholder: 'Search channels', style: { minWidth: '200px' } });
      search.addEventListener('input', function () { filter = search.value.toLowerCase(); draw(); });
      function load() {
        return Promise.all([DH.get('channels'), DH.L('advanced') ? DH.get('profiles') : Promise.resolve([])]).then(function (r) {
          channels = r[0]; profiles = r[1]; draw();
        }, function (e) { fill(list, DH.errorBox(e)); });
      }
      function bulk(action, value) {
        var ids = Object.keys(selected).filter(function (k) { return selected[k]; });
        if (!ids.length) { DH.toast('Select channels first', true); return; }
        if (action === 'delete' && !DH.confirm('Delete ' + ids.length + ' channel(s)?')) return;
        DH.act(DH.post('channels-bulk', { ids: ids, action: action, value: value || '' }), function (r) { return r.changed + ' channel(s) changed'; })
          .then(function () { selected = {}; load(); });
      }
      function draw() { DH.patch(list, [channels, profiles, filter, selected, DH.level], drawList); }
      function drawList() {
        var shown = channels.filter(function (c) { return !filter || c.name.toLowerCase().indexOf(filter) >= 0 || c.guideNumber.indexOf(filter) === 0; });
        if (!channels.length) {
          return h('div', { class: 'dh-card dh-empty' }, h('h2', {}, 'No channels yet'), h('p', { class: 'dh-muted' }, 'Scan for channels first.'),
            h('button', { class: 'dh-btn pri', onclick: function () { ctx.go('scan'); } }, 'Find channels'));
        }
        var adv = DH.L('advanced');
        var allBox = adv ? h('input', { type: 'checkbox', title: 'Select all', onchange: function () { shown.forEach(function (c) { selected[c.id] = allBox.checked; }); draw(); } }) : null;
        var rows = shown.map(function (c) {
          var now = c.now;
          return h('tr', { class: c.enabled ? '' : 'off' },
            adv ? h('td', {}, DH.toggle(!!selected[c.id], function (v) { selected[c.id] = v; list._dhSig = null; })) : null,
            h('td', { class: 'num' }, h('b', {}, c.guideNumber)),
            h('td', {}, h('div', {}, c.name, c.watching ? h('span', { class: 'dh-badge good', style: { marginLeft: '6px' } }, 'watching') : null),
              adv && c.services.length > 1 ? h('div', { class: 'dh-small dh-muted' }, (c.services.length - 1) + ' backup' + (c.services.length > 2 ? 's' : '')) : null),
            h('td', { class: 'dh-hide-sm' }, now ? h('div', {}, now.title, h('div', { class: 'dh-small dh-muted' }, DH.fmtTime(now.start) + '–' + DH.fmtTime(now.stop))) : h('span', { class: 'dh-muted dh-small' }, '—')),
            h('td', {}, DH.bars(c.signal)),
            adv ? h('td', { class: 'dh-small dh-hide-sm' }, c.profile ? (profiles.filter(function (p) { return p.id === c.profile; })[0] || {}).name || c.profile : h('span', { class: 'dh-muted' }, 'Default')) : null,
            DH.L('pro') ? h('td', { class: 'dh-small dh-muted dh-hide-sm' }, c.guideId ? 'XMLTV ' + c.guideId : 'broadcast') : null,
            h('td', {}, DH.toggle(c.enabled, function (v, el) {
              DH.act(DH.put('channels/' + c.id, { enabled: v })).then(load, function () { el.checked = !v; });
            }, 'Show this channel')),
            h('td', {}, h('button', { class: 'dh-btn sm', onclick: function () { DH.editChannel(c, load); } }, 'Edit')));
        });
        return h('div', { class: 'dh-card' }, h('div', { class: 'dh-table-wrap' }, h('table', { class: 'dh-t' },
          h('thead', {}, h('tr', {}, adv ? h('th', {}, allBox) : null, h('th', {}, 'No.'), h('th', {}, 'Name'), h('th', { class: 'dh-hide-sm' }, 'On now'), h('th', {}, 'Signal'),
            adv ? h('th', { class: 'dh-hide-sm' }, 'Quality') : null, DH.L('pro') ? h('th', { class: 'dh-hide-sm' }, 'Guide') : null, h('th', {}, 'On'), h('th', {}))),
          h('tbody', {}, rows))),
          h('p', { class: 'dh-small dh-muted', style: { margin: '8px 0 0' } }, shown.length + ' of ' + channels.length + ' channels'));
      }
      var bulkBar = DH.L('advanced') ? h('div', { class: 'dh-row' },
        DH.select([['', 'With selected…'], ['enable', 'Show'], ['disable', 'Hide'], ['profile', 'Set quality…'], ['renumber', 'Renumber from…'], ['delete', 'Delete']], '', function (v) {
          sel.value = '';
          if (v === 'profile') {
            var p = window.prompt('Profile id (empty = default): ' + profiles.map(function (x) { return x.id; }).join(', '), '');
            if (p !== null) bulk('profile', p.trim());
          } else if (v === 'renumber') {
            var n = window.prompt('First number', '1');
            if (n !== null) bulk('renumber', n);
          } else if (v) bulk(v);
        })) : null;
      var sel = bulkBar ? bulkBar.firstChild : null;
      fill(view, h('div', { class: 'dh-between', style: { marginBottom: '12px' } },
        h('div', { class: 'dh-row' }, search, bulkBar),
        h('div', { class: 'dh-row' },
          DH.L('advanced') ? h('button', { class: 'dh-btn', onclick: function () {
            DH.act(DH.post('map', { includeRadio: false, skipScrambled: true, mergeByName: true }), function (r) { return r.created + ' new channels, ' + r.merged + ' added as backups'; }).then(load);
          }, title: 'Create channels for services found by a scan that aren\'t channels yet' }, 'Add new services') : null,
          DH.L('pro') ? h('button', { class: 'dh-btn', onclick: function () { DH.editChannel(null, load); } }, 'New channel') : null)), list);
      ctx.every(15000, load);
      return load();
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
