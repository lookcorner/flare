// Flare 桌面版前端逻辑
// Go 绑定：window.go.main.App.*（Wails 注入）；事件：window.runtime.EventsOn

const App = window.go.main.App;
const RT = window.runtime;

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => [...document.querySelectorAll(sel)];
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, c => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
}[c]));

// ---------- Toast ----------
let toastTimer;
function toast(msg, isErr) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast show' + (isErr ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), isErr ? 5000 : 2200);
}

// ---------- 模态框 ----------
function openModal(id) { $('#' + id).classList.add('show'); }
function closeModal(id) { $('#' + id).classList.remove('show'); }
$$('.modal-mask').forEach(m =>
  m.addEventListener('click', e => { if (e.target === m) m.classList.remove('show'); }));
$$('[data-close]').forEach(b => b.addEventListener('click', () => closeModal(b.dataset.close)));

// 通用确认弹窗：标题、说明、按钮文案、回调
let confirmCb = null;
function confirmAction(msg, detail, okText, cb) {
  $('#confirm-msg').innerHTML = msg;
  $('#confirm-detail').textContent = detail || '';
  $('#btn-confirm-ok').textContent = okText || '确认';
  confirmCb = cb;
  openModal('modal-confirm');
}
$('#btn-confirm-ok').addEventListener('click', async () => {
  closeModal('modal-confirm');
  if (confirmCb) {
    try { await confirmCb(); } catch (e) { toast(String(e), true); }
    confirmCb = null;
  }
});

// ---------- 页面切换 ----------
let currentPage = 'dashboard';
async function go(page) {
  currentPage = page;
  $$('.nav-item').forEach(b => b.classList.toggle('active', b.dataset.page === page));
  $$('.page').forEach(p => p.classList.toggle('active', p.id === 'page-' + page));
  try {
    if (page === 'routes') await loadRoutes();
    if (page === 'relay') await loadRelay();
    if (page === 'logs') await loadLog();
    if (page === 'settings') await loadSettings();
  } catch (e) { toast(String(e), true); }
}
$$('.nav-item').forEach(b => b.addEventListener('click', () => go(b.dataset.page)));

// ---------- 全局状态 ----------
let state = {};
async function refreshState() {
  try {
    state = await App.GetState();
  } catch (e) { return; }
  renderSidebarStatus();
  $('#badge-routes').hidden = !state.routeCount;
  $('#badge-routes').textContent = state.routeCount || 0;
  $('#badge-rules').hidden = !state.ruleCount;
  $('#badge-rules').textContent = state.ruleCount || 0;
  if (currentPage === 'dashboard') renderDashboard();
  if (currentPage === 'routes') renderRoutesSub();
}

function renderSidebarStatus() {
  const row = (on, label) =>
    `<div class="row"><span class="dot ${on ? 'on' : 'off'}"></span>${esc(label)}</div>`;
  $('#sidebar-status').innerHTML =
    row(state.tunnelRunning, `cloudflared · ${state.tunnelRunning ? '运行中' : '未运行'}`) +
    row(state.relayRunning, `frpc · ${state.relayRunning ? '运行中' : '未运行'}`) +
    row(state.fastRunning, `快速穿透 · ${state.fastRunning ? '进行中' : '空闲'}`);
}

