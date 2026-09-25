package basispoints

import (
	"encoding/json"
	"net/http"
	"strings"
)

// catalogInterceptRequest 使用 CPA 的响应拦截 JSON 契约。
type catalogInterceptRequest struct {
	SourceFormat    string
	Model           string
	RequestedModel  string
	Stream          bool
	OriginalRequest []byte
	RequestBody     []byte
	Body            []byte
	StatusCode      int
}

// interceptModelCatalog 补齐本插件模型在 Codex 目录中的上下文容量。
//
// 上游别名不在原生目录里，若不补齐，客户端会拿到缺失的上下文窗口。
// 这里只从同一目录中的规范模型复制容量字段，不猜测数值。
func (s *Service) interceptModelCatalog(raw json.RawMessage) (any, error) {
	var request catalogInterceptRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "模型目录拦截请求无法解析")
	}
	if request.SourceFormat != "openai" || request.StatusCode != http.StatusOK || request.Stream ||
		request.Model != "" || request.RequestedModel != "" ||
		len(request.OriginalRequest) != 0 || len(request.RequestBody) != 0 {
		return map[string]any{}, nil
	}
	var catalog map[string]json.RawMessage
	if json.Unmarshal(request.Body, &catalog) != nil {
		return map[string]any{}, nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(catalog["models"], &entries) != nil || len(entries) == 0 {
		return map[string]any{}, nil
	}
	cfg := s.config()
	models := make([]map[string]json.RawMessage, len(entries))
	bySlug := make(map[string]map[string]json.RawMessage, len(entries))
	for i, entry := range entries {
		if json.Unmarshal(entry, &models[i]) != nil {
			continue
		}
		var slug string
		if json.Unmarshal(models[i]["slug"], &slug) == nil && slug != "" {
			bySlug[slug] = models[i]
		}
	}
	changed := false
	for i, model := range models {
		var slug string
		if json.Unmarshal(model["slug"], &slug) != nil {
			continue
		}
		canonicalSlug, owned := catalogCanonicalSlug(slug, cfg)
		if !owned {
			continue
		}
		canonical := bySlug[canonicalSlug]
		if canonical == nil {
			// 同目录没有规范模型时跳过，交由客户端使用自身默认值，
			// 不编造容量数值。
			continue
		}
		copied := false
		for _, field := range []string{"context_window", "max_context_window"} {
			var value int64
			if json.Unmarshal(canonical[field], &value) == nil && value > 0 {
				model[field] = canonical[field]
				copied = true
			}
		}
		if percent, exists := canonical["effective_context_window_percent"]; exists {
			model["effective_context_window_percent"] = percent
		}
		if !copied {
			continue
		}
		updated, errMarshal := json.Marshal(model)
		if errMarshal != nil {
			return nil, errMarshal
		}
		entries[i] = updated
		changed = true
	}
	if !changed {
		return map[string]any{}, nil
	}
	catalog["models"] = jsonBytes(entries)
	body, errMarshal := json.Marshal(catalog)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return map[string]any{"Body": body}, nil
}

func catalogCanonicalSlug(slug string, cfg Config) (string, bool) {
	if upstream, ok := cfg.upstreamModelForAlias(slug); ok {
		return upstream, true
	}
	// 凭据前缀属于 CPA 的路由标识，只在同一前缀下匹配规范模型。
	if prefix, base, found := strings.Cut(slug, "/"); found {
		if upstream, ok := cfg.upstreamModelForAlias(base); ok {
			return prefix + "/" + upstream, true
		}
	}
	return "", false
}
