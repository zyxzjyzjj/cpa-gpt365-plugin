package basispoints

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// 本文件实现会话粘性：同一会话固定使用同一账号。
//
// 为什么需要：上游按会话组织多轮对话（task_id / turn_id 由会话推导）。
// 若同一会话的不同轮次被调度到不同账号，上游会看到割裂的上下文，
// 既影响回答质量，也因为「同一会话在不同账号/IP 间跳跃」更容易触发风控。
//
// 实现方式：作为 CPA 的 scheduler 插件参与选号。宿主在选号前调用
// scheduler.pick，把候选账号与请求头一起交过来；插件按会话键查绑定表，
// 命中则返回该账号，未命中则交给内置调度器（DelegateBuiltin）并由宿主
// 回填绑定。这样既实现粘性，又不与宿主的负载均衡策略冲突。

// sessionBinder 维护「会话 -> 账号」的绑定表。
type sessionBinder struct {
	mu      sync.Mutex
	entries map[string]sessionBinding
	// order 记录插入顺序，用于按容量淘汰最旧记录。
	order []string
}

type sessionBinding struct {
	authID    string
	expiresAt time.Time
}

func newSessionBinder() *sessionBinder {
	return &sessionBinder{entries: map[string]sessionBinding{}}
}

// bind 记录会话与账号的绑定关系。
func (b *sessionBinder) bind(sessionKey, authID string, ttl time.Duration, maxEntries int) {
	if b == nil || sessionKey == "" || authID == "" {
		return
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.entries[sessionKey]; !exists {
		b.order = append(b.order, sessionKey)
	}
	b.entries[sessionKey] = sessionBinding{authID: authID, expiresAt: time.Now().Add(ttl)}

	// 容量超限时按插入顺序淘汰，避免绑定表无限增长。
	if maxEntries > 0 {
		for len(b.order) > maxEntries {
			oldest := b.order[0]
			b.order = b.order[1:]
			delete(b.entries, oldest)
		}
	}
}

// lookup 取出仍然有效的绑定。
func (b *sessionBinder) lookup(sessionKey string) (string, bool) {
	if b == nil || sessionKey == "" {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	binding, ok := b.entries[sessionKey]
	if !ok {
		return "", false
	}
	if time.Now().After(binding.expiresAt) {
		delete(b.entries, sessionKey)
		return "", false
	}
	return binding.authID, true
}

// count 返回当前绑定数量，用于管理界面展示。
func (b *sessionBinder) count() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// reset 清空绑定表。
func (b *sessionBinder) reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = map[string]sessionBinding{}
	b.order = nil
}

// sessionKeyFrom 从调度请求中推导会话键。
//
// 优先使用显式会话标识（客户端或宿主给出的），其次用请求头里的常见会话
// 字段，最后回退到「无会话」。不使用消息内容做键：内容随轮次变化，
// 用它会导致每轮都算作新会话，粘性形同虚设。
func sessionKeyFrom(headers map[string][]string, metadata map[string]any) string {
	// 1) 宿主提供的调度上下文。
	if metadata != nil {
		for _, key := range []string{"session_id", "sessionId", "conversation_id", "conversationId"} {
			if value := strings.TrimSpace(stringValue(metadata[key])); value != "" {
				return "meta:" + value
			}
		}
	}
	// 2) 请求头中的会话标识。不同客户端用的头不同，逐个尝试。
	if headers != nil {
		for _, name := range []string{
			"X-Session-Id", "X-Session-ID", "Session-Id", "Session-ID",
			"X-Conversation-Id", "Conversation-Id",
			"X-Request-Id", "X-Correlation-Id",
			"Prompt-Cache-Key", "X-Prompt-Cache-Key",
		} {
			for actualName, values := range headers {
				if !strings.EqualFold(actualName, name) || len(values) == 0 {
					continue
				}
				if value := strings.TrimSpace(values[0]); value != "" {
					return "hdr:" + value
				}
			}
		}
	}
	return ""
}

// schedulerPick 处理宿主的选号请求。
//
// 返回 Handled=false 表示「不干预，交给内置调度器」；宿主随后会按自己的
// 策略选号。为避免重复干预，插件不在此处回填绑定 —— 绑定在请求实际发出时
// 依据最终选中的账号写入（见 rememberSessionBinding）。
func (s *Service) schedulerPick(raw json.RawMessage) (any, error) {
	var request struct {
		Model     string `json:"Model"`
		Provider  string `json:"Provider"`
		Providers []string
		Options   struct {
			Headers  map[string][]string `json:"Headers"`
			Metadata map[string]any      `json:"Metadata"`
		} `json:"Options"`
		Candidates []struct {
			ID       string            `json:"ID"`
			Provider string            `json:"Provider"`
			Status   string            `json:"Status"`
			Disabled bool              `json:"Disabled"`
			Attrs    map[string]string `json:"Attributes"`
		} `json:"Candidates"`
	}
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fail(400, "invalid_request", "scheduler.pick 请求无法解析")
		}
	}

	cfg := s.config()
	if !cfg.StickySession.Enabled {
		return map[string]any{"Handled": false}, nil
	}

	sessionKey := sessionKeyFrom(request.Options.Headers, request.Options.Metadata)
	if sessionKey == "" {
		// 无法识别会话时不做干预，避免把无会话的请求错误地钉在某一账号上。
		return map[string]any{"Handled": false}, nil
	}

	boundID, ok := s.sessions.lookup(sessionKey)
	if !ok {
		// 尚未绑定：交给内置调度器选号，随后由宿主发起的请求回填。
		return map[string]any{"Handled": false}, nil
	}

	// 绑定仍然有效，但必须确认该账号仍在候选列表里且可用；
	// 否则回退到内置调度器，避免把请求钉在已停用或不匹配的账号上。
	for _, candidate := range request.Candidates {
		if candidate.ID != boundID {
			continue
		}
		if candidate.Disabled || strings.EqualFold(candidate.Status, "disabled") {
			break
		}
		return map[string]any{"Handled": true, "AuthID": boundID}, nil
	}
	return map[string]any{"Handled": false}, nil
}