// ---------- 概览 ----------
function renderDashboard() {
  const s = state;
  const body = $('#dashboard-body');
  const pill = (on, txt) =>
    `<span class="pill ${on ? 'on' : 'off'}"><span class="dot ${on ? 'on' : 'off'}"></span>${txt}</span>`;

  let banner = '';
  if (!s.hasAuth) {
    banner = `<div class="banner">还没有配置 Cloudflare 认证信息，先到「设置」页填写 API Token 与 Account ID。
      <button class="btn sm" style="margin-left:auto" onclick="go('settings')">去配置</button></div>`;
  }

  const tunnelTitle = s.tunnelName ? s.tunnelName : '未创建隧道';
  const tunnelBtns = s.tunnelId
    ? (s.tunnelRunning
      ? `<button class="btn sm" id="d-tunnel-re">重启</button><button class="btn sm danger" id="d-tunnel-down">停止</button>`
      : `<button class="btn sm primary" id="d-tunnel-up">启动</button>`)
    : `<button class="btn sm primary" id="d-tunnel-create">创建隧道</button>`;

  const relayTitle = s.relayServer || '未配置服务器';
  const relayBtns = s.relayServer
    ? (s.relayRunning
      ? `<button class="btn sm" id="d-relay-re">重启</button><button class="btn sm danger" id="d-relay-down">停止</button>`
      : `<button class="btn sm primary" id="d-relay-up">启动</button>`)
    : `<button class="btn sm" id="d-relay-cfg">去配置</button>`;

  body.innerHTML = `
    ${banner}
    <div class="svc-cards">
      <div class="svc-card">
        <div class="svc-head">
          <div class="svc-icon cf">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M17.5 19a4.5 4.5 0 0 0 .42-8.98 6.5 6.5 0 0 0-12.66-1.4A5 5 0 0 0 6 18.5h11.5z"/></svg>
          </div>
          <div>
            <div class="svc-name">Cloudflare 隧道</div>
            <div class="svc-sub">HTTP / HTTPS 域名路由</div>
          </div>
          ${pill(s.tunnelRunning, s.tunnelRunning ? '运行中' : '已停止')}
        </div>
        <div class="svc-meta">
          <div><span class="k">隧道</span><span class="mono">${esc(tunnelTitle)}</span></div>
          ${s.tunnelRunning ? `<div><span class="k">进程 PID</span><span class="mono">${s.tunnelPid}</span></div>` : ''}
          <div><span class="k">路由数</span><span class="mono">${s.routeCount} 条</span></div>
        </div>
        <div class="svc-actions">${tunnelBtns}</div>
      </div>

      <div class="svc-card">
        <div class="svc-head">
          <div class="svc-icon frp">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 17l6-5-6-5M12 19h8"/></svg>
          </div>
          <div>
            <div class="svc-name">端口中继</div>
            <div class="svc-sub">TCP / UDP · frp</div>
          </div>
          ${pill(s.relayRunning, s.relayRunning ? '运行中' : '已停止')}
        </div>
        <div class="svc-meta">
          <div><span class="k">服务器</span><span class="mono">${esc(relayTitle)}</span></div>
          ${s.relayRunning ? `<div><span class="k">进程 PID</span><span class="mono">${s.relayPid}</span></div>` : ''}
          <div><span class="k">穿透规则</span><span class="mono">${s.ruleCount} 条</span></div>
        </div>
        <div class="svc-actions">${relayBtns}</div>
      </div>
    </div>

    <div class="stat-row">
      <div class="stat">
        <div class="stat-icon" style="background:var(--accent-dim);color:var(--accent)">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18z"/></svg>
        </div>
        <div><div class="num">${s.routeCount || 0}</div><div class="lbl">域名路由</div></div>
      </div>
      <div class="stat">
        <div class="stat-icon" style="background:rgba(74,144,226,.13);color:#4a90e2">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 17l6-5-6-5M12 19h8"/></svg>
        </div>
        <div><div class="num">${s.ruleCount || 0}</div><div class="lbl">穿透规则</div></div>
      </div>
      <div class="stat">
        <div class="stat-icon" style="background:var(--green-dim);color:var(--green)">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
        </div>
        <div><div class="num">${s.authCount || 0}</div><div class="lbl">密码保护</div></div>
      </div>
    </div>

    <div class="card">
      <div class="card-title">快捷操作</div>
      <div class="quick-list">
        <button class="quick-item" onclick="go('fast')">
          <span class="q-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg></span>
          <div>
            <div>快速穿透一个端口</div>
            <div class="hint">免域名生成临时公网地址，适合临时分享</div>
          </div>
          <span class="arrow">→</span>
        </button>
        <button class="quick-item" onclick="openAddRoute()">
          <span class="q-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M12 8v8M8 12h8"/></svg></span>
          <div>
            <div>添加域名路由</div>
            <div class="hint">把一个域名指向本地服务（需 Cloudflare 账户）</div>
          </div>
          <span class="arrow">→</span>
        </button>
        <button class="quick-item" onclick="go('diagnose');document.getElementById('btn-diagnose').click()">
          <span class="q-icon"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg></span>
          <div>
            <div>运行链路诊断</div>
            <div class="hint">检测本地服务、DNS 解析、云端记录与 HTTPS 可达性</div>
          </div>
          <span class="arrow">→</span>
        </button>
      </div>
    </div>
    <div class="hint" style="margin-top:10px">数据目录: <span class="mono">${esc(s.dataDir)}</span>（${s.portable ? '便携' : '默认'}模式）</div>
  `;

  // 服务卡按钮
  const on = (id, fn) => { const el = $(id); if (el) el.onclick = () => fn().catch(e => toast(String(e), true)); };
  on('#d-tunnel-up', async () => { await App.TunnelUp(); toast('隧道已启动'); refreshState(); });
  on('#d-tunnel-down', async () => { await App.TunnelDown(); toast('隧道已停止'); refreshState(); });
  on('#d-tunnel-re', async () => { await App.TunnelDown().catch(() => {}); await App.TunnelUp(); toast('隧道已重启'); refreshState(); });
  on('#d-tunnel-create', async () => { $('#tunnel-err').textContent = ''; openModal('modal-tunnel'); });
  on('#d-relay-up', async () => { await App.RelayUp(); toast('frpc 已启动'); refreshState(); });
  on('#d-relay-down', async () => { await App.RelayDown(); toast('frpc 已停止'); refreshState(); });
  on('#d-relay-re', async () => { await App.RelayDown().catch(() => {}); await App.RelayUp(); toast('frpc 已重启'); refreshState(); });
  on('#d-relay-cfg', async () => { openRelayServerModal(); });
}
window.go = go; // 供 innerHTML 的 onclick 使用

