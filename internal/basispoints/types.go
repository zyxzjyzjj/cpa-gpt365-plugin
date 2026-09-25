// Package basispoints 实现 OpenAI Basis Points（bps.openai.com）到 CPA 的适配层。
//
// 本包不依赖 CLIProxyAPI 的 SDK：插件与宿主之间只通过 C ABI 交换 JSON，
// 因此全部契约都以显式结构体描述，便于单独测试。
package basispoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// Version 是插件版本。
	Version = "0.3.2"

	// Provider 是执行器标识与模型归属标识，必须为小写。
	Provider = "gpt365"

	// AuthProviderID 是本插件在 CPA 中独占的凭据提供者标识。
	//
	// 必须是插件自己的名字，不能复用 "codex"：CPA 用凭据文件里的 type 字段
	// 决定由哪个 auth provider 解析，复用 codex 会让插件寄生在 Codex 凭据上，
	// 于是既没有独立 provider，也没有 token 录入入口。
	AuthProviderID = "gpt365"

	// PluginID 与动态库文件名保持一致。
	PluginID = Provider

	DefaultResponsesURL = "https://bps.openai.com/basispoints/api/responses"

	// 默认上游模型。该值按实测可用性选取：免费档账号请求该名称可稳定
	// 返回结果，而其他名称会因账号无权限被上游拒绝。不同账号的可用模型
	// 集合不同，因此这里只是合理默认值，可随时在配置中覆盖。
	DefaultUpstreamModel = "gpt-5.6-luna"
	DefaultModelID       = "gpt-5.6-luna-basispoints"

	// 默认两级代理链参数：本地代理负责把流量送出本机，
	// 远程轮换代理再以目标地区出口访问上游。
	//
	// 这里只提供地址骨架，不含任何凭据。远程代理的用户名与口令必须由使用者
	// 在配置中提供：本仓库是公开的，把凭据写进源码等同于公开泄露。
	DefaultLocalProxy       = "http://127.0.0.1:15732"
	DefaultRemoteProxy      = ""
	DefaultRemoteUsername   = ""
	DefaultConnectTimeout   = 20
	defaultTimeoutSeconds   = 300
	defaultMaxResponseBytes = 64 << 20
)

// supportedReasoningEfforts 是上游真正接受的思考挡位。
// 上游没有 max 挡位，因此 max/x-high 一律归一化为 xhigh。
var supportedReasoningEfforts = map[string]struct{}{
	"low": {}, "medium": {}, "high": {}, "xhigh": {}, "ultra": {},
}

// APIError 承载需要透传给下游的 HTTP 状态码与错误类别。
type APIError struct {
	Status  int
	Kind    string
	Message string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *APIError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Status
}

func (e *APIError) Code() string {
	if e == nil || e.Kind == "" {
		return "plugin_error"
	}
	return e.Kind
}

func (e *APIError) Retryable() bool {
	return e != nil && (e.Status == http.StatusTooManyRequests || e.Status >= 500)
}

func fail(status int, kind, message string) error {
	return &APIError{Status: status, Kind: kind, Message: message}
}

// HostCall 是宿主回调签名：把一次 RPC 的 JSON 结果解码进 out。
type HostCall func(method string, payload any, out any) error

// ExecutorRequest 镜像 CPA 的 JSON 执行器契约。
// HTTPClient 不属于 JSON ABI，本插件自行建立网络连接。
type ExecutorRequest struct {
	AuthID          string            `json:"AuthID"`
	AuthProvider    string            `json:"AuthProvider"`
	Model           string            `json:"Model"`
	Format          string            `json:"Format"`
	Stream          bool              `json:"Stream"`
	Alt             string            `json:"Alt"`
	Headers         http.Header       `json:"Headers"`
	Query           url.Values        `json:"Query"`
	OriginalRequest []byte            `json:"OriginalRequest"`
	SourceFormat    string            `json:"SourceFormat"`
	Payload         []byte            `json:"Payload"`
	Metadata        map[string]any    `json:"Metadata"`
	StorageJSON     []byte            `json:"StorageJSON"`
	AuthMetadata    map[string]any    `json:"AuthMetadata"`
	AuthAttributes  map[string]string `json:"AuthAttributes"`
	StreamID        string            `json:"stream_id,omitempty"`
	HostCallbackID  string            `json:"host_callback_id,omitempty"`
}

