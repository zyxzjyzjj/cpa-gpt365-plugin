package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleToken = "eyJhbGciOiJSUzI1NiJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjLTEyMyIsImNoYXRncHRfcGxhb l90eXBlIjoiZnJlZSJ9fQ.sig"

// TestParseCredentialFromJWT 验证账号 ID 优先来自 JWT 的 auth 声明。
func TestParseCredentialFromJWT(t *testing.T) {
	claims := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-from-jwt",
			"chatgpt_plan_type":  "free",
		},
		"exp": float64(4102444800), // 2100-01-01
	}
	raw, _ := json.Marshal(map[string]any{
		"access_token": buildJWT(claims),
		"email":        "user@example.com",
	})
	c, err := parseCredential(raw)
	if err != nil {
		t.Fatalf("parseCredential 失败: %v", err)
	}
	if c.AccountID != "acct-from-jwt" {
		t.Errorf("账号 ID = %q，期望 acct-from-jwt", c.AccountID)
	}
	if c.AuthMode != "chatgpt" {
		t.Errorf("auth_mode = %q，期望 chatgpt", c.AuthMode)
	}
	if c.Email != "user@example.com" {
		t.Errorf("email = %q", c.Email)
	}
	if c.ExpiresAt.IsZero() {
		t.Error("exp 未被解析")
	}
}

// TestParseCredentialNestedTokenData 覆盖 token_data 嵌套与 account_id 回退。
func TestParseCredentialNestedTokenData(t *testing.T) {
	raw := []byte(`{"token_data":{"access_token":"` + buildJWT(map[string]any{"exp": float64(4102444800)}) + `"},"account_id":"acct-fallback"}`)
	c, err := parseCredential(raw)
	if err != nil {
		t.Fatalf("parseCredential 失败: %v", err)
	}
	if c.AccountID != "acct-fallback" {
		t.Errorf("账号 ID = %q，期望 acct-fallback", c.AccountID)
	}
}

// TestParseCredentialRejectsMissingToken 缺令牌必须明确报错，不得静默通过。
func TestParseCredentialRejectsMissingToken(t *testing.T) {
	if _, err := parseCredential([]byte(`{"foo":"bar"}`)); err == nil {
		t.Fatal("缺少 access_token 时应报错")
	}
}

// TestNormalizeEffortMapping 验证上游没有 max 挡位时的归一化。
func TestNormalizeEffortMapping(t *testing.T) {
	cases := map[string]string{
		"max":         "xhigh",
		"maximum":     "xhigh",
		"x-high":      "xhigh",
		"extra_high":  "xhigh",
		"ultra":       "ultra",
		"high":        "high",
		"low":         "low",
		"minimal":     "low",
		"bogus":       "medium",
		"":            "medium",
		"HIGH":        "high",
		"  xhigh  ":   "xhigh",
		"MAX":         "xhigh",
		"unsupported": "medium",
	}
	for input, want := range cases {
		if got := normalizeEffort(input); got != want {
			t.Errorf("normalizeEffort(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestConfigNormalizeRejectsBadMapping 别名未列入 models 时必须拒绝。
func TestConfigNormalizeRejectsBadMapping(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ModelMappings = map[string]string{"unknown-alias": "gpt-6-astra"}
	if err := cfg.normalize(); err == nil {
		t.Fatal("未列入 models 的别名应导致配置校验失败")
	}
}

func TestConfigNormalizeDefaults(t *testing.T) {
	clearProxyEnv(t)
	cfg := Config{}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("空配置应能补全默认值: %v", err)
	}
	if cfg.ResponsesURL != DefaultResponsesURL {
		t.Errorf("responses_url = %q", cfg.ResponsesURL)
	}
	if len(cfg.Models) != 1 || cfg.Models[0] != DefaultModelID {
		t.Errorf("models = %v", cfg.Models)
	}
	if cfg.ProxyChain.LocalProxy != DefaultLocalProxy {
		t.Errorf("local_proxy = %q", cfg.ProxyChain.LocalProxy)
	}
}

// TestConfigRejectsNonHTTPProxy 覆盖非法代理协议。
//
// 必须清空代理相关环境变量：环境变量优先级高于配置，若残留会覆盖测试
// 设置的非法值，使这条校验被绕过。
func TestConfigRejectsNonHTTPProxy(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ProxyChain.LocalProxy = "socks5://127.0.0.1:1080"
	if err := cfg.normalize(); err == nil {
		t.Fatal("非 http/https 代理应被拒绝")
	}
}

// clearProxyEnv 清空所有代理相关环境变量，保证测试不受运行环境影响。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		envLocalProxy, envRemoteProxy, envRemoteUsername, envRemotePassword,
		envResponsesURL, envUpstreamModel,
	} {
		t.Setenv(name, "")
	}
}

// TestResolveUpstreamModel 覆盖别名与已解析名称两种入参。
func TestResolveUpstreamModel(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ModelMappings = map[string]string{
		"gpt-6-astra-basispoints": "gpt-6-astra",
		"gpt-5.6-sol-basispoints": "gpt-5.6-sol",
	}
	cfg.Models = []string{"gpt-6-astra-basispoints", "gpt-5.6-sol-basispoints"}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize 失败: %v", err)
	}
	for input, want := range map[string]string{
		"gpt-6-astra-basispoints": "gpt-6-astra",
		"gpt-5.6-sol-basispoints": "gpt-5.6-sol",
		"gpt-6-astra":             "gpt-6-astra",
	} {
		got, ok := cfg.resolveUpstreamModel(input)
		if !ok || got != want {
			t.Errorf("resolveUpstreamModel(%q) = (%q,%v)，期望 %q", input, got, ok, want)
		}
	}
	if _, ok := cfg.resolveUpstreamModel("not-enabled"); ok {
		t.Error("未启用的模型不应被解析成功")
	}
}