// ---------- 创建隧道 ----------
$('#btn-tunnel-submit').addEventListener('click', async () => {
  const name = $('#tunnel-name').value.trim();
  $('#tunnel-err').textContent = '';
  try {
    await App.CreateTunnel(name);
    closeModal('modal-tunnel');
    toast(`隧道 ${name || 'flare'} 创建成功`);
    refreshState();
  } catch (e) {
    $('#tunnel-err').textContent = String(e);
  }
});

// ---------- 快速穿透 ----------
let fastMode = 'cf';
$('#fast-modes').addEventListener('click', e => {
  const card = e.target.closest('.mode-card');
  if (!card) return;
  $$('#fast-modes .mode-card').forEach(c => c.classList.remove('sel'));
  card.classList.add('sel');
  fastMode = card.dataset.mode;
  $('#fast-proto-row').hidden = fastMode !== 'relay';
  $('#fast-auth-row').style.display = fastMode === 'relay' ? 'none' : 'flex';
  if (fastMode === 'relay') $('#fast-auth-extra').hidden = true;
});

$('#fast-auth-sw').addEventListener('click', function () {
  this.classList.toggle('on');
  $('#fast-auth-extra').hidden = !this.classList.contains('on');
});

$('#btn-fast-start').addEventListener('click', async () => {
  const port = parseInt($('#fast-port').value, 10);
  if (!port) { toast('请填写本地端口', true); return; }
  const authOn = $('#fast-auth-sw').classList.contains('on') && fastMode === 'cf';
  const user = authOn ? $('#fast-user').value.trim() : '';
  const pass = authOn ? $('#fast-pass').value : '';
  if (authOn && (!user || !pass)) { toast('启用密码保护需填写用户名和密码', true); return; }

  const btn = $('#btn-fast-start');
  btn.disabled = true;
  $('#fast-result').hidden = true;
  $('#fast-log').innerHTML = '';
  $('#fast-log-card').hidden = false;
  appendFastLog('正在启动，首次运行可能需要下载组件…');
  try {
    await App.StartFast(port, fastMode === 'relay', $('#fast-proto').value, user, pass);
  } catch (e) {
    $('#fast-log-card').hidden = true;
    toast(String(e), true);
    btn.disabled = false;
  }
});

$('#btn-fast-stop').addEventListener('click', async () => {
  try { await App.StopFast(); } catch (e) { toast(String(e), true); }
});

$('#btn-fast-copy').addEventListener('click', () => {
  const txt = $('#fast-url').textContent;
  navigator.clipboard?.writeText(txt).then(() => toast('链接已复制'));
});

function appendFastLog(line) {
  const el = $('#fast-log');
  const div = document.createElement('div');
  div.textContent = line;
  el.appendChild(div);
  el.scrollTop = el.scrollHeight;
}

function showFastResult(url, mapText) {
  $('#fast-result').className = 'fast-result';
  $('#fast-result').hidden = false;
  $('#fast-url').textContent = url;
  $('#fast-map').textContent = mapText;
  $('#btn-fast-start').disabled = false;
  $('#btn-fast-start').textContent = '重新穿透';
}

function hideFastResult(msg) {
  $('#fast-result').hidden = true;
  $('#fast-log-card').hidden = true;
  $('#btn-fast-start').disabled = false;
  $('#btn-fast-start').textContent = '开始穿透';
  if (msg) toast(msg, true);
  refreshState();
}

