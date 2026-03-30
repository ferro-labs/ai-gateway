'use strict';

async function loadProviders() {
  var summary = document.getElementById('providers-summary');
  var grid = document.getElementById('providers-grid');
  if (!summary || !grid) return;

  try {
    var healthData = await apiRequest('/admin/health');
    var providersList = await apiRequest('/admin/providers');

    // Build a map of name -> models array from providers list
    var modelsMap = {};
    if (Array.isArray(providersList)) {
      providersList.forEach(function(p) {
        modelsMap[p.name] = Array.isArray(p.models) ? p.models : [];
      });
    }

    var providers = Array.isArray(healthData.providers) ? healthData.providers : [];
    var availableCount = providers.filter(function(p) { return p.status === 'available'; }).length;

    // Update summary line
    summary.textContent = providers.length + ' provider' + (providers.length !== 1 ? 's' : '') +
      ' — ' + availableCount + ' available, ' + (providers.length - availableCount) + ' unavailable';

    clearEl(grid);

    if (providers.length === 0) {
      grid.appendChild(createEl('div', { className: 'empty-state', textContent: 'No providers configured.' }));
      return;
    }

    providers.forEach(function(p) {
      var card = buildProviderCard(p, modelsMap[p.name] || []);
      grid.appendChild(card);
    });
  } catch (err) {
    summary.textContent = 'Failed to load providers.';
    clearEl(grid);
    grid.appendChild(createEl('div', { className: 'empty-state', textContent: err.message }));
  }
}

function buildProviderCard(provider, models) {
  var isAvailable = provider.status === 'available';
  var modelCount = provider.models != null ? provider.models : models.length;

  var dot = createEl('span', { className: 'status-dot ' + (isAvailable ? 'available' : 'unavailable') });

  var nameEl = createEl('span', { textContent: provider.name, style: 'font-weight:600;font-size:14px;color:var(--text-primary);' });

  var header = createEl('div', {
    style: 'display:flex;align-items:center;gap:6px;margin-bottom:8px;'
  }, [dot, nameEl]);

  var countEl = createEl('div', {
    textContent: modelCount + ' model' + (modelCount !== 1 ? 's' : ''),
    style: 'font-size:12px;color:var(--text-muted);margin-bottom:6px;'
  });

  var statusEl = createEl('div', {
    textContent: isAvailable ? 'Available' : (provider.message || 'Unavailable'),
    style: 'font-size:12px;color:' + (isAvailable ? 'var(--success)' : 'var(--error)') + ';'
  });

  var modelListEl = createEl('div', {
    'data-role': 'model-list',
    style: 'display:none;margin-top:10px;border-top:1px solid var(--border);padding-top:10px;'
  });

  if (models.length > 0) {
    models.forEach(function(m) {
      var modelId = (m && m.id) ? m.id : String(m);
      var item = createEl('div', {
        className: 'mono',
        textContent: modelId,
        style: 'font-size:11px;color:var(--text-secondary);padding:2px 0;'
      });
      modelListEl.appendChild(item);
    });
  } else if (modelCount > 0) {
    modelListEl.appendChild(createEl('div', {
      textContent: 'Model list not available',
      style: 'font-size:12px;color:var(--text-muted);'
    }));
  } else {
    modelListEl.appendChild(createEl('div', {
      textContent: 'No models',
      style: 'font-size:12px;color:var(--text-muted);'
    }));
  }

  var card = createEl('div', {
    className: 'provider-card',
    style: 'cursor:pointer;transition:border-color 0.15s;',
    onclick: function() { toggleModels(card); }
  }, [header, countEl, statusEl, modelListEl]);

  card.addEventListener('mouseenter', function() { card.style.borderColor = 'var(--accent)'; });
  card.addEventListener('mouseleave', function() { card.style.borderColor = ''; });

  return card;
}

function toggleModels(card) {
  var modelList = card.querySelector('[data-role="model-list"]');
  if (!modelList) return;
  modelList.style.display = modelList.style.display === 'none' ? 'block' : 'none';
}

document.addEventListener('DOMContentLoaded', loadProviders);