// ProxyChainConfig 描述「本机 -> 本地代理 -> 远程轮换代理 -> 上游」两级链路。
//
// 之所以自建连接而不用宿主 host.http.do：宿主的 HTTP 传输只能配置单一代理，
// 无法在一条 TCP 隧道内再嵌套一次 CONNECT，而本机的远程代理必须经本地代理转发。
type ProxyChainConfig struct {
	Enabled            bool   `yaml:"enabled" json:"enabled"`
	LocalProxy         string `yaml:"local_proxy" json:"local_proxy"`
	RemoteProxy        string `yaml:"remote_proxy" json:"remote_proxy"`
	RemoteUsername     string `yaml:"remote_username" json:"remote_username"`
	RemotePassword     string `yaml:"remote_password" json:"remote_password"`
	ConnectTimeoutSecs int    `yaml:"connect_timeout_seconds" json:"connect_timeout_seconds"`
}

type Config struct {
	DataDir          string            `yaml:"data_dir" json:"data_dir"`
	ResponsesURL     string            `yaml:"responses_url" json:"responses_url"`
	UpstreamModel    string            `yaml:"upstream_model" json:"upstream_model"`
	Models           []string          `yaml:"models" json:"models"`
	ModelMappings    map[string]string `yaml:"model_mappings" json:"model_mappings"`
	TimeoutSeconds   int               `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxResponseBytes int               `yaml:"max_response_bytes" json:"max_response_bytes"`
	AuthMode         string            `yaml:"auth_mode" json:"auth_mode"`
	ProxyChain       ProxyChainConfig  `yaml:"proxy_chain" json:"proxy_chain"`

	// ProxyPool 为每个账号分配独立出口，降低多账号被关联风控的风险。
	ProxyPool ProxyPoolConfig `yaml:"proxy_pool" json:"proxy_pool"`

	// ModelSelection 控制上游的 model_selection 字段。
	// 留空（默认）时不发送该字段，由上游按账号权限路由，兼容性最好。
	// 仅当确认账号拥有全部配置模型的权限时才设为 "explicit"。
	ModelSelection string `yaml:"model_selection" json:"model_selection"`

	// StickySession 控制会话粘性：同一会话固定使用同一账号。
	StickySession StickySessionConfig `yaml:"sticky_session" json:"sticky_session"`
}

// StickySessionConfig 描述会话粘性策略。
//
// 上游按 turn/task 组织多轮对话，若同一会话的不同轮次被分到不同账号，
// 上游会看到割裂的上下文，既影响效果也更容易触发风控。
type StickySessionConfig struct {
	// Enabled 为真时，同一会话固定绑定一个账号。
	Enabled bool `yaml:"enabled" json:"enabled"`

	// TTLSeconds 是绑定的存活时间。超时后允许重新分配，避免长期占用
	// 单一账号。默认 3600 秒。
	TTLSeconds int `yaml:"ttl_seconds" json:"ttl_seconds"`

	// MaxEntries 是绑定表容量上限，超出后淘汰最旧记录。
	MaxEntries int `yaml:"max_entries" json:"max_entries"`
}

func defaultConfig() Config {
	return Config{
		DataDir:          "",
		ResponsesURL:     DefaultResponsesURL,
		UpstreamModel:    DefaultUpstreamModel,
		Models:           []string{DefaultModelID},
		ModelMappings:    map[string]string{DefaultModelID: DefaultUpstreamModel},
		TimeoutSeconds:   defaultTimeoutSeconds,
		MaxResponseBytes: defaultMaxResponseBytes,
		AuthMode:         "chatgpt",
		ProxyChain: ProxyChainConfig{
			Enabled:            true,
			LocalProxy:         DefaultLocalProxy,
			RemoteProxy:        DefaultRemoteProxy,
			RemoteUsername:     DefaultRemoteUsername,
			ConnectTimeoutSecs: DefaultConnectTimeout,
		},
		StickySession: StickySessionConfig{
			Enabled:    true,
			TTLSeconds: 3600,
			MaxEntries: 4096,
		},
	}
}

func (c *Config) normalize() error {
	if c == nil {
		return fail(400, "invalid_config", "configuration is missing")
	}
	c.ResponsesURL = strings.TrimSpace(c.ResponsesURL)
	if c.ResponsesURL == "" {
		c.ResponsesURL = DefaultResponsesURL
	}
	u, err := url.Parse(c.ResponsesURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fail(400, "invalid_config", "responses_url 必须是绝对的 HTTP(S) 地址")
	}
	c.UpstreamModel = strings.TrimSpace(c.UpstreamModel)
	if c.UpstreamModel == "" {
		c.UpstreamModel = DefaultUpstreamModel
	}
	c.AuthMode = strings.TrimSpace(c.AuthMode)
	if c.AuthMode == "" {
		c.AuthMode = "chatgpt"
	}
	// 零值表示「未配置」，补默认值而不是当成非法输入。
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = defaultTimeoutSeconds
	}
	if c.TimeoutSeconds < 10 || c.TimeoutSeconds > 1800 {
		return fail(400, "invalid_config", "timeout_seconds 必须在 10 到 1800 之间")
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = defaultMaxResponseBytes
	}
	if c.MaxResponseBytes < 64<<10 || c.MaxResponseBytes > 128<<20 {
		return fail(400, "invalid_config", "max_response_bytes 必须在 64 KiB 到 128 MiB 之间")
	}

	seen := map[string]bool{}
	models := make([]string, 0, len(c.Models))
	for _, model := range c.Models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		models = append(models, model)
	}
	if len(models) == 0 {
		models = []string{DefaultModelID}
		seen[DefaultModelID] = true
	}
	mappings := make(map[string]string, len(c.ModelMappings))
	for alias, upstream := range c.ModelMappings {
		alias, upstream = strings.TrimSpace(alias), strings.TrimSpace(upstream)
		if alias == "" || upstream == "" {
			return fail(400, "invalid_config", "model_mappings 的别名与上游模型名都不能为空")
		}
		if !seen[alias] {
			return fail(400, "invalid_config", "model_mappings 中的别名未列入 models: "+alias)
		}
		mappings[alias] = upstream
	}
	c.Models = models
	c.ModelMappings = mappings

	applyTopLevelEnvOverrides(c)

	// 完全未提及代理链时套用默认值。此处用整体零值判定，是为了让直接构造的
	// Config{}（例如空配置节点）也能得到可用的默认链路；而显式写出
	// `enabled: false` 的配置不会落在零值分支上，仍能正确表达「关闭」。
	if c.ProxyChain.isZero() {
		c.ProxyChain = defaultConfig().ProxyChain
	}
	if errChain := c.ProxyChain.normalize(); errChain != nil {
		return errChain
	}
	if errPool := c.ProxyPool.normalize(); errPool != nil {
		return errPool
	}
	c.StickySession.normalize()
	return nil
}

// normalize 为会话粘性补默认值。
func (s *StickySessionConfig) normalize() {
	if s == nil {
		return
	}
	if s.TTLSeconds <= 0 {
		s.TTLSeconds = 3600
	}
	if s.TTLSeconds > 86400*7 {
		s.TTLSeconds = 86400 * 7
	}
	if s.MaxEntries <= 0 {
		s.MaxEntries = 4096
	}
	if s.MaxEntries > 100000 {
		s.MaxEntries = 100000
	}
}

// isZero 判断代理链配置是否完全未被赋值。
//
// 注意 Enabled 默认为 false，因此「只填了 local_proxy」这种最小配置
// 不会被误判为零值。
func (p ProxyChainConfig) isZero() bool {
	return !p.Enabled &&
		p.LocalProxy == "" &&
		p.RemoteProxy == "" &&
		p.RemoteUsername == "" &&
		p.RemotePassword == "" &&
		p.ConnectTimeoutSecs == 0
}

func (p *ProxyChainConfig) normalize() error {
	if p == nil {
		return nil
	}
	p.LocalProxy = strings.TrimSpace(p.LocalProxy)
	p.RemoteProxy = strings.TrimSpace(p.RemoteProxy)
	p.RemoteUsername = strings.TrimSpace(p.RemoteUsername)
	p.RemotePassword = strings.TrimSpace(p.RemotePassword)
	applyProxyEnvOverrides(p)
	if !p.Enabled {
		return nil
	}
	if p.ConnectTimeoutSecs <= 0 {
		p.ConnectTimeoutSecs = DefaultConnectTimeout
	}
	if p.ConnectTimeoutSecs > 300 {
		return fail(400, "invalid_config", "proxy_chain.connect_timeout_seconds 不能超过 300")
	}
	if p.LocalProxy == "" {
		return fail(400, "invalid_config", "proxy_chain.local_proxy 不能为空")
	}
	if errLocal := validateProxyURL(p.LocalProxy); errLocal != nil {
		return fail(400, "invalid_config", "proxy_chain.local_proxy 无效: "+errLocal.Error())
	}
	if p.RemoteProxy == "" {
		// 只配置本地代理时退化为单跳，仍然是合法配置。
		return nil
	}
	if errRemote := validateProxyURL(p.RemoteProxy); errRemote != nil {
		return fail(400, "invalid_config", "proxy_chain.remote_proxy 无效: "+errRemote.Error())
	}
	// 凭据必须成对出现。仓库内不含任何默认凭据，因此这里只做一致性校验，
	// 具体取值由使用者通过配置或环境变量提供。
	if (p.RemoteUsername == "") != (p.RemotePassword == "") {
		return fail(400, "invalid_config", "proxy_chain 的远程代理用户名与密码必须同时提供")
	}
	return nil
}

// 代理相关环境变量名。用于在容器与 CI 中注入凭据，避免把口令写进配置文件。
const (
	envLocalProxy     = "GPT365_LOCAL_PROXY"
	envRemoteProxy    = "GPT365_REMOTE_PROXY"
	envRemoteUsername = "GPT365_REMOTE_USERNAME"
	envRemotePassword = "GPT365_REMOTE_PASSWORD"
	envResponsesURL   = "GPT365_RESPONSES_URL"
	envUpstreamModel  = "GPT365_UPSTREAM_MODEL"
)

// applyProxyEnvOverrides 用环境变量覆盖代理链配置。
//
// 环境变量优先于配置文件：这样部署时可以把凭据留在环境里，
// 而仓库中的 config.example.yaml 只保留占位符。
func applyProxyEnvOverrides(p *ProxyChainConfig) {
	if p == nil {
		return
	}
	if value := strings.TrimSpace(os.Getenv(envLocalProxy)); value != "" {
		p.LocalProxy = value
	}
	if value := strings.TrimSpace(os.Getenv(envRemoteProxy)); value != "" {
		p.RemoteProxy = value
	}
	if value := strings.TrimSpace(os.Getenv(envRemoteUsername)); value != "" {
		p.RemoteUsername = value
	}
	if value := strings.TrimSpace(os.Getenv(envRemotePassword)); value != "" {
		p.RemotePassword = value
	}
}

// applyTopLevelEnvOverrides 处理与代理无关的少量环境变量覆盖。
func applyTopLevelEnvOverrides(c *Config) {
	if c == nil {
		return
	}
	if value := strings.TrimSpace(os.Getenv(envResponsesURL)); value != "" {
		c.ResponsesURL = value
	}
	if value := strings.TrimSpace(os.Getenv(envUpstreamModel)); value != "" {
		c.UpstreamModel = value
	}
}

// validateProxyURL 要求代理地址是显式的 HTTP(S) 绝对地址，避免误配成 SOCKS 而被静默忽略。
func validateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("仅支持 http/https 代理，收到 %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("缺少主机名")
	}
	return nil
}

func (c Config) clone() Config {
	c.Models = append([]string(nil), c.Models...)
	if c.ModelMappings != nil {
		mappings := make(map[string]string, len(c.ModelMappings))
		for alias, upstream := range c.ModelMappings {
			mappings[alias] = upstream
		}
		c.ModelMappings = mappings
	}
	return c
}

// normalizeEffort 把客户端挡位收敛到上游真实支持的集合。
// 上游没有 max 挡位：max 与 x-high 系列一律落到 xhigh。
func normalizeEffort(value any) string {
	s, _ := value.(string)
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "x-high", "extra-high", "extra_high", "max", "maximum":
		s = "xhigh"
	case "minimal", "none":
		s = "low"
	}
	if _, ok := supportedReasoningEfforts[s]; ok {
		return s
	}
	return "medium"
}

func rawObject(raw []byte) (map[string]any, error) {
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fail(400, "invalid_request", "请求体必须是 JSON 对象")
	}
	return object, nil
}

func jsonBytes(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func stringValue(value any) string {
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func numberValue(value any) int64 {
	switch n := value.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// errorMessage 抽取上游错误正文，并丢弃可能夹带对话内容的校验细节。
func errorMessage(body []byte) string {
	var object map[string]any
	if json.Unmarshal(body, &object) == nil {
		if details, ok := object["detail"].([]any); ok && len(details) > 0 {
			safe := make([]map[string]any, 0, len(details))
			for _, value := range details {
				entry := objectValue(value)
				if entry == nil {
					continue
				}
				safe = append(safe, map[string]any{"loc": entry["loc"], "msg": entry["msg"], "type": entry["type"]})
			}
			if len(safe) > 0 {
				return string(jsonBytes(map[string]any{"detail": safe}))
			}
		}
		if detail := stringValue(object["detail"]); detail != "" {
			return detail
		}
		if nested := objectValue(object["error"]); nested != nil {
			if message := stringValue(nested["message"]); message != "" {
				return message
			}
			if kind := stringValue(nested["type"]); kind != "" {
				return kind
			}
		}
		if message := stringValue(object["message"]); message != "" {
			return message
		}
		if message := stringValue(object["error"]); message != "" {
			return message
		}
	}
	if len(body) > 0 {
		message := strings.TrimSpace(string(body))
		if len(message) > 500 {
			message = message[:500]
		}
		return message
	}
	return "Basis Points 上游请求失败"
}

func timeoutError(cfg Config) error {
	return fail(504, "upstream_timeout", fmt.Sprintf("Basis Points 请求在 %d 秒后超时", cfg.TimeoutSeconds))
}

// upstreamModelForAlias 只解析已启用的别名；未单独映射时沿用全局上游模型。
func (c Config) upstreamModelForAlias(alias string) (string, bool) {
	for _, candidate := range c.Models {
		if alias == candidate {
			if upstream, exists := c.ModelMappings[alias]; exists {
				return upstream, true
			}
			return c.UpstreamModel, true
		}
	}
	return "", false
}

// resolveUpstreamModel 同时接受客户端别名和 CPA 传入的已配置上游名称。
func (c Config) resolveUpstreamModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" && len(c.Models) > 0 {
		model = c.Models[0]
	}
	if upstream, ok := c.upstreamModelForAlias(model); ok {
		return upstream, true
	}
	for _, alias := range c.Models {
		if upstream, ok := c.upstreamModelForAlias(alias); ok && model == upstream {
			return upstream, true
		}
	}
	return "", false
}

func nowUTC() time.Time { return time.Now().UTC() }
