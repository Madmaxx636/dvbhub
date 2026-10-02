/* Streaming: quality profiles (passthrough or transcoding) and GPU status. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  var KIND_NAMES = { nvenc: 'NVIDIA GPU (NVENC)', qsv: 'Intel GPU (Quick Sync)', vaapi: 'GPU (VAAPI)', cpu: 'the CPU' };
  var CODEC_NAMES = { h264: 'H.264', hevc: 'HEVC', av1: 'AV1' };

  function describeProfile(p, caps) {
    if (p.passthrough) return 'Sends the broadcast exactly as received. Uses almost no CPU and keeps full quality.';
    var enc = p.encoder === 'auto' || !p.encoder ? (caps ? caps.auto[p.codec] : 'auto') : p.encoder;
    var what = CODEC_NAMES[p.codec] + (p.height ? ' ' + p.height + 'p' : '') +
      (p.rateControl === 'cq' ? ' at quality ' + p.quality : ' at ' + DH.fmtKbps(p.bitrate)) + ' on ' + (KIND_NAMES[enc] || enc);
    if (p.mode === 'mpeg2') return 'Converts MPEG-2 channels (most US antenna TV) to ' + what + ', so phones and browsers can play them. Other channels pass through untouched.';
    return 'Converts every channel to ' + what + '.';
  }

  var HEIGHTS = [[0, 'Keep original'], [2160, '2160p (4K)'], [1080, '1080p'], [720, '720p'], [576, '576p'], [480, '480p'], [360, '360p']];

  function editProfile(p, caps, done) {
    var isNew = !p;
    var id = isNew ? '' : p.id;
    p = p || { name: '', mode: 'transcode', encoder: 'auto', codec: 'h264', height: 720, fps: 0, deinterlace: 'auto', rateControl: 'vbr', bitrate: 4000, maxrate: 6000,
      bufsize: 0, quality: 23, preset: 'balanced', gop: 0, audioCodec: 'aac', audioBitrate: 160, audioChannels: 2, audioTracks: 'all', subtitles: 'drop', gpu: 0, extra: '' };
    DH.modal(isNew ? 'New quality profile' : 'Edit ' + p.name, function (close) {
      var box = h('div', {});
      var idInput = isNew ? h('input', { placeholder: 'e.g. tablet-720p', pattern: '[a-z0-9._-]+' }) : null;
      var mode = p.mode, pending = {}, form;
      function draw() {
        var tx = mode !== 'passthrough';
        var t = function () { return tx; };
        form = DH.form(p, [
          { key: 'name', label: 'Name' },
          { key: 'mode', label: 'What to do', type: 'select', options: [['passthrough', 'Pass through (no conversion)'], ['mpeg2', 'Convert MPEG-2 channels only'], ['transcode', 'Convert every channel']],
            onchange: function (v) { mode = v; Object.assign(pending, form.changes()); p = Object.assign({}, p, pending); draw(); } },
          { section: 'Video', show: t },
          { key: 'encoder', label: 'Convert with', type: 'select', show: t, options: [['auto', 'Automatic (' + (KIND_NAMES[caps.auto[p.codec] || 'cpu']) + ')'], ['nvenc', KIND_NAMES.nvenc], ['qsv', KIND_NAMES.qsv], ['vaapi', KIND_NAMES.vaapi], ['cpu', 'CPU']] },
          { key: 'codec', label: 'Video format', type: 'select', show: t, options: [['h264', 'H.264 (plays everywhere)'], ['hevc', 'HEVC (smaller)'], ['av1', 'AV1 (smallest, newest devices)']] },
          { key: 'height', label: 'Resolution', type: 'select', show: t, options: HEIGHTS },
          { key: 'rateControl', label: 'Bitrate mode', type: 'select', show: t, options: [['vbr', 'Variable (VBR)'], ['cbr', 'Constant (CBR)'], ['cq', 'Constant quality']] },
          { key: 'bitrate', label: 'Video bitrate (kbit/s)', type: 'number', min: 0, show: t, help: '3000 ≈ 720p, 6000–8000 ≈ 1080p' },
          { key: 'maxrate', label: 'Peak bitrate (kbit/s)', type: 'number', min: 0, show: t, help: 'VBR only; 0 = 1.5× the bitrate' },
          { key: 'quality', label: 'Quality (constant quality mode)', type: 'number', min: 0, max: 63, show: t, help: 'Lower is better; 19–28 is typical' },
          { key: 'deinterlace', label: 'Deinterlace', type: 'select', show: t, options: [['auto', 'When needed'], ['on', 'Always'], ['off', 'Never']] },
          { key: 'fps', label: 'Frame rate (0 = keep)', type: 'number', min: 0, max: 120, lvl: 'pro', show: t },
          { key: 'bufsize', label: 'Rate buffer / VBV (kbit, 0 = 2× bitrate)', type: 'number', min: 0, lvl: 'pro', show: t },
          { key: 'preset', label: 'Speed', type: 'select', lvl: 'pro', show: t, options: [['fast', 'Fast'], ['balanced', 'Balanced'], ['quality', 'Best quality']] },
          { key: 'gop', label: 'Keyframe every (frames, 0 = 2 s)', type: 'number', min: 0, lvl: 'pro', show: t },
          { key: 'gpu', label: 'GPU number', type: 'number', min: 0, max: 16, lvl: 'pro', show: t, help: 'When there is more than one GPU' },
          { section: 'Audio and subtitles', show: t },
          { key: 'audioCodec', label: 'Audio', type: 'select', show: t, options: [['copy', 'Keep original'], ['aac', 'AAC'], ['ac3', 'Dolby Digital (AC-3)'], ['opus', 'Opus']] },
          { key: 'audioBitrate', label: 'Audio bitrate (kbit/s)', type: 'number', min: 0, show: t },
          { key: 'audioChannels', label: 'Audio channels', type: 'select', show: t, options: [[0, 'Keep'], [2, 'Stereo'], [6, '5.1']] },
          { key: 'audioTracks', label: 'Audio tracks', type: 'select', lvl: 'pro', show: t, options: [['all', 'All languages'], ['first', 'First only']] },
          { key: 'subtitles', label: 'Subtitles', type: 'select', lvl: 'pro', show: t, options: [['drop', 'Remove'], ['keep', 'Keep']] },
          { key: 'extra', label: 'Extra ffmpeg output options', lvl: 'pro', show: t, wide: true, placeholder: 'e.g. -tune film', help: 'Added before the output. No quotes or shell characters.' }
        ]);
        form.changes = (function (orig) { return function () { var c = orig(); c.mode = mode; return c; }; })(form.changes);
        fill(box, isNew ? DH.field('Profile id (letters, digits, - . _)', idInput, 'Used in the tuner address: /p/<id>') : null, form.el,
          !isNew && p.command && DH.L('pro') && mode !== 'passthrough' ? h('div', { style: { marginTop: '14px' } }, h('div', { class: 'dh-section' }, 'Command (first choice)'), h('pre', { class: 'dh-log' }, p.command),
            p.plans ? h('p', { class: 'dh-small dh-muted' }, 'If that fails: ' + p.plans.slice(1).map(function (x) { return x.encoder + (x.hwDecode ? ' + GPU decode' : ''); }).join(', then ')) : null) : null,
          h('div', { class: 'dh-row', style: { marginTop: '16px' } },
            h('button', { class: 'dh-btn pri', onclick: function () {
              var pid = isNew ? idInput.value.trim().toLowerCase() : id;
              if (!pid) { DH.toast('Enter a profile id', true); return; }
              var body = Object.assign({}, pending, form.changes());
              if (isNew) body = Object.assign({}, p, body);
              DH.act(DH.put('profiles/' + DH.enc(pid), body), 'Saved').then(function () { close(); done(); });
            } }, 'Save'),
            h('button', { class: 'dh-btn', onclick: close }, 'Cancel')));
      }
      draw();
      return box;
    });
  }

  function encoderCard(caps, reload) {
    var kinds = ['nvenc', 'qsv', 'vaapi', 'cpu'];
    var rows = kinds.map(function (k) {
      return h('tr', {}, h('td', {}, KIND_NAMES[k] === 'the CPU' ? 'CPU' : KIND_NAMES[k]), ['h264', 'hevc', 'av1'].map(function (c) {
        var e = caps.encoders.filter(function (x) { return x.kind === k && x.codec === c; })[0] || {};
        var cell = !e.inFfmpeg ? h('span', { class: 'dh-muted dh-small' }, 'not in ffmpeg')
          : e.works ? DH.badge('ok', 'works') : e.tested ? DH.badge('fail', 'fails') : h('span', { class: 'dh-muted dh-small' }, 'not tested');
        if (e.error) cell.title = e.error;
        return h('td', {}, cell, DH.L('pro') && e.error ? h('div', { class: 'dh-small dh-muted', style: { maxWidth: '220px' } }, e.error) : null);
      }));
    });
    return DH.card(h('div', { class: 'dh-between' }, h('h3', { style: { margin: 0 } }, 'Conversion hardware', DH.lvlTag('advanced')),
      h('button', { class: 'dh-btn sm', disabled: caps.probing, onclick: function () { DH.act(DH.post('transcode/probe'), 'Testing encoders…').then(function () { setTimeout(reload, 4000); }); } }, caps.probing ? 'Testing…' : 'Test again')),
      !caps.ffmpegOk ? h('div', { class: 'dh-alert bad', style: { marginTop: '10px' } }, 'ffmpeg wasn\'t found (' + caps.ffmpeg + '). Only "pass through" works until it is installed.') : null,
      h('div', { class: 'dh-table-wrap', style: { marginTop: '10px' } }, h('table', { class: 'dh-t' }, h('thead', {}, h('tr', {}, h('th', {}), h('th', {}, 'H.264'), h('th', {}, 'HEVC'), h('th', {}, 'AV1'))), h('tbody', {}, rows))),
      DH.L('pro') ? h('div', { class: 'dh-small dh-muted', style: { marginTop: '10px' } },
        h('div', {}, caps.version || ''),
        h('div', {}, 'Hardware decoders: ' + (caps.hwaccels.join(', ') || 'none')),
        h('div', {}, 'GPU filters: ' + Object.keys(caps.filters).filter(function (f) { return caps.filters[f] && f.indexOf('_') > 0; }).join(', ')),
        h('div', {}, 'Render devices: ' + ((caps.renderNodes || []).map(function (n) { return n.path + ' (' + n.vendor + (n.driver ? ', ' + n.driver : '') + ')'; }).join(', ') || 'none')),
        caps.smiError ? h('div', {}, caps.smiError) : null) : null,
      (caps.gpus || []).length ? h('div', { style: { marginTop: '10px' } }, caps.gpus.map(function (g) {
        return h('div', { class: 'dh-small' }, h('b', {}, 'GPU ' + g.index + ': ' + g.name), ' · load ' + g.util + '% · encoder ' + g.encUtil + '% · decoder ' + g.decUtil + '% · ' +
          Math.round(g.memUsedMb) + '/' + Math.round(g.memTotalMb) + ' MB · ' + g.tempC + ' °C · ' + g.encoderSessions + ' encode session(s)');
      })) : null);
  }

  DH.page('streaming', {
    title: 'Streaming', order: 50,
    render: function (view, ctx) {
      function load() {
        return Promise.all([DH.get('profiles'), DH.get('settings'), DH.get('transcode'), DH.get('jellyfin')]).then(function (r) {
          var profiles = r[0], settings = r[1], caps = r[2], jf = r[3];
          var out = [];
          var autoKind = caps.auto.h264;
          out.push(DH.card(h('h2', {}, 'Stream quality'),
            h('p', { class: 'dh-muted' }, 'How channels are sent to Jellyfin. Channels can also have their own quality (Channels → Edit).'),
            h('div', { class: 'dh-choices' }, profiles.map(function (p) {
              return h('button', { class: 'dh-choice' + (p.default ? ' on' : ''), onclick: function () {
                if (!p.default) DH.act(DH.put('settings', { defaultProfile: p.id }), p.name + ' is now the default').then(load);
              } }, h('b', {}, p.name), h('span', { class: 'dh-small dh-muted' }, describeProfile(p, caps)));
            })),
            h('p', { class: 'dh-small', style: { marginTop: '12px' } }, 'Conversion uses ', h('b', {}, KIND_NAMES[autoKind]),
              autoKind === 'cpu' ? ' (no working GPU encoder was found; converting HD on the CPU is slow).' : '.')));

          if (DH.L('advanced')) {
            out.push(DH.card(h('div', { class: 'dh-between' }, h('h3', { style: { margin: 0 } }, 'Profiles', DH.lvlTag('advanced')),
              h('button', { class: 'dh-btn sm', onclick: function () { editProfile(null, caps, load); } }, 'New profile')),
              h('p', { class: 'dh-small dh-muted' }, 'Each profile is also its own tuner for Jellyfin, so you can add, say, an "HD" tuner next to the original one.'),
              h('div', { class: 'dh-table-wrap' }, h('table', { class: 'dh-t' },
                h('thead', {}, h('tr', {}, h('th', {}, 'Profile'), h('th', {}, 'Tuner address for Jellyfin'), h('th', {}))),
                h('tbody', {}, profiles.map(function (p) {
                  var url = (jf.tuners.filter(function (t) { return t.profile === p.id; })[0] || {}).url || '';
                  return h('tr', {},
                    h('td', {}, h('b', {}, p.name), p.default ? h('span', { class: 'dh-badge acc', style: { marginLeft: '6px' } }, 'default') : null,
                      h('div', { class: 'dh-small dh-muted' }, describeProfile(p, caps)), p.channels ? h('div', { class: 'dh-small dh-muted' }, 'Used by ' + p.channels + ' channel(s)') : null),
                    h('td', { style: { minWidth: '260px' } }, DH.copyBox(url),
                      DH.host.jellyfin ? h('button', { class: 'dh-btn sm', style: { marginTop: '6px' }, onclick: function () {
                        DH.act(DH.host.jellyfin.addToLiveTv(url, jf.xmltv, 'dvbhub ' + p.name)).then(function (m) { DH.toast(m || 'Added to Live TV'); });
                      } }, 'Add to Jellyfin') : null),
                    h('td', { style: { whiteSpace: 'nowrap' } },
                      h('button', { class: 'dh-btn sm', onclick: function () { editProfile(p, caps, load); } }, 'Edit'), ' ',
                      !p.default ? h('button', { class: 'dh-btn sm red', onclick: function () {
                        if (DH.confirm('Delete the profile "' + p.name + '"?')) DH.act(DH.del('profiles/' + DH.enc(p.id)), 'Deleted').then(load);
                      } }, '✕') : null));
                }))))));
            out.push(encoderCard(caps, load));
            out.push(DH.card(h('h3', {}, 'Default for "Automatic"', DH.lvlTag('advanced')),
              DH.form(settings, [{ key: 'hwAccel', label: 'Convert with', type: 'select', options: [['auto', 'Best that works (' + KIND_NAMES[autoKind] + ')'], ['nvenc', KIND_NAMES.nvenc], ['qsv', KIND_NAMES.qsv], ['vaapi', KIND_NAMES.vaapi], ['cpu', 'CPU']],
                onchange: function (v) { DH.act(DH.put('settings', { hwAccel: v }), 'Saved').then(load); } }]).el,
              h('p', { class: 'dh-small dh-muted' }, 'Used by profiles set to "Automatic". If a GPU fails while streaming, dvbhub falls back to the CPU on its own.')));
          }
          fill(view, out);
        }, function (e) { fill(view, DH.errorBox(e)); });
      }
      return load();
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
