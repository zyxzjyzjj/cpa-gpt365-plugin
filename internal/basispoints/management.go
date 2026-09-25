package basispoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// 本文件实现插件自带的管理接口与资源页：
//
//	GET  /v0/resource/plugins/gpt365/           凭据管理页面（浏览器打开）
//	GET  /v0/management/plugins/gpt365/auths    列出本插件的凭据
//	POST /v0/management/plugins/gpt365/import   批量导入令牌
//	POST /v0/management/plugins/gpt365/delete   批量删除凭据
//
// 导入走宿主回调 host.auth.save，由 CPA 写入 auth-dir 并立即生效，
// 因此导入后无需重启。

const (
	// 管理接口路由（相对 /v0/management/）。
	routeAuthList   = "/plugins/gpt365/auths"
	routeAuthImport = "/plugins/gpt365/import"
	routeAuthDelete = "/plugins/gpt365/delete"

	// 浏览器资源页路由（相对 /v0/resource/plugins/gpt365/）。
	resourceRoot = "/"
)

// managementRegistration 声明插件拥有的管理路由与资源页。
func managementRegistration() map[string]any {
	return map[string]any{
		"Routes": []map[string]any{
			{"Method": http.MethodGet, "Path": routeAuthList},
			{"Method": http.MethodPost, "Path": routeAuthImport},
			{"Method": http.MethodPost, "Path": routeAuthDelete},
		},
		"Resources": []map[string]any{
			{
				"Path":        resourceRoot,
				"Menu":        "GPT365 凭据",
				"Description": "批量导入与查看 Basis Points 访问令牌。",
			},
		},
	}
}

// managementHandle 分发插件管理请求。
func (s *Service) managementHandle(raw json.RawMessage) (any, error) {
	var request struct {
		Method  string              `json:"Method"`
		Path    string              `json:"Path"`
		Headers map[string][]string `json:"Headers"`
		Query   map[string][]string `json:"Query"`
		Body    []byte              `json:"Body"`
	}
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fail(400, "invalid_request", "管理请求无法解析")
	}
	path := strings.TrimSpace(request.Path)
	// 资源页与 API 可能带前缀，这里按后缀匹配，避免路径前缀差异导致失配。
	switch {
	case strings.HasSuffix(path, routeAuthList):
		return s.handleAuthList()
	case strings.HasSuffix(path, routeAuthImport):
		return s.handleAuthImport(request.Body)
	case strings.HasSuffix(path, routeAuthDelete):
		return s.handleAuthDelete(request.Body)
	default:
		// 其余 GET 一律返回管理页面。
		if strings.EqualFold(request.Method, http.MethodGet) {
			return htmlResponse(authPageHTML), nil
		}
		return nil, fail(404, "not_found", "未知的管理路径: "+path)
	}
}

func jsonResponse(payload any) map[string]any {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		body = []byte(`{"error":"响应无法序列化"}`)
	}
	return map[string]any{
		"StatusCode": http.StatusOK,
		"Headers":    map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
		"Body":       body,
	}
}

func htmlResponse(html string) map[string]any {
	return map[string]any{
		"StatusCode": http.StatusOK,
		"Headers": map[string][]string{
			"Content-Type": {"text/html; charset=utf-8"},
			// 页面含内联脚本，禁止被嵌入其他站点。
			"X-Content-Type-Options": {"nosniff"},
			"Cache-Control":          {"no-store"},
		},
		"Body": []byte(html),
	}
}

// authRecord 是返回给前端的凭据摘要，不包含令牌本体。
type authRecord struct {
	Index     string `json:"auth_index"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	Disabled  bool   `json:"disabled"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	Expired   bool   `json:"expired"`
}

