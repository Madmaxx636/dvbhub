/* Settings: one set of settings; the level only decides which are shown. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  function xmltvCard(settings, reload) {
    var list = (settings.xmltv || []).map(function (x) { return { url: x.url, enabled: x.enabled }; });
    var box = h('div', {}), status = h('div', { class: 'dh-small dh-muted', style: { marginTop: '8px' } });
    function draw() {
      fill(box, list.map(function (x, i) {
        var u = h('input', { value: x.url, placeholder: 'https://example.com/guide.xml.gz or /path/guide.xml', style: { flex: 1, minWidth: '220px' } });
        u.addEventListener('input', function () { x.url = u.value; });
        return h('div', { class: 'dh-row', style: { marginBottom: '6px' } }, DH.toggle(x.enabled, function (v) { x.enabled = v; }, 'Use this source'), u,
          h('button', { class: 'dh-btn sm red', onclick: function () { list.splice(i, 1); draw(); } }, '✕'));
      }), h('div', { class: 'dh-row' },
        h('button', { class: 'dh-btn sm', onclick: function () { list.push({ url: '', enabled: true }); draw(); } }, 'Add source'),
        h('button', { class: 'dh-btn sm pri', onclick: function () {
          DH.act(DH.put('settings', { xmltv: list.filter(function (x) { return x.url.trim(); }) }), 'Saved; importing').then(reload);
        } }, 'Save sources'),
        h('button', { class: 'dh-btn sm', onclick: function () { DH.act(DH.post('xmltv/refresh'), 'Importing in the background'); } }, 'Import now'),
        h('button', { class: 'dh-btn sm', title: 'Give channels without an XMLTV id the id of the XMLTV channel with the same name', onclick: function () {
          DH.act(DH.post('xmltv/automap'), function (r) { return r.mapped + ' channel(s) matched by name'; });
        } }, 'Match channels by name')));
    }
    DH.get('xmltv/channels').then(function (x) {
      var s = x.status;
      fill(status, s.running ? 'Importing…' : DH.isZeroTime(s.lastRun) ? 'Not imported yet.' : 'Last import ' + DH.fmtDay(s.lastRun) + ': ' + s.info,
        s.lastError ? h('div', { style: { color: 'var(--bad)' } }, s.lastError) : null);
    });
    draw();
    return DH.card(h('h3', {}, 'XMLTV guide sources', DH.lvlTag('advanced')),
      h('p', { class: 'dh-small dh-muted' }, 'Optional. Channels with an XMLTV id (Channels → Edit → Guide source) take their guide from here instead of the broadcast.'),
      box, status);
  }

  function logsCard() {
    var pre = h('pre', { class: 'dh-log', style: { maxHeight: '480px' } }), last = 0, lines = [], filter = '';
    var f = h('input', { type: 'search', placeholder: 'Filter' });
    f.addEventListener('input', function () { filter = f.value.toLowerCase(); draw(); });
    function draw() {
      var atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 30;
      pre.textContent = lines.filter(function (l) { return !filter || l.toLowerCase().indexOf(filter) >= 0; }).join('\n');
      if (atBottom) pre.scrollTop = pre.scrollHeight;
    }
    function load() {
      return DH.get('logs?since=' + last).then(function (ls) {
        if (!ls.length) return;
        last = ls[ls.length - 1].n;
        lines = lines.concat(ls.map(function (l) { return l.text; })).slice(-1000);
        draw();
      });
    }
    return {
      load: load,
      el: DH.card(h('div', { class: 'dh-between' }, h('h3', { style: { margin: 0 } }, 'Log', DH.lvlTag('pro')), f), h('div', { style: { marginTop: '10px' } }, pre))
    };
  }

  DH.page('settings', {
    title: 'Settings', order: 70,
    render: function (view, ctx) {
      function load() {
        return DH.get('settings').then(function (s) {
          var form = DH.form(s, [
            { section: 'General' },
            { key: 'serverName', label: 'Name shown in Jellyfin' },
            { key: 'uiLevel', label: 'Detail level for new browsers', type: 'select', options: [['simple', 'Simple'], ['advanced', 'Advanced'], ['pro', 'Pro']], help: 'Each browser remembers its own choice (top right).' },
            { section: 'Programme guide' },
            { key: 'eit', label: 'Read the guide from the broadcast', type: 'bool', help: 'Free; comes with the channels.' },
            { key: 'guideDays', label: 'Days of guide to send to Jellyfin', type: 'number', min: 1, max: 31, lvl: 'advanced' },
            { key: 'guideGrabHours', label: 'Refresh the broadcast guide every (hours)', type: 'number', min: 0, max: 168, lvl: 'advanced', help: 'Briefly tunes idle tuners; 0 = only while watching.' },
            { key: 'xmltvHours', label: 'Import XMLTV every (hours)', type: 'number', min: 1, max: 168, lvl: 'advanced' },
            { section: 'Network', lvl: 'advanced' },
            { key: 'baseUrl', label: 'Address Jellyfin uses to reach dvbhub', lvl: 'advanced', placeholder: 'automatic (e.g. http://192.168.1.10:9980)', help: 'Only needed behind a proxy or when the automatic address is wrong.' },
            { key: 'ssdp', label: 'Let Jellyfin and Plex find dvbhub automatically', type: 'bool', lvl: 'pro', help: 'SSDP and HDHomeRun discovery. Takes effect after a restart.' },
            { key: 'deviceId', label: 'HDHomeRun device id', lvl: 'pro', help: '8 hex digits; changing it makes Jellyfin see a new tuner.' },
            { section: 'Signal loss', lvl: 'advanced' },
            { key: 'failoverSecs', label: 'Switch to a backup service after (seconds)', type: 'number', min: 1, max: 300, lvl: 'advanced' },
            { key: 'maxOutageSecs', label: 'Give up on a dead stream after (seconds)', type: 'number', min: 0, max: 3600, lvl: 'pro', help: 'Frees the tuner; 0 = never.' },
            { key: 'lingerSecs', label: 'Keep a tuner tuned after the last viewer (seconds)', type: 'number', min: 0, max: 300, lvl: 'pro', help: 'Makes channel changes back and forth faster.' },
            { section: 'System', lvl: 'pro' },
            { key: 'ffmpeg', label: 'ffmpeg program', lvl: 'pro' },
            { key: 'hotplugSecs', label: 'Check for plugged/unplugged tuners every (seconds)', type: 'number', min: 1, max: 3600, lvl: 'pro' },
            { key: 'virtualTuners', label: 'Test tuners that play files', type: 'number', min: 0, max: 8, lvl: 'pro' }
          ]);
          var out = [DH.card(form.el, h('div', { class: 'dh-row', style: { marginTop: '16px' } },
            h('button', { class: 'dh-btn pri', onclick: function () {
              if (!form.changed()) { DH.toast('Nothing changed'); return; }
              var body = form.changes();
              DH.act(DH.put('settings', body), 'Saved').then(function () {
                if (body.uiLevel && !DH.store.get('level', null)) { DH.level = body.uiLevel; }
                load();
              });
            } }, 'Save settings')))];
          if (DH.L('advanced')) out.push(xmltvCard(s, load));
          var logs = null;
          if (DH.L('pro')) {
            logs = logsCard();
            out.push(logs.el);
            out.push(DH.card(h('h3', {}, 'For scripts', DH.lvlTag('pro')), h('p', { class: 'dh-small' }, 'Signal of every tuner as JSON: ', h('code', {}, 'GET /api/signal'),
              '. Full status: ', h('code', {}, 'GET /api/status'), '. HDHomeRun lineup: ', h('code', {}, '/lineup.json'), '. Guide: ', h('code', {}, '/xmltv.xml'), '.')));
          }
          fill(view, out);
          if (logs) { logs.load(); ctx.every(2000, logs.load); }
        }, function (e) { fill(view, DH.errorBox(e)); });
      }
      return load();
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