// TestPrepareResponsesBodyStripsTools 确认客户端 tools 被剥离并转成目录说明。
func TestPrepareResponsesBodyStripsTools(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.Models = []string{"gpt-6-astra-basispoints"}
	cfg.ModelMappings = map[string]string{"gpt-6-astra-basispoints": "gpt-6-astra"}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize 失败: %v", err)
	}
	source := map[string]any{
		"model": "gpt-6-astra-basispoints",
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "查一下天气"}}},
		},
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "get_weather",
				"description": "查询城市天气",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"city": map[string]any{"type": "string"}},
					"required":   []any{"city"},
				},
			},
		},
	}
	body, err := prepareResponsesBody(source, cfg)
	if err != nil {
		t.Fatalf("prepareResponsesBody 失败: %v", err)
	}
	// 上游会硬拒 tools，因此改写后的请求绝不能带 tools。
	if _, exists := body["tools"]; exists {
		t.Error("改写后的请求不应包含 tools")
	}
	if body["model"] != "gpt-6-astra" {
		t.Errorf("model = %v，期望 gpt-6-astra", body["model"])
	}
	items, _ := body["input"].([]any)
	if len(items) < 2 {
		t.Fatalf("input 项数 = %d，期望包含 developer 说明与用户消息", len(items))
	}
	// 首项应是 developer 消息，且必须包含客户端工具目录。
	first := objectValue(items[0])
	if stringValue(first["role"]) != "developer" {
		t.Errorf("首项角色 = %q，期望 developer", stringValue(first["role"]))
	}
	all := string(jsonBytes(items))
	if !strings.Contains(all, "get_weather") {
		t.Error("developer 说明中应包含客户端工具目录 get_weather")
	}
	if !strings.Contains(all, transportName) {
		t.Error("developer 说明中应约定使用 run_officejs 作为运输载体")
	}
	if _, exists := body["metadata"]; !exists {
		t.Error("应生成 metadata（含 task_id / turn_id / agent_iteration）")
	}
}

// TestTurnIDStableAcrossToolResultRounds 是避免上游死循环的核心约束：
// 工具结果回合必须保持 turn_id 恒定，只递增 agent_iteration。
func TestTurnIDStableAcrossToolResultRounds(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.Models = []string{"gpt-6-astra-basispoints"}
	cfg.ModelMappings = map[string]string{"gpt-6-astra-basispoints": "gpt-6-astra"}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize 失败: %v", err)
	}
	base := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "跑一下命令"}}},
	}
	round1 := map[string]any{"model": "gpt-6-astra-basispoints", "input": base}
	body1, err := prepareResponsesBody(round1, cfg)
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	// 第二轮：追加一个工具调用与结果，属于同一用户回合。
	withResult := append(append([]any{}, base...),
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": `{"cmd":"pwd"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "/home"},
	)
	round2 := map[string]any{"model": "gpt-6-astra-basispoints", "input": withResult}
	body2, err := prepareResponsesBody(round2, cfg)
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	m1 := objectValue(body1["metadata"])
	m2 := objectValue(body2["metadata"])
	if stringValue(m1["turn_id"]) != stringValue(m2["turn_id"]) {
		t.Errorf("turn_id 应保持恒定：%q != %q", stringValue(m1["turn_id"]), stringValue(m2["turn_id"]))
	}
	if stringValue(m1["task_id"]) != stringValue(m2["task_id"]) {
		t.Error("task_id 应保持恒定")
	}
	if stringValue(m2["agent_iteration"]) != "2" {
		t.Errorf("agent_iteration = %q，期望 2", stringValue(m2["agent_iteration"]))
	}
}

