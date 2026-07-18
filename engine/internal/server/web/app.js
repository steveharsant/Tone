/* Tone settings page. */
(() => {
  const token = location.hash.slice(1) || sessionStorage.getItem('tone-token') || '';
  if (token) sessionStorage.setItem('tone-token', token);
  const $ = (id) => document.getElementById(id);

  if (!token) {
    $('no-token').classList.remove('hidden');
    return;
  }
  $('pairing-token').textContent = token;
  $('rerun-setup').href = '/setup#' + token;

  /* Token travels in the Authorization header only — query strings end up
   * in proxy/access logs. (The #fragment in the page URL never leaves the
   * browser, so bookmarking the tokened link stays safe.) */
  const api = (path, opts = {}) =>
    fetch(path, { ...opts, headers: { ...(opts.headers || {}), Authorization: 'Bearer ' + token } });

  async function loadHealth() {
    try {
      const h = await (await api('/v1/health')).json();
      const dot = h.status === 'ok' ? 'dot-ok' : h.status === 'setup_required' ? 'dot-warn' : 'dot-err';
      const label = { ok: 'Ready', setup_required: 'Setup required', backend_unavailable: 'Model backend unavailable' }[h.status] || h.status;
      $('health').innerHTML = `<span class="status-dot ${dot}"></span>${label} · engine v${h.engine_version}`;
      $('health-detail').textContent = h.ollama.running
        ? `Ollama v${h.ollama.version}${h.ollama.supervised ? ' (managed by Tone)' : ''} · model ${h.provider.model || 'none'}`
        : 'Ollama is not running.';
    } catch (e) {
      $('health').innerHTML = `<span class="status-dot dot-err"></span>Engine unreachable: ${e.message}`;
    }
  }

  async function loadSettings() {
    const s = await (await api('/api/settings')).json();
    $('chk-spelling').checked = s.checks.spelling;
    $('chk-grammar').checked = s.checks.grammar;
    $('chk-clarity').checked = s.checks.clarity;
    $('chk-vocabulary').checked = s.checks.vocabulary;
    $('chk-tone').checked = s.checks.tone;
    $('tone-target').value = s.tone_target || '';
    $('style-rules').value = (s.style_rules || []).join('\n');
    $('disabled-rules').value = (s.disabled_rules || []).join('\n');

    const status = await (await api('/api/setup/status')).json();
    const sel = $('model-select');
    sel.innerHTML = '';
    const models = (status.installed_models || []).map((m) => m.name);
    if (models.length === 0 && s.provider.model) models.push(s.provider.model);
    for (const name of models) {
      const opt = document.createElement('option');
      opt.value = name;
      opt.textContent = name;
      if (name === s.provider.model || name === s.provider.model + ':latest') opt.selected = true;
      sel.appendChild(opt);
    }
  }

  async function loadPairing() {
    try {
      const { pending } = await (await api('/api/pair/pending')).json();
      const card = $('pairing-card');
      const list = $('pairing-list');
      if (!pending.length) {
        card.classList.add('hidden');
        return;
      }
      card.classList.remove('hidden');
      list.innerHTML = '';
      for (const req of pending) {
        const row = document.createElement('div');
        row.className = 'row';
        row.style.margin = '8px 0';
        const label = document.createElement('span');
        label.textContent = req.client;
        const btns = document.createElement('span');
        for (const [text, approve] of [['Approve', true], ['Deny', false]]) {
          const b = document.createElement('button');
          b.textContent = text;
          if (!approve) b.className = 'ghost';
          b.style.marginLeft = '8px';
          b.onclick = async () => {
            await api('/api/pair/decide', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ id: req.id, approve }),
            });
            loadPairing();
          };
          btns.appendChild(b);
        }
        row.append(label, btns);
        list.appendChild(row);
      }
    } catch {
      /* engine restarting; retry next poll */
    }
  }

  $('save').onclick = async () => {
    const lines = (v) => v.split('\n').map((l) => l.trim()).filter(Boolean);
    const body = {
      checks: {
        spelling: $('chk-spelling').checked,
        grammar: $('chk-grammar').checked,
        clarity: $('chk-clarity').checked,
        vocabulary: $('chk-vocabulary').checked,
        tone: $('chk-tone').checked,
      },
      tone_target: $('tone-target').value,
      style_rules: lines($('style-rules').value),
      disabled_rules: lines($('disabled-rules').value),
      model: $('model-select').value || undefined,
    };
    const r = await api('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('save-status').textContent = r.ok ? 'Saved — takes effect on the next check.' : 'Save failed.';
    setTimeout(() => ($('save-status').textContent = ''), 4000);
    loadHealth();
  };

  loadHealth();
  loadSettings().catch((e) => ($('save-status').textContent = 'Load failed: ' + e.message));
  loadPairing();
  setInterval(loadPairing, 3000);
})();