RT.EventsOn('fast:url', url => showFastResult(url, `→ http://localhost:${$('#fast-port').value} · 停止后地址失效`));
RT.EventsOn('fast:target', (target, proto) => showFastResult(`${proto}://${target}`, `→ localhost:${$('#fast-port').value} · 停止后端口关闭`));
RT.EventsOn('fast:log', line => appendFastLog(line));
RT.EventsOn('fast:exit', errMsg => hideFastResult(errMsg ? `快速穿透已结束: ${errMsg}` : ''));

// ---------- 域名路由 ----------
function renderRoutesSub() {
  const s = state;
  $('#routes-sub').innerHTML = s.tunnelId
    ? `Cloudflare 隧道 · <span class="mono">${esc(s.tunnelName)}</span> ` +
      `<span class="pill ${s.tunnelRunning ? 'on' : 'off'}" style="margin-left:6px">` +
      `<span class="dot ${s.tunnelRunning ? 'on' : 'off'}"></span>${s.tunnelRunning ? '运行中' : '已停止'}</span>`
    : '尚未创建隧道';
}

async function loadRoutes() {
  renderRoutesSub();
  const body = $('#routes-body');
  if (!state.tunnelId) {
    body.innerHTML = `<div class="empty">还没有隧道。先创建隧道，才能添加域名路由。<br>
      <button class="btn primary" onclick="document.getElementById('tunnel-err').textContent='';openModalById('modal-tunnel')">创建隧道</button></div>`;
    return;
  }
  let routes = [];
  try { routes = await App.ListRoutes() || []; } catch (e) { toast(String(e), true); }
  if (!routes.length) {
    body.innerHTML = `<div class="empty">尚无路由。<br>
      <button class="btn primary" onclick="openAddRoute()">+ 添加第一条路由</button></div>`;
    return;
  }
  body.innerHTML = `
    <div class="card" style="padding:6px 8px">
      <table>
        <thead><tr><th style="padding-left:12px">名称</th><th>域名</th><th>本地服务</th><th>密码保护</th><th></th></tr></thead>
        <tbody>
          ${routes.map(r => `
            <tr>
              <td><strong>${esc(r.name)}</strong></td>
              <td class="mono">${esc(r.hostname)}</td>
              <td class="mono dim">${esc(r.service)}</td>
              <td>${r.authUser ? `<span class="pill on">${esc(r.authUser)}</span>` : '<span class="faint">—</span>'}</td>
              <td style="text-align:right">
                <button class="btn sm ghost" style="color:var(--red)" data-del-route="${esc(r.name)}" data-host="${esc(r.hostname)}">删除</button>
              </td>
            </tr>`).join('')}
        </tbody>
      </table>
    </div>
    <div class="hint" style="margin-top:10px">路由变更会推送到云端 ingress；删除路由会一并清理对应 DNS 记录。${state.tunnelRunning ? '' : '隧道当前未运行，公网不可访问。'}</div>`;

  $$('#routes-body [data-del-route]').forEach(b => b.onclick = () => {
    confirmAction(`确定删除路由 <strong>${esc(b.dataset.delRoute)}</strong> 吗？`,
      `将同时删除云端 DNS 记录 ${b.dataset.host}`, '删除',
      async () => { await App.RemoveRoute(b.dataset.delRoute); toast('路由已删除'); refreshState(); loadRoutes(); });
  });
}
function openAddRoute() {
  if (!state.tunnelId) { toast('请先创建隧道', true); return; }
  $('#route-err').textContent = '';
  openModal('modal-route');
}
window.openAddRoute = openAddRoute;
window.openModalById = openModal;

$('#btn-add-route').addEventListener('click', openAddRoute);
$('#btn-sync-ingress').addEventListener('click', async () => {
  try { await App.SyncIngress(); toast('ingress 已同步'); }
  catch (e) { toast(String(e), true); }
});
$('#route-auth-sw').addEventListener('click', function () {
  this.classList.toggle('on');
  $('#route-auth-extra').hidden = !this.classList.contains('on');
});
$('#btn-route-submit').addEventListener('click', async () => {
  const domain = $('#route-domain').value.trim();
  const port = parseInt($('#route-port').value, 10);
  const name = $('#route-name').value.trim();
  const authOn = $('#route-auth-sw').classList.contains('on');
  const user = authOn ? $('#route-user').value.trim() : '';
  const pass = authOn ? $('#route-pass').value : '';
  const err = $('#route-err');
  if (!domain) { err.textContent = '域名不能为空'; return; }
  if (!port) { err.textContent = '端口必须是 1~65535 的数字'; return; }
  err.textContent = '';
  try {
    await App.AddRoute(domain, port, name, user, pass);
    closeModal('modal-route');
    toast(`路由已添加: ${domain}`);
    refreshState();
    if (currentPage === 'routes') loadRoutes();
  } catch (e) {
    err.textContent = String(e);
  }
});

