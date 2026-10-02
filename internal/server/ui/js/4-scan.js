/* Scan: find channels (wizard), networks, frequencies (muxes) and services. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  var PLAN_TEXT = {
    'us-atsc': ['Antenna — USA / Canada', 'Over-the-air TV with an antenna (ATSC). Channel names and numbers like 7.1 come from the stations.'],
    'us-cable': ['Cable — USA', 'Unencrypted (clear QAM) cable channels. Most cable channels are encrypted and can\'t be received.'],
    'eu-dvbt': ['Antenna — Europe', 'Freeview, TNT, DVB-T/T2 and similar antenna TV.'],
    'au-dvbt': ['Antenna — Australia', 'Freeview Australia (DVB-T).']
  };
  var TYPE_NAMES = { atsc: 'Antenna (ATSC)', cable: 'Cable (US QAM)', dvbt: 'Antenna (DVB-T/T2)', dvbc: 'Cable (DVB-C)', dvbs: 'Satellite (DVB-S/S2)', virtual: 'Test files' };

  function wizard(view, ctx, st, plans, networks, redraw) {
    var chosen = DH.store.get('plan', '') || (networks.filter(function (n) { return n.plan; })[0] || {}).plan || '';
    var choices = h('div', { class: 'dh-choices' });
    var startBtn = h('button', { class: 'dh-btn pri big', onclick: start }, 'Start scan');
    var note = h('p', { class: 'dh-small dh-muted', style: { marginTop: '10px' } });
    function drawChoices() {
      fill(choices, plans.map(function (p) {
        var t = PLAN_TEXT[p.id] || [p.name, ''];
        return h('button', { class: 'dh-choice' + (p.id === chosen ? ' on' : ''), onclick: function () { chosen = p.id; DH.store.set('plan', chosen); drawChoices(); } },
          h('b', {}, t[0]), h('span', { class: 'dh-small dh-muted' }, t[1]));
      }));
      var plan = plans.filter(function (p) { return p.id === chosen; })[0];
      var tuners = st.tuners.filter(function (t) { return t.config.enabled && !t.virtual; }).length || 1;
      var DS = { atsc: 'ATSC', cable: 'DVB-C/B', dvbt: 'DVB-T' };
      var canReceive = !plan || st.tuners.some(function (t) { return t.config.enabled && !t.virtual && (t.delsys || []).indexOf(DS[plan.type]) >= 0; });
      startBtn.disabled = !plan;
      fill(note, plan && !canReceive ? h('span', { style: { color: 'var(--warn)' } }, st.hardwareScanned
        ? 'None of your tuners can receive this. Check the Tuners page first (the tuner may be missing or need firmware).'
        : 'Still looking for tuners…') : plan ? 'Checks ' + plan.channels + ' channels; about ' + Math.max(1, Math.round(plan.channels * 7 / tuners / 60)) +
        ' minute(s) with ' + tuners + ' tuner' + (tuners === 1 ? '' : 's') + '. Channels found are added automatically, and your changes to existing channels are kept.' : 'Choose where your TV comes from.');
    }
    function start() {
      DH.act(DH.post('setup', { plan: chosen, autoMap: true })).then(function () { setTimeout(redraw, 300); });
    }
    drawChoices();
    var existing = networks.length;
    return DH.card(h('h2', {}, existing ? 'Scan for channels' : 'Find your channels'),
      h('p', {}, 'Where does your TV signal come from?'), choices,
      h('div', { class: 'dh-row', style: { marginTop: '14px' } }, startBtn), note,
      !DH.L('advanced') ? h('p', { class: 'dh-small dh-muted' }, 'Satellite or another country? Switch to Advanced (top right) to import a scan table.') : null);
  }

  function muxRow(m, reload, netType) {
    var pro = DH.L('pro');
    return h('tr', { class: m.enabled ? '' : 'off' },
      h('td', {}, h('div', {}, m.name), pro && m.file === '' ? h('div', { class: 'dh-small dh-muted' }, describe(m.tuning)) : null),
      h('td', {}, DH.scanBadge(m.scan), m.scan && m.scan.error && DH.L('advanced') ? h('div', { class: 'dh-small dh-muted', style: { maxWidth: '260px' } }, m.scan.error) : null),
      h('td', {}, m.scan.status === 'ok' || m.signal ? DH.bars(m.signal) : h('span', { class: 'dh-muted' }, '–')),
      h('td', { class: 'num' }, m.services || ''),
      h('td', { class: 'dh-small dh-muted dh-hide-sm' }, DH.isZeroTime(m.scan.at) ? '' : DH.fmtDay(m.scan.at)),
      pro ? h('td', {}, DH.toggle(m.enabled, function (v) { DH.act(DH.put('muxes/' + m.id, { enabled: v })).then(reload); }, 'Use this frequency')) : null,
      h('td', { style: { whiteSpace: 'nowrap' } },
        h('button', { class: 'dh-btn sm', onclick: function () { DH.act(DH.post('muxes/' + m.id + '/scan'), 'Queued').then(reload); } }, 'Scan'),
        pro ? [' ', h('button', { class: 'dh-btn sm', onclick: function () { editMux(m, netType, reload); } }, 'Edit'), ' ',
          h('button', { class: 'dh-btn sm red', onclick: function () {
            if (DH.confirm('Delete ' + m.name + ' and its services?')) DH.act(DH.del('muxes/' + m.id), 'Deleted').then(reload);
          } }, '✕')] : null));
  }

  function describe(t) {
    if (!t) return '';
    var parts = [t.delsys, t.modulation, t.symbolRate ? (t.symbolRate / 1000) + ' kS/s' : '', t.bandwidth ? (t.bandwidth / 1e6) + ' MHz bw' : '',
      t.polarization, t.fec && t.fec !== 'AUTO' ? 'FEC ' + t.fec : '', t.streamId >= 0 ? 'PLP ' + t.streamId : ''];
    return parts.filter(Boolean).join(' · ');
  }

  var DELSYS = { atsc: ['ATSC'], cable: ['DVB-C/B'], dvbt: ['DVB-T', 'DVB-T2'], dvbc: ['DVB-C'], dvbs: ['DVB-S', 'DVB-S2'] };

  function editMux(m, netType, done, netID) {
    var isNew = !m;
    m = m || { networkId: netID, enabled: true, tuning: { delsys: (DELSYS[netType] || ['DVB-T'])[0], frequency: 0, streamId: -1 } };
    DH.modal(isNew ? 'Add a frequency' : 'Edit ' + m.name, function (close) {
      var fields = netType === 'virtual' ? [{ key: 'file', label: 'Transport stream file (on the dvbhub computer)', wide: true }] : [
        { key: 'label', label: 'Label', placeholder: 'e.g. RF 14' },
        { key: 'tuning.delsys', label: 'Delivery system', type: 'select', options: DELSYS[netType] || ['DVB-T', 'DVB-T2', 'DVB-C', 'DVB-C/B', 'DVB-S', 'DVB-S2', 'ATSC'] },
        { key: 'tuning.frequency', label: 'Frequency (kHz)', type: 'number', help: 'Satellite: the transponder frequency, e.g. 11727000.' },
        { key: 'tuning.symbolRate', label: 'Symbol rate (S/s)', type: 'number', show: function () { return netType !== 'atsc' && netType !== 'dvbt'; } },
        { key: 'tuning.bandwidth', label: 'Bandwidth (Hz)', type: 'number', show: function () { return netType === 'dvbt'; } },
        { key: 'tuning.modulation', label: 'Modulation', type: 'select', options: ['', 'AUTO', 'QPSK', '8PSK', '16APSK', '32APSK', 'QAM/16', 'QAM/32', 'QAM/64', 'QAM/128', 'QAM/256', '8VSB'] },
        { key: 'tuning.polarization', label: 'Polarization', type: 'select', options: [['', '—'], 'H', 'V', 'L', 'R'], show: function () { return netType === 'dvbs'; } },
        { key: 'tuning.fec', label: 'FEC', type: 'select', options: ['', 'AUTO', '1/2', '2/3', '3/4', '3/5', '4/5', '5/6', '7/8', '8/9', '9/10'] },
        { key: 'tuning.rolloff', label: 'Roll-off', type: 'select', options: ['', 'AUTO', '35', '25', '20'], show: function () { return netType === 'dvbs'; } },
        { key: 'tuning.transmissionMode', label: 'Transmission mode', type: 'select', options: ['', 'AUTO', '2K', '8K', '1K', '4K', '16K', '32K'], show: function () { return netType === 'dvbt'; } },
        { key: 'tuning.guardInterval', label: 'Guard interval', type: 'select', options: ['', 'AUTO', '1/4', '1/8', '1/16', '1/32', '1/128', '19/128', '19/256'], show: function () { return netType === 'dvbt'; } },
        { key: 'tuning.streamId', label: 'PLP / stream id (-1 = none)', type: 'number' },
        { key: 'enabled', label: 'Use this frequency', type: 'bool' }
      ];
      var form = DH.form(m, fields);
      return h('div', {}, form.el, h('div', { class: 'dh-row', style: { marginTop: '14px' } },
        h('button', { class: 'dh-btn pri', onclick: function () {
          var body = form.changes();
          if (body.tuning) body.tuning = Object.assign({}, m.tuning, body.tuning);
          var req = isNew ? DH.post('muxes', Object.assign({ networkId: m.networkId, enabled: true }, body)) : DH.put('muxes/' + m.id, body);
          DH.act(req, isNew ? 'Added; scanning' : 'Saved').then(function () { close(); done(); });
        } }, isNew ? 'Add and scan' : 'Save'), h('button', { class: 'dh-btn', onclick: close }, 'Cancel')));
    });
  }

  function importTable(net, done) {
    DH.modal('Import a scan table', function (close) {
      var ta = h('textarea', { placeholder: '[CHANNEL]\n\tDELIVERY_SYSTEM = DVBT\n\tFREQUENCY = 506000000\n…\n\nor legacy lines like:  T 506000000 8MHz AUTO AUTO AUTO AUTO AUTO NONE' });
      var files = h('select', {}, h('option', { value: '' }, 'Loading tables…'));
      DH.get('scanfiles').then(function (list) {
        fill(files, h('option', { value: '' }, list.length ? 'Choose a table from dtv-scan-tables…' : 'No tables installed here; paste one below'),
          list.map(function (p) { return h('option', { value: p }, p.replace(/^\/usr\/(local\/)?share\/dvbv?5?\//, '')); }));
      });
      files.addEventListener('change', function () {
        if (files.value) DH.api('GET', 'scanfile?path=' + DH.enc(files.value)).then(function (t) { ta.value = t; });
      });
      return h('div', {}, DH.field('Table', files), h('div', { style: { margin: '10px 0' } }, ta),
        h('div', { class: 'dh-row' }, h('button', { class: 'dh-btn pri', onclick: function () {
          DH.act(DH.api('POST', 'networks/' + net.id + '/import', ta.value), function (r) { return r.added + ' of ' + r.parsed + ' frequencies added; scanning'; })
            .then(function () { close(); done(); });
        } }, 'Import and scan'), h('button', { class: 'dh-btn', onclick: close }, 'Cancel')));
    });
  }

  function editNetwork(net, done) {
    var isNew = !net;
    net = net || { type: 'atsc', name: '', discoverMuxes: true };
    DH.modal(isNew ? 'Add a network' : 'Edit ' + net.name, function (close) {
      var types = Object.keys(TYPE_NAMES).filter(function (t) { return t !== 'virtual' || DH.L('pro'); }).map(function (t) { return [t, TYPE_NAMES[t]]; });
      var form = DH.form(net, [
        { key: 'name', label: 'Name', placeholder: 'e.g. Antenna' },
        { key: 'type', label: 'Type', type: 'select', options: types },
        { key: 'discoverMuxes', label: 'Add frequencies announced by the broadcast (NIT)', type: 'bool', lvl: 'pro', help: 'DVB networks only.' }
      ]);
      return h('div', {}, form.el, h('div', { class: 'dh-row', style: { marginTop: '14px' } },
        h('button', { class: 'dh-btn pri', onclick: function () {
          var body = form.changes();
          var req = isNew ? DH.post('networks', Object.assign({ type: net.type, discoverMuxes: true }, body)) : DH.put('networks/' + net.id, body);
          DH.act(req, 'Saved').then(function () { close(); done(); });
        } }, 'Save'), h('button', { class: 'dh-btn', onclick: close }, 'Cancel')));
    });
  }

  function networkCard(net, muxes, plans, reload) {
    var open = DH.store.get('net.' + net.id, muxes.length <= 40);
    var body = h('div', {});
    function drawBody() {
      if (!open) { fill(body); return; }
      var pro = DH.L('pro');
      fill(body, muxes.length ? h('div', { class: 'dh-table-wrap', style: { marginTop: '10px' } }, h('table', { class: 'dh-t' },
        h('thead', {}, h('tr', {}, h('th', {}, 'Frequency'), h('th', {}, 'Result'), h('th', {}, 'Signal'), h('th', {}, 'Stations'), h('th', { class: 'dh-hide-sm' }, 'Scanned'), pro ? h('th', {}, 'On') : null, h('th', {}))),
        h('tbody', {}, muxes.map(function (m) { return muxRow(m, reload, net.type); })))) : h('p', { class: 'dh-muted' }, 'No frequencies yet. Add them from a channel plan or a scan table.'));
    }
    var planSel = DH.select([['', 'Add channels from a plan…']].concat(plans.filter(function (p) { return p.type === net.type; }).map(function (p) { return [p.id, p.name]; })), '', function (v) {
      if (v) DH.act(DH.post('networks/' + net.id + '/plan', { plan: v, scan: true }), function (r) { return r.added + ' frequencies added; scanning'; }).then(reload);
    });
    drawBody();
    return DH.card(h('div', { class: 'dh-between' },
      h('div', {}, h('b', {}, net.name), ' ', h('span', { class: 'dh-small dh-muted' }, TYPE_NAMES[net.type] || net.type),
        h('div', { class: 'dh-small dh-muted' }, net.muxes + ' frequencies · ' + net.locked + ' with signal · ' + net.services + ' stations')),
      h('div', { class: 'dh-row' },
        h('button', { class: 'dh-btn sm pri', onclick: function () { DH.act(DH.post('networks/' + net.id + '/scan', { autoMap: false }), function (r) { return r.queued + ' frequencies queued'; }).then(reload); } }, 'Scan all'),
        net.type !== 'virtual' && plans.some(function (p) { return p.type === net.type; }) ? planSel : null,
        net.type !== 'virtual' ? h('button', { class: 'dh-btn sm', onclick: function () { importTable(net, reload); } }, 'Import table') : null,
        DH.L('pro') ? h('button', { class: 'dh-btn sm', onclick: function () { editMux(null, net.type, reload, net.id); } }, 'Add frequency') : null,
        DH.L('pro') ? h('button', { class: 'dh-btn sm', onclick: function () { editNetwork(net, reload); } }, 'Edit') : null,
        h('button', { class: 'dh-btn sm red', onclick: function () {
          if (DH.confirm('Delete the network "' + net.name + '" with all its frequencies, stations and their channels?')) DH.act(DH.del('networks/' + net.id), 'Deleted').then(reload);
        } }, 'Delete'),
        h('button', { class: 'dh-btn sm', onclick: function () { open = !open; DH.store.set('net.' + net.id, open); drawBody(); } }, open ? 'Hide' : 'Show'))),
      body);
  }

  var svcQuery = ''; // survives the page redrawing while a scan runs

  function servicesCard(services, reload) {
    var onlyNew = DH.store.get('svcOnlyNew', false), q = svcQuery;
    var opt = DH.store.get('mapOpt', { includeRadio: false, skipScrambled: true, mergeByName: true });
    var box = h('div', {});
    var search = h('input', { type: 'search', placeholder: 'Filter', value: svcQuery });
    search.addEventListener('input', function () { q = svcQuery = search.value.toLowerCase(); draw(); });
    function map(ids) {
      var body = Object.assign({}, opt, ids ? { serviceIds: ids, skipScrambled: false } : {});
      DH.act(DH.post('map', body), function (r) { return r.created + ' channels created, ' + r.merged + ' added as backups'; }).then(reload);
    }
    function draw() {
      var list = services.filter(function (s) {
        return (!onlyNew || !s.mappedTo.length) && (!q || s.name.toLowerCase().indexOf(q) >= 0) && (DH.L('pro') || s.kind !== 'other');
      });
      fill(box, h('div', { class: 'dh-table-wrap' }, h('table', { class: 'dh-t' },
        h('thead', {}, h('tr', {}, h('th', {}, 'No.'), h('th', {}, 'Name'), h('th', {}, 'Type'), h('th', { class: 'dh-hide-sm' }, 'Frequency'), h('th', {}, 'Signal'), h('th', {}, 'Channel'), DH.L('pro') ? h('th', {}, 'On') : null)),
        h('tbody', {}, list.map(function (s) {
          return h('tr', { class: s.enabled ? '' : 'off' },
            h('td', { class: 'num' }, s.major ? s.major + (s.minor ? '.' + s.minor : '') : ''),
            h('td', {}, s.name, s.scrambled ? h('span', { class: 'dh-badge warn', style: { marginLeft: '6px' }, title: 'Encrypted: can\'t be watched' }, 'encrypted') : null,
              DH.L('pro') ? h('div', { class: 'dh-small dh-muted' }, 'sid ' + s.sid + ' · ' + s.streams.map(function (x) { return x.kind || ('0x' + x.streamType.toString(16)); }).join(', ')) : null),
            h('td', { class: 'dh-small' }, s.kind === 'tv' ? 'TV' : s.kind === 'radio' ? 'Radio' : 'Data'),
            h('td', { class: 'dh-small dh-muted dh-hide-sm' }, s.mux),
            h('td', {}, DH.bars(s.signal)),
            h('td', { class: 'dh-small' }, s.mappedTo.length ? s.mappedTo.join(', ') : h('button', { class: 'dh-btn sm', onclick: function () { map([s.id]); } }, 'Add')),
            DH.L('pro') ? h('td', {}, DH.toggle(s.enabled, function (v) { DH.act(DH.put('services/' + s.id, { enabled: v })).then(reload); }, 'Allow this service to be used')) : null);
        })))), h('p', { class: 'dh-small dh-muted' }, list.length + ' of ' + services.length + ' services'));
    }
    var opts = h('div', { class: 'dh-row dh-small' }, [['includeRadio', 'Include radio'], ['skipScrambled', 'Skip encrypted'], ['mergeByName', 'Same channel on two frequencies → one channel with a backup']].map(function (o) {
      return h('label', { class: 'dh-row', style: { gap: '6px' } }, DH.toggle(opt[o[0]], function (v) { opt[o[0]] = v; DH.store.set('mapOpt', opt); }), o[1]);
    }));
    draw();
    return DH.card(h('div', { class: 'dh-between' }, h('h3', { style: { margin: 0 } }, 'Stations found', DH.lvlTag('advanced')),
      h('div', { class: 'dh-row' }, search,
        h('label', { class: 'dh-row dh-small', style: { gap: '6px' } }, DH.toggle(onlyNew, function (v) { onlyNew = v; DH.store.set('svcOnlyNew', v); draw(); }), 'Not on a channel yet'),
        h('button', { class: 'dh-btn sm pri', onclick: function () { map(null); } }, 'Add all new as channels'))),
      h('div', { style: { margin: '10px 0' } }, opts), box);
  }

  DH.page('scan', {
    title: 'Scan', order: 40,
    attention: function (st) { return (st.scan.jobs || []).some(function (j) { return !j.finished; }) ? 'Scanning' : ''; },
    render: function (view, ctx) {
      var last = '';
      function load() {
        var adv = DH.L('advanced');
        return Promise.all([DH.get('status'), DH.get('plans'), DH.get('networks'), adv ? DH.get('muxes') : Promise.resolve([]), adv ? DH.get('services') : Promise.resolve([])])
          .then(function (r) {
            var st = r[0], plans = r[1], nets = r[2], muxes = r[3], services = r[4];
            var sig = JSON.stringify([st.scan, nets, muxes.map(function (m) { return [m.id, m.scan, m.signal && m.signal.bars, m.enabled]; }), services.length, DH.level]);
            if (sig === last) return;
            last = sig;
            var out = [];
            var jobs = st.scan.jobs || [], running = jobs.filter(function (j) { return !j.finished; });
            running.forEach(function (j) {
              out.push(DH.card(DH.scanProgress(j), h('div', { class: 'dh-row', style: { marginTop: '10px' } },
                h('button', { class: 'dh-btn sm', onclick: function () { DH.act(DH.post('scan/cancel?network=' + DH.enc(j.networkId)), 'Stopped').then(load); } }, 'Stop scan'))));
            });
            var done = jobs.filter(function (j) { return j.finished && !j.cancelled; })[0];
            if (!running.length && done && Date.now() - new Date(done.finished).getTime() < 3600000) {
              out.push(h('div', { class: 'dh-alert ' + (done.services ? 'good' : 'warn') },
                done.services ? h('span', {}, h('b', {}, 'Scan finished: '), done.services + ' stations found' + (done.autoMap ? ', ' + done.mapped + ' new channels added. ' : '. '),
                  h('button', { class: 'dh-link', onclick: function () { ctx.go('channels'); } }, 'See channels'))
                  : h('span', {}, h('b', {}, 'Nothing found. '), 'Check the antenna or cable connection, and that the tuner works (Tuners page). Then scan again.')));
            }
            if (!running.length) out.push(wizard(view, ctx, st, plans, nets, function () { last = ''; load(); }));
            if (adv) {
              out.push(h('div', { class: 'dh-between', style: { margin: '20px 0 10px' } }, h('h2', { style: { margin: 0 } }, 'Networks', DH.lvlTag('advanced')),
                h('button', { class: 'dh-btn', onclick: function () { editNetwork(null, function () { last = ''; load(); }); } }, 'Add network')));
              if (!nets.length) out.push(DH.card(h('p', { class: 'dh-muted' }, 'No networks yet.')));
              nets.forEach(function (n) { out.push(networkCard(n, muxes.filter(function (m) { return m.networkId === n.id; }), plans, function () { last = ''; load(); })); });
              if (services.length) out.push(servicesCard(services, function () { last = ''; load(); }));
            }
            fill(view, out);
          }, function (e) { fill(view, DH.errorBox(e)); });
      }
      ctx.every(1500, load);
      return load();
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
