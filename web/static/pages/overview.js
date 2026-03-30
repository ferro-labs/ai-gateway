'use strict';

function loadOverview() {
  // GET /admin/dashboard — providers and key counts
  apiRequest('/admin/dashboard')
    .then(function(data) {
      var providersEl = document.getElementById('stat-providers');
      if (providersEl) {
        var available = (data.providers && data.providers.available != null) ? data.providers.available : 0;
        var total = (data.providers && data.providers.total != null) ? data.providers.total : 0;
        providersEl.textContent = available + '/' + total;
      }

      var keysEl = document.getElementById('stat-keys');
      if (keysEl) {
        var active = (data.keys && data.keys.active != null) ? data.keys.active : 0;
        keysEl.textContent = formatNumber(active);
      }

      var requestsEl = document.getElementById('stat-requests');
      if (requestsEl) {
        var totalUsage = (data.total_usage != null) ? data.total_usage : 0;
        requestsEl.textContent = formatNumber(totalUsage);
      }
    })
    .catch(function(err) {
      showToast('Failed to load dashboard stats: ' + err.message, 'error');
    });

  // GET /admin/logs/stats — error rate
  apiRequest('/admin/logs/stats')
    .then(function(data) {
      var summary = data.summary || {};
      var totalEntries = summary.total_entries || 0;
      var errorEntries = summary.error_entries || 0;
      var errorRate = totalEntries > 0 ? (errorEntries / totalEntries * 100) : 0;

      var errEl = document.getElementById('stat-errors');
      if (errEl) {
        errEl.textContent = errorRate.toFixed(1) + '%';
        if (errorRate < 1) {
          errEl.style.color = 'var(--color-success, #10B981)';
        } else if (errorRate < 5) {
          errEl.style.color = 'var(--color-warning, #F59E0B)';
        } else {
          errEl.style.color = 'var(--color-error, #EF4444)';
        }
      }

      renderChart(summary);
    })
    .catch(function(err) {
      showToast('Failed to load log stats: ' + err.message, 'error');
    });

  loadRecentRequests();
}

function renderChart(stats) {
  var container = document.getElementById('chart-container');
  if (!container) return;

  if (typeof uPlot === 'undefined') {
    container.appendChild(createEl('p', { className: 'empty-state', textContent: 'Chart library not loaded.' }));
    return;
  }

  var now = Math.floor(Date.now() / 1000);
  var buckets = 24;
  var bucketSize = 3600; // 1 hour per bucket
  var totalEntries = (stats && stats.total_entries) ? stats.total_entries : 0;
  var perBucket = buckets > 0 ? Math.round(totalEntries / buckets) : 0;

  var timestamps = [];
  var values = [];
  for (var i = 0; i < buckets; i++) {
    timestamps.push(now - (buckets - 1 - i) * bucketSize);
    values.push(perBucket);
  }

  var isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  var gridColor = isDark ? '#1E293B' : '#F1F5F9';

  var opts = {
    width: container.clientWidth || 600,
    height: 200,
    series: [
      {},
      {
        label: 'Requests',
        stroke: '#0D9488',
        fill: '#0D9488',
        width: 1,
        paths: uPlot.paths.bars({ size: [0.6, 100] }),
        points: { show: false }
      }
    ],
    axes: [
      {
        grid: { stroke: gridColor, width: 1 },
        values: function(u, vals) {
          return vals.map(function(v) {
            var d = new Date(v * 1000);
            return d.getHours() + ':' + ('0' + d.getMinutes()).slice(-2);
          });
        }
      },
      {
        grid: { stroke: gridColor, width: 1 }
      }
    ],
    scales: { x: { time: true }, y: { auto: true } },
    legend: { show: false }
  };

  // Clear any existing chart
  while (container.firstChild) {
    container.removeChild(container.firstChild);
  }

  new uPlot(opts, [timestamps, values], container);
}

function loadRecentRequests() {
  var tbody = document.getElementById('recent-body');
  if (!tbody) return;

  apiRequest('/admin/logs?limit=5')
    .then(function(result) {
      var entries = (result && result.data) ? result.data : [];
      clearEl(tbody);

      if (!entries || entries.length === 0) {
        var link = createEl('a', { href: '/dashboard/playground', textContent: 'Playground.' });
        var td = createEl('td', { colspan: '6', className: 'empty-state' });
        td.appendChild(document.createTextNode('No requests yet. Try the '));
        td.appendChild(link);
        tbody.appendChild(createEl('tr', null, [td]));
        return;
      }

      entries.forEach(function(log) {
        var createdAt = log.created_at || log.CreatedAt || '';
        var provider = log.provider || '-';
        var model = log.model || '-';
        var latencyMs = log.latency_ms != null ? log.latency_ms : null;
        var totalTokens = log.total_tokens != null ? log.total_tokens : (log.TotalTokens != null ? log.TotalTokens : null);
        var errorMessage = log.error_message || log.ErrorMessage || '';
        var stage = log.stage || '';

        var isError = !!(errorMessage || stage === 'on_error');

        var timeEl = createEl('span', { className: 'mono', textContent: timeAgo(createdAt) });
        var modelEl = createEl('span', { className: 'mono', textContent: model });

        var statusEl;
        if (isError) {
          statusEl = createEl('span', { className: 'badge badge-error', textContent: 'ERR' });
        } else {
          statusEl = createEl('span', { className: 'badge badge-success', textContent: 'OK' });
        }

        var latencyEl = createEl('span', {
          className: 'mono',
          textContent: latencyMs != null ? latencyMs + 'ms' : '-'
        });

        var tokensEl = createEl('span', {
          className: 'mono',
          textContent: totalTokens != null ? formatNumber(totalTokens) : '-'
        });

        var tr = createEl('tr', null, [
          createEl('td', null, [timeEl]),
          createEl('td', { textContent: provider }),
          createEl('td', null, [modelEl]),
          createEl('td', null, [statusEl]),
          createEl('td', null, [latencyEl]),
          createEl('td', null, [tokensEl])
        ]);
        tbody.appendChild(tr);
      });
    })
    .catch(function(err) {
      clearEl(tbody);
      var td = createEl('td', { colspan: '6', className: 'empty-state', textContent: 'Failed to load recent requests.' });
      tbody.appendChild(createEl('tr', null, [td]));
    });
}

document.addEventListener('DOMContentLoaded', function() {
  loadOverview();
});
