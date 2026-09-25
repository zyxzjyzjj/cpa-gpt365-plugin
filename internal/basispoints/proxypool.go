package basispoints

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// 本文件实现「每个账号一个出口代理」的代理池。
//
// 动机：多个账号共用一个出口 IP 时，上游很容易把它们关联起来并触发风控。
// 让每个账号固定走自己的代理，既降低关联风险，也让单个账号的出入口稳定
// （同一账号始终同一出口，不会因为轮换而出现异地跳变）。
//
// 与全局 proxy_chain 的关系：
//
//	proxy_chain 描述「怎么连出去」——本机 -> 本地代理 -> 远程代理。
//	代理池描述「用哪个远程代理」——每个账号分配池中的一个条目。
//
// 因此池中的条目只替换 proxy_chain 的远程段，本地代理那一跳保持不变。

// ProxyPoolEntry 是代理池中的一个出口。
type ProxyPoolEntry struct {
	// Name 是便于识别的标签，同时用于稳定分配（同名同账号）。
	Name string `yaml:"name" json:"name"`
	// URL 是远程代理地址，支持 http://user:pass@host:port 形式。
	URL string `yaml:"url" json:"url"`
}

// ProxyPoolConfig 描述代理池。
type ProxyPoolConfig struct {
	// Enabled 控制是否按账号分配代理。关闭时所有账号共用 proxy_chain。
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Entries 是可用出口列表。
	Entries []ProxyPoolEntry `yaml:"entries" json:"entries"`

	// Strategy 决定账号与出口的对应方式：
	//   hash   —— 按账号 ID 稳定散列（默认）。同一账号永远拿到同一出口，
	//             且账号增减不会打乱既有分配。
	//   sticky —— 按导入顺序依次分配，并把结果写进凭据，之后不再改变。
	Strategy string `yaml:"strategy" json:"strategy"`

	// Strict 为真时，池已启用但没有可用出口会让请求直接失败，
	// 而不是静默回退到 proxy_chain。用于避免「以为在隔离，其实共用出口」。
	Strict bool `yaml:"strict" json:"strict"`
}

// normalize 校验并规整代理池配置。
func (p *ProxyPoolConfig) normalize() error {
	if p == nil {
		return nil
	}
	p.Strategy = strings.ToLower(strings.TrimSpace(p.Strategy))
	if p.Strategy == "" {
		p.Strategy = "hash"
	}
	switch p.Strategy {
	case "hash", "sticky":
	default:
		return fail(400, "invalid_config", "proxy_pool.strategy 只支持 hash 或 sticky")
	}

	seen := map[string]bool{}
	entries := make([]ProxyPoolEntry, 0, len(p.Entries))
	for index, entry := range p.Entries {
		entry.Name = strings.TrimSpace(entry.Name)
		entry.URL = strings.TrimSpace(entry.URL)
		if entry.URL == "" {
			continue
		}
		if errURL := validateProxyURL(entry.URL); errURL != nil {
			return fail(400, "invalid_config",
				fmt.Sprintf("proxy_pool.entries[%d] 地址无效: %s", index, errURL.Error()))
		}
		if entry.Name == "" {
			// 未命名时用主机名兜底，保证有可读标签。
			entry.Name = proxyHostLabel(entry.URL, index)
		}
		if seen[entry.Name] {
			return fail(400, "invalid_config", "proxy_pool.entries 名称重复: "+entry.Name)
		}
		seen[entry.Name] = true
		entries = append(entries, entry)
	}
	p.Entries = entries

	if p.Enabled && len(entries) == 0 {
		if p.Strict {
			return fail(400, "invalid_config", "proxy_pool 已启用且 strict 为真，但没有任何可用出口")
		}
		// 非严格模式下允许启用空池：退化为使用 proxy_chain。
	}
	return nil
}

func proxyHostLabel(raw string, index int) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return fmt.Sprintf("proxy-%d", index+1)
}

