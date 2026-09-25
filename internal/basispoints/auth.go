package basispoints

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// 本文件负责从 CPA 既有的 ChatGPT/Codex OAuth 凭据中取出访问令牌与账号 ID。
// 插件只在内存中读取，不生成、不改写任何凭据文件。

type authParseRequest struct {
	Provider string `json:"Provider"`
	Path     string `json:"Path"`
	FileName string `json:"FileName"`
	RawJSON  []byte `json:"RawJSON"`
}

type authRefreshRequest struct {
	AuthID      string         `json:"AuthID"`
	StorageJSON []byte         `json:"StorageJSON"`
	Metadata    map[string]any `json:"Metadata"`
}

type credential struct {
	AccessToken string
	AccountID   string
	AuthMode    string
	Email       string
	ExpiresAt   time.Time
}

// parseCredential 从凭据 JSON 中提取所需字段。
// 账号 ID 优先取自 JWT 的 auth 声明，其次回退到凭据本身的字段。
func parseCredential(raw []byte) (credential, error) {
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil || root == nil {
		return credential{}, fail(400, "invalid_auth", "OAuth 凭据不是合法的 JSON")
	}
	token := findToken(root)
	if token == "" {
		return credential{}, fail(401, "invalid_auth", "ChatGPT OAuth 凭据中缺少 access_token")
	}
	claims := jwtPayload(token)
	accountID := accountIDFromClaims(claims)
	if accountID == "" {
		accountID = findAccountID(root)
	}
	if accountID == "" {
		return credential{}, fail(401, "invalid_auth", "ChatGPT OAuth 凭据中缺少账号 ID")
	}
	authMode := firstString(root, "auth_mode", "authMode")
	if !strings.EqualFold(authMode, "chatgpt") {
		authMode = "chatgpt"
	}
	email := firstString(root, "email")
	if email == "" {
		email = stringValue(claims["email"])
	}
	if email == "" {
		if profile := objectValue(claims["https://api.openai.com/profile"]); profile != nil {
			email = stringValue(profile["email"])
		}
	}
	expiresAt := jwtExpiry(claims)
	if expiresAt.IsZero() {
		expiresAt = timeFromValue(firstValue(root, "expires_at", "expired"))
	}
	return credential{
		AccessToken: token,
		AccountID:   accountID,
		AuthMode:    authMode,
		Email:       email,
		ExpiresAt:   expiresAt,
	}, nil
}

func findToken(root map[string]any) string {
	for _, key := range []string{"access_token", "accessToken"} {
		if token := stringValue(root[key]); token != "" {
			return strings.TrimPrefix(strings.TrimSpace(token), "Bearer ")
		}
	}
	for _, key := range []string{"token_data", "tokenData", "sessionInfo", "session_info", "oauth", "tokens"} {
		if nested := objectValue(root[key]); nested != nil {
			if token := findToken(nested); token != "" {
				return token
			}
		}
	}
	return ""
}

