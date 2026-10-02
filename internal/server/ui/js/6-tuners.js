/* Tuners: hardware and drivers, tuner settings, antenna alignment. */
(function (DH) {
  'use strict';
  var h = DH.h, fill = DH.fill;

  var ACTION_DONE = { firmware: 'Firmware installed. Reset the USB tuners (or restart the computer) so the driver loads it.', replug: 'Tuners reset.', tools: 'Tools installed.', tbs: 'TBS drivers installed. Restart the computer to load them.' };

  function installerCard(hw, reload) {
    var inst = hw.installer, rep = hw.report;
    var card = DH.card(h('h3', {}, 'Drivers and firmware'));
    if (!inst.enabled) {
      var docker = rep.container;
      var dataArg = docker ? './data' : hw.dataDir;
      card.appendChild(h('p', {}, 'dvbhub can install tuner firmware and drivers for you, from here or from Jellyfin. That needs a one-time setup on the computer that runs dvbhub, because installing drivers needs administrator rights.'));
      card.appendChild(h('ol', { class: 'dh-steps' },
        docker ? h('li', {}, 'Open a terminal on that computer and go to the folder with docker-compose.yml:', DH.copyBox('cd ~/dvbhub')) : h('li', {}, 'Open a terminal on that computer.'),
        h('li', {}, 'Download the installer:', DH.copyBox('curl -fsSL ' + hw.scriptUrl + ' -o install-drivers.sh')),
        h('li', {}, 'Turn on installing from the web page (asks for your password):', DH.copyBox('sudo bash install-drivers.sh --enable-web-trigger ' + dataArg)),
        h('li', {}, 'Come back here and press ', h('button', { class: 'dh-link', onclick: reload }, 'Check again'), '.')));
      card.appendChild(h('p', { class: 'dh-small dh-muted' }, 'Prefer to do it by hand? Step 2, then ', h('code', {}, 'sudo bash install-drivers.sh --firmware'),
        '. Only fixed actions can be run from here: install firmware, reset USB tuners, install tools, build TBS drivers. Turn it off again with ', h('code', {}, '--disable-web-trigger'), '.'));
      return card;
    }
    var busy = inst.jobs.some(function (j) { return j.status === 'queued' || j.status === 'running'; });
    var suggested = rep.actions || [];
    var actions = inst.actions.filter(function (a) { return DH.L('advanced') || suggested.indexOf(a.id) >= 0 || a.id === 'replug'; });
    card.appendChild(h('p', { class: 'dh-small dh-muted' }, 'Installer ready on ' + (inst.host || 'the host') + '.'));
    actions.forEach(function (a) {
      card.appendChild(h('div', { class: 'dh-row', style: { margin: '8px 0' } },
        h('button', { class: 'dh-btn' + (suggested.indexOf(a.id) >= 0 ? ' pri' : ''), disabled: busy, onclick: function () {
          if (!DH.confirm(a.name + '?\n\n' + a.description)) return;
          DH.act(DH.post('hardware/install', { action: a.id }), 'Started: ' + a.name).then(reload);
        } }, a.name), h('span', { class: 'dh-small dh-muted', style: { flex: 1, minWidth: '200px' } }, a.description)));
    });
    inst.jobs.forEach(function (j) {
      var pre = h('pre', { class: 'dh-log' }, '…');
      var cls = { ok: 'ok', failed: 'fail', running: 'running', queued: 'queued' }[j.status] || '';
      var det = h('details', { style: { marginTop: '8px' } },
        h('summary', {}, (inst.actions.filter(function (a) { return a.id === j.action; })[0] || { name: j.action }).name + ' · ' + DH.fmtDay(j.created) + ' · ', DH.badge(cls, j.status)),
        j.status === 'ok' && ACTION_DONE[j.action] ? h('p', { class: 'dh-small' }, ACTION_DONE[j.action]) : null, pre);
      det.addEventListener('toggle', function () { if (det.open) DH.get('hardware/jobs/' + j.id).then(function (x) { pre.textContent = x.log || '(no output yet)'; }); });
      card.appendChild(det);
    });
    return card;
  }

  function hardwareCard(hw, reload, rediscover) {
    var rep = hw.report;
    var card = DH.card(h('div', { class: 'dh-between' }, h('h3', { style: { margin: 0 } }, 'Tuner hardware'),
      h('div', { class: 'dh-row' }, h('button', { class: 'dh-btn sm', onclick: function () { reload(true); } }, 'Check again'),
        DH.L('advanced') ? h('button', { class: 'dh-btn sm', onclick: rediscover, title: 'Probe /dev/dvb again' }, 'Look for tuners') : null)),
      h('div', { class: 'dh-alert ' + (rep.problem ? 'warn' : 'good'), style: { marginTop: '10px' } }, rep.summary));
    rep.devices.forEach(function (d) {
      card.appendChild(h('div', { style: { padding: '8px 0', borderTop: '1px solid var(--line)' } },
        h('div', { class: 'dh-between' }, h('b', {}, d.name), DH.badge(d.status)),
        h('div', { class: 'dh-small dh-muted' }, [d.brand, d.bus.toUpperCase() + ' ' + d.id, d.driver ? 'driver ' + d.driver : null, d.adapters && d.adapters.length ? d.adapters.join(', ') : null].filter(Boolean).join(' · ')),
        (d.hints || []).map(function (x) { return h('div', { class: 'dh-small' }, x); })));
    });
    if (DH.L('advanced') && hw.frontends.length) {
      card.appendChild(h('div', { class: 'dh-section' }, 'Tuner devices (/dev/dvb)'));
      hw.frontends.forEach(function (f) {
        card.appendChild(h('div', { class: 'dh-small' }, h('b', {}, f.key), ' ', f.name, ' ', f.delsys ? h('span', { class: 'dh-muted' }, '(' + f.delsys.join(', ') + ')') : null,
          f.ok ? null : h('span', { class: 'dh-badge warn', style: { marginLeft: '6px' } }, 'not responding')));
      });
    }
    rep.notes.forEach(function (n) { card.appendChild(h('p', { class: 'dh-small dh-muted', style: { marginTop: '8px' } }, n)); });
    if (DH.L('pro')) {
      var needed = rep.firmware.filter(function (f) { return f.needed; }), optional = rep.firmware.filter(function (f) { return !f.needed; });
      card.appendChild(h('div', { class: 'dh-section' }, 'Details ', DH.lvlTag('pro')));
      card.appendChild(h('div', { class: 'dh-small' }, 'Kernel ' + rep.kernel + ' · Secure Boot ' + rep.secureBoot + (rep.container ? ' · in a container' : '')));
      if (needed.length) card.appendChild(h('div', { class: 'dh-small', style: { marginTop: '6px' } }, h('b', {}, 'Firmware a driver failed to load: '), needed.map(function (f) { return f.file + (f.present ? ' (present)' : ''); }).join(', ')));
      if (optional.length) card.appendChild(h('details', { class: 'dh-small', style: { marginTop: '6px' } }, h('summary', {}, optional.length + ' optional firmware files not installed (usually for other chip models; fine to ignore)'),
        h('div', { class: 'dh-muted' }, optional.map(function (f) { return f.file + ' (' + f.module + ')'; }).join(', '))));
      if (rep.blacklisted.length) card.appendChild(h('div', { class: 'dh-small', style: { marginTop: '6px' } }, 'Blacklisted: ' + rep.blacklisted.join(', ')));
      card.appendChild(h('details', { style: { marginTop: '8px' } }, h('summary', { class: 'dh-small' }, 'Kernel messages about tuners' + (rep.kernelLogReadable ? '' : ' (not readable from here)')),
        h('pre', { class: 'dh-log' }, rep.kernelLog.join('\n') || '(none)')));
    }
    return card;
  }

  function tunerSettings(t, networks, reload) {
    var card = DH.tunerCard(t, { refresh: reload });
    var cfg = t.config;
    var form = DH.form(cfg, [
      { key: 'enabled', label: 'Use this tuner', type: 'bool' },
      { key: 'name', label: 'Name', lvl: 'advanced' },
      { key: 'priority', label: 'Priority', type: 'number', lvl: 'advanced', help: 'Higher numbers are used first.' },
      { key: 'tuneTimeout', label: 'Lock timeout (seconds)', type: 'number', min: 1, max: 120, lvl: 'advanced', help: 'Some tuners load firmware first; 15 s is safe.' },
      { key: 'hold', label: 'Keep this tuner reserved for dvbhub', type: 'bool', lvl: 'advanced', help: 'Other programs (e.g. Tvheadend) can\'t open it while dvbhub holds it.', show: function () { return !t.virtual; } }
    ]);
    var nets = DH.L('advanced') && networks.length ? h('div', { class: 'dh-field', style: { marginTop: '10px' } }, h('span', {}, 'Networks', DH.lvlTag('advanced')),
      h('div', { class: 'dh-row dh-small' }, networks.map(function (n) {
        return h('label', { class: 'dh-row', style: { gap: '5px' } }, DH.toggle((cfg.networks || []).indexOf(n.id) >= 0, function (v) {
          var list = (cfg.networks || []).filter(function (x) { return x !== n.id; });
          if (v) list.push(n.id);
          DH.act(DH.put('tuners/' + t.key, { networks: list }), 'Saved').then(reload);
        }), n.name);
      })), h('small', {}, 'None ticked = any network this tuner supports.')) : null;
    var sat = DH.L('pro') ? networks.filter(function (n) { return n.type === 'dvbs'; }).map(function (n) {
      var s = (cfg.sat || {})[n.id] || { lnb: 'universal', lofLow: 9750000, lofHigh: 10600000, switch: 11700000, diseqcPort: 0 };
      var f = DH.form(s, [
        { key: 'lnb', label: 'LNB (' + n.name + ')', type: 'select', options: [['universal', 'Universal (Ku band)'], ['single', 'Single LO'], ['circular', 'Circular'], ['none', 'None']] },
        { key: 'lofLow', label: 'LO low (kHz)', type: 'number' }, { key: 'lofHigh', label: 'LO high (kHz)', type: 'number' },
        { key: 'switch', label: 'Switch (kHz)', type: 'number' },
        { key: 'diseqcPort', label: 'DiSEqC port', type: 'select', options: [[0, 'None'], [1, '1 (A)'], [2, '2 (B)'], [3, '3 (C)'], [4, '4 (D)']] }]);
      return h('div', { style: { marginTop: '10px' } }, h('div', { class: 'dh-section' }, 'Satellite input ', DH.lvlTag('pro')), f.el,
        h('button', { class: 'dh-btn sm', style: { marginTop: '8px' }, onclick: function () {
          var all = Object.assign({}, cfg.sat || {});
          all[n.id] = Object.assign({}, s, f.changes());
          DH.act(DH.put('tuners/' + t.key, { sat: all }), 'Saved').then(reload);
        } }, 'Save satellite input'));
    }) : null;
    var hist = DH.L('pro') && t.history && t.history.length > 2 ? h('div', { style: { marginTop: '10px' } },
      h('div', { class: 'dh-small dh-muted' }, 'Signal, last ' + Math.round(t.history.length / 60) + ' min (' + (t.history[t.history.length - 1].snrIsDb ? 'SNR dB' : 'SNR %') + ')'),
      DH.sparkline(t.history.map(function (x) { return x.snr; }), { color: 'var(--good)', max: t.history[0].snrIsDb ? 35 : 100 }),
      h('div', { class: 'dh-small dh-muted' }, 'Bitrate'), DH.sparkline(t.history.map(function (x) { return x.kbps; }))) : null;
    var drop = DH.L('pro') && t.virtual ? h('div', { class: 'dh-row', style: { marginTop: '10px' } }, h('span', { class: 'dh-small dh-muted' }, 'Test signal loss:'),
      [5, 20].map(function (s) { return h('button', { class: 'dh-btn sm', onclick: function () { DH.act(DH.post('tuners-drop/' + t.key + '?seconds=' + s), 'Dropping ' + s + ' s'); } }, s + ' s'); })) : null;
    card.appendChild(h('div', { style: { marginTop: '12px', borderTop: '1px solid var(--line)', paddingTop: '10px' } }, form.el, nets, sat,
      h('button', { class: 'dh-btn sm pri', style: { marginTop: '10px' }, onclick: function () {
        if (!form.changed()) { DH.toast('Nothing changed'); return; }
        DH.act(DH.put('tuners/' + t.key, form.changes()), 'Saved').then(reload);
      } }, 'Save'), hist, drop,
      DH.L('advanced') && t.users && t.users.length ? h('p', { class: 'dh-small', style: { color: 'var(--warn)', marginTop: '8px' } }, 'Also opened by: ' + t.users.map(function (u) { return u.name + ' (pid ' + u.pid + ')'; }).join(', ')) : null));
    return card;
  }

  // ---------- antenna alignment ----------
  function alignCard(tuners, muxes) {
    var card = DH.card(h('h3', {}, 'Antenna alignment', DH.lvlTag('advanced')),
      h('p', { class: 'dh-small dh-muted' }, 'Holds one tuner on one channel and shows its signal four times a second while you point the antenna or dish.'));
    var hw = tuners.filter(function (t) { return t.config.enabled; });
    var good = muxes.slice().sort(function (a, b) { return (b.scan.status === 'ok') - (a.scan.status === 'ok') || a.tuning.frequency - b.tuning.frequency; });
    if (!hw.length || !good.length) { card.appendChild(h('p', { class: 'dh-muted' }, 'Needs a tuner and a scanned channel.')); return { el: card, stop: function () { } }; }
    var tSel = DH.select(hw.map(function (t) { return [t.key, t.config.name || t.name]; }), hw[0].key);
    var mSel = DH.select(good.map(function (m) { return [m.id, m.name + (m.scan.status === 'ok' ? '' : ' (' + m.scan.status + ')')]; }), good[0].id);
    var out = h('div', {});
    var toneBox = h('input', { type: 'checkbox' });
    var timer = null, peak = -1, tuner = null, audio = null, osc = null;
    var startBtn = h('button', { class: 'dh-btn pri', onclick: start }, 'Start');
    var stopBtn = h('button', { class: 'dh-btn', onclick: stop, disabled: true }, 'Stop');
    function start() {
      tuner = tSel.value; peak = -1;
      DH.act(DH.post('align', { tuner: tuner, muxId: mSel.value })).then(function () {
        startBtn.disabled = true; stopBtn.disabled = false;
        timer = setInterval(poll, 250);
      });
    }
    function stop() {
      clearInterval(timer); timer = null;
      if (osc) { try { osc.stop(); } catch (e) { } osc = null; }
      if (tuner) DH.del('align?tuner=' + DH.enc(tuner)).catch(function () { });
      startBtn.disabled = false; stopBtn.disabled = true;
    }
    function poll() {
      DH.get('align?tuner=' + DH.enc(tuner)).then(function (a) {
        var s = a.signal, sc = DH.score(s);
        if (s.locked && sc > peak) peak = sc;
        var big = s.snrDb != null ? s.snrDb.toFixed(1) + ' dB' : sc >= 0 ? Math.round(sc) + '%' : '–';
        fill(out, h('div', { class: 'dh-row', style: { gap: '24px', marginTop: '12px', alignItems: 'flex-end' } },
          h('div', {}, h('div', { class: 'dh-small dh-muted' }, s.snrDb != null ? 'Signal quality (SNR)' : 'Signal quality'), h('div', { class: 'dh-align-big', style: { color: DH.signalColor(sc) } }, big)),
          h('div', {}, DH.bars({ bars: a.bars, locked: s.locked, quality: a.quality, live: true }, { size: 34, noLabel: true }),
            h('div', { style: { marginTop: '6px' } }, s.locked ? DH.badge('ok', 'LOCKED') : DH.badge('fail', a.state === 'tuning' ? 'TUNING' : 'NO LOCK')))),
          h('div', { style: { marginTop: '12px' } },
            h('div', { class: 'dh-small' }, 'Strength ' + (s.strengthPct >= 0 ? Math.round(s.strengthPct) + '%' : '–') + (s.strengthDbm != null ? ' (' + s.strengthDbm.toFixed(1) + ' dBm)' : '')),
            h('div', { class: 'dh-meter' }, h('i', { style: { width: Math.max(0, s.strengthPct) + '%', background: DH.signalColor(s.strengthPct) } })),
            h('div', { class: 'dh-small', style: { marginTop: '8px' } }, 'Quality ' + (sc >= 0 ? Math.round(sc) + '%' : '–') + (peak >= 0 ? ' · best so far ' + Math.round(peak) + '%' : '')),
            h('div', { class: 'dh-meter' }, h('i', { style: { width: Math.max(0, Math.min(100, sc)) + '%', background: DH.signalColor(sc) } })),
            h('div', { class: 'dh-small dh-muted', style: { marginTop: '8px' } }, a.mux + (s.unc ? ' · uncorrected blocks ' + s.unc : '') + (s.ber >= 0 ? ' · BER ' + s.ber.toExponential(1) : ''))));
        tone(s.locked ? sc : 0);
      }, function () { });
    }
    function tone(sc) {
      if (!toneBox.checked) { if (osc) { try { osc.stop(); } catch (e) { } osc = null; } return; }
      try {
        audio = audio || new (window.AudioContext || window.webkitAudioContext)();
        if (!osc) {
          osc = audio.createOscillator(); var g = audio.createGain(); g.gain.value = 0.05;
          osc.connect(g); g.connect(audio.destination); osc.start();
        }
        osc.frequency.setTargetAtTime(220 + Math.max(0, sc) * 9, audio.currentTime, 0.1);
      } catch (e) { }
    }
    card.appendChild(h('div', { class: 'dh-row' }, DH.field('Tuner', tSel), DH.field('Channel', mSel),
      h('label', { class: 'dh-row dh-small', style: { gap: '6px', alignSelf: 'flex-end' } }, toneBox, 'Tone (higher = better)'),
      h('div', { class: 'dh-row', style: { alignSelf: 'flex-end' } }, startBtn, stopBtn)));
    card.appendChild(out);
    return { el: card, stop: stop };
  }

  DH.page('tuners', {
    title: 'Tuners', order: 60,
    attention: function (st) { return st.hardwareScanned && !st.tuners.length ? 'No tuner' : ''; },
    render: function (view, ctx) {
      var hwBox = h('div', {}), tunersBox = h('div', {}), alignBox = h('div', {});
      var align = null, lastSig = '', dirty = false;
      tunersBox.addEventListener('input', function () { dirty = true; });
      function saved() { dirty = false; return loadTuners(true); }
      function loadHW(force) {
        return DH.get('hardware' + (force === true ? '?refresh=1' : '')).then(function (hw) {
          fill(hwBox, hw.report.problem || DH.L('advanced') || hw.installer.jobs.length || hw.installer.enabled ? installerCard(hw, loadHW) : null, hardwareCard(hw, loadHW, function () {
            DH.act(DH.post('tuners-rediscover'), 'Looked for tuners').then(function () { loadHW(true); loadTuners(); });
          }));
        }, function (e) { fill(hwBox, DH.errorBox(e)); });
      }
      function loadTuners(force) {
        return Promise.all([DH.get('tuners' + (DH.L('pro') ? '?history=1' : '')), DH.get('networks')]).then(function (r) {
          // While a form has unsaved edits, redraw only if the tuners themselves changed.
          var sig = JSON.stringify(r[0].map(function (t) { return [t.config, t.state, (t.subscriptions || []).length, t.hold]; }));
          if (force !== true && (dirty || tunersBox.contains(document.activeElement)) && sig === lastSig) return r[0];
          lastSig = sig;
          fill(tunersBox, r[0].length ? h('div', { class: 'dh-grid' }, r[0].map(function (t) { return tunerSettings(t, r[1], saved); }))
            : DH.card(h('p', { class: 'dh-muted' }, 'No tuners. See "Tuner hardware" above.')));
          return r[0];
        });
      }
      fill(view, hwBox, h('h2', { style: { marginTop: '6px' } }, 'Tuners'), tunersBox, alignBox);
      loadHW();
      var first = loadTuners().then(function (tuners) {
        if (!DH.L('advanced') || !tuners) return;
        DH.get('muxes').then(function (muxes) { align = alignCard(tuners, muxes); fill(alignBox, align.el); });
      });
      ctx.every(3000, loadTuners);
      ctx.every(20000, loadHW);
      return first.then(function () { return function () { if (align) align.stop(); }; });
    }
  });
})(window.DvbHubUI = window.DvbHubUI || {});
