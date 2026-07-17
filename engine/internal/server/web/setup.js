/* Tone setup wizard. The pairing token arrives in the URL fragment (never
 * sent to the server in the URL path/query of the page load itself) and is
 * attached to API calls as ?token=. */
(() => {
  const token = location.hash.slice(1) || sessionStorage.getItem('tone-token') || '';
  if (token) sessionStorage.setItem('tone-token', token);
  const $ = (id) => document.getElementById(id);

  if (!token) {
    $('no-token').classList.remove('hidden');
    return;
  }
  $('to-settings').href = '/#' + token;
  $('footer-settings').href = '/#' + token;

  const api = (path, opts = {}) =>
    fetch(path + (path.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(token), opts);

  const gb = (n) => (n / 1e9).toFixed(1) + ' GB';
  const pct = (done, total) => (total > 0 ? Math.round((100 * done) / total) : 0);

  /* Reads an NDJSON streaming response, invoking cb per parsed line. */
  async function stream(resp, cb) {
    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let nl;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (line) cb(JSON.parse(line));
      }
    }
  }

  let state = { ollamaReady: false, selected: null, installedTags: new Set() };

  async function refresh() {
    const r = await api('/api/setup/status');
    if (!r.ok) throw new Error('status ' + r.status);
    const s = await r.json();
    state.ollamaReady = s.ollama.running;
    state.installedTags = new Set((s.installed_models || []).map((m) => m.name));
    renderOllama(s.ollama);
    renderModels(s.curated, s.provider.model);
  }

  function renderOllama(o) {
    const el = $('ollama-status');
    const btn = $('ollama-install');
    if (o.running) {
      el.innerHTML = '<span class="status-dot dot-ok"></span>Running' +
        (o.version ? ' · v' + o.version : '') +
        (o.supervised ? ' (managed by Tone)' : o.system_install ? ' (your install)' : '');
      btn.classList.add('hidden');
    } else if (o.system_install || o.managed_install) {
      el.innerHTML = '<span class="status-dot dot-warn"></span>Installed but not running';
      btn.textContent = 'Start';
      btn.classList.remove('hidden');
    } else {
      el.innerHTML = '<span class="status-dot dot-err"></span>Not installed';
      btn.textContent = 'Install locally';
      btn.classList.remove('hidden');
    }
  }

  function renderModels(curated, currentModel) {
    const box = $('models');
    box.innerHTML = '';
    for (const m of curated) {
      const el = document.createElement('button');
      el.type = 'button';
      el.className = 'model';
      const installed = state.installedTags.has(m.tag) || state.installedTags.has(m.tag + ':latest');
      el.innerHTML =
        `<span class="name">${m.name}</span>` +
        `<span class="meta">~${m.size_gb} GB · needs ${m.min_ram_gb} GB+</span>` +
        (m.default ? '<span class="badge">recommended</span>' : '') +
        (installed ? '<span class="badge subtle">installed</span>' : '') +
        (m.tag === currentModel ? '<span class="badge subtle">current</span>' : '') +
        `<div class="pitch">${m.pitch}</div>`;
      el.onclick = () => select(m, el, installed);
      box.appendChild(el);
      if (m.default && !state.selected) select(m, el, installed);
    }
  }

  function select(m, el, installed) {
    document.querySelectorAll('.model').forEach((x) => x.classList.remove('selected'));
    el.classList.add('selected');
    state.selected = m;
    $('model-continue').disabled = false;
    $('model-continue').textContent = installed ? 'Use this model' : 'Download & continue';
    $('model-hint').textContent = installed ? '' : `Downloads ~${m.size_gb} GB once, then it's yours.`;
  }

  $('ollama-install').onclick = async () => {
    const btn = $('ollama-install');
    btn.disabled = true;
    $('ollama-error').classList.add('hidden');
    $('ollama-progress').classList.remove('hidden');
    try {
      const resp = await api('/api/setup/ollama/install', { method: 'POST' });
      await stream(resp, (ev) => {
        if (ev.error) throw new Error(ev.error);
        if (ev.phase === 'download') {
          $('ollama-bar').value = pct(ev.completed, ev.total);
          $('ollama-progress-text').textContent =
            `Downloading Ollama… ${gb(ev.completed)} / ${gb(ev.total)}`;
        } else if (ev.phase === 'verify') {
          $('ollama-progress-text').textContent = 'Verifying checksum…';
        } else if (ev.phase === 'extract') {
          $('ollama-progress-text').textContent = 'Extracting…';
        } else if (ev.phase === 'starting') {
          $('ollama-progress-text').textContent = 'Starting Ollama…';
        }
      });
      $('ollama-progress-text').textContent = 'Ready.';
    } catch (e) {
      $('ollama-error').textContent = String(e.message || e);
      $('ollama-error').classList.remove('hidden');
    } finally {
      btn.disabled = false;
      refresh();
    }
  };

  $('model-continue').onclick = async () => {
    const m = state.selected;
    if (!m) return;
    if (!state.ollamaReady) {
      $('pull-error').textContent = 'Start the AI runtime first (step 1).';
      $('pull-error').classList.remove('hidden');
      return;
    }
    $('model-continue').disabled = true;
    $('pull-error').classList.add('hidden');
    $('pull-progress').classList.remove('hidden');
    try {
      const resp = await api('/api/setup/pull', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: m.tag }),
      });
      await stream(resp, (ev) => {
        if (ev.error) throw new Error(ev.error);
        if (ev.total > 0) {
          $('pull-bar').value = pct(ev.completed, ev.total);
          $('pull-progress-text').textContent =
            `${ev.phase || 'downloading'}… ${gb(ev.completed)} / ${gb(ev.total)}`;
        } else {
          $('pull-progress-text').textContent = ev.phase || '…';
        }
      });
      const done = await api('/api/setup/complete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: m.tag }),
      });
      if (!done.ok) throw new Error('failed to save configuration');
      $('pairing-token').textContent = token;
      $('step-done').classList.remove('hidden');
      $('step-done').scrollIntoView({ behavior: 'smooth' });
    } catch (e) {
      $('pull-error').textContent = String(e.message || e);
      $('pull-error').classList.remove('hidden');
    } finally {
      $('model-continue').disabled = false;
    }
  };

  refresh().catch((e) => {
    $('ollama-status').innerHTML =
      '<span class="status-dot dot-err"></span>Cannot reach engine: ' + e.message;
  });
})();
