'use strict';

function getToken() { return localStorage.getItem('gw-admin-key') || ''; }
function setToken(token) { localStorage.setItem('gw-admin-key', token); }

async function apiRequest(path, options) {
  var token = getToken();
  if (!token) throw new Error('API key is required. Enter it in the header.');
  var res = await fetch(path, {
    ...(options || {}),
    headers: { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', ...((options && options.headers) || {}) }
  });
  var data = await res.json();
  if (!res.ok) {
    var msg = (data && data.error && data.error.message) ? data.error.message : 'Request failed';
    throw new Error(msg);
  }
  return data;
}

function initDarkMode() {
  var saved = localStorage.getItem('gw-theme');
  if (saved) document.documentElement.setAttribute('data-theme', saved);
  else if (window.matchMedia('(prefers-color-scheme: dark)').matches) document.documentElement.setAttribute('data-theme', 'dark');
  updateThemeIcon();
}

function toggleDarkMode() {
  var current = document.documentElement.getAttribute('data-theme');
  var next = current === 'dark' ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', next);
  localStorage.setItem('gw-theme', next);
  updateThemeIcon();
}

function updateThemeIcon() {
  var btn = document.getElementById('theme-toggle');
  if (!btn) return;
  btn.textContent = document.documentElement.getAttribute('data-theme') === 'dark' ? '\u2600' : '\u263E';
}

function timeAgo(isoString) {
  if (!isoString) return '-';
  var diff = (Date.now() - new Date(isoString).getTime()) / 1000;
  if (diff < 60) return Math.floor(diff) + 's ago';
  if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
  if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
  return Math.floor(diff / 86400) + 'd ago';
}

function formatNumber(n) { if (n == null) return '-'; return Number(n).toLocaleString(); }

function showToast(message, type) {
  var existing = document.querySelector('.toast');
  if (existing) existing.remove();
  var el = document.createElement('div');
  el.className = 'toast toast-' + (type || 'success');
  el.textContent = message;
  document.body.appendChild(el);
  setTimeout(function() { el.remove(); }, 3000);
}

function createEl(tag, attrs, children) {
  var el = document.createElement(tag);
  if (attrs) {
    Object.keys(attrs).forEach(function(key) {
      if (key === 'className') el.className = attrs[key];
      else if (key === 'textContent') el.textContent = attrs[key];
      else if (key === 'onclick') el.addEventListener('click', attrs[key]);
      else if (key === 'style') el.style.cssText = attrs[key];
      else el.setAttribute(key, attrs[key]);
    });
  }
  if (children) {
    children.forEach(function(child) {
      if (typeof child === 'string') el.appendChild(document.createTextNode(child));
      else if (child) el.appendChild(child);
    });
  }
  return el;
}

function clearEl(el) { while (el.firstChild) el.removeChild(el.firstChild); }

function checkAuth() {
  var token = getToken();
  if (!token) {
    window.location.href = '/dashboard/login';
    return false;
  }
  return true;
}

function initScopeBadge() {
  var badge = document.getElementById('scope-badge');
  var logoutBtn = document.getElementById('logout-btn');
  if (!badge || !logoutBtn) return;

  var token = getToken();
  if (!token) return;

  badge.style.display = 'inline-block';
  logoutBtn.style.display = 'inline-block';

  var scopes = JSON.parse(localStorage.getItem('gw-scopes') || '["admin"]');
  if (scopes.indexOf('admin') >= 0) {
    badge.textContent = 'Admin';
    badge.className = 'badge badge-admin';
  } else {
    badge.textContent = 'Read Only';
    badge.className = 'badge badge-readonly';
    hideWriteActions();
  }
}

function hideWriteActions() {
  var els = document.querySelectorAll('[data-scope="admin"]');
  for (var i = 0; i < els.length; i++) {
    els[i].style.display = 'none';
  }
}

function logout() {
  localStorage.removeItem('gw-admin-key');
  localStorage.removeItem('gw-scopes');
  window.location.href = '/dashboard/login';
}

document.addEventListener('DOMContentLoaded', function() {
  initDarkMode();
  if (checkAuth()) {
    initScopeBadge();
  }
});
