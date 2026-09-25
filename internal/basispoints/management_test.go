package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseTokenInputPlainJWT 纯 access_token 是最常见的导入形态。
func TestParseTokenInputPlainJWT(t *testing.T) {
	token := buildJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-plain",
			"chatgpt_plan_type":  "free",
		},
		"exp": float64(4102444800),
	})
	authJSON, err := ParseTokenInput(token)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	c, errCredential := parseCredential(authJSON)
	if errCredential != nil {
		t.Fatalf("回读失败: %v", errCredential)
	}
	if c.AccessToken != token {
		t.Error("令牌未原样保留")
	}
	if c.AccountID != "acct-plain" {
		t.Errorf("账号 ID = %q，期望 acct-plain", c.AccountID)
	}
	// 凭据文件必须带本插件的 type，否则不会被 auth.parse 接管。
	var root map[string]any
	_ = json.Unmarshal(authJSON, &root)
	if stringValue(root["type"]) != Provider {
		t.Errorf("type = %q，期望 %q", stringValue(root["type"]), Provider)
	}
}

// TestParseTokenInputVariants 覆盖各种粘贴形态。
func TestParseTokenInputVariants(t *testing.T) {
	token := buildJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-x"},
	})
	cases := map[string]string{
		"纯令牌":           token,
		"带空格":           "   " + token + "  ",
		"Bearer 前缀":     "Bearer " + token,
		"access_token:": "access_token: " + token,
		"access_token=": "access_token=" + token,
		"引号包裹":          `"` + token + `"`,
	}
	for name, input := range cases {
		authJSON, err := ParseTokenInput(input)
		if err != nil {
			t.Errorf("%s: 解析失败: %v", name, err)
			continue
		}
		c, errCredential := parseCredential(authJSON)
		if errCredential != nil {
			t.Errorf("%s: 回读失败: %v", name, errCredential)
			continue
		}
		if c.AccessToken != token {
			t.Errorf("%s: 令牌 = %q", name, clipText(c.AccessToken, 40))
		}
	}
}

// TestParseTokenInputJSON 完整 JSON 输入应保留额外字段。
func TestParseTokenInputJSON(t *testing.T) {
	token := buildJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-json"},
	})
	input := `{"access_token":"` + token + `","refresh_token":"rt-123","custom":"kept"}`
	authJSON, err := ParseTokenInput(input)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	var root map[string]any
	_ = json.Unmarshal(authJSON, &root)
	if stringValue(root["refresh_token"]) != "rt-123" {
		t.Error("refresh_token 应被保留")
	}
	if stringValue(root["custom"]) != "kept" {
		t.Error("自定义字段应被保留")
	}
	if stringValue(root["type"]) != Provider {
		t.Errorf("type = %q", stringValue(root["type"]))
	}
}

// TestParseTokenInputRejectsEmpty 空输入必须明确报错。
func TestParseTokenInputRejectsEmpty(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\n", "# 只有注释"} {
		if _, err := ParseTokenInput(input); err == nil {
			t.Errorf("输入 %q 应报错", input)
		}
	}
}