// listOwnAuths 读取 CPA 中的凭据列表，只挑出本插件拥有的记录。
func (s *Service) listOwnAuths() ([]authRecord, error) {
	var response struct {
		Files []struct {
			AuthIndex string `json:"auth_index"`
			Name      string `json:"name"`
			Type      string `json:"type"`
			Provider  string `json:"provider"`
			Label     string `json:"label"`
			Email     string `json:"email"`
			Status    string `json:"status"`
			Disabled  bool   `json:"disabled"`
			Path      string `json:"path"`
		} `json:"files"`
	}
	if errCall := s.call("host.auth.list", map[string]any{}, &response); errCall != nil {
		return nil, fail(502, "host_callback", "读取凭据列表失败: "+errCall.Error())
	}
	records := make([]authRecord, 0, len(response.Files))
	for _, file := range response.Files {
		// 只展示本插件的凭据：type 或 provider 命中即可。
		provider := strings.ToLower(strings.TrimSpace(file.Provider))
		fileType := strings.ToLower(strings.TrimSpace(file.Type))
		if provider != Provider && fileType != Provider {
			continue
		}
		record := authRecord{
			Index:    file.AuthIndex,
			Name:     file.Name,
			Label:    file.Label,
			Email:    file.Email,
			Disabled: file.Disabled,
			Status:   file.Status,
		}
		// 读取凭据正文以补全账号与过期信息；失败不影响列表展示。
		if file.AuthIndex != "" {
			if c, errCredential := s.readAuthCredential(file.AuthIndex); errCredential == nil {
				record.AccountID = c.AccountID
				if record.Email == "" {
					record.Email = c.Email
				}
				if !c.ExpiresAt.IsZero() {
					record.ExpiresAt = c.ExpiresAt.UTC().Format("2006-01-02 15:04 MST")
					record.Expired = !nowUTC().Before(c.ExpiresAt)
				}
			}
		}
		if record.Label == "" {
			record.Label = record.Email
		}
		if record.Label == "" {
			record.Label = record.Name
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

func (s *Service) readAuthCredential(authIndex string) (credential, error) {
	var response struct {
		JSON []byte `json:"json"`
	}
	if errCall := s.call("host.auth.get", map[string]any{"auth_index": authIndex}, &response); errCall != nil {
		return credential{}, errCall
	}
	return parseCredential(response.JSON)
}

func (s *Service) handleAuthList() (any, error) {
	records, errList := s.listOwnAuths()
	if errList != nil {
		return nil, errList
	}
	cfg := s.config()
	return jsonResponse(map[string]any{
		"provider": Provider,
		"total":    len(records),
		"auths":    records,
		"models":   cfg.Models,
		"upstream": cfg.UpstreamModel,
		"proxy_chain": map[string]any{
			"enabled":      cfg.ProxyChain.Enabled,
			"local_proxy":  cfg.ProxyChain.LocalProxy,
			"remote_proxy": cfg.ProxyChain.RemoteProxy,
			// 只暴露是否已配置，绝不回传口令。
			"has_remote_credentials": cfg.ProxyChain.RemoteUsername != "" && cfg.ProxyChain.RemotePassword != "",
		},
	}), nil
}

// handleAuthImport 批量导入令牌。
//
// 请求体接受两种形态：
//
//	{"tokens": ["<token1>", "<token2>"]}      纯令牌列表（最常用）
//	{"tokens": [{"access_token": "...", ...}]} 完整 JSON 对象列表
//
// 也接受整段文本（{"text": "..."}），按行/空白切分，便于直接粘贴一大段。
func (s *Service) handleAuthImport(body []byte) (any, error) {
	var request struct {
		Tokens []json.RawMessage `json:"tokens"`
		Text   string            `json:"text"`
	}
	if errUnmarshal := json.Unmarshal(body, &request); errUnmarshal != nil {
		return nil, fail(400, "invalid_request", "导入请求无法解析")
	}

	inputs := make([]string, 0, len(request.Tokens)+1)
	for _, rawToken := range request.Tokens {
		// 既接受字符串字面量，也接受 JSON 对象。
		var text string
		if json.Unmarshal(rawToken, &text) == nil {
			if strings.TrimSpace(text) != "" {
				inputs = append(inputs, text)
			}
			continue
		}
		inputs = append(inputs, string(rawToken))
	}
	if strings.TrimSpace(request.Text) != "" {
		for _, line := range splitTokenText(request.Text) {
			inputs = append(inputs, line)
		}
	}
	if len(inputs) == 0 {
		return nil, fail(400, "invalid_request", "没有提供任何令牌")
	}

	type importResult struct {
		Name      string `json:"name"`
		OK        bool   `json:"ok"`
		AccountID string `json:"account_id,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	results := make([]importResult, 0, len(inputs))
	imported, failed := 0, 0
	// 同一批次内按账号去重，避免重复导入同一个令牌。
	seen := map[string]bool{}

	for index, input := range inputs {
		authJSON, errBuild := ParseTokenInput(input)
		if errBuild != nil {
			failed++
			results = append(results, importResult{Name: fmt.Sprintf("#%d", index+1), Error: errBuild.Error()})
			continue
		}
		c, errCredential := parseCredential(authJSON)
		if errCredential != nil {
			failed++
			results = append(results, importResult{Name: fmt.Sprintf("#%d", index+1), Error: errCredential.Error()})
			continue
		}
		dedupeKey := c.AccountID
		if dedupeKey == "" {
			dedupeKey = shortHash(c.AccessToken)
		}
		if seen[dedupeKey] {
			failed++
			results = append(results, importResult{
				Name:      fmt.Sprintf("#%d", index+1),
				AccountID: c.AccountID,
				Error:     "本批次内重复",
			})
			continue
		}
		seen[dedupeKey] = true

		name := authFileName(c, index)
		var saved struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		// 注意：宿主契约里 json 字段是 json.RawMessage（原始 JSON 对象）。
		// 若传 []byte，encoding/json 会把它编码成 base64 字符串，
		// 宿主拿到字符串而非对象，解析必然失败。
		errSave := s.call("host.auth.save", map[string]any{
			"name": name,
			"json": json.RawMessage(authJSON),
		}, &saved)
		if errSave != nil {
			failed++
			results = append(results, importResult{Name: name, AccountID: c.AccountID, Error: errSave.Error()})
			continue
		}
		imported++
		results = append(results, importResult{Name: name, OK: true, AccountID: c.AccountID})
	}

	return jsonResponse(map[string]any{
		"imported": imported,
		"failed":   failed,
		"results":  results,
	}), nil
}

// authFileName 生成稳定且唯一的凭据文件名。
//
// 用账号 ID 命名便于识别；没有账号 ID 时退化为令牌哈希，
// 保证同一令牌重复导入会覆盖同一文件而不是堆积副本。
func authFileName(c credential, index int) string {
	suffix := ""
	if c.AccountID != "" {
		suffix = sanitizeName(c.AccountID)
	}
	if suffix == "" {
		suffix = shortHash(c.AccessToken)[:16]
	}
	if suffix == "" {
		suffix = fmt.Sprintf("%d", index+1)
	}
	return "gpt365-" + suffix + ".json"
}

// sanitizeName 把账号 ID 之类的取值收敛为安全的文件名字片段。
//
// 除了替换非法字符，还要折叠连续的点和连字符：账号 ID 里若含 ".."，
// 折叠后必须不能形成路径穿越片段。
func sanitizeName(value string) string {
	var builder strings.Builder
	lastDot := false
	lastDash := false
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
			lastDot, lastDash = false, false
		case r == '.':
			// 连续的点折叠为单个，杜绝 ".." 这类穿越片段。
			if !lastDot && builder.Len() > 0 {
				builder.WriteByte('.')
			}
			lastDot, lastDash = true, false
		default:
			// 其余字符（含 '-'、空格、斜杠）统一折叠为单个连字符。
			if !lastDash && !lastDot && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-.")
}

// splitTokenText 把一大段粘贴文本切成候选令牌。
// 支持换行、逗号分隔，并忽略空行与注释行。
func splitTokenText(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// handleAuthDelete 批量删除本插件的凭据。
//
// 插件 ABI 没有提供凭据删除回调（只有 list/get/save），因此这里通过
// host.http.do 调用宿主自身的凭据管理接口，由宿主完成删除并同步运行时记录。
// 删除范围严格限制在 host.auth.list 返回且属于本插件的文件。
func (s *Service) handleAuthDelete(body []byte) (any, error) {
	var request struct {
		Names []string `json:"names"`
		All   bool     `json:"all"`
	}
	if errUnmarshal := json.Unmarshal(body, &request); errUnmarshal != nil {
		return nil, fail(400, "invalid_request", "删除请求无法解析")
	}
	records, errList := s.listOwnAuths()
	if errList != nil {
		return nil, errList
	}

	targets := map[string]bool{}
	for _, name := range request.Names {
		targets[strings.TrimSpace(name)] = true
	}

	// 逐个删除：宿主接口按 name 查询参数接收，支持重复参数。
	deleted, failed := 0, 0
	errors := make([]string, 0)
	for _, record := range records {
		if !request.All && !targets[record.Name] {
			continue
		}
		errDelete := s.deleteAuthFile(record.Name)
		if errDelete != nil {
			failed++
			errors = append(errors, record.Name+": "+errDelete.Error())
			continue
		}
		deleted++
	}
	return jsonResponse(map[string]any{
		"deleted": deleted,
		"failed":  failed,
		"errors":  errors,
	}), nil
}

// deleteAuthFile 调用宿主凭据管理接口删除一个凭据文件。
func (s *Service) deleteAuthFile(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("文件名为空")
	}
	// 只允许删除本插件命名规则下的文件，避免误删其他提供者的凭据。
	if !strings.HasPrefix(name, "gpt365-") || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return fmt.Errorf("拒绝删除不属于本插件的凭据文件: %s", name)
	}
	var response struct {
		StatusCode int    `json:"StatusCode"`
		Body       []byte `json:"Body"`
	}
	endpoint := "/v0/management/auth-files?name=" + urlQueryEscape(name)
	errCall := s.call("host.http.do", map[string]any{
		"method":  http.MethodDelete,
		"url":     endpoint,
		"headers": map[string][]string{"Accept": {"application/json"}},
	}, &response)
	if errCall != nil {
		return fmt.Errorf("%w", errCall)
	}
	if response.StatusCode != 0 && (response.StatusCode < 200 || response.StatusCode >= 300) {
		return fmt.Errorf("宿主返回 HTTP %d: %s", response.StatusCode, clipText(string(response.Body), 200))
	}
	return nil
}

// urlQueryEscape 对查询参数值做最小转义。
func urlQueryEscape(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			builder.WriteByte(c)
		default:
			fmt.Fprintf(&builder, "%%%02X", c)
		}
	}
	return builder.String()
}

func clipText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
