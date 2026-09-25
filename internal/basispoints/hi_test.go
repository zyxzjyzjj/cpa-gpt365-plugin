package basispoints

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestSendHi 发一条真实的 "hi"，把上游返回的原文完整打印出来。
func TestSendHi(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1")
	}
	token := strings.TrimSpace(os.Getenv("BP_TOKEN"))
	accountID := strings.TrimSpace(os.Getenv("BP_ACCOUNT_ID"))
	if token == "" || accountID == "" {
		t.Skip("需要 BP_TOKEN 与 BP_ACCOUNT_ID")
	}

	cfg := liveConfig(t)
	service := NewService()
	service.mu.Lock()
	service.cfg = cfg
	service.mu.Unlock()

	credentialJSON := jsonBytes(map[string]any{"access_token": token, "account_id": accountID})
	requestBody := jsonBytes(map[string]any{
		"model":  cfg.Models[0],
		"stream": false,
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "hi"}},
			},
		},
	})
	executorRequest := jsonBytes(map[string]any{
		"AuthID":          "hi-test",
		"Model":           cfg.Models[0],
		"Format":          "openai-response",
		"Stream":          false,
		"OriginalRequest": requestBody,
		"StorageJSON":     credentialJSON,
	})

	result, err := service.execute(json.RawMessage(executorRequest), false)
	if err != nil {
		skipIfRateLimited(t, err)
		t.Fatalf("失败: %v", err)
	}
	payload, _ := result.(map[string]any)
	raw, _ := payload["Payload"].([]byte)

	var response map[string]any
	if json.Unmarshal(raw, &response) != nil {
		t.Fatalf("响应不是 JSON")
	}

	t.Logf("========== 上游原始返回（已转为 JSON）==========")
	t.Logf("id       : %s", stringValue(response["id"]))
	t.Logf("model    : %s", stringValue(response["model"]))
	t.Logf("status   : %s", stringValue(response["status"]))
	t.Logf("---------- 正文 ----------")
	t.Logf("%s", assistantText(response))
	t.Logf("--------------------------")
	if usage := objectValue(response["usage"]); usage != nil {
		t.Logf("tokens   : in=%v out=%v total=%v",
			usage["input_tokens"], usage["output_tokens"], usage["total_tokens"])
	}
	if output, ok := response["output"].([]any); ok {
		for i, value := range output {
			item := objectValue(value)
			if item == nil {
				continue
			}
			t.Logf("output[%d] type=%s role=%s", i, stringValue(item["type"]), stringValue(item["role"]))
		}
	}

	if strings.TrimSpace(assistantText(response)) == "" {
		t.Error("助手正文为空")
	}
}