// ---------- 端口中继 ----------
async function loadRelay() {
  const body = $('#relay-body');
  let info;
  try { info = await App.GetRelay(); } catch (e) { toast(String(e), true); return; }
  if (!info.server) {
    body.innerHTML = `<div class="empty">尚未配置中继服务器（frps）。<br>
      <button class="btn primary" id="relay-cfg-empty">配置服务器</button></div>`;
    $('#relay-cfg-empty').onclick = openRelayServerModal;
    return;
  }
  const rules = info.rules || [];
  body.innerHTML = `
    <div class="card" style="margin-bottom:14px;display:flex;align-items:center;gap:16px">
      <div style="flex:1">
        <div style="display:flex;align-items:center;gap:9px;flex-wrap:wrap">
          <span class="mono" style="font-size:14px;font-weight:600">${esc(info.server)}</span>
          <span class="pill ${info.running ? 'on' : 'off'}"><span class="dot ${info.running ? 'on' : 'off'}"></span>${info.running ? `frpc 运行中 · PID ${info.pid}` : 'frpc 未运行'}</span>
        </div>
        <div class="hint" style="margin-top:5px">${info.hasToken ? '令牌已设置' : '未设置令牌'} · 规则改动后需重启 frpc 生效</div>
      </div>
      ${info.running
        ? `<button class="btn sm" id="relay-re">重启</button><button class="btn sm danger" id="relay-down">停止</button>`
        : `<button class="btn sm primary" id="relay-up">启动</button>`}
    </div>
    <div id="relay-check-out"></div>
    ${rules.length ? `
    <div class="card" style="padding:6px 8px">
      <table>
        <thead><tr><th style="padding-left:12px">名称</th><th>协议</th><th>本地地址</th><th>公网地址</th><th></th></tr></thead>
        <tbody>
          ${rules.map(r => `
            <tr>
              <td><strong>${esc(r.name)}</strong></td>
              <td><span class="pill off" style="font-family:var(--mono)">${esc(r.proto)}</span></td>
              <td class="mono dim">${esc(r.source)}</td>
              <td class="mono">${esc(r.target)}</td>
              <td style="text-align:right">
                <button class="btn sm ghost" style="color:var(--red)" data-del-rule="${esc(r.name)}">删除</button>
              </td>
            </tr>`).join('')}
        </tbody>
      </table>
    </div>` : `<div class="empty">尚无穿透规则。<br><button class="btn primary" onclick="openModalById('modal-rule')">+ 添加第一条规则</button></div>`}
  `;

  const on = (id, fn) => { const el = $(id); if (el) el.onclick = () => fn().catch(e => toast(String(e), true)); };
  on('#relay-up', async () => { await App.RelayUp(); toast('frpc 已启动'); refreshState(); loadRelay(); });
  on('#relay-down', async () => { await App.RelayDown(); toast('frpc 已停止'); refreshState(); loadRelay(); });
  on('#relay-re', async () => { await App.RelayDown().catch(() => {}); await App.RelayUp(); toast('frpc 已重启'); refreshState(); loadRelay(); });
  $$('#relay-body [data-del-rule]').forEach(b => b.onclick = () => {
    confirmAction(`确定删除穿透规则 <strong>${esc(b.dataset.delRule)}</strong> 吗？`,
      info.running ? 'frpc 正在运行，删除后需重启生效' : '', '删除',
      async () => { await App.RemoveRelayRule(b.dataset.delRule); toast('规则已删除'); refreshState(); loadRelay(); });
  });
}

function openRelayServerModal() {
  App.GetRelay().then(info => { $('#relay-addr').value = info.server || ''; }).catch(() => {});
  $('#relay-err').textContent = '';
  openModal('modal-relay-server');
}
$('#btn-relay-server').addEventListener('click', openRelayServerModal);
$('#btn-add-rule').addEventListener('click', () => { $('#rule-err').textContent = ''; openModal('modal-rule'); });