// TestExtractNativeClientToolCall 验证从 run_officejs 中抠出内层真实工具。
func TestExtractNativeClientToolCall(t *testing.T) {
	specs := map[string]toolSpec{
		"get_weather": {
			Key:  "get_weather",
			Name: "get_weather",
			Type: "function",
			Spec: map[string]any{
				"type": "function",
				"name": "get_weather",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"city": map[string]any{"type": "string"}},
					"required":   []any{"city"},
				},
			},
		},
	}
	inner := `{"tool":"get_weather","args":{"city":"Tokyo"}}`
	outer, _ := json.Marshal(map[string]any{
		"summary":          "Get current weather for Tokyo",
		"code":             inner,
		"destructive":      false,
		"references":       []any{"Tokyo weather"},
		"extended_summary": "relay",
	})
	native := map[string]any{
		"type":      "function_call",
		"id":        "fc_abc",
		"call_id":   "call_abc",
		"name":      transportName,
		"arguments": string(outer),
	}
	call, ok := extractNativeClientToolCall(native, specs)
	if !ok {
		t.Fatal("应成功提取内层客户端工具调用")
	}
	if stringValue(call["name"]) != "get_weather" {
		t.Errorf("name = %q", stringValue(call["name"]))
	}
	if stringValue(call["call_id"]) != "call_abc" {
		t.Errorf("call_id = %q", stringValue(call["call_id"]))
	}
	args := parseArgumentsObject(call["arguments"])
	if stringValue(args["city"]) != "Tokyo" {
		t.Errorf("arguments.city = %q，期望 Tokyo", stringValue(args["city"]))
	}
}

// TestExtractRejectsUnknownTool 目录外的工具必须拒绝，不得放行给客户端。
func TestExtractRejectsUnknownTool(t *testing.T) {
	specs := map[string]toolSpec{"known": {Key: "known", Name: "known", Type: "function"}}
	outer, _ := json.Marshal(map[string]any{"code": `{"tool":"evil","args":{}}`})
	native := map[string]any{
		"type":      "function_call",
		"call_id":   "call_x",
		"name":      transportName,
		"arguments": string(outer),
	}
	if _, ok := extractNativeClientToolCall(native, specs); ok {
		t.Fatal("目录外的工具不应被提取")
	}
}

// TestExtractRejectsNestedTransport 内层再次出现运输工具时必须拒绝，防止自引用。
func TestExtractRejectsNestedTransport(t *testing.T) {
	specs := map[string]toolSpec{"known": {Key: "known", Name: "known", Type: "function"}}
	innerOuter, _ := json.Marshal(map[string]any{"code": `{"tool":"run_officejs","args":{}}`})
	outer, _ := json.Marshal(map[string]any{"code": string(innerOuter)})
	native := map[string]any{
		"type":      "function_call",
		"call_id":   "call_y",
		"name":      transportName,
		"arguments": string(outer),
	}
	if _, ok := extractNativeClientToolCall(native, specs); ok {
		t.Fatal("嵌套的运输工具不应被提取")
	}
}

// TestSchemaMatches 覆盖参数校验的关键分支。
func TestSchemaMatches(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []any{"city"},
	}
	if !schemaMatches(map[string]any{"city": "Tokyo"}, schema) {
		t.Error("合法参数应通过校验")
	}
	if schemaMatches(map[string]any{}, schema) {
		t.Error("缺少必需字段应校验失败")
	}
	if schemaMatches(map[string]any{"city": 1}, schema) {
		t.Error("字段类型不符应校验失败")
	}
	if !schemaMatches(map[string]any{"city": "A"}, map[string]any{}) {
		t.Error("空 schema 应放行")
	}
}

