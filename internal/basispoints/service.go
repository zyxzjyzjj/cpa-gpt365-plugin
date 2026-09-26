package basispoints

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Service 是插件的核心对象，承载配置、宿主回调与全部 RPC 方法分发。
type Service struct {
	mu      sync.RWMutex
	cfg     Config
	host    HostCall
	stopped bool

	// sessions 维护会话与账号的粘性绑定。
	sessions *sessionBinder

	// proxies 缓存账号到出口代理的分配结果。
	proxies *proxyPool
}

func NewService() *Service {
	return &Service{
		cfg:      defaultConfig(),
		sessions: newSessionBinder(),
		proxies:  newProxyPool(),
	}
}

func (s *Service) SetHost(host HostCall) {
	s.mu.Lock()
	s.host = host
	s.mu.Unlock()
}

func (s *Service) call(method string, payload any, out any) error {
	s.mu.RLock()
	host := s.host
	s.mu.RUnlock()
	if host == nil {
		return errors.New("宿主回调尚未初始化")
	}
	return host(method, payload, out)
}

func (s *Service) config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.clone()
}

func (s *Service) configure(raw json.RawMessage) error {
	cfg := defaultConfig()
	var request struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return fail(400, "invalid_config", "插件配置请求无法解析")
		}
	}
	if len(request.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(request.ConfigYAML, &cfg); err != nil {
			return fail(400, "invalid_config", "插件配置无效: "+err.Error())
		}
	}
	// 页面编辑代理池时，已有条目的口令不会回传，地址框留空表示保持原值。
	// 这里按上一份生效配置把原地址补回去，再清空辅助字段。
	cfg.ProxyPool.mergeKeepEntries(s.config().ProxyPool.Entries)
	// 只持久化非敏感配置；令牌始终留在 CPA 的凭据存储中。
	if cfg.DataDir != "" {
		if data, errRead := os.ReadFile(filepath.Join(cfg.DataDir, "settings.json")); errRead == nil {
			_ = json.Unmarshal(data, &cfg)
		}
	}
	if err := cfg.normalize(); err != nil {
		return err
	}
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return fail(500, "config_storage", "无法创建插件数据目录")
		}
		// 远程代理口令属于凭据，不写入磁盘。
		persisted := cfg.clone()
		persisted.ProxyChain.RemotePassword = ""
		data, _ := json.MarshalIndent(persisted, "", "  ")
		_ = os.WriteFile(filepath.Join(cfg.DataDir, "settings.json"), data, 0o600)
	}
	s.mu.Lock()
	s.cfg = cfg
	s.stopped = false
	s.mu.Unlock()

	// 配置变更后旧分配可能失效，清空缓存让下次请求按新配置重新计算。
	s.proxies.reset()
	return nil
}

func (s *Service) Handle(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if err := s.configure(raw); err != nil {
			return nil, err
		}
		return registration(s.config()), nil

	case "plugin.quiesce", "plugin.shutdown":
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		return map[string]any{}, nil

	case "auth.identifier":
		// 声明接管 CPA 中既有的 ChatGPT/Codex OAuth 凭据。
		return map[string]any{"identifier": AuthProviderID}, nil

	case "executor.identifier":
		return map[string]any{"identifier": Provider}, nil

	case "auth.parse":
		return authParse(raw)

	case "auth.login.start":
		return nil, fail(400, "login_unavailable", "请先在 CPA 中导入已有的 ChatGPT/Codex OAuth 凭据；本插件不提供交互式登录")

	case "auth.login.poll":
		return map[string]any{"Status": "error", "Message": "请先在 CPA 中导入已有的 ChatGPT/Codex OAuth 凭据"}, nil

	case "auth.refresh":
		return authRefresh(raw)

	case "model.register", "model.static", "model.for_auth":
		return modelRegistration(s.config()), nil

	case "scheduler.identifier":
		return map[string]any{"identifier": Provider}, nil

	case "scheduler.pick":
		return s.schedulerPick(raw)

	case "response.intercept_after":
		return s.interceptModelCatalog(raw)

	case "executor.execute":
		return s.execute(raw, false)

	case "executor.execute_stream":
		return s.execute(raw, true)

	case "executor.count_tokens":
		return map[string]any{"Payload": jsonBytes(map[string]any{"input_tokens": 0})}, nil

	case "executor.http_request":
		return nil, fail(400, "unsupported_method", "请使用本插件的 Basis Points 模型执行器")

	case "management.register":
		return managementRegistration(), nil

	case "management.handle":
		return s.managementHandle(raw)

	default:
		return nil, fail(400, "unsupported_method", "不支持的插件方法: "+method)
	}
}

