'use strict';

function switchTab(tab) {
  var panelCurrent = document.getElementById('panel-current');
  var panelHistory = document.getElementById('panel-history');
  var tabCurrent = document.getElementById('tab-current');
  var tabHistory = document.getElementById('tab-history');

  if (tab === 'current') {
    panelCurrent.style.display = '';
    panelHistory.style.display = 'none';
    tabCurrent.classList.add('active');
    tabHistory.classList.remove('active');
  } else {
    panelCurrent.style.display = 'none';
    panelHistory.style.display = '';
    tabCurrent.classList.remove('active');
    tabHistory.classList.add('active');
    loadHistory();
  }
}

function loadConfig() {
  apiRequest('/admin/config').then(function(data) {
    var display = document.getElementById('config-display');
    display.textContent = JSON.stringify(data, null, 2);
  }).catch(function(err) {
    showToast('Failed to load config: ' + err.message, 'error');
  });
}

function startEdit() {
  var display = document.getElementById('config-display');
  var editor = document.getElementById('config-editor');
  var btnEdit = document.getElementById('btn-edit');
  var editActions = document.getElementById('edit-actions');

  editor.value = display.textContent;
  display.style.display = 'none';
  editor.style.display = '';
  btnEdit.style.display = 'none';
  editActions.style.display = 'flex';
}

function cancelEdit() {
  var display = document.getElementById('config-display');
  var editor = document.getElementById('config-editor');
  var btnEdit = document.getElementById('btn-edit');
  var editActions = document.getElementById('edit-actions');

  editor.style.display = 'none';
  display.style.display = '';
  btnEdit.style.display = '';
  editActions.style.display = 'none';
}

function saveConfig() {
  var editor = document.getElementById('config-editor');
  var raw = editor.value.trim();
  var parsed;
  try {
    parsed = JSON.parse(raw);
  } catch (e) {
    showToast('Invalid JSON: ' + e.message, 'error');
    return;
  }

  apiRequest('/admin/config', {
    method: 'PUT',
    body: JSON.stringify(parsed)
  }).then(function() {
    showToast('Config saved successfully', 'success');
    cancelEdit();
    loadConfig();
  }).catch(function(err) {
    showToast('Failed to save config: ' + err.message, 'error');
  });
}

function loadHistory() {
  apiRequest('/admin/config/history').then(function(data) {
    var tbody = document.getElementById('history-body');
    var empty = document.getElementById('history-empty');
    clearEl(tbody);

    var entries = (data && data.data) ? data.data : [];
    if (entries.length === 0) {
      empty.style.display = '';
      return;
    }
    empty.style.display = 'none';

    entries.slice().reverse().forEach(function(entry) {
      var strategyMode = (entry.config && entry.config.strategy && entry.config.strategy.mode) ? entry.config.strategy.mode : '-';
      var rolledBackFrom = entry.rolled_back_from != null ? String(entry.rolled_back_from) : '-';
      var updatedAt = entry.updated_at ? new Date(entry.updated_at).toLocaleString() : '-';

      var rollbackBtn = createEl('button', {
        className: 'btn btn-secondary',
        textContent: 'Rollback',
        style: 'padding:4px 12px;font-size:13px;',
        onclick: function() { rollbackTo(entry.version); }
      });

      var tr = createEl('tr', {}, [
        createEl('td', {}, [createEl('span', { className: 'mono', textContent: 'v' + entry.version })]),
        createEl('td', { textContent: updatedAt }),
        createEl('td', {}, [createEl('span', { className: 'badge badge-info', textContent: strategyMode })]),
        createEl('td', { textContent: rolledBackFrom }),
        createEl('td', {}, [rollbackBtn])
      ]);
      tbody.appendChild(tr);
    });
  }).catch(function(err) {
    showToast('Failed to load history: ' + err.message, 'error');
  });
}

function rollbackTo(version) {
  if (!confirm('Roll back to config version v' + version + '?')) return;

  apiRequest('/admin/config/rollback/' + version, { method: 'POST' }).then(function() {
    showToast('Rolled back to version v' + version, 'success');
    loadConfig();
    loadHistory();
  }).catch(function(err) {
    showToast('Rollback failed: ' + err.message, 'error');
  });
}

document.addEventListener('DOMContentLoaded', function() {
  loadConfig();
});