// pickForAccount 为账号挑选出口。
//
// hash 策略用账号 ID 做稳定散列：同一账号永远得到同一条目，
// 且池中条目增删只会影响少量账号（一致性哈希的简化版：取模会重排，
// 因此这里对「条目集合」做排序后再取模，至少保证同一配置下结果稳定）。
func (p ProxyPoolConfig) pickForAccount(accountID string) (ProxyPoolEntry, bool) {
	if !p.Enabled || len(p.Entries) == 0 {
		return ProxyPoolEntry{}, false
	}
	// 排序保证条目顺序不影响分配结果。
	sorted := append([]ProxyPoolEntry(nil), p.Entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	key := strings.TrimSpace(accountID)
	if key == "" {
		key = "anonymous"
	}
	index := int(stableHash(key) % uint32(len(sorted)))
	return sorted[index], true
}

// stableHash 是 FNV-1a，用于把账号 ID 稳定映射到池下标。
func stableHash(input string) uint32 {
	var hash uint32 = 2166136261
	for i := 0; i < len(input); i++ {
		hash ^= uint32(input[i])
		hash *= 16777619
	}
	return hash
}

// proxyPool 承载运行期的分配缓存，避免每次请求都重新计算。
type proxyPool struct {
	mu       sync.RWMutex
	resolved map[string]string // accountID -> 代理 URL
}

func newProxyPool() *proxyPool {
	return &proxyPool{resolved: map[string]string{}}
}

func (p *proxyPool) lookup(accountID string) (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	value, ok := p.resolved[accountID]
	return value, ok
}

func (p *proxyPool) remember(accountID, proxyURL string) {
	if p == nil || accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved[accountID] = proxyURL
}

func (p *proxyPool) reset() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved = map[string]string{}
}

// proxyURLFromMetadata 从凭据元数据或属性里取出该账号绑定的代理。
//
// 执行阶段的 ExecutorRequest 没有独立的 ProxyURL 字段，但 AuthAttributes
// 会原样携带凭据的 Attributes，因此代理地址通过 Attributes 传递。
func proxyURLFromMetadata(attributes map[string]string, metadata map[string]any) string {
	if attributes != nil {
		if value := strings.TrimSpace(attributes[attrProxyURL]); value != "" {
			return value
		}
	}
	if metadata != nil {
		if value := strings.TrimSpace(stringValue(metadata[attrProxyURL])); value != "" {
			return value
		}
	}
	return ""
}

// attrProxyURL 是凭据 Attributes 中承载出口代理的键名。
//
// 用 proxy_url 这个名字，与 CPA 自身的 Auth.ProxyURL 语义保持一致；
// 宿主也会把该字段识别为凭据级代理。
const attrProxyURL = "proxy_url"

// effectiveProxyChain 把账号级代理合并进全局代理链。
//
// 账号代理只替换远程段：本地代理那一跳必须保留，否则本机无法到达远程代理。
func effectiveProxyChain(base ProxyChainConfig, accountProxyURL string) (ProxyChainConfig, error) {
	accountProxyURL = strings.TrimSpace(accountProxyURL)
	if accountProxyURL == "" {
		return base, nil
	}
	merged := base
	merged.RemoteProxy = accountProxyURL
	// 账号代理的凭据写在 URL 里时，清掉可能残留的全局用户名口令，
	// 避免把全局凭据发给另一个出口。
	if parsed, err := url.Parse(accountProxyURL); err == nil && parsed.User != nil {
		merged.RemoteUsername = ""
		merged.RemotePassword = ""
	} else if err != nil {
		return base, fail(400, "invalid_config", "账号代理地址无效: "+err.Error())
	}
	return merged, nil
}

// resolvePoolProxy 返回某账号应使用的出口代理。
//
// 先查运行期缓存（同一账号在整个进程生命周期内保持同一出口），
// 未命中再按策略计算并缓存。
func (s *Service) resolvePoolProxy(accountID string) string {
	if s == nil {
		return ""
	}
	cfg := s.config()
	if !cfg.ProxyPool.Enabled || len(cfg.ProxyPool.Entries) == 0 {
		return ""
	}
	if cached, ok := s.proxies.lookup(accountID); ok {
		return cached
	}
	entry, ok := cfg.ProxyPool.pickForAccount(accountID)
	if !ok {
		return ""
	}
	s.proxies.remember(accountID, entry.URL)
	return entry.URL
}

// bindProxyToAuthJSON 把出口代理写进凭据 JSON。
//
// 写进凭据而不是只留在内存，是为了让分配在 CPA 重启后依然有效：
// 同一账号重启后仍然走同一出口，避免出口跳变被上游判定为异常。
func bindProxyToAuthJSON(authJSON []byte, proxyURL string) ([]byte, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return authJSON, nil
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(authJSON, &root); errUnmarshal != nil {
		return authJSON, fail(400, "invalid_auth", "凭据不是合法的 JSON")
	}
	root[attrProxyURL] = proxyURL
	// type 必须保持为本插件，防止上游字段覆盖。
	root["type"] = Provider
	return json.MarshalIndent(root, "", "  ")
}
func (p ProxyPoolConfig) describePool() []map[string]any {
	out := make([]map[string]any, 0, len(p.Entries))
	for _, entry := range p.Entries {
		out = append(out, map[string]any{
			"name": entry.Name,
			// 只暴露主机名与是否带凭据，避免泄露口令。
			"host":            proxyHostLabel(entry.URL, 0),
			"has_credentials": strings.Contains(entry.URL, "@"),
		})
	}
	return out
}
