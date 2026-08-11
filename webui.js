// FreebuffProxy Web UI 逻辑
const $ = (sel) => document.querySelector(sel);

const ERROR_HINTS = {
  '401': '认证失败：Token 无效或已过期，需重新登录获取',
  '403': '禁止访问：账号可能被风控或地区限制',
  '404': '资源不存在：模型/Agent 未找到或服务端路径变更',
  '409': '冲突：Session 模型不匹配，需先删除旧 Session',
  '429': '限流/额度耗尽：请求过多或每日额度用完',
  '500': '服务器内部错误：Freebuff 服务端异常',
  '502': '网关错误：上游服务不可达',
  '503': '服务不可用：等待队列已满或服务维护',
  'unknown': '未知错误',
};

function flash(msg) {
  const f = $('#flash');
  f.textContent = msg;
  setTimeout(() => { f.textContent = ''; }, 5000);
}

async function api(path, opts = {}) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

async function loadStatus() {
  try {
    const s = await api('/api/webui/status');
    $('#metrics').innerHTML = `
      <div class="metric"><div class="num">${s.models}</div><div class="label">模型数</div></div>
      <div class="metric"><div class="num">${s.tokens}</div><div class="label">Freebuff Token 数</div></div>
      <div class="metric"><div class="num">${s.api_keys}</div><div class="label">API Key 数</div></div>
      <div class="metric"><div class="num">${Math.floor(s.uptime_sec / 60)}m</div><div class="label">运行时长</div></div>
    `;
  } catch (e) { flash('状态加载失败: ' + e.message); }
}

function renderChips(models) {
  const box = $('#models-chips');
  box.classList.remove('hidden');
  box.innerHTML = '';
  const sel = $('#chat-model');
  sel.innerHTML = '';
  models.forEach(m => {
    const chip = document.createElement('span');
    chip.className = 'chip';
    chip.textContent = m;
    box.appendChild(chip);
    const opt = document.createElement('option');
    opt.value = m;
    opt.textContent = m;
    sel.appendChild(opt);
  });
  flash(`已拉起 ${models.length} 个模型`);
}

async function loadModels() {
  try {
    const res = await fetch('/v1/models');
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const d = await res.json();
    const models = (d.data || []).map(m => m.id);
    renderChips(models);
  } catch (e) {
    flash('拉起模型失败: ' + e.message);
    $('#models-chips').classList.remove('hidden');
    $('#models-chips').innerHTML = '<span class="chip err">模型拉取失败（检查上游 Token 是否有效）</span>';
  }
}

function errHint(code) {
  return ERROR_HINTS[code] || ERROR_HINTS['unknown'];
}

