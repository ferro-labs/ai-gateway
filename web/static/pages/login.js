'use strict';

// Served as a file rather than an inline <script> so the dashboard can run under
// Content-Security-Policy: script-src 'self'.

document.addEventListener('DOMContentLoaded', function() {
  // Already authenticated — skip the form.
  if (localStorage.getItem('gw-admin-key')) {
    window.location.href = '/dashboard/overview';
    return;
  }

  var form = document.getElementById('login-form');
  if (!form) return;

  form.addEventListener('submit', async function(e) {
    e.preventDefault();
    var key = document.getElementById('login-key').value.trim();
    var errEl = document.getElementById('login-error');
    errEl.style.display = 'none';

    if (!key) {
      errEl.textContent = 'Please enter a key.';
      errEl.style.display = 'block';
      return;
    }

    try {
      // /admin/health always answers 200 when the key is valid, even if the
      // gateway is degraded, so a non-2xx here really does mean the key failed.
      var res = await fetch('/admin/health', {
        headers: { 'Authorization': 'Bearer ' + key }
      });
      if (!res.ok) {
        var data = await res.json().catch(function() { return {}; });
        errEl.textContent = (data.error && data.error.message) || 'Invalid key.';
        errEl.style.display = 'block';
        return;
      }
      var health = await res.json();
      localStorage.setItem('gw-admin-key', key);
      if (health.scopes) {
        localStorage.setItem('gw-scopes', JSON.stringify(health.scopes));
      }
      window.location.href = '/dashboard/overview';
    } catch (err) {
      errEl.textContent = 'Cannot reach gateway. Is it running?';
      errEl.style.display = 'block';
    }
  });
});