func (s *Service) execute(raw json.RawMessage, stream bool) (any, error) {
	s.mu.RLock()
	stopped := s.stopped
	s.mu.RUnlock()
	if stopped {
		return nil, fail(503, "plugin_stopped", "插件已停止")
	}
	var request ExecutorRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "执行器请求无法解析")
	}
	cfg := s.config()
	c, errCredential := credentialFromExecutor(request)
	if errCredential != nil {
		return nil, errCredential
	}
	if !c.ExpiresAt.IsZero() && !nowUTC().Before(c.ExpiresAt) {
		return nil, fail(401, "auth_expired", "ChatGPT OAuth 访问令牌已过期，请先在 CPA 中更新凭据")
	}

	source, errSource := rawObject(request.OriginalRequest)
	if errSource != nil {
		source, errSource = rawObject(request.Payload)
	}
	if errSource != nil {
		return nil, errSource
	}
	if model := stringValue(source["model"]); model == "" {
		source["model"] = strings.TrimSpace(request.Model)
	}
	if _, exists := source["stream"]; !exists {
		source["stream"] = request.Stream
	}
	body, errBody := prepareResponsesBody(source, cfg)
	if errBody != nil {
		return nil, errBody
	}

	// 解析本账号的出口代理，并把配置收敛为本次请求实际使用的链路。
	//
	// 优先级：凭据上绑定的代理 > 代理池按账号分配 > 全局 proxy_chain。
	accountID := c.AccountID
	if accountID == "" {
		accountID = request.AuthID
	}
	accountProxy := proxyURLFromMetadata(request.AuthAttributes, request.AuthMetadata)
	if accountProxy == "" {
		accountProxy = s.resolvePoolProxy(accountID)
	}
	chain, errChain := effectiveProxyChain(cfg.ProxyChain, accountProxy)
	if errChain != nil {
		return nil, errChain
	}
	if cfg.ProxyPool.Enabled && cfg.ProxyPool.Strict && accountProxy == "" {
		return nil, fail(400, "proxy_pool_empty",
			"proxy_pool 已启用且 strict 为真，但该账号没有分配到出口代理")
	}
	cfg.ProxyChain = chain

	// 记录会话粘性绑定：走到这里说明该账号就是本次实际使用的账号。
	if sessionKey := sessionKeyFromExecutor(request); sessionKey != "" {
		s.rememberSessionBinding(sessionKey, request.AuthID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration(cfg))
	defer cancel()

	// 流式请求走异步转发：正文边收边发，客户端立刻能看到内容。
	//
	// 早期实现把整个上游流读完再一次性返回，客户端在数十秒内收不到任何
	// 字节，表现为「对话卡住」并被主动放弃（HTTP 499）。
	if stream {
		return s.executeStreamAsync(request, cfg, source, body, c)
	}

	upstream, errUpstream := s.doUpstream(ctx, cfg, body, c, stream)
	if errUpstream != nil {
		return nil, errUpstream
	}

	// 上游无论 stream 取值如何都返回 SSE 分帧（实测 stream=false 时
	// Content-Type 仍为 text/event-stream），因此这里按内容而不是按请求参数
	// 判断，先统一解析出最终响应对象。
	payload := upstream.Body
	if isEventStreamBody(upstream.Body) {
		final, errParse := parseFinalStreamResponse(upstream.Body)
		if errParse != nil {
			return nil, errParse
		}
		payload = jsonBytes(final)
	}
	transformed, response, _, errTransform := transformResponseBody(payload, source)
	if errTransform != nil {
		return nil, errTransform
	}
	if stream {
		return map[string]any{
			"Payload": syntheticStream(response),
			"Headers": map[string][]string{
				"Content-Type":  {"text/event-stream"},
				"Cache-Control": {"no-cache"},
			},
		}, nil
	}
	return map[string]any{
		"Payload": transformed,
		"Headers": map[string][]string{"Content-Type": {"application/json"}},
	}, nil
}

