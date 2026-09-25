# GPT365 Basis Points（CPA 原生插件）

将 `bps.openai.com` 的 Basis Points Responses 接口接入 CLIProxyAPI（CPA），
复用 CPA 中已有的 ChatGPT/Codex OAuth 凭据，并内置**两级代理链**支持。

## 通过 CPA 插件商店安装（推荐）

在管理界面的「第三方插件源 → 插件源 registry URL」中添加本仓库的 registry 地址：

```text
https://raw.githubusercontent.com/zyxzjyzjj/cpa-gpt365-plugin/main/registry.json
```

也可合并到 CPA 宿主配置（`config.yaml`，与下方插件配置共用同一个 `plugins` 节点）：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/zyxzjyzjj/cpa-gpt365-plugin/main/registry.json
```

保留已有插件源，不要整体覆盖原有 `plugins` 配置。本源使用宿主原生的
`github-release` 安装方式，最新版本以本仓库已发布的 GitHub Release 为准。
CPA 会按运行平台下载 `gpt365_<version>_<goos>_<goarch>.zip`，并用同一 Release 的
`checksums.txt` 校验。

发行包覆盖 Linux、macOS、Windows 的 AMD64 与 ARM64。
**更新已加载的动态库后必须重启 CPA**，新代码与 OAuth 认证解析才会生效。

## 手动安装

1. 构建或下载动态库，得到 `gpt365.dll`（Linux 为 `.so`，macOS 为 `.dylib`）。
2. 放入 CPA 插件目录，按平台优先级查找：
   - `plugins/<GOOS>/<GOARCH>/gpt365.dll`
   - `plugins/gpt365.dll`
3. 将 `config.example.yaml` 中的 `plugins` 节点合并进 CPA 的 `config.yaml`。
4. 重启 CPA。

CPA 必须是 CGO 构建（管理接口响应头会标明动态库插件支持）。

## 代理池：每个账号一个出口

多个账号共用一个出口 IP 时，上游很容易把它们关联起来并触发风控。启用代理池后，**每个账号固定分配一个出口**，且分配结果写进凭据，CPA 重启后依然有效。

```yaml
proxy_pool:
  enabled: true
  entries:
    - name: "jp-1"
      url: "http://USER-zone-custom-region-JP:password@global.example.com:10000"
    - name: "jp-2"
      url: "http://USER-zone-custom-region-JP2:password@global.example.com:10000"
  strategy: hash    # hash（默认）按账号稳定散列；sticky 按导入顺序
  strict: false     # 为真时无可用出口直接失败，不静默回退
```

与 `proxy_chain` 的分工：

| 配置 | 决定什么 |
| --- | --- |
| `proxy_chain` | **怎么连出去**：本机 → 本地代理 → 远程代理 |
| `proxy_pool` | **用哪个远程代理**：每个账号分配池中一条 |

账号代理**只替换远程段**，本地代理那一跳保持不变（否则本机无法到达远程代理）。账号代理自带凭据时，全局代理的用户名口令会被清空，避免把凭据发给别的出口。

`strategy: hash` 用账号 ID 做稳定散列，条目顺序不影响结果 —— 同一账号永远拿到同一出口，账号增减也不会打乱既有分配。

## 会话粘性：同一会话固定一个账号

上游按会话组织多轮对话。若同一会话的不同轮次落到不同账号，上游会看到割裂的上下文，既影响效果，也因「同会话在不同账号/IP 间跳跃」更容易触发风控。

```yaml
sticky_session:
  enabled: true
  ttl_seconds: 3600   # 绑定存活时间
  max_entries: 4096   # 绑定表容量，超出淘汰最旧
```

实现方式是通过 CPA 的 **scheduler 能力**参与选号：命中绑定则返回该账号，未命中则交给内置调度器（`Handled: false`），**不与宿主自身的负载均衡冲突**。

会话键按以下顺序推导：请求元数据 → 请求头（`X-Session-Id`、`Session-Id`、`Conversation-Id`、`Prompt-Cache-Key` 等）→ 请求体中的 `prompt_cache_key` / `session_id`。**不使用消息内容做键** —— 内容随轮次变化，用它会让每轮都算新会话，粘性形同虚设。

绑定的账号被删除或停用时，会自动回退到内置调度器，不会把请求钉死。

## 导入令牌

插件有**独立的凭据提供者**（provider 标识 `gpt365`），不依赖 Codex 凭据。

### 方式一：管理页面（推荐）

打开：

```text
http://localhost:8317/v0/resource/plugins/gpt365/panel
```

> 路径必须以 `/panel` 结尾。资源页注册在具体路径段下，宿主不会为插件保留根路径。

在页面里直接粘贴令牌，一行一条，点「导入」。页面同时显示每条凭据的账号、过期时间与状态，支持单条或全部删除。

页面调用管理接口需要管理密钥，按以下顺序自动获取：

1. 页面顶部输入框手动填写（保存在当前标签页）
2. URL 上的 `?key=<管理密钥>` 参数
3. 同源 CPA 管理面板写入 `localStorage` 的 `cli-proxy-auth`（含加密值解码）
4. 当前标签页的 `sessionStorage`

若从 CPA 管理界面跳转过来，通常会自动读取成功；否则在页面顶部填入管理密钥即可。

### 方式二：管理接口

```bash
curl -X POST http://localhost:8317/v0/management/plugins/gpt365/import \
  -H "Authorization: Bearer <管理密钥>" \
  -H "Content-Type: application/json" \
  -d '{"text": "eyJhbGciOi...\neyJhbGciOi...\n"}'
