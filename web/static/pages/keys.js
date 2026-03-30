'use strict';

var _createdKeyValue = '';

document.addEventListener('DOMContentLoaded', function() {
  loadKeys();
});

async function loadKeys() {
  var tbody = document.getElementById('keys-tbody');
  var summary = document.getElementById('keys-summary');
  if (!tbody) return;

  clearEl(tbody);
  tbody.appendChild(
    createEl('tr', null, [
      createEl('td', { colspan: '7' }, [
        createEl('div', { className: 'empty-state', textContent: 'Loading...' })
      ])
    ])
  );

  try {
    var keys = await apiRequest('/admin/keys');
    var list = Array.isArray(keys) ? keys : [];
    if (summary) {
      var activeCount = list.filter(function(k) { return k.active; }).length;
      summary.textContent = list.length + ' key' + (list.length !== 1 ? 's' : '') + ' — ' + activeCount + ' active';
    }
    renderKeysTable(list);
  } catch (e) {
    if (summary) summary.textContent = 'Failed to load keys.';
    clearEl(tbody);
    tbody.appendChild(
      createEl('tr', null, [
        createEl('td', { colspan: '7' }, [
          createEl('div', { className: 'empty-state', textContent: e.message || 'Failed to load API keys.' })
        ])
      ])
    );
  }
}

function keyStatusBadge(key) {
  if (!key.active) {
    return createEl('span', { className: 'badge badge-error', textContent: 'Revoked' });
  }
  if (key.expires_at && new Date(key.expires_at).getTime() < Date.now()) {
    return createEl('span', { className: 'badge badge-warning', textContent: 'Expired' });
  }
  return createEl('span', { className: 'badge badge-success', textContent: 'Active' });
}

function maskKey(keyStr) {
  if (!keyStr) return '-';
  if (keyStr.length <= 12) return keyStr.slice(0, 4) + '...';
  return keyStr.slice(0, 8) + '...' + keyStr.slice(-4);
}

function renderKeysTable(keys) {
  var tbody = document.getElementById('keys-tbody');
  if (!tbody) return;
  clearEl(tbody);

  if (!keys || keys.length === 0) {
    tbody.appendChild(
      createEl('tr', null, [
        createEl('td', { colspan: '7' }, [
          createEl('div', { className: 'empty-state', textContent: 'No API keys found. Create one to get started.' })
        ])
      ])
    );
    return;
  }

  keys.forEach(function(key) {
    var scopesText = Array.isArray(key.scopes) && key.scopes.length > 0 ? key.scopes.join(', ') : '-';
    var lastUsed = key.last_used_at ? timeAgo(key.last_used_at) : 'Never';

    var rotateBtn = createEl('button', {
      className: 'btn btn-secondary',
      textContent: 'Rotate',
      style: 'font-size:12px;padding:4px 10px;',
      onclick: function() { rotateKey(key.id); }
    });

    var revokeBtn = createEl('button', {
      className: 'btn btn-danger',
      textContent: 'Revoke',
      style: 'font-size:12px;padding:4px 10px;',
      onclick: function() { revokeKey(key.id); }
    });

    if (!key.active) {
      rotateBtn.setAttribute('disabled', 'disabled');
      revokeBtn.setAttribute('disabled', 'disabled');
    }

    var actionsCell = createEl('td', { style: 'display:flex;gap:6px;align-items:center;' }, [rotateBtn, revokeBtn]);

    var tr = createEl('tr', null, [
      createEl('td', { textContent: key.name || '-' }),
      createEl('td', { className: 'mono', textContent: maskKey(key.key) }),
      createEl('td', null, [keyStatusBadge(key)]),
      createEl('td', { className: 'mono', textContent: scopesText }),
      createEl('td', { textContent: formatNumber(key.usage_count) }),
      createEl('td', { textContent: lastUsed }),
      actionsCell
    ]);
    tbody.appendChild(tr);
  });
}

function showCreateKeyModal() {
  var nameInput = document.getElementById('key-name-input');
  var expiresInput = document.getElementById('key-expires-input');
  var adminCheck = document.getElementById('scope-admin');
  var readOnlyCheck = document.getElementById('scope-read-only');

  if (nameInput) nameInput.value = '';
  if (expiresInput) expiresInput.value = '';
  if (adminCheck) adminCheck.checked = true;
  if (readOnlyCheck) readOnlyCheck.checked = false;

  var modal = document.getElementById('create-key-modal');
  if (modal) modal.style.display = 'flex';
}

function closeModal(id) {
  var modal = document.getElementById(id);
  if (modal) modal.style.display = 'none';
}

function handleOverlayClick(event, id) {
  if (event.target === event.currentTarget) closeModal(id);
}

async function submitCreateKey() {
  var nameInput = document.getElementById('key-name-input');
  var expiresInput = document.getElementById('key-expires-input');
  var adminCheck = document.getElementById('scope-admin');
  var readOnlyCheck = document.getElementById('scope-read-only');

  var name = nameInput ? nameInput.value.trim() : '';
  if (!name) {
    showToast('Name is required.', 'error');
    return;
  }

  var scopes = [];
  if (adminCheck && adminCheck.checked) scopes.push('admin');
  if (readOnlyCheck && readOnlyCheck.checked) scopes.push('read-only');
  if (scopes.length === 0) scopes.push('admin');

  var expiresAt = '';
  if (expiresInput && expiresInput.value) {
    var d = new Date(expiresInput.value);
    if (!isNaN(d.getTime())) expiresAt = d.toISOString();
  }

  var body = { name: name, scopes: scopes };
  if (expiresAt) body.expires_at = expiresAt;

  try {
    var created = await apiRequest('/admin/keys', {
      method: 'POST',
      body: JSON.stringify(body)
    });

    closeModal('create-key-modal');

    _createdKeyValue = created.key || '';
    var display = document.getElementById('created-key-display');
    if (display) display.textContent = _createdKeyValue;

    var createdModal = document.getElementById('key-created-modal');
    if (createdModal) createdModal.style.display = 'flex';

    loadKeys();
  } catch (e) {
    showToast(e.message || 'Failed to create key.', 'error');
  }
}

function copyCreatedKey() {
  if (!_createdKeyValue) return;
  navigator.clipboard.writeText(_createdKeyValue).then(function() {
    showToast('Key copied to clipboard.', 'success');
  }).catch(function() {
    showToast('Copy failed — select and copy manually.', 'error');
  });
}

async function rotateKey(id) {
  if (!confirm('Rotate this key? The current key will be invalidated and a new one will be issued.')) return;
  try {
    var result = await apiRequest('/admin/keys/' + id + '/rotate', { method: 'POST' });

    _createdKeyValue = result.key || '';
    var display = document.getElementById('created-key-display');
    if (display) display.textContent = _createdKeyValue;

    var createdModal = document.getElementById('key-created-modal');
    if (createdModal) createdModal.style.display = 'flex';

    loadKeys();
  } catch (e) {
    showToast(e.message || 'Failed to rotate key.', 'error');
  }
}

async function revokeKey(id) {
  if (!confirm('Revoke this key? This cannot be undone.')) return;
  try {
    await apiRequest('/admin/keys/' + id + '/revoke', { method: 'POST' });
    showToast('Key revoked.', 'success');
    loadKeys();
  } catch (e) {
    showToast(e.message || 'Failed to revoke key.', 'error');
  }
}