// TestAuthParseProducesNativeAndVirtual 确认同时产出原生 Codex 与本插件记录。
func TestAuthParseProducesNativeAndVirtual(t *testing.T) {
	raw := []byte(`{"access_token":"` + buildJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
		"exp":                         float64(4102444800),
	}) + `","refresh_token":"rt"}`)
	request, _ := json.Marshal(map[string]any{
		"Provider": "codex",
		"FileName": "chatgpt.json",
		"RawJSON":  raw,
	})
	result, err := authParse(request)
	if err != nil {
		t.Fatalf("authParse 失败: %v", err)
	}
	if result["Handled"] != true {
		t.Fatal("应声明已处理 codex 凭据")
	}
	auths, _ := result["Auths"].([]any)
	if len(auths) != 2 {
		t.Fatalf("应产出 2 条认证（原生 + 虚拟），实际 %d", len(auths))
	}
	native := objectValue(auths[0])
	virtual := objectValue(auths[1])
	if stringValue(native["Provider"]) != AuthProviderID {
		t.Errorf("原生记录 Provider = %q，期望 %q", stringValue(native["Provider"]), AuthProviderID)
	}
	if stringValue(virtual["Provider"]) != Provider {
		t.Errorf("虚拟记录 Provider = %q，期望 %q", stringValue(virtual["Provider"]), Provider)
	}
	if !strings.HasPrefix(stringValue(virtual["ID"]), "bp-") {
		t.Errorf("虚拟记录 ID = %q，应带 bp- 前缀", stringValue(virtual["ID"]))
	}
}

// TestAuthParseIgnoresForeignProvider 其他提供者的凭据必须放行给别的插件。
func TestAuthParseIgnoresForeignProvider(t *testing.T) {
	request, _ := json.Marshal(map[string]any{"Provider": "codearts", "RawJSON": []byte(`{}`)})
	result, err := authParse(request)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if result["Handled"] != false {
		t.Error("非本插件负责的提供者应返回 Handled=false")
	}
}

// TestRedactTokenMessage 令牌不得出现在错误文本中。
func TestRedactTokenMessage(t *testing.T) {
	secret := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature"
	got := redactTokenMessage("upstream said: Bearer " + secret + " is invalid")
	if strings.Contains(got, secret) {
		t.Errorf("令牌未被抹除: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("应保留抹除标记: %s", got)
	}
}

// TestParseProxyEndpoint 覆盖代理地址解析与内嵌凭据。
func TestParseProxyEndpoint(t *testing.T) {
	endpoint, err := parseProxyEndpoint("http://user:pass@proxy.example.com:10000")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if endpoint.address != "proxy.example.com:10000" {
		t.Errorf("address = %q", endpoint.address)
	}
	if endpoint.auth == "" {
		t.Error("内嵌凭据应被解析为认证头")
	}
	if endpoint.tls {
		t.Error("http 不应标记为 TLS")
	}
	if _, errDefault := parseProxyEndpoint("http://proxy.example.com"); errDefault != nil {
		t.Errorf("缺省端口应被补全: %v", errDefault)
	}
	if _, errBad := parseProxyEndpoint("ftp://x"); errBad == nil {
		t.Error("非 http/https 应报错")
	}
}

// TestParseFinalStreamResponse 从 SSE 流中取最终响应。
func TestParseFinalStreamResponse(t *testing.T) {
	stream := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant"}]}}` + "\n\n" +
		"data: [DONE]\n\n"
	response, err := parseFinalStreamResponse([]byte(stream))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if stringValue(response["status"]) != "completed" {
		t.Errorf("status = %q", stringValue(response["status"]))
	}
}

func TestParseFinalStreamResponseRejectsEmpty(t *testing.T) {
	if _, err := parseFinalStreamResponse([]byte("   ")); err == nil {
		t.Fatal("空流应报错")
	}
}

// TestUpstreamErrorSurfacesNotEnabled 账号无权限时必须给出可执行的判断。
func TestUpstreamErrorSurfacesNotEnabled(t *testing.T) {
	body := []byte(`{"error":{"type":"not_enabled","message":"Access not enabled"}}`)
	err := upstreamRequestError(429, body, map[string]any{"reasoning_effort": "high"}, credential{})
	if err == nil {
		t.Fatal("应返回错误")
	}
	message := err.Error()
	if !strings.Contains(message, "429") {
		t.Errorf("应包含状态码: %s", message)
	}
	if !strings.Contains(message, "not_enabled") && !strings.Contains(message, "未获得") {
		t.Errorf("应说明账号无权限: %s", message)
	}
}

// buildJWT 用给定声明构造一个结构合法（不校验签名）的 JWT。
func buildJWT(claims map[string]any) string {
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64URL(jsonBytes(claims))
	return header + "." + payload + ".signature"
}
