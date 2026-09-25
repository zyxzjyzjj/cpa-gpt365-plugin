package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModelForAuthResponseShape 验证 model.for_auth 的响应结构。
//
// 宿主 ModelsForAuth 走的是 model.for_auth，期望 pluginapi.ModelResponse：
//
//	{"Provider": "...", "Models": [...]}
//
// 若字段名或结构不匹配，宿主会解析出空模型列表，账号的模型列表就是空的。
func TestModelForAuthResponseShape(t *testing.T) {
	service := NewService()
	request, _ := json.Marshal(map[string]any{
		"AuthID":       "gpt365-7b8c1fc7-f1a3-4cee-98b7-c1ade3db7a0c",
		"AuthProvider": "gpt365",
	})
	result, err := service.Handle("model.for_auth", request)
	if err != nil {
		t.Fatalf("model.for_auth 失败: %v", err)
	}

	// 宿主按 pluginapi.ModelResponse 解码，字段是 Provider / Models。
	encoded, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		t.Fatalf("序列化失败: %v", errMarshal)
	}
	t.Logf("model.for_auth 响应: %s", clipText(string(encoded), 600))

	var decoded struct {
		Provider string `json:"Provider"`
		Models   []struct {
			ID          string `json:"ID"`
			Name        string `json:"Name"`
			DisplayName string `json:"DisplayName"`
		} `json:"Models"`
	}
	if errUnmarshal := json.Unmarshal(encoded, &decoded); errUnmarshal != nil {
		t.Fatalf("宿主无法解析该响应: %v", errUnmarshal)
	}
	if decoded.Provider != Provider {
		t.Errorf("Provider = %q，期望 %q", decoded.Provider, Provider)
	}
	if len(decoded.Models) == 0 {
		t.Fatal("Models 为空，账号模型列表会显示为空")
	}
	for _, model := range decoded.Models {
		if strings.TrimSpace(model.ID) == "" {
			t.Error("模型 ID 不能为空")
		}
	}
	t.Logf("模型数: %d，首个: id=%s name=%s", len(decoded.Models), decoded.Models[0].ID, decoded.Models[0].Name)
}

// TestModelStaticResponseShape 静态模型走同样的结构。
func TestModelStaticResponseShape(t *testing.T) {
	service := NewService()
	result, err := service.Handle("model.static", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("model.static 失败: %v", err)
	}
	encoded, _ := json.Marshal(result)
	var decoded struct {
		Provider string `json:"Provider"`
		Models   []struct {
			ID string `json:"ID"`
		} `json:"Models"`
	}
	if errUnmarshal := json.Unmarshal(encoded, &decoded); errUnmarshal != nil {
		t.Fatalf("无法解析: %v", errUnmarshal)
	}
	if decoded.Provider == "" || len(decoded.Models) == 0 {
		t.Errorf("Provider=%q 模型数=%d", decoded.Provider, len(decoded.Models))
	}
}