```

也支持结构化列表：

```json
{ "tokens": ["<token1>", { "access_token": "<token2>", "account_id": "..." }] }
```

支持的输入形态（页面与接口一致）：

- 纯 `access_token`（JWT 或任意字符串）
- `access_token: xxx` 或 `access_token=xxx`
- 带 `Bearer ` 前缀、被引号包裹
- 完整 JSON 对象（`refresh_token` 等额外字段会保留）

导入经宿主凭据接口写入 `auth-dir`，**立即生效，无需重启**。同一账号重复导入会覆盖同一文件，不会堆积副本。

### 方式三：手动放置文件

在 CPA 的 `auth-dir` 下新建 `gpt365-<账号>.json`：

```json
{
  "type": "gpt365",
  "access_token": "eyJhbGciOi...",
  "account_id": "b3f49d1b-538c-4788-be4b-ce7912603dfc"
}
```

`type` 必须是 `gpt365`，否则不会被本插件接管。

### 其他管理接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/v0/management/plugins/gpt365/auths` | 列出本插件凭据（不含令牌本体） |
| POST | `/v0/management/plugins/gpt365/import` | 批量导入 |
| POST | `/v0/management/plugins/gpt365/delete` | 批量删除（`{"names":[...]}` 或 `{"all":true}`） |

## 这个插件解决什么问题

本机无法直连远程轮换代理，必须经本地代理转发；而 CPA 宿主的 HTTP 传输只支持
单一代理，无法在已建立的隧道内再发一次 CONNECT。因此本插件自带拨号器，
自行建立两级隧道：

```
本进程 --CONNECT--> 本地代理(15732) --CONNECT--> 远程轮换代理 --> bps.openai.com
```

同时按 `run_officejs` 工具偷渡约定，把客户端的工具调用安全地转换成上游原生调用
并回放，使 Codex 等客户端可以正常使用工具。

## 构建

需要 Go 1.25+ 与 C 编译器（插件 ABI 是 C ABI，CGO 必需）。

Windows（MinGW-w64）：

```bat
set CC=C:\path\to\mingw64\bin\gcc.exe
set CGO_ENABLED=1
build.bat
```

Linux / macOS：

```bash
make build
```

## 配置要点

| 配置项 | 说明 |
| --- | --- |
| `responses_url` | Basis Points 接口地址，通常无需修改。 |
| `upstream_model` | 默认上游模型。**可用模型取决于账号权限**，默认值为实测可用的保守选择。 |
| `models` | 暴露给客户端的别名列表，建议保留 `-basispoints` 后缀。 |
| `model_mappings` | 别名到上游模型的映射，键必须已列入 `models`。 |
| `model_selection` | 默认留空（不发送）。详见下方说明。 |
| `proxy_chain.*` | 两级代理链参数。 |
| `auth_mode` | 认证模式，通常为 `chatgpt`。 |

### 关于 model_selection（重要）

上游对 `model_selection` 的取值非常敏感，实测结果：

| 取值 | 结果 |
| --- | --- |
| 留空（默认） | 上游按账号实际权限路由，成功返回 |
| `explicit` | 按名称严格校验权限，账号无该模型权限时返回 **403** `Model access has changed` |
| `auto` | 被判定为非法值，返回 **422** `Invalid request body` |

因此插件默认**不发送**该字段。只有确认账号拥有全部配置模型权限时才填写 `explicit`。

### 关于 metadata（重要）

上游要求请求必须携带 `metadata`，其中 `task_id`、`turn_id`、`agent_iteration`
是必填项，缺失会返回 **422**。插件会自动生成：

