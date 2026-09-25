package basispoints

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestLiveServiceExecute 走 Service.execute 完整入口，覆盖从凭据解析、
// 请求改写、代理链传输、SSE 解析、工具转换到流合成的全链路。
//
// 需要 BP_LIVE=1、BP_TOKEN，以及可用的两级代理链。
func TestLiveServiceExecute(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1")
	}
	token := strings.TrimSpace(os.Getenv("BP_TOKEN"))
	if token == "" {
		t.Skip("需要 BP_TOKEN")
	}
	accountID := strings.TrimSpace(os.Getenv("BP_ACCOUNT_ID"))
	if accountID == "" {
		t.Skip("需要 BP_ACCOUNT_ID")
	}

	cfg := liveConfig(t)
	service := NewService()
	// 直接注入配置与凭据，模拟宿主已完成 register 与凭据解析。
	service.mu.Lock()
	service.cfg = cfg
	service.mu.Unlock()

	credentialJSON := jsonBytes(map[string]any{
		"access_token": token,
		"account_id":   accountID,
	})
	requestBody := jsonBytes(map[string]any{
		"model":  cfg.Models[0],
		"stream": false,
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
			},
		},
	})
	// 注意：ExecutorRequest 的 []byte 字段在 JSON 中按 base64 传输，
	// 因此这里直接传 []byte，由编码器完成转换（与 CPA 宿主行为一致）。
	executorRequest := jsonBytes(map[string]any{
		"AuthID":          "live-test",
		"Model":           cfg.Models[0],
		"Format":          "openai-response",
		"Stream":          false,
		"OriginalRequest": requestBody,
		"StorageJSON":     credentialJSON,
	})

	result, err := service.execute(json.RawMessage(executorRequest), false)
	if err != nil {
		skipIfRateLimited(t, err)
		t.Fatalf("execute 失败: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("返回类型异常: %T", result)
	}
	raw, ok := payload["Payload"].([]byte)
	if !ok || len(raw) == 0 {
		t.Fatalf("Payload 为空或类型异常: %T", payload["Payload"])
	}
	t.Logf("execute 返回 %d 字节", len(raw))

	var response map[string]any
	if json.Unmarshal(raw, &response) != nil {
		t.Fatalf("Payload 不是合法 JSON，前 200 字节: %s", truncateText(string(raw), 200))
	}
	if status := stringValue(response["status"]); status != "completed" {
		t.Errorf("status = %q，期望 completed", status)
	}
	t.Logf("上游回显 model=%s, status=%s", stringValue(response["model"]), stringValue(response["status"]))

	// 校验助手正文确实存在且内容正确。
	text := assistantText(response)
	if !strings.Contains(text, "PONG") {
		t.Errorf("助手正文 = %q，期望包含 PONG", truncateText(text, 200))
	}
	t.Logf("助手正文: %q", truncateText(text, 100))
}

// TestLiveServiceExecuteStream 覆盖流式路径：应返回完整 SSE 事件序列。
func TestLiveServiceExecuteStream(t *testing.T) {
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
		"stream": true,
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
			},
		},
	})
	executorRequest := jsonBytes(map[string]any{
		"AuthID":          "live-test-stream",
		"Model":           cfg.Models[0],
		"Format":          "openai-response",
		"Stream":          true,
		"OriginalRequest": requestBody,
		"StorageJSON":     credentialJSON,
	})

	result, err := service.execute(json.RawMessage(executorRequest), true)
	if err != nil {
		skipIfRateLimited(t, err)
		t.Fatalf("流式 execute 失败: %v", err)
	}
	payload, _ := result.(map[string]any)
	raw, _ := payload["Payload"].([]byte)
	stream := string(raw)
	t.Logf("流式返回 %d 字节", len(raw))

	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.completed",
		"data: [DONE]",
	} {
		if !strings.Contains(stream, want) {
			t.Errorf("SSE 流缺少 %q", want)
		}
	}
	if !strings.Contains(stream, "PONG") {
		t.Errorf("SSE 流中未找到助手正文 PONG")
	}
	headers, _ := payload["Headers"].(map[string][]string)
	if len(headers["Content-Type"]) == 0 || !strings.Contains(headers["Content-Type"][0], "event-stream") {
		t.Errorf("Content-Type 应为 text/event-stream，实际 %v", headers)
	}
}

// skipIfRateLimited 区分「上游限流」与「实现缺陷」。
//
// 免费档账号有较低的速率上限，连续压测会触发 429。限流说明链路与请求
// 构造都已被上游接受，因此跳过而不是判失败，避免把配额问题误报成 bug。
func skipIfRateLimited(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	message := err.Error()
	if strings.Contains(message, "429") && strings.Contains(message, "Rate limit") {
		t.Skipf("上游限流（非实现缺陷）: %s", truncateText(message, 160))
	}
}

// assistantText 提取响应中所有助手文本，便于断言。
func assistantText(response map[string]any) string {
	output, _ := response["output"].([]any)
	var builder strings.Builder
	for _, value := range output {
		item := objectValue(value)
		if item == nil || stringValue(item["type"]) != "message" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, part := range content {
			partObj := objectValue(part)
			if partObj == nil {
				continue
			}
			builder.WriteString(stringValue(partObj["text"]))
		}
	}
	return builder.String()
}
