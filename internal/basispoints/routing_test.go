package basispoints

import (
	"encoding/json"
	"testing"
)

// TestAuthDataCarriesRoutingFields 验证 auth.parse 产出的记录带齐路由字段。
//
// 宿主用这些字段决定该凭据归属哪个 provider、以及能否匹配插件的模型提供者：
//   - Attributes["auth_kind"] 决定 AuthKind()，进而决定模型注册的分类
//   - Provider 决定 auth.Provider，ModelsForAuth 用它匹配插件 identifier
//
// 缺任何一个，账号的模型列表都可能为空。
func TestAuthDataCarriesRoutingFields(t *testing.T) {
	raw := []byte(`{"type":"gpt365","access_token":"` + buildJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
		"exp":                         float64(4102444800),
	}) + `","account_id":"acct-1"}`)

	request, _ := json.Marshal(map[string]any{
		"Provider": Provider,
		"FileName": "gpt365-acct-1.json",
		"RawJSON":  raw,
	})
	result, err := authParse(request)
	if err != nil {
		t.Fatalf("authParse 失败: %v", err)
	}
	auth := objectValue(result["Auth"])

	if stringValue(auth["Provider"]) != Provider {
		t.Errorf("Provider = %q，期望 %q", stringValue(auth["Provider"]), Provider)
	}
	if got := attrValue(auth["Attributes"], "auth_kind"); got == "" {
		t.Error("Attributes 缺少 auth_kind，宿主无法判定凭据类型（会影响模型注册分类）")
	}
	if got := attrValue(auth["Attributes"], "account_id"); got != "acct-1" {
		t.Errorf("Attributes.account_id = %q", got)
	}
	// Metadata 里的 type 必须与 Provider 一致，否则宿主归类到别的 provider。
	metadata := objectValue(auth["Metadata"])
	if stringValue(metadata["type"]) != Provider {
		t.Errorf("Metadata.type = %q，期望 %q", stringValue(metadata["type"]), Provider)
	}

	encoded, _ := json.Marshal(auth)
	t.Logf("auth.parse 产出: %s", clipText(string(encoded), 700))
}
