'use strict';

var _currentRangeHours = 1;

function setRange(btn, hours) {
  _currentRangeHours = hours;
  var tabs = document.querySelectorAll('#range-tabs .tab');
  tabs.forEach(function(t) { t.classList.remove('active'); });
  btn.classList.add('active');
  loadAnalytics();
}

function loadAnalytics() {
  var since = new Date(Date.now() - _currentRangeHours * 3600 * 1000).toISOString();
  apiRequest('/admin/logs/stats?since=' + encodeURIComponent(since))
    .then(function(data) {
      var summary = data.summary || {};
      var byProvider = data.by_provider || {};
      var byModel = data.by_model || {};

      var totalEl = document.getElementById('stat-total-requests');
      if (totalEl) totalEl.textContent = formatNumber(summary.total_entries != null ? summary.total_entries : 0);

      var tokensEl = document.getElementById('stat-total-tokens');
      if (tokensEl) tokensEl.textContent = formatNumber(summary.total_tokens != null ? summary.total_tokens : 0);

      var errorsEl = document.getElementById('stat-errors');
      if (errorsEl) errorsEl.textContent = formatNumber(summary.error_entries != null ? summary.error_entries : 0);

      renderBarChart('chart-by-provider', byProvider);
      renderBarChart('chart-by-model', byModel);
      renderTokenChart(summary);
    })
    .catch(function(err) {
      showToast('Failed to load analytics: ' + err.message, 'error');
    });
}

function renderBarChart(containerId, data) {
  var container = document.getElementById(containerId);
  if (!container) return;
  clearEl(container);

  var keys = Object.keys(data);
  if (keys.length === 0) {
    container.appendChild(createEl('p', { className: 'empty-state', textContent: 'No data for this time range.' }));
    return;
  }

  var maxVal = 0;
  keys.forEach(function(k) { if (data[k] > maxVal) maxVal = data[k]; });

  keys.sort(function(a, b) { return data[b] - data[a]; });

  keys.forEach(function(key) {
    var count = data[key];
    var pct = maxVal > 0 ? (count / maxVal * 100).toFixed(1) : 0;

    var fill = createEl('div', {
      className: 'bar-chart-fill',
      style: 'width:' + pct + '%'
    });
    var track = createEl('div', { className: 'bar-chart-track' }, [fill]);
    var label = createEl('div', { className: 'bar-chart-label', textContent: key });
    var value = createEl('div', { className: 'bar-chart-value', textContent: formatNumber(count) });
    var row = createEl('div', { className: 'bar-chart-row' }, [label, track, value]);
    container.appendChild(row);
  });
}

function renderTokenChart(summary) {
  var container = document.getElementById('chart-tokens');
  if (!container) return;
  clearEl(container);

  if (typeof uPlot === 'undefined') {
    container.appendChild(createEl('p', { className: 'empty-state', textContent: 'Chart library not loaded.' }));
    return;
  }

  var totalTokens = summary.total_tokens || 0;
  var now = Math.floor(Date.now() / 1000);
  var buckets = _currentRangeHours <= 1 ? 12 : _currentRangeHours <= 6 ? 12 : _currentRangeHours <= 24 ? 24 : 28;
  var bucketSize = Math.floor(_currentRangeHours * 3600 / buckets);

  var timestamps = [];
  var values = [];
  var perBucket = buckets > 0 ? Math.round(totalTokens / buckets) : 0;
  for (var i = 0; i < buckets; i++) {
    timestamps.push(now - (buckets - 1 - i) * bucketSize);
    values.push(perBucket);
  }

  var opts = {
    width: container.clientWidth || 600,
    height: 200,
    series: [
      {},
      {
        label: 'Tokens',
        stroke: '#0D9488',
        fill: 'rgba(13,148,136,0.12)',
        width: 2,
      }
    ],
    axes: [
      {
        values: function(u, vals) {
          return vals.map(function(v) {
            var d = new Date(v * 1000);
            return d.getHours() + ':' + ('0' + d.getMinutes()).slice(-2);
          });
        }
      },
      {}
    ],
    scales: { x: { time: true }, y: { auto: true } },
  };

  new uPlot(opts, [timestamps, values], container);
}

document.addEventListener('DOMContentLoaded', function() {
  loadAnalytics();
});
