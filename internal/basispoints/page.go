package basispoints

// authPageHTML 是插件自带的凭据管理页面。
//
// 设计意图：这是一个运维工具页，不是营销页。信息密度优先，一行一条凭据，
// 导入区足够大以便直接粘贴一整批令牌。配色刻意保持低饱和，让「已过期」
// 与「即将过期」这类状态色成为页面上唯一的强信号。
//
// 页面通过同源 localStorage 读取管理密钥，再调用插件自己的
// /v0/management/... 路由完成导入与删除；资源页本身不做敏感操作。
const authPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GPT365 凭据</title>
<style>
  :root {
    --ink: #16181d;
    --ink-soft: #5a6070;
    --ink-faint: #8b91a1;
    --paper: #f7f7f5;
    --card: #ffffff;
    --line: #e2e3e0;
    --line-strong: #c9cbc6;
    --accent: #1f5f4b;
    --accent-soft: #e8f1ed;
    --warn: #8a5a00;
    --warn-soft: #fdf3e0;
    --danger: #9b2c2c;
    --danger-soft: #fbeaea;
    --mono: ui-monospace, "SF Mono", "Cascadia Mono", "Roboto Mono", Menlo, monospace;
    --sans: "Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC", sans-serif;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: var(--paper);
    color: var(--ink);
    font-family: var(--sans);
    font-size: 14px;
    line-height: 1.55;
    -webkit-font-smoothing: antialiased;
  }
  .wrap { max-width: 960px; margin: 0 auto; padding: 40px 24px 72px; }

  header { margin-bottom: 32px; }
  .eyebrow {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: .12em;
    text-transform: uppercase;
    color: var(--ink-faint);
    margin: 0 0 10px;
  }
  h1 { font-size: 26px; font-weight: 600; letter-spacing: -.02em; margin: 0 0 8px; }
  .lede { margin: 0; color: var(--ink-soft); max-width: 62ch; }

  .stats { display: flex; gap: 28px; margin: 28px 0 0; padding: 0; list-style: none; flex-wrap: wrap; }
  .stats li { display: flex; flex-direction: column; gap: 2px; }
  .stats .k { font-family: var(--mono); font-size: 11px; letter-spacing: .08em; text-transform: uppercase; color: var(--ink-faint); }
  .stats .v { font-size: 20px; font-weight: 600; font-variant-numeric: tabular-nums; }

  section { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 24px; margin-bottom: 20px; }
  h2 { font-size: 15px; font-weight: 600; margin: 0 0 4px; }
  .hint { color: var(--ink-soft); margin: 0 0 16px; font-size: 13px; }

  textarea {
    width: 100%;
    min-height: 148px;
    padding: 14px;
    font-family: var(--mono);
    font-size: 12.5px;
    line-height: 1.6;
    color: var(--ink);
    background: #fcfcfb;
    border: 1px solid var(--line-strong);
    border-radius: 8px;
    resize: vertical;
  }
  textarea:focus { outline: 2px solid var(--accent); outline-offset: -1px; border-color: var(--accent); }

  input[type="password"] {
    width: 100%;
    padding: 10px 14px;
    font-family: var(--mono);
    font-size: 13px;
    color: var(--ink);
    background: #fcfcfb;
    border: 1px solid var(--line-strong);
    border-radius: 8px;
  }
  input[type="password"]:focus { outline: 2px solid var(--accent); outline-offset: -1px; border-color: var(--accent); }

  .row { display: flex; align-items: center; gap: 12px; margin-top: 14px; flex-wrap: wrap; }
  button {
    font: inherit;
    font-weight: 500;
    padding: 9px 18px;
    border-radius: 7px;
    border: 1px solid transparent;
    cursor: pointer;
    transition: background .15s, border-color .15s;
  }
  button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .primary { background: var(--accent); color: #fff; }
  .primary:hover { background: #17493a; }
  .primary:disabled { background: var(--line-strong); cursor: not-allowed; }
  .ghost { background: transparent; border-color: var(--line-strong); color: var(--ink); }
  .ghost:hover { background: #f0f0ee; }
  .danger { background: transparent; border-color: var(--line-strong); color: var(--danger); }
  .danger:hover { background: var(--danger-soft); border-color: var(--danger); }

  .note { font-family: var(--mono); font-size: 12px; color: var(--ink-soft); }

  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 11px 10px; border-bottom: 1px solid var(--line); vertical-align: top; }
  th {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: .07em;
    text-transform: uppercase;
    color: var(--ink-faint);
    font-weight: 500;
    border-bottom-color: var(--line-strong);
  }
  td.mono { font-family: var(--mono); font-size: 12px; }
  tr:last-child td { border-bottom: none; }
  tbody tr:hover { background: #fafaf9; }

  .tag {
    display: inline-block;
    font-family: var(--mono);
    font-size: 11px;
    padding: 2px 7px;
    border-radius: 4px;
    background: var(--accent-soft);
    color: var(--accent);
    white-space: nowrap;
  }
  .tag.warn { background: var(--warn-soft); color: var(--warn); }
  .tag.danger { background: var(--danger-soft); color: var(--danger); }
  .tag.mute { background: #eeeeec; color: var(--ink-faint); }

  .empty { text-align: center; padding: 36px 16px; color: var(--ink-faint); }
  .empty strong { display: block; color: var(--ink-soft); font-weight: 500; margin-bottom: 4px; }

  .log { margin-top: 16px; display: none; }
  .log.show { display: block; }
  .log-item {
    font-family: var(--mono);
    font-size: 12px;
    padding: 8px 12px;
    border-radius: 6px;
    margin-bottom: 6px;
    border-left: 3px solid var(--line-strong);
    background: #fafaf9;
  }
  .log-item.ok { border-left-color: var(--accent); }
  .log-item.err { border-left-color: var(--danger); background: var(--danger-soft); }

  .banner {
    padding: 12px 14px;
    border-radius: 8px;
    background: var(--warn-soft);
    color: var(--warn);
    margin-bottom: 20px;
    font-size: 13px;
  }

  /* 代理池编辑区：行式布局，一行一条出口。 */
  .pool-row {
    display: grid;
    grid-template-columns: 1fr 2.2fr auto;
    gap: 8px;
    align-items: center;
    margin-bottom: 8px;
  }
  .pool-row input { width: 100%; }
  .pool-row .ghost { padding: 9px 12px; }
  .pool-head {
    display: grid;
    grid-template-columns: 1fr 2.2fr auto;
    gap: 8px;
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: .07em;
    text-transform: uppercase;
    color: var(--ink-faint);
    margin-bottom: 6px;
  }
  .pool-empty {
    padding: 20px;
    text-align: center;
    color: var(--ink-faint);
    border: 1px dashed var(--line-strong);
    border-radius: 8px;
    font-size: 13px;
  }
  .switch-row {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 14px;
    flex-wrap: wrap;
  }
  .switch-row label { display: flex; align-items: center; gap: 7px; cursor: pointer; }
  .switch-row input[type="checkbox"] { width: 16px; height: 16px; accent-color: var(--accent); }
  .field { display: flex; flex-direction: column; gap: 4px; }
  .field > span {
    font-family: var(--mono);
    font-size: 11px;
    letter-spacing: .06em;
    text-transform: uppercase;
    color: var(--ink-faint);
  }
  .field input, .field select {
    padding: 8px 10px;
    font-family: var(--mono);
    font-size: 12.5px;
    color: var(--ink);
    background: #fcfcfb;
    border: 1px solid var(--line-strong);
    border-radius: 7px;
  }
  .field input:focus, .field select:focus {
    outline: 2px solid var(--accent); outline-offset: -1px; border-color: var(--accent);
  }
  .grid-3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; }

  @media (prefers-reduced-motion: reduce) { * { transition: none !important; } }
  @media (max-width: 640px) {
    .wrap { padding: 24px 16px 56px; }
    .stats { gap: 20px; }
    th:nth-child(4), td:nth-child(4) { display: none; }
    .pool-row, .pool-head { grid-template-columns: 1fr; }
    .pool-head { display: none; }
    .grid-3 { grid-template-columns: 1fr; }
  }
</style>
</head>
<body>
<div class="wrap">
  <header>
    <p class="eyebrow">CLIProxyAPI · 插件 gpt365</p>
    <h1>Basis Points 凭据</h1>
    <p class="lede">
      在这里批量导入 ChatGPT 访问令牌。每条令牌会成为 CPA 的一个独立凭据，
      请求时按账号轮换使用。导入后立即生效，无需重启 CPA。
    </p>
    <ul class="stats">
      <li><span class="k">凭据</span><span class="v" id="stat-total">–</span></li>
      <li><span class="k">可用</span><span class="v" id="stat-active">–</span></li>
      <li><span class="k">出口代理</span><span class="v" id="stat-pool">–</span></li>
      <li><span class="k">粘性会话</span><span class="v" id="stat-sticky">–</span></li>
      <li><span class="k">上游模型</span><span class="v" id="stat-model" style="font-size:14px;font-family:var(--mono)">–</span></li>
    </ul>
  </header>

  <div class="banner" id="key-banner" style="display:none">
    <strong>需要管理密钥。</strong>在下方填入 CPA 的管理密钥后即可导入。
    也可以从管理界面跳转时带上 <code>?key=</code> 参数自动填入。
  </div>

  <section id="key-section">
    <h2>管理密钥</h2>
    <p class="hint">
      用于调用 CPA 的管理接口。若从 CPA 管理界面打开本页，通常会自动读取；
      读取不到时在此填写，只保存在当前标签页。
    </p>
    <input type="password" id="mgmt-key" placeholder="管理密钥" autocomplete="off" spellcheck="false">
    <div class="row">
      <button class="ghost" id="btn-key">保存并测试</button>
      <span class="note" id="key-state"></span>
    </div>
  </section>

  <section>
    <h2>代理池</h2>
    <p class="hint">
      为每个账号分配独立出口。多个账号共用一个出口 IP 时，上游容易把它们关联
      起来并触发风控；启用后每个账号固定走自己的出口。出口只替换远程代理，
      本地代理那一跳保持不变。
    </p>

    <div class="switch-row">
      <label><input type="checkbox" id="pool-enabled"> 启用代理池</label>
      <label><input type="checkbox" id="pool-strict"> 严格模式</label>
      <span class="note" title="严格模式下没有可用出口会让请求直接失败，而不是回退到全局代理链">
        严格模式：无出口时直接失败
      </span>
    </div>

    <div class="pool-head">
      <span>名称</span><span>代理地址</span><span></span>
    </div>
    <div id="pool-rows"></div>
    <div class="row">
      <button class="ghost" id="btn-add-proxy">添加出口</button>
      <span class="note" id="pool-count"></span>
    </div>

    <div class="grid-3" style="margin-top:16px">
      <label class="field">
        <span>分配策略</span>
        <select id="pool-strategy">
          <option value="hash">hash — 按账号稳定散列</option>
          <option value="sticky">sticky — 按导入顺序</option>
        </select>
      </label>
      <label class="field">
        <span>会话粘性</span>
        <select id="sticky-enabled">
          <option value="on">开启 — 同会话固定账号</option>
          <option value="off">关闭</option>
        </select>
      </label>
      <label class="field">
        <span>绑定有效期（秒）</span>
        <input type="number" id="sticky-ttl" min="60" max="604800" step="60" value="3600">
      </label>
    </div>

    <div class="row">
      <button class="primary" id="btn-save-pool">保存配置</button>
      <button class="ghost" id="btn-rebind">重新分配出口</button>
      <span class="note" id="pool-state"></span>
    </div>
    <div class="log" id="pool-log"></div>
  </section>

  <section>
    <h2>导入令牌</h2>
    <p class="hint">
      每行一条。可直接粘贴纯 access_token、<code>access_token: xxx</code> 形式，
      或完整 JSON。空行与以 # 开头的行会被忽略。
    </p>
    <textarea id="input" spellcheck="false" placeholder="eyJhbGciOiJSUzI1NiIs…
eyJhbGciOiJSUzI1NiIs…
# 支持一次粘贴多行"></textarea>
    <div class="row">
      <button class="primary" id="btn-import">导入</button>
      <button class="ghost" id="btn-clear">清空</button>
      <span class="note" id="input-count"></span>
    </div>
    <div class="log" id="import-log"></div>
  </section>

  <section>
    <div class="row" style="margin-top:0;justify-content:space-between">
      <div>
        <h2>已有凭据</h2>
        <p class="hint" style="margin-bottom:0">只列出本插件导入的凭据。</p>
      </div>
      <div class="row" style="margin-top:0">
        <button class="ghost" id="btn-refresh">刷新</button>
        <button class="danger" id="btn-delete-all">全部删除</button>
      </div>
    </div>
    <div id="list" style="margin-top:16px"></div>
  </section>
</div>

<script>
(function () {
  "use strict";

  // API 基址。
  //
  // 用绝对路径（以 / 开头）而不是相对路径：资源页挂在
  // /v0/resource/plugins/gpt365/panel 下，若用相对路径，浏览器会以当前
  // 目录为基准解析，在带尾斜杠或子路径的部署下会打到错误地址，表现为
  // 请求根本发不出去（Failed to fetch）。
  var API = "/v0/management/plugins/gpt365";
  var state = { auths: [] };

  // ---- 管理密钥获取 ----------------------------------------------------------
  //
  // 资源页本身不做鉴权，所有敏感操作都走 /v0/management/... 并需要管理密钥。
  // 密钥按以下顺序获取，与 codearts 插件保持一致的做法：
  //   1. 用户在本页输入框手动填写（存 sessionStorage，仅当前标签页）
  //   2. URL 上的 ?key= 参数（读取后立即从地址栏抹掉）
  //   3. 同源管理面板写入 localStorage 的 "cli-proxy-auth"
  //   4. 本标签页 sessionStorage 里的历史值
  //
  // CPA 管理面板把状态存在 localStorage 的 cli-proxy-auth 键下，值是 JSON
  // （形如 {"state":{"managementKey":"..."}}），部分版本还会加 enc::v1:: 前缀
  // 做异或混淆。只按固定键名读取，不猜键名。
  var PANEL_STORE = "cli-proxy-auth";
  var ENC_PREFIX = "enc::v1::";
  var SECRET_SALT = "cli-proxy-api-webui::secure-storage";
  var SS_KEY = "gpt365-mgmt-key";

  function encBytes(text) { return new TextEncoder().encode(text); }

  function xorBytes(data, key) {
    var out = new Uint8Array(data.length);
    for (var i = 0; i < data.length; i++) out[i] = data[i] ^ key[i % key.length];
    return out;
  }

  function base64Bytes(text) {
    var bin = window.atob(text);
    var out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  function deobfuscate(value) {
    if (!value || value.indexOf(ENC_PREFIX) !== 0) return value;
    try {
      var key = encBytes(SECRET_SALT + "|" + window.location.host + "|" + window.navigator.userAgent);
      return new TextDecoder().decode(xorBytes(base64Bytes(value.slice(ENC_PREFIX.length)), key));
    } catch (e) { return value; }
  }

  function recall(key) {
    try { return window.sessionStorage.getItem(key); } catch (e) { return null; }
  }

  function store(key, value) {
    try { window.sessionStorage.setItem(key, value); } catch (e) {}
  }

  // embeddedKey 读取同源管理面板保存的密钥。
  //
  // CPA 面板把状态存在 localStorage 的 cli-proxy-auth 键下。不同版本字段名
  // 略有差异，因此按已知名依次尝试，再退化为扫描所有字符串字段。
  function embeddedKey() {
    var raw;
    try { raw = window.localStorage.getItem(PANEL_STORE); } catch (e) { return null; }
    if (!raw) return null;

    var parsed = null;
    try { parsed = JSON.parse(deobfuscate(raw)); } catch (e) { parsed = null; }

    if (parsed && typeof parsed === "object") {
      var found = findKeyDeep(parsed, 0);
      if (found) return found;
    }
    // 不是 JSON 时按裸值处理。
    var trimmed = String(raw).trim();
    if (trimmed && trimmed.charAt(0) !== "{" && trimmed.charAt(0) !== "[") return trimmed;
    return null;
  }

  // findKeyDeep 在对象里查找形似管理密钥的字符串字段。
  //
  // 已知字段名优先；都没有时扫描一层，取长度足够的字符串值。
  // 限制深度与遍历量，避免在异常数据上卡住。
  function findKeyDeep(node, depth) {
    if (!node || typeof node !== "object" || depth > 4) return null;
    var known = ["managementKey", "management_key", "key", "apiKey", "api_key", "secretKey", "secret_key"];
    for (var i = 0; i < known.length; i++) {
      var v = node[known[i]];
      if (typeof v === "string" && v.trim()) return v.trim();
    }
    var keys = Object.keys(node);
    for (var j = 0; j < keys.length && j < 40; j++) {
      var child = node[keys[j]];
      if (child && typeof child === "object") {
        var nested = findKeyDeep(child, depth + 1);
        if (nested) return nested;
      }
    }
    // 最后兜底：扫描一层字符串值。
    for (var k = 0; k < keys.length && k < 40; k++) {
      var value = node[keys[k]];
      if (typeof value === "string" && value.trim().length >= 12) return value.trim();
    }
    return null;
  }

  // urlKey 读取 ?key= 参数，并立即从地址栏移除以免留在浏览历史里。
  var initialURLKey = (function () {
    var query = new URLSearchParams(window.location.search);
    if (!query.has("key")) return null;
    var value = (query.get("key") || "").trim();
    query.delete("key");
    var rest = query.toString();
    try {
      window.history.replaceState(null, "", window.location.pathname +
        (rest ? "?" + rest : "") + window.location.hash);
    } catch (e) { /* 忽略 */ }
    return value || null;
  })();

  function adminKey() {
    var input = document.getElementById("mgmt-key");
    var entered = input ? input.value.trim() : "";
    if (entered) { store(SS_KEY, entered); return entered; }
    if (initialURLKey) { store(SS_KEY, initialURLKey); return initialURLKey; }
    var embedded = embeddedKey();
    if (embedded) return embedded;
    return recall(SS_KEY) || "";
  }

  function headers() {
    var h = { "Content-Type": "application/json" };
    var k = adminKey();
    // 同时带上两种头：CPA 不同版本的鉴权中间件接受的名称不同。
    if (k) { h["Authorization"] = "Bearer " + k; h["X-API-Key"] = k; }
    return h;
  }

  // request 是统一的请求封装，把三类失败区分开：
  //
  //   1. fetch 本身抛异常（Failed to fetch）—— 请求没发出去
  //   2. HTTP 4xx/5xx                      —— 请求到了，被拒绝
  //   3. 响应不是合法 JSON                  —— 到了但不是预期接口
  //
  // 三者原因完全不同，混在一起会让人无从下手。这里分别给出可操作的提示。
  function request(path, options) {
    options = options || {};
    options.headers = headers();
    return fetch(path, options).then(function (resp) {
      return resp.text().then(function (text) {
        var data = null;
        try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }

        if (!resp.ok) {
          if (resp.status === 401) {
            throw new Error("管理密钥无效或未填写。请在页面上方填入 CPA 的管理密钥。");
          }
          if (resp.status === 403) {
            throw new Error("管理密钥被拒绝（403）。请确认填入的是管理密钥，" +
              "而不是客户端 API Key。");
          }
          if (resp.status === 404) {
            throw new Error("接口不存在（404）。可能是 CPA 版本不支持该管理端点，" +
              "或插件未正确加载。");
          }
          var msg = (data && (data.message || data.error)) ||
            (text ? text.slice(0, 300) : ("HTTP " + resp.status));
          throw new Error(typeof msg === "string" ? msg : JSON.stringify(msg));
        }
        if (text && data === null) {
          throw new Error("响应不是合法 JSON。请确认请求命中的是 CPA 管理接口。");
        }
        return data;
      });
    }).catch(function (err) {
      // fetch 在网络层失败时抛 TypeError，消息通常是 "Failed to fetch"。
      if (err instanceof TypeError || /Failed to fetch|NetworkError|load failed/i.test(err.message || "")) {
        throw new Error("请求无法发出（网络层失败）。请检查：" +
          "① 页面是否与 CPA 同源（同一地址与端口）；" +
          "② CPA 是否仍在运行；" +
          "③ 是否有反向代理或浏览器插件拦截了该请求。");
      }
      throw err;
    });
  }

  // api 走插件自己的管理路由。
  function api(path, options) {
    return request(API + path, options);
  }

  function el(id) { return document.getElementById(id); }

  // ---- 代理池编辑 ------------------------------------------------------------
  //
  // 配置的权威在宿主：保存时调用宿主的插件配置接口
  // PATCH /v0/management/plugins/gpt365/config，由宿主写盘并触发热重载。
  // 插件自己的路由不能覆盖宿主已有路由，因此这里直接调宿主接口。
  //
  // 口令处理：接口只回传主机名，不回传口令。编辑时口令框留空表示保持原值；
  // 若用户改了地址但没填口令，则按「无凭据地址」保存。

  var poolState = { entries: [], original: [] };

  function newPoolRow(name, url, hasCreds) {
    var row = document.createElement("div");
    row.className = "pool-row";

    var nameInput = document.createElement("input");
    nameInput.type = "text";
    nameInput.placeholder = "jp-1";
    nameInput.value = name || "";

    var urlInput = document.createElement("input");
    urlInput.type = "text";
    urlInput.placeholder = "http://user:pass@host:10000";
    urlInput.value = url || "";

    var remove = document.createElement("button");
    remove.className = "ghost";
    remove.type = "button";
    remove.textContent = "移除";
    remove.addEventListener("click", function () {
      row.parentNode.removeChild(row);
      updatePoolCount();
    });

    if (hasCreds) {
      urlInput.placeholder = "留空保持不变（已配置凭据）";
    }

    row.appendChild(nameInput);
    row.appendChild(urlInput);
    row.appendChild(remove);
    return row;
  }

  function updatePoolCount() {
    var rows = el("pool-rows").querySelectorAll(".pool-row");
    el("pool-count").textContent = rows.length ? (rows.length + " 个出口") : "";
    var empty = el("pool-rows").querySelector(".pool-empty");
    if (rows.length === 0 && !empty) {
      var hint = document.createElement("div");
      hint.className = "pool-empty";
      hint.textContent = "还没有配置出口。未启用时所有账号共用全局代理链。";
      el("pool-rows").appendChild(hint);
    } else if (rows.length > 0 && empty) {
      el("pool-rows").removeChild(empty);
    }
  }

  function renderPool(settings) {
    var pool = (settings && settings.proxy_pool) || {};
    var sticky = (settings && settings.sticky_session) || {};

    el("pool-enabled").checked = !!pool.enabled;
    el("pool-strict").checked = !!pool.strict;
    el("pool-strategy").value = pool.strategy || "hash";
    el("sticky-enabled").value = sticky.enabled ? "on" : "off";
    el("sticky-ttl").value = sticky.ttl_seconds || 3600;

    poolState.original = pool.entries || [];
    el("pool-rows").innerHTML = "";
    poolState.original.forEach(function (entry) {
      // 已有条目只显示主机名，口令不回传；地址框留空即保持原值。
      el("pool-rows").appendChild(newPoolRow(entry.name, "", entry.has_credentials));
    });
    updatePoolCount();
  }

  // collectPoolEntries 汇总编辑区的出口列表。
  //
  // 返回 { entries, keepNames }：keepNames 是地址留空、需要沿用原值的条目名，
  // 它们由后端按原名保留原地址，避免把口令回传到浏览器再传回来。
  function collectPoolEntries() {
    var rows = el("pool-rows").querySelectorAll(".pool-row");
    var entries = [];
    var keepNames = [];
    Array.prototype.forEach.call(rows, function (row) {
      var inputs = row.querySelectorAll("input");
      var name = inputs[0].value.trim();
      var url = inputs[1].value.trim();
      if (!name && !url) return;
      if (!url) {
        if (name) keepNames.push(name);
        return;
      }
      entries.push({ name: name, url: url });
    });
    return { entries: entries, keepNames: keepNames };
  }

  function showPoolLog(items) {
    var log = el("pool-log");
    log.innerHTML = items.map(function (it) {
      return '<div class="log-item ' + (it.ok ? "ok" : "err") + '">' +
        esc((it.ok ? "✓ " : "✕ ") + it.text) + "</div>";
    }).join("");
    log.className = "log show";
  }

  function savePool() {
    var collected = collectPoolEntries();
    var payload = {
      proxy_pool: {
        enabled: el("pool-enabled").checked,
        strict: el("pool-strict").checked,
        strategy: el("pool-strategy").value,
        entries: collected.entries,
        // 地址留空的条目按原名保留原值，由后端合并。
        keep_entries: collected.keepNames
      },
      sticky_session: {
        enabled: el("sticky-enabled").value === "on",
        ttl_seconds: parseInt(el("sticky-ttl").value, 10) || 3600
      }
    };

    var btn = el("btn-save-pool");
    btn.disabled = true;
    btn.textContent = "保存中…";
    el("pool-state").textContent = "";

    // 保存到宿主的插件配置：由宿主写盘并触发热重载。
    // 用统一封装以获得可区分的错误提示（网络层 / 401 / 403 / 404）。
    request("/v0/management/plugins/gpt365/config", {
      method: "PATCH",
      body: JSON.stringify(payload)
    }).then(function () {
      showPoolLog([{ ok: true, text: "已保存，宿主已重新加载插件配置" }]);
      el("pool-state").textContent = "已保存";
      return loadSettings();
    }).catch(function (err) {
      showPoolLog([{ ok: false, text: "保存失败：" + err.message }]);
    }).then(function () {
      btn.disabled = false;
      btn.textContent = "保存配置";
    });
  }

  function rebind() {
    var btn = el("btn-rebind");
    btn.disabled = true;
    btn.textContent = "分配中…";
    api("/rebind", { method: "POST", body: JSON.stringify({ force: true }) })
      .then(function (data) {
        showPoolLog([{
          ok: true,
          text: "重新分配完成：成功 " + (data.rebound || 0) +
            "，跳过 " + (data.skipped || 0) + "，失败 " + (data.failed || 0)
        }]);
        return loadSettings();
      })
      .catch(function (err) {
        showPoolLog([{ ok: false, text: "分配失败：" + err.message }]);
      })
      .then(function () {
        btn.disabled = false;
        btn.textContent = "重新分配出口";
      });
  }

  function loadSettings() {
    return api("/settings").then(function (data) {
      renderPool(data);
    }).catch(function (err) {
      el("pool-state").textContent = err.message;
    });
  }

  function countLines() {
    var text = el("input").value;
    var n = text.split(/\r?\n/).filter(function (l) {
      var t = l.trim();
      return t && t.charAt(0) !== "#";
    }).length;
    el("input-count").textContent = n ? ("待导入 " + n + " 条") : "";
  }

  function render() {
    var list = el("list");
    var auths = state.auths;
    el("stat-total").textContent = auths.length;
    var active = auths.filter(function (a) { return !a.disabled && !a.expired; }).length;
    el("stat-active").textContent = active;

    // 出口代理状态：显示池中条目数，未启用时说明共用全局链路。
    var pool = state.pool || {};
    if (pool.enabled) {
      el("stat-pool").textContent = pool.count ? (pool.count + " 个") : "空池";
      el("stat-pool").title = "策略 " + (pool.strategy || "hash") +
        (pool.strict ? "（严格模式）" : "");
    } else {
      el("stat-pool").textContent = "共用";
      el("stat-pool").title = "未启用代理池，所有账号共用全局代理链";
    }

    // 粘性会话：显示当前活跃绑定数。
    var sticky = state.sticky || {};
    el("stat-sticky").textContent = sticky.enabled ? (sticky.active || 0) + " 个" : "关闭";
    el("stat-sticky").title = sticky.enabled
      ? ("TTL " + (sticky.ttl_seconds || 0) + " 秒")
      : "会话粘性已关闭，同一会话可能切换账号";

    if (!auths.length) {
      list.innerHTML = '<div class="empty"><strong>还没有导入任何凭据</strong>在上方粘贴令牌即可开始。</div>';
      return;
    }

    var rows = auths.map(function (a) {
      var tag;
      if (a.disabled) tag = '<span class="tag mute">已停用</span>';
      else if (a.expired) tag = '<span class="tag danger">已过期</span>';
      else tag = '<span class="tag">有效</span>';

      return "<tr>" +
        '<td class="mono">' + esc(a.name) + "</td>" +
        "<td>" + esc(a.label || a.email || "—") + "</td>" +
        '<td class="mono">' + esc(a.account_id || "—") + "</td>" +
        '<td class="mono">' + esc(a.expires_at || "—") + "</td>" +
        "<td>" + tag + "</td>" +
        '<td style="text-align:right"><button class="danger" data-name="' + esc(a.name) + '">删除</button></td>' +
        "</tr>";
    }).join("");

    list.innerHTML = "<table><thead><tr>" +
      "<th>文件</th><th>标识</th><th>账号</th><th>过期</th><th>状态</th><th></th>" +
      "</tr></thead><tbody>" + rows + "</tbody></table>";

    Array.prototype.forEach.call(list.querySelectorAll("button[data-name]"), function (btn) {
      btn.addEventListener("click", function () { removeOne(btn.getAttribute("data-name")); });
    });
  }

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function showLog(items) {
    var log = el("import-log");
    log.innerHTML = items.map(function (it) {
      var cls = it.ok ? "log-item ok" : "log-item err";
      var text = it.ok
        ? "✓ " + it.name + (it.account_id ? "  ·  " + it.account_id : "")
        : "✕ " + it.name + "  ·  " + it.error;
      return '<div class="' + cls + '">' + esc(text) + "</div>";
    }).join("");
    log.className = "log show";
  }

  function load() {
    return api("/auths").then(function (data) {
      state.auths = (data && data.auths) || [];
      state.pool = (data && data.proxy_pool) || {};
      state.sticky = (data && data.sticky_session) || {};
      if (data && data.upstream) el("stat-model").textContent = data.upstream;
      render();
    }).catch(function (err) {
      el("list").innerHTML = '<div class="empty"><strong>读取失败</strong>' + esc(err.message) + "</div>";
    });
  }

  function doImport() {
    var text = el("input").value.trim();
    if (!text) return;
    var btn = el("btn-import");
    btn.disabled = true;
    btn.textContent = "导入中…";

    api("/import", { method: "POST", body: JSON.stringify({ text: text }) })
      .then(function (data) {
        showLog((data && data.results) || []);
        if (data && data.imported > 0) el("input").value = "";
        countLines();
        return load();
      })
      .catch(function (err) {
        showLog([{ ok: false, name: "导入", error: err.message }]);
      })
      .then(function () {
        btn.disabled = false;
        btn.textContent = "导入";
      });
  }

  function removeOne(name) {
    if (!confirm("删除凭据 " + name + "？此操作不可撤销。")) return;
    api("/delete", { method: "POST", body: JSON.stringify({ names: [name] }) })
      .then(function () { return load(); })
      .catch(function (err) { alert("删除失败：" + err.message); });
  }

  function removeAll() {
    if (!state.auths.length) return;
    if (!confirm("删除全部 " + state.auths.length + " 条凭据？此操作不可撤销。")) return;
    api("/delete", { method: "POST", body: JSON.stringify({ all: true }) })
      .then(function () { return load(); })
      .catch(function (err) { alert("删除失败：" + err.message); });
  }

  function init() {
    var keyInput = el("mgmt-key");
    var auto = adminKey();
    if (auto) keyInput.value = auto;
    if (!auto) el("key-banner").style.display = "block";

    el("btn-key").addEventListener("click", function () {
      el("key-state").textContent = "检测中…";
      load().then(function () {
        el("key-state").textContent = "密钥可用";
        el("key-banner").style.display = "none";
      }).catch(function (err) {
        el("key-state").textContent = err.message;
      });
    });
    keyInput.addEventListener("keydown", function (event) {
      if (event.key === "Enter") el("btn-key").click();
    });

    el("btn-import").addEventListener("click", doImport);
    el("btn-refresh").addEventListener("click", load);
    el("btn-delete-all").addEventListener("click", removeAll);
    el("btn-add-proxy").addEventListener("click", function () {
      var empty = el("pool-rows").querySelector(".pool-empty");
      if (empty) el("pool-rows").removeChild(empty);
      el("pool-rows").appendChild(newPoolRow("", "", false));
      updatePoolCount();
    });
    el("btn-save-pool").addEventListener("click", savePool);
    el("btn-rebind").addEventListener("click", rebind);
    el("btn-clear").addEventListener("click", function () {
      el("input").value = "";
      countLines();
      el("import-log").className = "log";
    });
    el("input").addEventListener("input", countLines);
    countLines();
    loadSettings();
    load().then(function () {
      if (auto) {
        el("key-state").textContent = "已自动读取密钥";
        el("key-banner").style.display = "none";
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
</script>
</body>
</html>
`
