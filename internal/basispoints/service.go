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
}

func NewService() *Service {
	return &Service{cfg: defaultConfig()}
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

	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration(cfg))
	defer cancel()

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
				{"Name": "proxy_chain.enabled", "Type": "boolean", "Description": "是否启用两级代理链。"},
				{"Name": "proxy_chain.local_proxy", "Type": "string", "Description": "第一跳本地代理，例如 http://127.0.0.1:15732。"},
				{"Name": "proxy_chain.remote_proxy", "Type": "string", "Description": "第二跳远程轮换代理地址。"},
				{"Name": "proxy_chain.remote_username", "Type": "string", "Description": "远程代理用户名。"},
				{"Name": "proxy_chain.remote_password", "Type": "string", "Description": "远程代理口令；建议通过环境变量或配置文件注入。"},
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
			"management_api":          false,
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