func findAccountID(root map[string]any) string {
	for _, key := range []string{"userInfo", "user_info", "auth", "token_data", "tokenData", "sessionInfo", "session_info"} {
		if nested := objectValue(root[key]); nested != nil {
			if id := firstString(nested, "chatgpt_account_id", "account_id", "accountId"); id != "" {
				return id
			}
		}
	}
	return firstString(root, "chatgpt_account_id", "account_id", "accountId")
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(object[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

// jwtPayload 解出 JWT 的载荷段。签名不做校验：这里只读取声明，
// 令牌的真实性由上游负责判定。
func jwtPayload(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return map[string]any{}
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return map[string]any{}
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil || claims == nil {
		return map[string]any{}
	}
	return claims
}

func accountIDFromClaims(claims map[string]any) string {
	if auth := objectValue(claims["https://api.openai.com/auth"]); auth != nil {
		if accountID := firstString(auth, "chatgpt_account_id", "account_id"); accountID != "" {
			return accountID
		}
	}
	return firstString(claims, "chatgpt_account_id", "account_id")
}

func jwtExpiry(claims map[string]any) time.Time {
	if claims == nil {
		return time.Time{}
	}
	if value, ok := claims["exp"]; ok {
		return timeFromValue(value)
	}
	return time.Time{}
}

func timeFromValue(value any) time.Time {
	switch number := value.(type) {
	case json.Number:
		if seconds, err := number.Int64(); err == nil && seconds > 0 {
			return time.Unix(seconds, 0)
		}
	case float64:
		if number > 0 {
			return time.Unix(int64(number), 0)
		}
	case int64:
		if number > 0 {
			return time.Unix(number, 0)
		}
	case string:
		if timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(number)); err == nil {
			return timestamp
		}
	}
	return time.Time{}
}

// credentialID 生成 CPA 内用于区分账号的稳定标识。
// 前缀 bp- 使 Basis Points 记录与原生 Codex 记录不会互相覆盖。
func credentialID(fileName string) string {
	base := strings.TrimSpace(filepath.Base(fileName))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		base = "chatgpt"
	}
	var builder strings.Builder
	builder.WriteString("bp-")
	for _, character := range base {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	return builder.String()
}

// authData 构造本插件的认证记录。
//
// Attributes 里的 account_id 会进入执行器请求的 AuthAttributes，
// 执行阶段据此还原账号；Metadata 供管理界面展示。
func authData(raw []byte, fileName string, c credential) map[string]any {
	label := c.Email
	if label == "" {
		label = c.AccountID
	}
	if label == "" {
		label = fileName
	}
	metadata := map[string]any{
		"type":       Provider,
		"auth_kind":  "oauth",
		"account_id": c.AccountID,
		"auth_mode":  c.AuthMode,
	}
	attributes := map[string]string{
		"auth_kind": "oauth",
		"auth_mode": c.AuthMode,
	}
	if c.AccountID != "" {
		attributes["account_id"] = c.AccountID
	}
	if c.Email != "" {
		metadata["email"] = c.Email
	}
	if planType := codexPlanType(raw, c.AccessToken); planType != "" {
		metadata["plan_type"] = planType
		attributes["plan_type"] = planType
	}
	if !c.ExpiresAt.IsZero() {
		metadata["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"Provider":    Provider,
		"ID":          credentialID(fileName),
		"FileName":    fileName,
		"Label":       label,
		"StorageJSON": raw,
		"Metadata":    metadata,
		"Attributes":  attributes,
	}
}

// codexPlanType 从凭据或令牌中读取套餐类型，用于界面展示。
func codexPlanType(raw []byte, accessToken string) string {
	var root map[string]any
	if json.Unmarshal(raw, &root) == nil {
		if planType := firstString(root, "plan_type", "planType"); planType != "" {
			return planType
		}
	}
	idClaims := jwtPayload(stringValue(root["id_token"]))
	if auth := objectValue(idClaims["https://api.openai.com/auth"]); auth != nil {
		if planType := firstString(auth, "chatgpt_plan_type", "plan_type"); planType != "" {
			return planType
		}
	}
	claims := jwtPayload(accessToken)
	if auth := objectValue(claims["https://api.openai.com/auth"]); auth != nil {
		return firstString(auth, "chatgpt_plan_type", "plan_type")
	}
	return ""
}

// authParse 解析属于本插件的凭据文件。
//
// 只接管 type 为 gpt365 的文件：凭据文件里的 type 字段由使用者或本插件的
// 导入接口写入，因此不会与 Codex 等其他提供者互相干扰。
func authParse(raw []byte) (map[string]any, error) {
	var request authParseRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "auth.parse 请求无法解析")
	}
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	// 只认自己的 provider，其余一律交给别的插件处理。
	if provider != "" && provider != Provider {
		return map[string]any{"Handled": false}, nil
	}
	fileName := strings.TrimSpace(request.FileName)
	if fileName == "" {
		fileName = filepath.Base(strings.TrimSpace(request.Path))
	}
	if fileName == "" {
		fileName = "gpt365.json"
	}
	c, err := parseCredential(request.RawJSON)
	if err != nil {
		if provider == Provider {
			return nil, err
		}
		return map[string]any{"Handled": false}, nil
	}
	return map[string]any{"Handled": true, "Auth": authData(request.RawJSON, fileName, c)}, nil
}

// ParseTokenInput 解析用户提供的单条凭据输入。
//
// 支持三种常见形态，便于批量导入时直接粘贴：
//
//  1. 纯 access_token（JWT 或任意字符串）—— 最常见
//  2. 形如 "access_token: xxx" 或 "access_token=xxx" 的单行键值
//  3. 完整 JSON 对象，含 access_token / account_id 等字段
//
// 返回的凭据 JSON 会被规范化为 CPA 凭据文件格式（带 type 字段）。
func ParseTokenInput(input string) ([]byte, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fail(400, "invalid_token", "令牌为空")
	}

	// 形态 3：完整 JSON。
	if strings.HasPrefix(trimmed, "{") {
		c, errParse := parseCredential([]byte(trimmed))
		if errParse != nil {
			return nil, errParse
		}
		return buildAuthFileJSON(trimmed, c)
	}

	// 形态 2：单行键值。仅剥离前缀，其余原样作为令牌。
	token := trimmed
	if index := strings.Index(token, ":"); index >= 0 && !strings.Contains(token[:index], ".") {
		token = strings.TrimSpace(token[index+1:])
	} else if index := strings.Index(token, "="); index >= 0 && !strings.Contains(token[:index], ".") {
		token = strings.TrimSpace(token[index+1:])
	}
	token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(token), "Bearer "))
	token = strings.Trim(strings.TrimSpace(token), `"'`)
	if token == "" {
		return nil, fail(400, "invalid_token", "令牌为空")
	}

	// 形态 1：纯令牌。账号 ID 由 JWT 声明推导。
	c, errParse := parseCredential(jsonBytes(map[string]any{"access_token": token}))
	if errParse != nil {
		return nil, errParse
	}
	return buildAuthFileJSON("", c)
}

