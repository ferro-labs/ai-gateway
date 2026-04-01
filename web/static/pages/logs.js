'use strict';

var logsOffset = 0;
var logsLimit = 50;
var logsTotal = 0;

document.addEventListener('DOMContentLoaded', function() {
  localStorage.setItem('gw-visited-logs', 'true');
  populateFilterOptions();
  loadLogs();

  var applyBtn = document.getElementById('logs-apply-btn');
  if (applyBtn) applyBtn.addEventListener('click', function() { logsOffset = 0; loadLogs(); });

  var clearBtn = document.getElementById('logs-clear-btn');
  if (clearBtn) clearBtn.addEventListener('click', function() {
    var providerSel = document.getElementById('filter-provider');
    var modelSel = document.getElementById('filter-model');
    var stageSel = document.getElementById('filter-stage');
    var sinceInput = document.getElementById('filter-since');
    if (providerSel) providerSel.value = '';
    if (modelSel) modelSel.value = '';
    if (stageSel) stageSel.value = '';
    if (sinceInput) sinceInput.value = '';
    logsOffset = 0;
    loadLogs();
  });
});

async function populateFilterOptions() {
  try {
    var stats = await apiRequest('/admin/logs/stats');
    var providerSel = document.getElementById('filter-provider');
    var modelSel = document.getElementById('filter-model');

    if (providerSel && stats.by_provider) {
      Object.keys(stats.by_provider).sort().forEach(function(name) {
        var opt = createEl('option', { value: name, textContent: name });
        providerSel.appendChild(opt);
      });
    }

    if (modelSel && stats.by_model) {
      Object.keys(stats.by_model).sort().forEach(function(name) {
        var opt = createEl('option', { value: name, textContent: name });
        modelSel.appendChild(opt);
      });
    }
  } catch (e) {
    // Filter options are best-effort; silently fail if logs not enabled
  }
}

function buildQueryParams() {
  var params = new URLSearchParams();
  params.set('limit', String(logsLimit));
  params.set('offset', String(logsOffset));

  var provider = document.getElementById('filter-provider');
  if (provider && provider.value) params.set('provider', provider.value);

  var model = document.getElementById('filter-model');
  if (model && model.value) params.set('model', model.value);

  var stage = document.getElementById('filter-stage');
  if (stage && stage.value) params.set('stage', stage.value);

  var since = document.getElementById('filter-since');
  if (since && since.value) {
    // datetime-local gives "YYYY-MM-DDTHH:MM", convert to RFC3339
    var d = new Date(since.value);
    if (!isNaN(d.getTime())) params.set('since', d.toISOString());
  }

  return params.toString();
}

async function fetchLogs() {
  var qs = buildQueryParams();
  return await apiRequest('/admin/logs?' + qs);
}

async function loadLogs() {
  var tbody = document.getElementById('logs-tbody');
  if (!tbody) return;

  clearEl(tbody);
  tbody.appendChild(
    createEl('tr', null, [
      createEl('td', { colspan: '8' }, [
        createEl('div', { className: 'empty-state', textContent: 'Loading...' })
      ])
    ])
  );

  try {
    var result = await fetchLogs();
    logsTotal = (result.summary && result.summary.total_entries) || 0;
    renderLogsTable(result.data || []);
    renderPagination();
  } catch (e) {
    clearEl(tbody);
    tbody.appendChild(
      createEl('tr', null, [
        createEl('td', { colspan: '8' }, [
          createEl('div', { className: 'empty-state', textContent: e.message || 'Failed to load request logs.' })
        ])
      ])
    );
    renderPagination();
  }
}

function stageBadgeClass(stage) {
  if (stage === 'on_error') return 'badge badge-error';
  if (stage === 'before_request') return 'badge badge-info';
  if (stage === 'after_request') return 'badge badge-success';
  return 'badge badge-muted';
}

function statusBadge(entry) {
  if (entry.error_message || entry.ErrorMessage) {
    return createEl('span', { className: 'badge badge-error', textContent: 'error' });
  }
  return createEl('span', { className: 'badge badge-success', textContent: 'ok' });
}

function renderLogsTable(entries) {
  var tbody = document.getElementById('logs-tbody');
  if (!tbody) return;
  clearEl(tbody);

  if (!entries || entries.length === 0) {
    tbody.appendChild(
      createEl('tr', null, [
        createEl('td', { colspan: '8' }, [
          createEl('div', { className: 'empty-state', textContent: 'No request logs found.' })
        ])
      ])
    );
    return;
  }

  entries.forEach(function(entry) {
    var stage = entry.stage || entry.Stage || '';
    var provider = entry.provider || entry.Provider || '-';
    var model = entry.model || entry.Model || '-';
    var totalTokens = entry.total_tokens != null ? entry.total_tokens : (entry.TotalTokens != null ? entry.TotalTokens : null);
    var errorMsg = entry.error_message || entry.ErrorMessage || '';
    var createdAt = entry.created_at || entry.CreatedAt || '';
    var latencyMs = entry.latency_ms != null ? entry.latency_ms : (entry.LatencyMS != null ? entry.LatencyMS : null);

    var stageBadge = createEl('span', { className: stageBadgeClass(stage), textContent: stage || '-' });

    var latencyCell = '-';
    if (latencyMs != null) {
      latencyCell = latencyMs + 'ms';
    }

    var errorCell = createEl('span', { className: 'mono', textContent: errorMsg ? errorMsg.substring(0, 60) + (errorMsg.length > 60 ? '…' : '') : '-' });
    if (errorMsg) errorCell.title = errorMsg;

    var tr = createEl('tr', null, [
      createEl('td', { className: 'mono', textContent: timeAgo(createdAt) }),
      createEl('td', { textContent: provider }),
      createEl('td', { className: 'mono', textContent: model }),
      createEl('td', null, [stageBadge]),
      createEl('td', null, [statusBadge(entry)]),
      createEl('td', { className: 'mono', textContent: latencyCell }),
      createEl('td', { className: 'mono', textContent: totalTokens != null ? formatNumber(totalTokens) : '-' }),
      createEl('td', null, [errorCell])
    ]);
    tbody.appendChild(tr);
  });
}

function renderPagination() {
  var container = document.getElementById('logs-pagination');
  if (!container) return;
  clearEl(container);

  var totalPages = Math.max(1, Math.ceil(logsTotal / logsLimit));
  var currentPage = Math.floor(logsOffset / logsLimit) + 1;

  var info = createEl('span', {
    textContent: logsTotal > 0
      ? 'Page ' + currentPage + ' of ' + totalPages + ' (' + formatNumber(logsTotal) + ' total)'
      : 'No results',
    style: 'font-size:13px;color:var(--text-muted);margin:0 8px;'
  });

  var prevBtn = createEl('button', {
    className: 'btn btn-secondary',
    textContent: 'Previous',
    onclick: function() {
      if (logsOffset > 0) {
        logsOffset = Math.max(0, logsOffset - logsLimit);
        loadLogs();
      }
    }
  });
  if (logsOffset === 0) prevBtn.setAttribute('disabled', 'disabled');

  var nextBtn = createEl('button', {
    className: 'btn btn-secondary',
    textContent: 'Next',
    onclick: function() {
      if (logsOffset + logsLimit < logsTotal) {
        logsOffset += logsLimit;
        loadLogs();
      }
    }
  });
  if (logsOffset + logsLimit >= logsTotal) nextBtn.setAttribute('disabled', 'disabled');

  container.appendChild(prevBtn);
  container.appendChild(info);
  container.appendChild(nextBtn);
}
