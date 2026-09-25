# 更新日志

## v0.1.0 — 2026-09-25（UTC）

首个发布版本。

### 功能

- 通过两级代理链访问 `bps.openai.com` 的 Basis Points Responses 接口，复用 CPA 中已有的 ChatGPT/Codex OAuth 凭据。
- 自带拨号器实现嵌套 CONNECT：`本进程 → 本地代理 → 远程轮换代理 → 上游`。宿主的 HTTP 传输只支持单一代理，无法在已建立的隧道内再发一次 CONNECT，因此必须由插件自行建立连接。
- 客户端工具协议适配：上游硬拒客户端传入的 `tools`，插件将客户端工具描述为自然语言目录注入 developer 消息，约定以原生 `run_officejs` 作为运输载体，拦截后还原为标准 `function_call` 交给客户端执行，并在后续回合回放原生身份。
- 认证解析同时产出原生 Codex 记录与本插件虚拟记录，使既有 Codex 模型继续走 CPA 原生执行器，只有本插件别名走 Basis Points。
- 模型目录响应钩子补齐本插件别名的上下文容量，数据只从同一目录的规范模型复制。
- 提供 `registry.json`，可通过 CPA 的 `plugins.store-sources` 接入插件商店，版本由 GitHub 最新 Release 决定。
- GitHub Actions 校验并构建 Linux / macOS / Windows 的 AMD64 与 ARM64 动态库，发布压缩包及 SHA-256 校验清单。

### 实测结论（写入默认值依据）

- 上游要求请求携带 `metadata`，其中 `task_id`、`turn_id`、`agent_iteration` 为必填，缺失返回 422。工具结果回合只递增 `agent_iteration`、保持 `turn_id` 恒定，否则上游会把已完成的计划当成新 turn 重新规划。
- 上游对 `model_selection` 的取值敏感：留空时按账号权限路由；`explicit` 在账号无该模型权限时返回 403；`auto` 被判定为非法值返回 422。因此默认不发送该字段。
- 上游无论 `stream` 取值如何都返回 SSE 分帧（`stream=false` 时 Content-Type 仍为 `text/event-stream`），插件按正文而非请求参数判断并统一解析。
- 可用模型由账号权限决定，默认上游模型为实测可用的保守选择。

### 安全

- 仓库内不含任何凭据。远程代理的用户名与口令通过配置或环境变量提供，并优先使用环境变量：
  `GPT365_LOCAL_PROXY`、`GPT365_REMOTE_PROXY`、`GPT365_REMOTE_USERNAME`、`GPT365_REMOTE_PASSWORD`、`GPT365_RESPONSES_URL`、`GPT365_UPSTREAM_MODEL`。
- 持久化设置文件时会剔除远程代理口令；错误信息中的 Bearer 令牌会被抹除。

### 已知限制

- 流式响应会先读完上游 SSE 再回放给客户端，不是 token 级实时转发。这是为了让工具调用能基于完整 item 做安全转换。
- 免费档账号限流较紧，连续请求可能触发 429。
- 本地测试与 Actions 打包不等于已完成真实上游联调；模型可用性仍受账号权限限制。
- 更换已加载的动态库后必须重启 CPA 才能生效。