$('#btn-relay-save').addEventListener('click', async () => {
  const addr = $('#relay-addr').value.trim();
  const token = $('#relay-token').value.trim();
  const err = $('#relay-err');
  err.textContent = '';
  try {
    await App.SaveRelayServer(addr, token);
    closeModal('modal-relay-server');
    toast('中继服务器已保存');
    refreshState();
    if (currentPage === 'relay') loadRelay();
  } catch (e) {
    err.textContent = String(e);
  }
});

$('#btn-rule-submit').addEventListener('click', async () => {
  const name = $('#rule-name').value.trim();
  const proto = $('#rule-proto').value;
  const ip = $('#rule-ip').value.trim();
  const local = parseInt($('#rule-local').value, 10);
  const remote = parseInt($('#rule-remote').value, 10) || 0;
  const force = $('#rule-force').classList.contains('on');
  const err = $('#rule-err');
  if (!name) { err.textContent = '规则名称不能为空'; return; }
  if (!local) { err.textContent = '本地端口必须是 1~65535 的数字'; return; }
  err.textContent = '';
  try {
    await App.AddRelayRule(name, proto, ip, local, remote, force);
    closeModal('modal-rule');
    toast(`规则已添加: ${name}`);
    refreshState();
    loadRelay();
  } catch (e) {
    err.textContent = String(e);
  }
});

$('#btn-relay-check').addEventListener('click', async () => {
  const out = $('#relay-check-out');
  out.innerHTML = `<div class="card"><div class="hint">正在探测服务器与每条规则…</div></div>`;
  try {
    const r = await App.CheckRelay();
    const rules = r.rules || [];
    out.innerHTML = `
      <div class="card" style="margin-bottom:14px">
        <div class="diag-item">
          <span class="diag-check ${r.server_ok ? 'ok' : 'fail'}">${r.server_ok ? '✓' : '✗'}</span>
          <div><div class="diag-name">frps 服务器</div><div class="diag-detail mono">${esc(r.server)}</div></div>
          <div class="diag-right">${r.server_ok ? `可达 ${r.server_latency_ms}ms` : '不可达'}<br>frpc ${r.frpc_running ? `PID ${r.frpc_pid}` : '未运行'}</div>
        </div>
        ${rules.map(rule => `
          <div class="diag-item">
            <span class="diag-check ${rule.skipped ? 'skip' : (rule.local_ok && (rule.remote_ok || !rule.remote_port) ? 'ok' : 'fail')}">${rule.skipped ? '–' : (rule.local_ok && (rule.remote_ok || !rule.remote_port) ? '✓' : '✗')}</span>
            <div><div class="diag-name">${esc(rule.name)} <span class="faint mono">${esc(rule.proto)}</span></div>
            <div class="diag-detail mono">:${rule.local_port} → :${rule.remote_port || '分配'}</div></div>
            <div class="diag-right">
              ${rule.skipped ? 'udp 不探测' : `本地 ${rule.local_ok ? '✓' : '✗ ' + esc(rule.local_err || '')} · 远端 ${rule.remote_port ? (rule.remote_ok ? `✓ ${rule.latency_ms}ms` : '✗ ' + esc(rule.remote_err || '')) : '由服务器分配'}`}
            </div>
          </div>`).join('')}
        <div style="padding:10px 4px 0" class="hint">结果: ${r.total} 条规则, ${r.passed} 通 / ${r.failed} 断${r.skipped ? ` / ${r.skipped} 未探测` : ''}</div>
      </div>`;
  } catch (e) {
    out.innerHTML = '';
    toast(String(e), true);
  }
});

// ---------- 诊断 ----------
$('#btn-diagnose').addEventListener('click', async () => {
  const body = $('#diagnose-body');
  body.innerHTML = `<div class="empty">正在诊断，网络探测需要几秒钟…</div>`;
  try {
    const r = await App.RunDiagnose();
    renderDiagnose(r);
  } catch (e) {
    body.innerHTML = `<div class="empty">诊断失败: ${esc(String(e))}</div>`;
  }
});

