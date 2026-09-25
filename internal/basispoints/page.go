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
  @media (prefers-reduced-motion: reduce) { * { transition: none !important; } }
  @media (max-width: 640px) {
    .wrap { padding: 24px 16px 56px; }
    .stats { gap: 20px; }
    th:nth-child(4), td:nth-child(4) { display: none; }
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
      <li><span class="k">上游模型</span><span class="v" id="stat-model" style="font-size:14px;font-family:var(--mono)">–</span></li>
    </ul>
  </header>

  <div class="banner" id="key-banner" style="display:none">
    未找到管理密钥。请在 CPA 管理界面中打开本页面，或先登录管理后台。
  </div>

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

  var API = "/v0/management/plugins/gpt365";
  var state = { auths: [], key: null };

  function findKey() {
    // 同源部署下管理页把密钥存在 localStorage；键名在不同版本间有差异，
    // 因此逐个尝试常见键，任一命中即可。
    var names = ["management_key", "managementKey", "cpa_management_key", "management-key", "key", "apiKey"];
    for (var i = 0; i < names.length; i++) {
      try {
        var v = localStorage.getItem(names[i]);
        if (v && v.trim()) return v.trim();
      } catch (e) { /* 隐私模式等场景忽略 */ }
    }
    // 回退：扫描所有键，取形似密钥的值。
    try {
      for (var j = 0; j < localStorage.length; j++) {
        var k = localStorage.key(j);
        if (k && /key|token/i.test(k)) {
          var val = localStorage.getItem(k);
          if (val && val.trim().length >= 8) return val.trim();
        }
      }
    } catch (e) { /* 忽略 */ }
    return null;
  }

  function headers() {
    var h = { "Content-Type": "application/json" };
    if (state.key) h["Authorization"] = "Bearer " + state.key;
    return h;
  }

  function api(path, options) {
    options = options || {};
    options.headers = headers();
    return fetch(API + path, options).then(function (resp) {
      return resp.text().then(function (text) {
        var data = null;
        try { data = text ? JSON.parse(text) : null; } catch (e) { data = { raw: text }; }
        if (!resp.ok) {
          var msg = (data && (data.message || data.error || data.raw)) || ("HTTP " + resp.status);
          throw new Error(typeof msg === "string" ? msg : JSON.stringify(msg));
        }
        return data;
      });
    });
  }

  function el(id) { return document.getElementById(id); }

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
    state.key = findKey();
    if (!state.key) el("key-banner").style.display = "block";

    el("btn-import").addEventListener("click", doImport);
    el("btn-refresh").addEventListener("click", load);
    el("btn-delete-all").addEventListener("click", removeAll);
    el("btn-clear").addEventListener("click", function () {
      el("input").value = "";
      countLines();
      el("import-log").className = "log";
    });
    el("input").addEventListener("input", countLines);
    countLines();
    load();
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