// isEventStreamBody 按正文判断是否为 SSE 分帧。
//
// 上游对 stream=false 的请求同样返回 SSE，因此不能依赖请求参数或
// Content-Type 来判断，必须看正文本身。
func isEventStreamBody(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, "data:")
}

func timeoutDuration(cfg Config) time.Duration {
	return time.Duration(cfg.TimeoutSeconds) * time.Second
}

func registration(cfg Config) map[string]any {
	return map[string]any{
		"schema_version": 6,
		"metadata": map[string]any{
			"Name":             "OpenAI Basis Points",
			"Version":          Version,
			"Author":           "zyxzjyzjj",
			"GitHubRepository": "https://github.com/zyxzjyzjj/cpa-gpt365-plugin",
			"Description":      "通过两级代理链接入 bps.openai.com 的 CPA Responses 适配器",
			"ConfigFields": []map[string]any{
				{"Name": "responses_url", "Type": "string", "Description": "Basis Points Responses 接口地址。"},
				{"Name": "upstream_model", "Type": "string", "Description": "未单独配置 model_mappings 的别名使用的上游模型。"},
				{"Name": "models", "Type": "array", "Description": "启用的客户端模型别名列表。"},
				{"Name": "model_mappings", "Type": "object", "Description": "客户端别名到上游模型的映射；键必须已列入 models。"},
				{"Name": "timeout_seconds", "Type": "integer", "Description": "上游请求超时秒数。"},
				{"Name": "max_response_bytes", "Type": "integer", "Description": "上游响应体大小上限。"},
				{"Name": "auth_mode", "Type": "string", "Description": "认证模式，通常为 chatgpt。"},
				{"Name": "proxy_chain.enabled", "Type": "boolean", "Description": "是否经代理出网。默认直连，无需代理。"},
				{"Name": "proxy_chain.local_proxy", "Type": "string", "Description": "第一跳代理，仅本机需经代理软件转发时填写。"},
				{"Name": "proxy_chain.remote_proxy", "Type": "string", "Description": "远程代理地址。服务器直连代理时只填这一项。"},
				{"Name": "proxy_chain.remote_username", "Type": "string", "Description": "远程代理用户名。"},
				{"Name": "proxy_chain.remote_password", "Type": "string", "Description": "远程代理口令；建议通过环境变量注入。"},
				{"Name": "proxy_pool.enabled", "Type": "boolean", "Description": "是否为每个账号分配独立出口。"},
				{"Name": "proxy_pool.entries", "Type": "array", "Description": "出口列表，每项含 name 与 url。"},
				{"Name": "proxy_pool.strategy", "Type": "string", "Description": "账号与出口的对应方式：hash 或 sticky。"},
				{"Name": "sticky_session.enabled", "Type": "boolean", "Description": "同一会话是否固定使用同一账号。"},
			},
		},
		"capabilities": map[string]any{
			"auth_provider":           true,
			"model_provider":          true,
			"executor":                true,
			"executor_model_scope":    "both",
			"executor_input_formats":  []string{"openai-response"},
			"executor_output_formats": []string{"openai-response"},
			"response_interceptor":    true,
			// 调度器用于实现会话粘性：同一会话固定使用同一账号。
			"scheduler": true,
			// 开启管理接口，提供凭据批量导入与状态页面。
			"management_api": true,
		},
		"config": cfg,
	}
}

func modelRegistration(cfg Config) map[string]any {
	models := make([]map[string]any, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		upstream, _ := cfg.upstreamModelForAlias(model)
		models = append(models, map[string]any{
			"ID":                         model,
			"Object":                     "model",
			"Name":                       upstream,
			"OwnedBy":                    Provider,
			"DisplayName":                model,
			"SupportedGenerationMethods": []string{"responses"},
			"SupportedInputModalities":   []string{"text", "image"},
			"SupportedOutputModalities":  []string{"text"},
			// 上游没有 max 挡位，因此只声明真实支持的等级。
			"Thinking":    map[string]any{"Levels": []string{"low", "medium", "high", "xhigh", "ultra"}},
			"UserDefined": true,
		})
	}
	return map[string]any{"Provider": Provider, "Models": models}
}