function renderDiagnose(r) {
  const c = r.cloudflared, a = r.api;
  const check = (ok, name, detail, right) => `
    <div class="diag-item">
      <span class="diag-check ${ok === null ? 'skip' : ok ? 'ok' : 'fail'}">${ok === null ? '–' : ok ? '✓' : '✗'}</span>
      <div><div class="diag-name">${esc(name)}</div><div class="diag-detail">${detail}</div></div>
      <div class="diag-right">${right || ''}</div>
    </div>`;

  let html = `<div class="card" style="margin-bottom:14px"><div class="card-title">基础环境</div>`;
  html += check(c.installed, 'cloudflared',
    c.installed ? `已安装 · <span class="mono">${esc(c.path)}</span>` : `未安装（${esc(c.path)}，启动隧道时会自动下载）`,
    c.installed ? `${esc(c.version || '版本未知')}<br>${c.running ? `PID ${c.pid} 运行中` : '未运行'}` : '');
  html += check(a.reachable ? (a.authed ? true : null) : false, 'Cloudflare API',
    a.reachable
      ? (a.authed ? `认证有效 · 账户下 ${a.zones} 个域名` : `可达但认证失败：${esc(a.err || '')}`)
      : esc(a.err || '不可达'),
    a.reachable ? `${a.latency_ms}ms` : '');
  html += `</div>`;

  const routes = r.routes || [];
  html += `<div class="card"><div class="card-title">路由链路（${r.total} 条 · ${r.passed} 通 / ${r.failed} 断）</div>`;
  if (!routes.length) {
    html += `<div class="hint" style="padding:4px">尚无路由</div>`;
  }
  for (const rt of routes) {
    const broken = !(rt.local_ok && rt.dns_ok && (!rt.record_checked || rt.record_ok));
    const parts = [];
    parts.push(rt.local_ok ? '本地 ✓' : `<span style="color:var(--red)">本地 ✗ ${esc(rt.local_err || '')}</span>`);
    parts.push(rt.dns_ok ? 'DNS ✓' : `<span style="color:var(--red)">DNS ✗ ${esc(rt.dns_err || '')}</span>`);
    if (rt.http_ok) parts.push(`HTTPS ${rt.http_status || '✓'}`);
    else if (rt.http_err) parts.push(`<span style="color:var(--red)">HTTPS ${esc(rt.http_err)}</span>`);
    if (rt.record_checked) parts.push(rt.record_ok ? '记录 ✓' : `<span style="color:var(--red)">记录 ✗ ${esc(rt.record_err || '')}</span>`);
    html += check(broken ? false : true, rt.name,
      `<span class="mono">${esc(rt.hostname)} → ${esc(rt.service)}</span>`,
      parts.join(' · '));
  }
  html += `</div>`;
  if (r.failed > 0 && !c.running) {
    html += `<div class="banner">cloudflared 未运行，公网访问必然失败，请先到「概览」启动隧道</div>`;
  }
  $('#diagnose-body').innerHTML = html;
}

// ---------- 日志 ----------
let logComp = 'flare';
let logFollowing = false;
const logViews = {};

$('#log-tabs').addEventListener('click', async e => {
  const tab = e.target.closest('.log-tab');
  if (!tab) return;
  if (logFollowing) { await App.UnfollowLog(logComp); logFollowing = false; $('#btn-log-follow').textContent = '实时跟踪'; }
  $$('#log-tabs .log-tab').forEach(t => t.classList.remove('active'));
  tab.classList.add('active');
  logComp = tab.dataset.comp;
  await loadLog();
});

async function loadLog() {
  $('#log-dir').textContent = '日志目录: ' + (await App.LogDir());
  const lines = await App.TailLog(logComp, 200);
  const view = $('#log-view');
  view.innerHTML = lines.map(logLineHtml).join('') || '<span class="faint">（暂无日志）</span>';
  view.scrollTop = view.scrollHeight;
}

function logLineHtml(line) {
  const e = esc(line);
  return e
    .replace(/^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[\d.]*)/, '<span class="t">$1</span>')
    .replace(/\b(DEBUG|INFO|WARN|ERROR|ERR|FATAL)\b/, m =>
      `<span class="${/ERR|FATAL/.test(m) ? 'lv-e' : /WARN/.test(m) ? 'lv-w' : 'lv-i'}">${m}</span>`);
}

$('#btn-log-follow').addEventListener('click', async () => {
  const btn = $('#btn-log-follow');
  if (!logFollowing) {
    await App.FollowLog(logComp);
    logFollowing = true;
    btn.textContent = '停止跟踪';
    $('#log-view').innerHTML += `<div class="faint">--- 实时跟踪 ${logComp} ---</div>`;
  } else {
    await App.UnfollowLog(logComp);
    logFollowing = false;
    btn.textContent = '实时跟踪';
  }
});

