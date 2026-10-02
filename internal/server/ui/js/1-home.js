/* Home: what's happening now, problems that need attention, Jellyfin setup. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  DH.tunerCard = function (t, opts) {
    opts = opts || {};
    var sig = t.signal || {};
    var snap = t.state === 'idle' ? null : { bars: t.bars, locked: sig.locked, snrDb: sig.snrDb, strengthPct: sig.strengthPct, quality: t.quality, live: true };
    var card = h('div', { class: 'dh-card dh-tuner' });
    card.appendChild(h('div', { class: 'dh-between' },
      h('div', {}, h('b', {}, t.config.name || t.name), !t.config.enabled ? h('span', { class: 'dh-badge', style: { marginLeft: '6px' } }, 'Off') : null),
      DH.badge(t.state)));
    card.appendChild(h('div', { class: 'dh-small dh-muted' }, DH.L('advanced') ? t.key + ' · ' : '', (t.delsys || []).join(', ') || t.name));
    if (t.state !== 'idle') {
      card.appendChild(h('div', { class: 'dh-row', style: { marginTop: '8px' } }, DH.bars(snap), h('span', { class: 'dh-small' }, t.mux || '')));
    }
    if (t.lastError && t.state !== 'streaming' && t.state !== 'idle') card.appendChild(h('div', { class: 'dh-small', style: { color: 'var(--bad)', marginTop: '4px' } }, t.lastError));
    if (t.hold && t.hold.indexOf('blocked') === 0) card.appendChild(h('div', { class: 'dh-small', style: { color: 'var(--warn)', marginTop: '4px' } }, 'Exclusive hold ' + t.hold));
    if (DH.L('pro') && t.state !== 'idle') {
      card.appendChild(h('div', { class: 'dh-small dh-muted', style: { marginTop: '6px' } }, [
        sig.strengthPct >= 0 ? 'Strength ' + Math.round(sig.strengthPct) + '%' + (sig.strengthDbm != null ? ' (' + sig.strengthDbm.toFixed(1) + ' dBm)' : '') : null,
        sig.snrDb != null ? 'SNR ' + sig.snrDb.toFixed(1) + ' dB' : sig.snrPct >= 0 ? 'SNR ' + Math.round(sig.snrPct) + '%' : null,
        sig.ber >= 0 ? 'BER ' + sig.ber.toExponential(1) : null,
        'Uncorrected ' + (sig.unc || 0), 'CC errors ' + (t.ccErrors || 0), DH.fmtKbps(t.kbps), t.retunes ? 'Re-tunes ' + t.retunes : null
      ].filter(Boolean).join(' · ')));
    }
    var JOBS = { guide: 'Collecting the programme guide', scan: 'Scanning', 'antenna alignment': 'Antenna alignment' };
    (t.subscriptions || []).forEach(function (s) {
      var tx = s.transcode;
      if (s.weight < 100 || JOBS[s.name]) {
        card.appendChild(h('div', { class: 'dh-sub dh-muted' }, JOBS[s.name] || s.name, h('span', { class: 'dh-small' }, ' · ' + DH.since(s.started))));
        return;
      }
      card.appendChild(h('div', { class: 'dh-sub' },
        h('div', { style: { flex: 1, minWidth: 0 } },
          h('div', {}, h('b', {}, s.name), s.failovers ? h('span', { class: 'dh-badge warn', style: { marginLeft: '6px' }, title: 'Switched to a backup service' }, 'backup') : null),
          h('div', { class: 'dh-small dh-muted' }, [s.client || null, s.profile || null, DH.since(s.started)].filter(Boolean).join(' · ')),
          tx && DH.L('advanced') ? h('div', { class: 'dh-small dh-muted' }, (tx.encoder || '') + (tx.hwDecode ? ' + GPU decode' : '') +
            (tx.fps ? ' · ' + Math.round(tx.fps) + ' fps' : '') + (tx.speed ? ' · ' + tx.speed : '') + (tx.outKbps ? ' · ' + DH.fmtKbps(tx.outKbps) : '') +
            (tx.fallbacks ? ' · fell back ' + tx.fallbacks + '×' : '')) : null),
        s.weight >= 100 && !opts.noStop ? h('button', { class: 'dh-btn sm', title: 'Stop this stream', onclick: function () {
          if (DH.confirm('Stop "' + s.name + '" for ' + (s.client || 'this viewer') + '?')) DH.act(DH.del('subscriptions/' + s.id), 'Stopped').then(opts.refresh);
        } }, 'Stop') : null));
    });
    return card;
  };

  // Jellyfin connection instructions, or one-click setup inside the plugin.
  DH.jellyfinCard = function (info) {
    var card = DH.card(h('h3', {}, 'Watch in Jellyfin'));
    if (DH.host.jellyfin) {
      var state = h('div', { class: 'dh-small dh-muted', style: { marginTop: '8px' } });
      card.appendChild(h('p', {}, 'Adds dvbhub to Jellyfin\'s Live TV as a tuner, with its programme guide. Recordings use Jellyfin\'s own DVR.'));
      card.appendChild(h('button', { class: 'dh-btn pri', onclick: function () {
        DH.act(DH.host.jellyfin.addToLiveTv(info.base, info.xmltv, 'dvbhub')).then(function (msg) { DH.toast(msg || 'Added to Live TV'); check(); });
      } }, 'Add dvbhub to Live TV'));
      card.appendChild(state);
      function check() {
        if (!DH.host.jellyfin.status) return;
        DH.host.jellyfin.status(info.base).then(function (s) { fill(state, s); }, function () { });
      }
      check();
      return card;
    }
    card.appendChild(h('ol', { class: 'dh-steps' },
      h('li', {}, 'In Jellyfin open ', h('b', {}, 'Dashboard → Live TV → Tuner Devices → + → HDHomeRun'), ' and enter:', DH.copyBox(info.base)),
      h('li', {}, 'Then ', h('b', {}, 'TV Guide Data Providers → + → XMLTV'), ' and enter:', DH.copyBox(info.xmltv)),
      h('li', {}, 'Optional: install the dvbhub plugin in Jellyfin to control everything from Jellyfin\'s dashboard.')));
    if (DH.L('advanced')) card.appendChild(h('p', { class: 'dh-small dh-muted' }, 'Other players: M3U playlist ', h('code', {}, info.m3u), '. Transcoded variants are on the Streaming page.'));
    return card;
  };

  DH.scanProgress = function (job, compact) {
    var pct = job.total ? Math.round(job.done * 100 / job.total) : 0;
    return h('div', {},
      h('div', { class: 'dh-between' }, h('b', {}, job.finished ? 'Scan finished' : 'Scanning ' + job.network + '…'),
        h('span', { class: 'dh-small dh-muted' }, job.done + ' of ' + job.total + ' channels checked')),
      h('div', { class: 'dh-progress', style: { margin: '8px 0' } }, h('i', { style: { width: pct + '%' } })),
      h('div', { class: 'dh-small' }, job.services + ' station' + (job.services === 1 ? '' : 's') + ' found on ' + job.locked + ' frequenc' + (job.locked === 1 ? 'y' : 'ies'),
        !job.finished && job.current && job.current.length ? h('span', { class: 'dh-muted' }, ' · now: ' + job.current.join(', ')) : null,
        job.finished && job.autoMap ? h('span', {}, ' · ' + job.mapped + ' new channel' + (job.mapped === 1 ? '' : 's') + ' added') : null),
      compact ? null : null);
  };

  DH.page('home', {
    title: 'Home', order: 10,
    attention: function (st) { return st.hardwareScanned && !st.tuners.length ? 'No tuner found' : ''; },
    render: function (view, ctx) {
      var hw = null, jf = null, st = null;
      var boxes = {}, order = ['alert', 'scan', 'empty', 'tuners', 'jellyfin', 'stats'];
      fill(view, order.map(function (k) { return boxes[k] = h('div', {}); }));
      DH.get('hardware').then(function (x) { hw = x; draw(); }, function () { });
      DH.get('jellyfin').then(function (x) { jf = x; draw(); }, function () { });
      function load() { return DH.get('status').then(function (x) { st = x; ctx.app.status = x; draw(); }, function (e) { fill(boxes.alert, DH.errorBox(e)); }); }
      function draw() {
        if (!st) return;
        var showHW = hw && (hw.report.problem || (st.hardwareScanned && !st.tuners.length));
        DH.patch(boxes.alert, [showHW, hw && hw.report.summary], function () {
          return showHW ? h('div', { class: 'dh-alert warn dh-between' }, h('span', {}, h('b', {}, 'Tuner hardware: '), hw.report.summary),
            h('button', { class: 'dh-btn sm', onclick: function () { ctx.go('tuners'); } }, 'Fix it')) : null;
        });
        var running = (st.scan.jobs || []).filter(function (j) { return !j.finished; });
        DH.patch(boxes.scan, running, function () {
          return running.map(function (j) { return DH.card(DH.scanProgress(j), h('button', { class: 'dh-btn sm', style: { marginTop: '8px' }, onclick: function () { ctx.go('scan'); } }, 'Open scan')); });
        });
        var empty = !st.counts.channels && !running.length;
        DH.patch(boxes.empty, [empty, st.tuners.length], function () {
          return empty ? h('div', { class: 'dh-card dh-empty' }, h('h2', {}, 'Let\'s find your channels'),
            h('p', { class: 'dh-muted' }, st.tuners.length ? 'dvbhub found ' + st.tuners.length + ' tuner' + (st.tuners.length === 1 ? '' : 's') + '. Scan for channels to get started.' : 'Connect a TV tuner, then scan for channels.'),
            h('button', { class: 'dh-btn pri big', onclick: function () { ctx.go('scan'); } }, 'Find channels')) : null;
        });
        DH.patch(boxes.tuners, [st.tuners, DH.level], function () {
          if (st.tuners.length) return h('div', { class: 'dh-grid' }, st.tuners.map(function (t) { return DH.tunerCard(t, { refresh: load }); }));
          return !st.hardwareScanned ? DH.card(h('p', { class: 'dh-muted' }, 'Looking for tuners…')) : null;
        });
        DH.patch(boxes.jellyfin, [!!jf, st.counts.channels > 0, jf && jf.base, DH.level], function () {
          return jf && st.counts.channels ? DH.jellyfinCard(jf) : null;
        });
        var watching = st.subscriptions.filter(function (s) { return s.weight >= 100; }).length;
        DH.patch(boxes.stats, [st.counts, st.guide.events, watching, DH.level, DH.L('pro') ? Math.round(st.uptime / 60) : 0], function () {
          return DH.card(h('div', { class: 'dh-stats' },
            stat(st.counts.enabledChannels || 0, 'channels'),
            stat(st.guide.events, 'guide programmes'),
            stat(watching, 'watching now'),
            DH.L('advanced') ? stat(st.counts.services, 'services') : null,
            DH.L('advanced') ? stat(st.counts.muxes, 'frequencies') : null,
            DH.L('pro') ? stat(DH.fmtDur(st.uptime), 'up') : null),
            DH.L('advanced') ? h('div', { class: 'dh-small dh-muted', style: { marginTop: '8px' } }, 'dvbhub ' + st.version) : null);
        });
      }
      function stat(v, label) { return h('div', { class: 'dh-stat' }, h('b', {}, v), h('span', { class: 'dh-small dh-muted' }, label)); }
      ctx.every(2000, load);
      return load();
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