// buildAuthFileJSON 生成 CPA 凭据文件内容。
//
// type 字段必须写成本插件的 provider，这样文件才会被 auth.parse 接管；
// 同时保留原始令牌，使 CPA 能把它作为持久化内容交给执行器。
func buildAuthFileJSON(original string, c credential) ([]byte, error) {
	payload := map[string]any{
		"type":         Provider,
		"access_token": c.AccessToken,
	}
	if c.AccountID != "" {
		payload["account_id"] = c.AccountID
	}
	if c.Email != "" {
		payload["email"] = c.Email
	}
	if !c.ExpiresAt.IsZero() {
		payload["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	// 原始 JSON 中的额外字段（refresh_token 等）一并保留，避免丢信息。
	if strings.HasPrefix(strings.TrimSpace(original), "{") {
		var root map[string]any
		if json.Unmarshal([]byte(original), &root) == nil {
			for key, value := range root {
				if _, exists := payload[key]; !exists {
					payload[key] = value
				}
			}
			payload["type"] = Provider
		}
	}
	return json.MarshalIndent(payload, "", "  ")
}

// AuthFileJSON 供管理接口构造凭据文件内容。
func AuthFileJSON(c credential) ([]byte, error) { return buildAuthFileJSON("", c) }

// AccountIDFromToken 从令牌推导账号 ID，用于导入时去重与命名。
func AccountIDFromToken(token string) string {
	claims := jwtPayload(token)
	return accountIDFromClaims(claims)
}

func authRefresh(raw []byte) (map[string]any, error) {
	var request authRefreshRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "auth.refresh 请求无法解析")
	}
	c, err := parseCredential(request.StorageJSON)
	if err != nil {
		return nil, err
	}
	if !c.ExpiresAt.IsZero() && !time.Now().Before(c.ExpiresAt) {
		return nil, fail(401, "auth_expired", "ChatGPT OAuth 访问令牌已过期，请先在 CPA 中更新或重新导入凭据")
	}
	fileName := strings.TrimSpace(request.AuthID)
	if fileName == "" {
		fileName = "chatgpt.json"
	}
	if !strings.HasSuffix(strings.ToLower(fileName), ".json") {
		fileName += ".json"
	}
	// 令牌由 CPA 的其他通道刷新；这里只在临近过期时提示复查，
	// 不伪造新的令牌，避免把无效凭据写回。
	next := time.Now().Add(30 * time.Minute)
	if !c.ExpiresAt.IsZero() {
		next = c.ExpiresAt.Add(-10 * time.Minute)
		if next.Before(time.Now().Add(time.Minute)) {
			next = time.Now().Add(time.Minute)
		}
	}
	return map[string]any{
		"Auth":             authData(request.StorageJSON, fileName, c),
		"NextRefreshAfter": next.UTC(),
	}, nil
}

// credentialFromExecutor 在执行阶段取回凭据。
func credentialFromExecutor(request ExecutorRequest) (credential, error) {
	if len(request.StorageJSON) > 0 {
		return parseCredential(request.StorageJSON)
	}
	metadata := map[string]any{}
	for key, value := range request.AuthMetadata {
		metadata[key] = value
	}
	for key, value := range request.AuthAttributes {
		metadata[key] = value
	}
	if token := stringValue(metadata["access_token"]); token != "" {
		data := map[string]any{"access_token": token}
		if accountID := stringValue(metadata["account_id"]); accountID != "" {
			data["account_id"] = accountID
		}
		return parseCredential(jsonBytes(data))
	}
	return credential{}, fail(401, "missing_auth", "CPA 未提供 ChatGPT OAuth 凭据")
}

// redactTokenMessage 从错误文本中抹掉可能出现的 Bearer 令牌。
//
// 注意不能用「替换后从头再搜」的写法：替换结果本身仍含 "Bearer "，
// 会原地打转成为死循环。这里每次从已处理位置之后继续扫描。
func redactTokenMessage(message string) string {
	const marker = "[REDACTED]"
	var builder strings.Builder
	cursor := 0
	for cursor < len(message) {
		relative := strings.Index(strings.ToLower(message[cursor:]), "bearer ")
		if relative < 0 {
			builder.WriteString(message[cursor:])
			break
		}
		start := cursor + relative
		prefixEnd := start + len("bearer ")
		builder.WriteString(message[cursor:prefixEnd])
		builder.WriteString(marker)
		// 跳过令牌本体，直到分隔符为止。
		end := prefixEnd
		for end < len(message) && message[end] != ' ' && message[end] != '"' && message[end] != '\n' && message[end] != '\r' {
			end++
		}
		cursor = end
	}
	return builder.String()
}