// rememberSessionBinding 在请求确定使用某个账号后记录绑定。
//
// 绑定时机放在执行阶段而非调度阶段：只有走到执行器，才能确定最终生效的
// 账号，避免把「候选」误当成「实际使用」。
func (s *Service) rememberSessionBinding(sessionKey, authID string) {
	if s == nil || sessionKey == "" || authID == "" {
		return
	}
	cfg := s.config()
	if !cfg.StickySession.Enabled {
		return
	}
	s.sessions.bind(sessionKey,
		authID,
		time.Duration(cfg.StickySession.TTLSeconds)*time.Second,
		cfg.StickySession.MaxEntries)
}

// sessionKeyFromExecutor 从执行器请求中推导会话键。
//
// 与调度阶段保持同一套规则，确保两处指向同一会话。
func sessionKeyFromExecutor(request ExecutorRequest) string {
	if key := sessionKeyFrom(request.Headers, request.Metadata); key != "" {
		return key
	}
	// 回退：用原始请求体中的会话相关字段。
	if len(request.OriginalRequest) > 0 {
		if source, errObject := rawObject(request.OriginalRequest); errObject == nil {
			if key := sessionKeyFrom(nil, source); key != "" {
				return key
			}
			// 客户端可能把会话标识放在 metadata 或 prompt_cache_key 里。
			for _, field := range []string{"prompt_cache_key", "session_id", "conversation_id"} {
				if value := strings.TrimSpace(stringValue(source[field])); value != "" {
					return "body:" + value
				}
			}
			if metadata := objectValue(source["metadata"]); metadata != nil {
				for _, field := range []string{"session_id", "conversation_id", "thread_id"} {
					if value := strings.TrimSpace(stringValue(metadata[field])); value != "" {
						return "bodymeta:" + value
					}
				}
			}
		}
	}
	return ""
}