// TestSplitTokenText 整段粘贴按行切分。
func TestSplitTokenText(t *testing.T) {
	text := "tok1\ntok2\n\n# 注释\n  tok3  \ntok4,tok5;tok6\r\ntok7"
	got := splitTokenText(text)
	want := []string{"tok1", "tok2", "tok3", "tok4", "tok5", "tok6", "tok7"}
	if len(got) != len(want) {
		t.Fatalf("切分出 %d 项，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestAuthFileNameStableAndSafe 文件名必须稳定、唯一且不含危险字符。
func TestAuthFileNameStableAndSafe(t *testing.T) {
	token := buildJWT(map[string]any{})
	c := credential{AccessToken: token, AccountID: "acct/../evil id"}
	name := authFileName(c, 0)
	if strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, " ") {
		t.Errorf("文件名含危险字符: %q", name)
	}
	if !strings.HasPrefix(name, "gpt365-") || !strings.HasSuffix(name, ".json") {
		t.Errorf("文件名格式不符: %q", name)
	}
	// 同一凭据必须得到同一文件名，重复导入才能覆盖而非堆积。
	if again := authFileName(c, 5); again != name {
		t.Errorf("同名凭据应得到稳定文件名: %q != %q", again, name)
	}
	// 无账号 ID 时退化为令牌哈希，同样稳定。
	noAccount := credential{AccessToken: token}
	first := authFileName(noAccount, 0)
	second := authFileName(noAccount, 3)
	if first != second {
		t.Errorf("无账号 ID 时应按令牌哈希命名: %q != %q", first, second)
	}
}

// TestManagementRegistrationDeclaresRoutes 必须声明导入与页面路由。
func TestManagementRegistrationDeclaresRoutes(t *testing.T) {
	registration := managementRegistration()
	routes, _ := registration["Routes"].([]map[string]any)
	paths := map[string]string{}
	for _, route := range routes {
		paths[stringValue(route["Path"])] = stringValue(route["Method"])
	}
	for _, want := range []string{routeAuthList, routeAuthImport, routeAuthDelete} {
		if _, exists := paths[want]; !exists {
			t.Errorf("缺少路由 %s", want)
		}
	}
	resources, _ := registration["Resources"].([]map[string]any)
	if len(resources) == 0 {
		t.Fatal("应声明资源页")
	}
	// 资源页路径必须是具体的非空路径段。
	//
	// 宿主 normalizeResourceRoute 会先做 strings.TrimRight(path, "/")，
	// 因此注册成 "/" 会被裁成空串并判为无效、静默丢弃，页面永远不会出现。
	panelPath := stringValue(resources[0]["Path"])
	if panelPath == "" || strings.Trim(panelPath, "/") == "" {
		t.Errorf("资源页路径 %q 会被宿主丢弃：规范化后为空", panelPath)
	}
	if panelPath != resourcePanel {
		t.Errorf("资源页路径 = %q，期望 %q", panelPath, resourcePanel)
	}
}

// TestResourcePathSurvivesHostNormalization 锁定一个曾经真实存在的缺陷。
//
// 宿主 internal/pluginhost/management.go 的 normalizeResourceRoute 会执行
// strings.TrimRight(path, "/") 并在结果为空时返回无效。插件曾把资源页注册为
// "/"，于是被静默丢弃，页面上线后一直 404。这里复现宿主规则以防回归。
func TestResourcePathSurvivesHostNormalization(t *testing.T) {
	registration := managementRegistration()
	resources, _ := registration["Resources"].([]map[string]any)
	if len(resources) == 0 {
		t.Fatal("应声明资源页")
	}
	for _, resource := range resources {
		path := stringValue(resource["Path"])
		if path == "" {
			t.Errorf("资源页路径不能为空")
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// 宿主的规范化：裁掉尾部斜杠，空则无效。
		trimmed := strings.TrimRight(path, "/")
		if trimmed == "" {
			t.Errorf("资源页路径 %q 经宿主 TrimRight 后为空，会被静默丢弃", stringValue(resource["Path"]))
		}
		full := "/v0/resource/plugins/gpt365" + trimmed
		if strings.Contains(full, "..") || strings.ContainsAny(full, " \t") {
			t.Errorf("资源页完整路径非法: %q", full)
		}
	}
}

// TestManagementHandleServesPage 资源页请求应返回 HTML。
func TestManagementHandleServesPage(t *testing.T) {
	service := NewService()
	request, _ := json.Marshal(map[string]any{
		"Method": "GET",
		"Path":   "/v0/resource/plugins/gpt365/panel",
	})
	result, err := service.Handle("management.handle", request)
	if err != nil {
		t.Fatalf("处理失败: %v", err)
	}
	response := objectValue(result)
	body, _ := response["Body"].([]byte)
	html := string(body)
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("应返回 HTML 页面")
	}
	// 页面必须包含导入入口，且不得内联任何真实令牌。
	if !strings.Contains(html, "导入") {
		t.Error("页面应包含导入入口")
	}
	if strings.Contains(html, "eyJhbGciOiJSUzI1NiIsImtpZCI6Im4wejZQcjE") {
		t.Error("页面不得包含任何真实令牌")
	}
	headers := response["Headers"].(map[string][]string)
	if len(headers["Content-Type"]) == 0 || !strings.Contains(headers["Content-Type"][0], "text/html") {
		t.Errorf("Content-Type 错误: %v", headers)
	}
}

// TestManagementHandleRejectsUnknown 未知路径必须明确报错。
func TestManagementHandleRejectsUnknown(t *testing.T) {
	service := NewService()
	request, _ := json.Marshal(map[string]any{
		"Method": "POST",
		"Path":   "/v0/management/plugins/gpt365/nope",
	})
	if _, err := service.Handle("management.handle", request); err == nil {
		t.Fatal("未知路径应报错")
	}
}

// TestDeleteGuardsForeignFiles 删除必须严格限制在本插件文件内。
func TestDeleteGuardsForeignFiles(t *testing.T) {
	service := NewService()
	for _, name := range []string{
		"codex.json",
		"gpt365-evil.txt",
		"",
		"../gpt365-escape.json",
		"other-provider-1.json",
	} {
		if err := service.deleteAuthFile(name); err == nil {
			t.Errorf("文件名 %q 应被拒绝删除", name)
		}
	}
}

// TestBuildAuthFileJSONMarksProvider 生成的凭据文件必须带本插件 type。
func TestBuildAuthFileJSONMarksProvider(t *testing.T) {
	c := credential{
		AccessToken: "tok",
		AccountID:   "acct-1",
		Email:       "a@b.c",
	}
	raw, err := buildAuthFileJSON("", c)
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	if stringValue(root["type"]) != Provider {
		t.Errorf("type = %q，期望 %q", stringValue(root["type"]), Provider)
	}
	if stringValue(root["account_id"]) != "acct-1" {
		t.Errorf("account_id = %q", stringValue(root["account_id"]))
	}
	if stringValue(root["email"]) != "a@b.c" {
		t.Errorf("email = %q", stringValue(root["email"]))
	}
}