$('#btn-log-open').addEventListener('click', () => App.OpenPath($('#log-dir').textContent.replace('日志目录: ', '')));

for (const comp of ['flare', 'cloudflared', 'frpc', 'frps']) {
  RT.EventsOn('log:' + comp, line => {
    if (comp !== logComp) return;
    const view = $('#log-view');
    const nearBottom = view.scrollTop + view.clientHeight >= view.scrollHeight - 40;
    const div = document.createElement('div');
    div.innerHTML = logLineHtml(line);
    view.appendChild(div);
    if (nearBottom) view.scrollTop = view.scrollHeight;
  });
}

// ---------- 设置 ----------
async function loadSettings() {
  $('#auth-state').textContent = state.hasAuth ? '已配置 ✓' : '未配置';
  $('#set-datadir').textContent = state.dataDir;
  $('#set-mode').textContent = state.portable ? '便携模式（数据在程序旁边）' : '默认模式';

  const ti = $('#tunnel-info');
  if (state.tunnelId) {
    ti.innerHTML = `
      <div class="kv"><span class="k">名称</span><span class="v mono">${esc(state.tunnelName)}</span></div>
      <div class="kv"><span class="k">隧道 ID</span><span class="v mono">${esc(state.tunnelId)}</span></div>
      <div class="kv"><span class="k">运行状态</span><span class="v">${state.tunnelRunning ? `运行中 (PID ${state.tunnelPid})` : '已停止'}</span></div>`;
  } else {
    ti.innerHTML = `<div class="hint">还没有隧道。<button class="btn sm primary" style="margin-top:8px" onclick="openModalById('modal-tunnel')">创建隧道</button></div>`;
  }
}

$('#btn-save-auth').addEventListener('click', async () => {
  try {
    await App.SaveAuth($('#set-token').value, $('#set-account').value);
    toast('认证信息已保存');
    $('#set-token').value = '';
    refreshState().then(() => { $('#auth-state').textContent = state.hasAuth ? '已配置 ✓' : '未配置'; });
  } catch (e) { toast(String(e), true); }
});

$('#btn-list-tunnels').addEventListener('click', async () => {
  const box = $('#cloud-tunnels');
  box.innerHTML = '<div class="hint">加载中…</div>';
  try {
    const list = await App.ListTunnels();
    if (!list || !list.length) { box.innerHTML = '<div class="hint">云端没有任何隧道</div>'; return; }
    box.innerHTML = `<table><tbody>${list.map(t => `
      <tr>
        <td class="mono">${esc(t.id)}</td>
        <td>${esc(t.name)}${t.inUse ? ' <span class="pill on">使用中</span>' : ''}</td>
        <td style="text-align:right">${t.inUse ? '' : `<button class="btn sm ghost" style="color:var(--red)" data-del-tunnel="${esc(t.id)}" data-tname="${esc(t.name)}">删除</button>`}</td>
      </tr>`).join('')}</tbody></table>`;
    $$('#cloud-tunnels [data-del-tunnel]').forEach(b => b.onclick = () => {
      confirmAction(`确定删除云端孤儿隧道 <strong>${esc(b.dataset.tname)}</strong> 吗？`,
        `ID: ${b.dataset.delTunnel}，仅清理云端记录`, '删除',
        async () => { await App.DeleteCloudTunnel(b.dataset.delTunnel); toast('已删除'); $('#btn-list-tunnels').click(); });
    });
  } catch (e) {
    box.innerHTML = `<button class="btn sm" id="btn-list-tunnels">加载列表</button>`;
    $('#btn-list-tunnels').onclick = () => $('#btn-list-tunnels').click();
    toast(String(e), true);
  }
});

$('#btn-open-datadir').addEventListener('click', () => App.OpenPath(state.dataDir));
$('#btn-destroy').addEventListener('click', () => {
  confirmAction(`确定销毁隧道 <strong>${esc(state.tunnelName)}</strong> 吗？`,
    '将删除云端隧道、全部 DNS 记录与本地配置，不可恢复。隧道需先停止。', '销毁',
    async () => { await App.DestroyTunnel(); toast('隧道已销毁'); refreshState(); loadSettings(); });
});

$('#btn-refresh').addEventListener('click', refreshState);

// ---------- 启动 ----------
(async function init() {
  if (!App) { document.body.innerHTML = '<div class="empty">Wails 运行时未注入</div>'; return; }
  await refreshState();
  renderDashboard();
  setInterval(refreshState, 3000);
})();