async function probeAll() {
  const btn = $('#btn-probe');
  btn.disabled = true;
  btn.textContent = '探测中...';
  const tbody = $('#probe-table tbody');
  tbody.innerHTML = '<tr><td colspan="4">探测中...</td></tr>';
  try {
    const results = await api('/api/webui/probe');
    tbody.innerHTML = '';
    results.forEach(r => {
      const tr = document.createElement('tr');
      let statusHtml, delayHtml, errHtml;
      if (r.status === 'ok') {
        statusHtml = '<span class="badge badge-good">正常</span>';
        delayHtml = `<span class="chip time">${r.latency_ms} ms</span>`;
        errHtml = '—';
      } else {
        const code = r.error_code || 'unknown';
        statusHtml = `<span class="badge badge-bad">异常</span>`;
        delayHtml = '—';
        errHtml = `<strong style="color:#f85149">${code}</strong> — ${errHint(code)}`;
      }
      tr.innerHTML = `<td>${r.model}</td><td>${statusHtml}</td><td>${delayHtml}</td><td>${errHtml}</td>`;
      tbody.appendChild(tr);
    });
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="4" style="color:#f85149">探测失败: ${e.message}</td></tr>`;
  }
  btn.disabled = false;
  btn.textContent = '探测延迟';
}

async function loadTokens() {
  const tbody = $('#token-table tbody');
  try {
    const snaps = await api('/api/webui/tokens');
    tbody.innerHTML = '';
    if (!snaps.length) {
      tbody.innerHTML = '<tr><td colspan="4">无 Token 配置</td></tr>';
      return;
    }
    snaps.forEach(s => {
      const tr = document.createElement('tr');
      const name = s.name || (s.token ? s.token.slice(0, 8) + '***' : 'token');
      let st, cls;
      if (s.cooldown_until && new Date(s.cooldown_until) > new Date()) {
        st = '限流/冷却'; cls = 'badge-warn';
      } else if (s.last_error) {
        st = '异常'; cls = 'badge-bad';
      } else {
        st = '正常'; cls = 'badge-good';
      }
      tr.innerHTML = `<td>${name}</td><td><span class="badge ${cls}">${st}</span></td><td>${s.runs ? s.runs.length : 0} 个 run</td><td>${s.last_error || '—'}</td>`;
      tbody.appendChild(tr);
    });
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="4">加载失败: ${e.message}</td></tr>`;
  }
}
// ============ 用量统计 ============
async function loadUsage() {
  const days = $('#usage-days').value;
  try {
    const d = await api('/api/webui/usage?days=' + days);
    const m = $('#usage-model tbody');
    const k = $('#usage-key tbody');
    m.innerHTML = '';
    k.innerHTML = '';
    const byModel = d.by_model || {};
    const byKey = d.by_api_key || {};
    const entriesM = Object.entries(byModel).sort((a, b) => b[1] - a[1]);
    const entriesK = Object.entries(byKey).sort((a, b) => b[1] - a[1]);
    if (!entriesM.length) m.innerHTML = '<tr><td colspan="2" style="color:#8b949e">暂无数据</td></tr>';
    entriesM.forEach(([model, tokens]) => {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td>${model}</td><td>${tokens.toLocaleString()}</td>`;
      m.appendChild(tr);
    });
    if (!entriesK.length) k.innerHTML = '<tr><td colspan="2" style="color:#8b949e">暂无数据</td></tr>';
    entriesK.forEach(([key, tokens]) => {
      const tr = document.createElement('tr');
      const short = key.length > 12 ? key.slice(0, 6) + '***' + key.slice(-4) : (key || '(默认)');
      tr.innerHTML = `<td>${short}</td><td>${tokens.toLocaleString()}</td>`;
      k.appendChild(tr);
    });
  } catch (e) {
    flash('用量加载失败: ' + e.message);
  }
}

// ============ 日志 ============
async function loadLogs() {
  try {
    const d = await api('/api/webui/logs');
    const box = $('#log-box');
    box.innerHTML = '';
    (d.logs || []).forEach(l => {
      const div = document.createElement('div');
      div.className = 'log-line log-' + (l.level || 'info');
      div.textContent = `[${l.time}] [${l.level}] [${l.source}] ${l.message}`;
      box.appendChild(div);
    });
    if (!(d.logs || []).length) box.innerHTML = '<div class="log-line" style="color:#8b949e">暂无日志</div>';
  } catch (e) { /* 忽略 */ }
}

async function clearLogs() {
  try {
    await api('/api/webui/logs/clear', { method: 'POST' });
    loadLogs();
    flash('日志已清空');
  } catch (e) { flash('清空失败: ' + e.message); }
}

// ============ 聊天测试（流式/非流式） ============
let chatAbort = null;

function addMsg(role, content) {
  const box = $('#chat-history');
  const div = document.createElement('div');
  div.className = 'msg ' + role;
  const r = role === 'user' ? '👤' : '🤖';
  div.innerHTML = `<span class="role">${r}</span><span class="content"></span>`;
  box.appendChild(div);
  div.querySelector('.content').textContent = content;
  box.scrollTop = box.scrollHeight;
  return div.querySelector('.content');
}

async function sendChat() {
  const text = $('#chat-input').value.trim();
  if (!text) return;
  const model = $('#chat-model').value;
  const stream = $('#chat-stream').value === 'true';
  addMsg('user', text);
  $('#chat-input').value = '';
  const contentEl = addMsg('assistant', '…');

  if (chatAbort) chatAbort.abort();
  chatAbort = new AbortController();

  try {
    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model: model,
        messages: [{ role: 'user', content: text }],
        max_tokens: 500,
        stream: stream
      }),
      signal: chatAbort.signal
    });
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try { const e = await res.json(); msg = e.error?.message || e.detail || msg; } catch(e2) {}
      contentEl.textContent = '错误: ' + msg;
      return;
    }
    if (!stream) {
      const d = await res.json();
      contentEl.textContent = d.choices?.[0]?.message?.content || '(空回复)';
    } else {
      // 流式: SSE 解析
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      let full = '';
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf('\n')) >= 0) {
          const line = buf.slice(0, idx).trim();
          buf = buf.slice(idx + 1);
          if (!line.startsWith('data:')) continue;
          const data = line.slice(5).trim();
          if (data === '[DONE]') continue;
          try {
            const j = JSON.parse(data);
            const delta = j.choices?.[0]?.delta?.content || '';
            if (delta) { full += delta; contentEl.textContent = full; }
          } catch (e) {}
        }
      }
      if (!full) contentEl.textContent = '(空回复)';
    }
    loadLogs();
  } catch (e) {
    if (e.name !== 'AbortError') contentEl.textContent = '错误: ' + e.message;
  }
}

function stopChat() {
  if (chatAbort) { chatAbort.abort(); chatAbort = null; }
}

$('#chat-input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendChat();
  }
});

// ============ 自动刷新 ============
setInterval(() => { loadLogs(); }, 5000);
setInterval(() => { loadTokens(); }, 15000);

// ============ 初始化: 打开自动拉起模型 ============
(async function init() {
  loadStatus();
  await loadModels();
  loadTokens();
  loadUsage();
  loadLogs();
})();

// ============ Freebuff OAuth 登录 ============
let authSession = null;
let authPollAbort = null;

async function startAuth() {
  const btn = document.querySelector('#auth-step2 ~ .card .btn-primary');
  const status = $('#auth-status');
  status.textContent = '生成中...';
  try {
    const s = await api('/api/webui/auth/code', { method: 'POST' });
    authSession = s;
    $('#auth-login-url').textContent = s.loginUrl;
    $('#auth-login-url').href = s.loginUrl;
    $('#auth-step2').classList.remove('hidden');
    $('#auth-result').classList.add('hidden');
    status.textContent = '已生成登录链接，请登录后粘贴回调 URL';
    // 自动复制到剪贴板
    navigator.clipboard.writeText(s.loginUrl).catch(() => {});
  } catch (e) {
    status.textContent = '生成失败: ' + e.message;
  }
}

function copyUrl() {
  const url = $('#auth-login-url').textContent;
  navigator.clipboard.writeText(url).catch(() => {});
  flash('登录链接已复制');
}

function cancelAuth() {
  if (authPollAbort) { authPollAbort.abort(); authPollAbort = null; }
  authSession = null;
  $('#auth-step2').classList.add('hidden');
  $('#auth-progress').style.display = 'none';
  $('#auth-result').classList.add('hidden');
  $('#auth-status').textContent = '';
}

async function verifyAuth() {
  const raw = $('#auth-callback-input').value.trim();
  if (!raw) { flash('请粘贴回调 URL'); return; }

  // 从 URL 提取 auth_code
  let code = raw;
  if (raw.includes('auth_code=')) {
    try { const u = new URL(raw); code = u.searchParams.get('auth_code'); } catch(e) {}
  }
  // freebuff.llm.pm 的校验：auth_code 必须点分隔 ≥3 段
  if (!code || code.split('.').length < 3) {
    if (raw.includes('auth_code')) {
      flash('URL 格式不对，请粘贴完整的回调 URL（包含 auth_code=...）');
    } else {
      flash('输入的 auth_code 无效，请粘贴完整的回调 URL');
    }
    return;
  }

  // fingerprint 前缀校验
  if (authSession && authSession.fingerprintId) {
    const prefix = code.split('.')[0];
    if (prefix !== authSession.fingerprintId) {
      flash('Fingerprint 不匹配！请重新生成登录链接再试');
      return;
    }
  }

  const btn = document.querySelector('#auth-step2 .btn-primary');
  btn.disabled = true;
  btn.textContent = '验证中...';
  $('#auth-progress').style.display = 'block';
  $('#auth-progress-bar').style.width = '0%';
  $('#auth-progress-text').textContent = '等待登录完成...';

  authPollAbort = new AbortController();
  const max = 15;

  try {
    let user = null;
    for (let i = 0; i < max; i++) {
      if (authPollAbort.signal.aborted) throw new Error('已取消');
      $('#auth-progress-bar').style.width = Math.min(((i + 1) / max) * 100, 100) + '%';

      const r = await fetch('/api/webui/auth/status', {
        method: 'POST',
        signal: authPollAbort.signal,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          fingerprintId: authSession.fingerprintId,
          fingerprintHash: authSession.fingerprintHash,
          expiresAt: authSession.expiresAt
        })
      });
      if (!r.ok) throw new Error('状态检查失败 (' + r.status + ')');
      const d = await r.json();
      if (d.error) throw new Error(d.error);
      if (!d.pending && d.user) { user = d.user; break; }
      await new Promise(resolve => setTimeout(resolve, 2000));
    }

    if (!user) throw new Error('超时 — 30 秒内未完成登录');

    // 拿到 token，写入 config
    const importRes = await fetch('/api/webui/auth/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        authToken: user.authToken,
        email: user.email,
        name: user.name,
        fingerprintId: user.fingerprintId
      })
    });
    const importData = await importRes.json();
    if (!importData.ok) throw new Error(importData.error || '导入失败');

    // 显示结果
    $('#auth-result').classList.remove('hidden');
    $('#auth-result-detail').textContent = `账号: ${user.name} (${user.email}) | Token: ${user.authToken.slice(0, 8)}***`;
    $('#auth-step2').classList.add('hidden');
    $('#auth-progress').style.display = 'none';
    $('#auth-status').textContent = '✅ 登录成功，Token 已添加';
    flash('Token 已成功添加并持久化到 config.json！');

    // 刷新账号状态
    loadTokens();
  } catch (e) {
    if (e.message !== '已取消') {
      $('#auth-status').textContent = '验证失败: ' + e.message;
      flash('验证失败: ' + e.message);
    }
  } finally {
    btn.disabled = false;
    btn.textContent = '验证并获取 Token';
    authPollAbort = null;
    $('#auth-progress').style.display = 'none';
  }
}
