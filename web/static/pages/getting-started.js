'use strict';

document.addEventListener('DOMContentLoaded', function() { loadChecklist(); });

async function loadChecklist() {
  var root = document.getElementById('checklist');
  if (!root) return;
  var steps = [
    { id: 'started', label: 'Start the gateway', done: true },
    { id: 'providers', label: 'Configure providers', done: false, link: '/dashboard/providers' },
    { id: 'keys', label: 'Create your first API key', done: false, link: '/dashboard/keys' },
    { id: 'request', label: 'Make a test request', done: false, link: '/dashboard/playground' },
    { id: 'logs', label: 'Check request logs', done: false, link: '/dashboard/logs' }
  ];
  try {
    var dashboard = await apiRequest('/admin/dashboard');
    if (dashboard.providers && dashboard.providers.available > 0) steps[1].done = true;
    if (dashboard.keys && dashboard.keys.total > 0) steps[2].done = true;
    if (localStorage.getItem('gw-made-request') || (dashboard.request_logs && dashboard.request_logs.total > 0)) {
      steps[3].done = true;
    }
    if (localStorage.getItem('gw-visited-logs')) steps[4].done = true;
  } catch (e) {
    root.appendChild(createEl('p', { className: 'empty-state', textContent: e.message }));
    return;
  }
  var allDone = steps.every(function(s) { return s.done; });
  if (allDone && localStorage.getItem('gw-onboarding-complete')) {
    window.location.href = '/dashboard/overview';
    return;
  }
  clearEl(root);
  steps.forEach(function(step) {
    var check = createEl('div', { className: 'checklist-check' + (step.done ? ' done' : ''), textContent: step.done ? '\u2713' : '' });
    var text = createEl('span', { className: 'checklist-text' + (step.done ? ' done' : ''), textContent: step.label });
    var children = [check, text];
    if (!step.done && step.link) {
      children.push(createEl('a', { className: 'btn btn-primary', href: step.link, textContent: 'Go', style: 'margin-left:auto;font-size:12px;padding:4px 12px;' }));
    }
    root.appendChild(createEl('div', { className: 'checklist-item' }, children));
  });
  if (allDone) {
    localStorage.setItem('gw-onboarding-complete', 'true');
    root.appendChild(createEl('div', { style: 'padding:16px;text-align:center;' }, [
      createEl('p', { style: 'color:var(--success);font-weight:600;margin-bottom:8px;', textContent: 'All done!' }),
      createEl('a', { className: 'btn btn-primary', href: '/dashboard/overview', textContent: 'Go to Overview' })
    ]));
  }
}