- `task_id` / `turn_id` 由会话指纹确定性推导（UUID v5），同一会话内保持恒定。
- 工具结果回合**只递增 `agent_iteration`**，不改变 `turn_id`。
  这一点很关键：若 turn 变化，上游会把已完成的计划当作新 turn 重新规划，造成死循环。

## 凭据与安全

**本仓库不含任何凭据。** 远程代理的用户名与口令请通过环境变量注入，
其优先级高于配置文件：

| 环境变量 | 作用 |
| --- | --- |
| `GPT365_LOCAL_PROXY` | 第一跳本地代理地址 |
| `GPT365_REMOTE_PROXY` | 第二跳远程轮换代理地址 |
| `GPT365_REMOTE_USERNAME` | 远程代理用户名 |
| `GPT365_REMOTE_PASSWORD` | 远程代理口令 |
| `GPT365_RESPONSES_URL` | 上游接口地址 |
| `GPT365_UPSTREAM_MODEL` | 默认上游模型 |

其他安全约定：

- 持久化设置文件时会自动剔除远程代理口令。
- 错误信息中的 `Bearer` 令牌会被抹除，不会写入日志。
- 令牌只在内存中读取，不生成、不改写任何凭据文件。

### 代理链配置示例

```yaml
proxy_chain:
  enabled: true
  local_proxy: "http://127.0.0.1:15732"
  remote_proxy: ""          # 建议由 GPT365_REMOTE_PROXY 提供
  remote_username: ""       # 建议由 GPT365_REMOTE_USERNAME 提供
  remote_password: ""       # 建议由 GPT365_REMOTE_PASSWORD 提供
  connect_timeout_seconds: 20
```

`remote_proxy` 留空则退化为只使用本地代理的单跳模式。

## 协议边界

- 上游请求始终携带 `Authorization: Bearer <access_token>`、`chatgpt-account-id`、
  `x-openai-account-id` 与 `x-basispoints-auth-mode: chatgpt`。
- 上游**硬拒客户端传入的 `tools`**（带 tools 直接 422），因此插件会把客户端工具
  描述成自然语言目录注入 developer 消息，并约定用原生 `run_officejs` 作为运输载体；
  代理拦截该调用、取出内层真实工具，转成标准 `function_call` 交给客户端执行。
- `code` 字段是**嵌套 JSON 字符串**，不是 JavaScript。插件只解析它，绝不执行其中内容。
- 工具名不在客户端目录中、`call_id` 重复、内层再次出现运输工具等情况一律报错，
  不把服务端注入工具或损坏的中转载荷交给客户端。
- 认证解析会同时产出**原生 Codex 记录**与**本插件虚拟记录**，使既有 Codex 模型
  继续走 CPA 原生执行器，只有本插件的别名走 Basis Points。
- 本插件只解析 `type: gpt365` 的凭据文件，不会接管 Codex 或其他提供者的凭据。
- 删除操作只允许作用于 `gpt365-*.json`，拒绝删除其他提供者的凭据。
- 管理页面不内联任何令牌；列表接口只返回账号、过期时间与状态。

## 已知限制

- 流式响应会先读完上游 SSE 再回放给客户端，不是 token 级实时转发。
  这是为了让工具调用能基于完整 item 做安全转换。
- 上游无论 `stream` 取值如何都返回 SSE 分帧（实测 `stream=false` 时
  Content-Type 仍为 `text/event-stream`），插件按正文而非请求参数判断并统一解析。
- 可用模型由账号权限决定；免费档账号可能遇到 429 限流或 403 无权限。

## 测试

```bash
# 单元测试（无需网络）
go test ./...

# 真实链路集成测试（需要本机两级代理链可用）
BP_LIVE=1 BP_TOKEN=<access_token> BP_ACCOUNT_ID=<account_id> \
  go test ./internal/basispoints/ -run TestLive -v -timeout 900s
```

集成测试包含一条关键的负向对照：把远程代理口令故意写错，**必须**被远程代理以
`407 Proxy Authentication Required` 拒绝。若错误口令也能成功，说明第二跳根本没
执行，两级链路是假的。

辅助工具见 `tools/`：

- `tools/chainprobe` —— 独立验证两级代理链（含负向对照与出口 IP 轮换）。
- `tools/abicheck` —— 模拟宿主 `dlopen` 校验插件 ABI 契约。

## 发布

推送形如 `v0.1.0` 的标签即可触发 GitHub Actions：
先跑检查（格式、`go mod tidy`、actionlint、`go test -race`、`go vet`），
再构建六个平台的动态库，最后发布 Release 与 `checksums.txt`。

发布标签必须与 `internal/basispoints/types.go` 中的 `Version` 一致，工作流会强制校验。

## 版权

MIT License。
