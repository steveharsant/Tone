/* Tone settings/status page. */
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

  fetch('/v1/health?token=' + encodeURIComponent(token))
    .then((r) => r.json())
    .then((h) => {
      const dot = h.status === 'ok' ? 'dot-ok' : h.status === 'setup_required' ? 'dot-warn' : 'dot-err';
      const label = { ok: 'Ready', setup_required: 'Setup required', backend_unavailable: 'Model backend unavailable' }[h.status] || h.status;
      $('health').innerHTML = `<span class="status-dot ${dot}"></span>${label} · engine v${h.engine_version}`;
      $('health-detail').textContent = h.ollama.running
        ? `Ollama v${h.ollama.version}${h.ollama.supervised ? ' (managed by Tone)' : ''}`
        : 'Ollama is not running.';
      $('model-info').textContent = h.provider.model
        ? `${h.provider.type} · ${h.provider.model}`
        : 'No model configured yet.';
    })
    .catch((e) => {
      $('health').innerHTML = `<span class="status-dot dot-err"></span>Engine unreachable: ${e.message}`;
    });
})();
