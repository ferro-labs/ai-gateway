'use strict';

function toggleSystemPrompt() {
  var textarea = document.getElementById('pg-system');
  var btn = document.getElementById('pg-system-toggle');
  if (!textarea || !btn) return;
  var hidden = textarea.style.display === 'none';
  textarea.style.display = hidden ? 'block' : 'none';
  btn.textContent = hidden ? 'Hide' : 'Show';
}

async function loadModels() {
  var select = document.getElementById('pg-model');
  if (!select) return;
  try {
    var headers = {};
    var token = getToken();
    if (token) headers['Authorization'] = 'Bearer ' + token;
    var res = await fetch('/v1/models', { headers: headers });
    var data = await res.json();
    var models = (data && data.data) ? data.data : [];
    clearEl(select);
    if (models.length === 0) {
      select.appendChild(createEl('option', { value: '', textContent: 'No models available' }));
      return;
    }
    models.forEach(function(m) {
      var id = m.id || '';
      select.appendChild(createEl('option', { value: id, textContent: id }));
    });
  } catch (err) {
    clearEl(select);
    select.appendChild(createEl('option', { value: '', textContent: 'Failed to load models' }));
  }
}

async function sendMessage() {
  var sendBtn = document.getElementById('pg-send');
  var userTextarea = document.getElementById('pg-user');
  var modelSelect = document.getElementById('pg-model');
  var tempInput = document.getElementById('pg-temp');
  var maxTokensInput = document.getElementById('pg-max-tokens');
  var systemTextarea = document.getElementById('pg-system');
  var responseCard = document.getElementById('pg-response-card');
  var responseDiv = document.getElementById('pg-response');
  var metaSpan = document.getElementById('pg-meta');

  if (!userTextarea || !modelSelect || !sendBtn) return;

  var userMessage = userTextarea.value.trim();
  if (!userMessage) {
    showToast('Please enter a message.', 'error');
    return;
  }

  var model = modelSelect.value;
  if (!model) {
    showToast('Please select a model.', 'error');
    return;
  }

  var token = getToken();
  if (!token) {
    showToast('API key is required. Enter it in the header.', 'error');
    return;
  }

  var temperature = parseFloat(tempInput.value);
  if (isNaN(temperature)) temperature = 0.7;

  var maxTokens = parseInt(maxTokensInput.value, 10);
  if (isNaN(maxTokens) || maxTokens < 1) maxTokens = 1024;

  var messages = [];
  var systemContent = systemTextarea ? systemTextarea.value.trim() : '';
  if (systemContent) {
    messages.push({ role: 'system', content: systemContent });
  }
  messages.push({ role: 'user', content: userMessage });

  var body = {
    model: model,
    messages: messages,
    temperature: temperature,
    max_tokens: maxTokens,
    stream: true
  };

  sendBtn.disabled = true;
  sendBtn.textContent = 'Sending...';

  responseCard.style.display = 'block';
  responseDiv.textContent = '';
  responseDiv.style.color = '';
  metaSpan.textContent = '';

  var startTime = Date.now();

  try {
    var res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: {
        'Authorization': 'Bearer ' + token,
        'Content-Type': 'application/json'
      },
      body: JSON.stringify(body)
    });

    if (!res.ok) {
      var errData = null;
      try { errData = await res.json(); } catch (_) {}
      var errMsg = (errData && errData.error && errData.error.message) ? errData.error.message : ('Request failed: ' + res.status);
      responseDiv.textContent = errMsg;
      responseDiv.style.color = 'var(--error)';
      return;
    }

    var reader = res.body.getReader();
    var decoder = new TextDecoder();
    var fullText = '';
    var buffer = '';
    var promptTokens = 0;
    var completionTokens = 0;

    while (true) {
      var chunk = await reader.read();
      if (chunk.done) break;
      buffer += decoder.decode(chunk.value, { stream: true });
      var lines = buffer.split('\n');
      buffer = lines.pop() || '';
      for (var i = 0; i < lines.length; i++) {
        var line = lines[i].trim();
        if (!line.startsWith('data: ')) continue;
        var raw = line.slice(6).trim();
        if (raw === '[DONE]') break;
        try {
          var parsed = JSON.parse(raw);
          var delta = parsed.choices && parsed.choices[0] && parsed.choices[0].delta;
          if (delta && delta.content) {
            fullText += delta.content;
            responseDiv.textContent = fullText;
          }
          if (parsed.usage) {
            promptTokens = parsed.usage.prompt_tokens || 0;
            completionTokens = parsed.usage.completion_tokens || 0;
          }
        } catch (_) {}
      }
    }

    var elapsed = ((Date.now() - startTime) / 1000).toFixed(2);
    var metaParts = [elapsed + 's'];
    if (promptTokens || completionTokens) {
      metaParts.push(promptTokens + ' in / ' + completionTokens + ' out tokens');
    }
    metaSpan.textContent = metaParts.join(' · ');
    localStorage.setItem('gw-made-request', 'true');

  } catch (err) {
    responseDiv.textContent = 'Error: ' + (err.message || 'Unknown error');
    responseDiv.style.color = 'var(--error)';
  } finally {
    sendBtn.disabled = false;
    sendBtn.textContent = 'Send';
  }
}

document.addEventListener('DOMContentLoaded', function() {
  loadModels();

  var userTextarea = document.getElementById('pg-user');
  if (userTextarea) {
    userTextarea.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        sendMessage();
      }
    });
  }
});
