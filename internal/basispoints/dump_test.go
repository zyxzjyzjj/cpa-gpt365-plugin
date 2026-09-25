package basispoints

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDumpUpstreamBody 打印上游响应的真实结构，用于确认 200 是否携带有效内容。
//
// 状态码为 200 不代表响应可用，因此这里直接检查正文。
func TestDumpUpstreamBody(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	body := map[string]any{
		"model":  cfg.UpstreamModel,
		"input":  []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}}}},
		"stream": false,
		"store":  false,
		"metadata": map[string]any{
			"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
			"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
			"agent_iteration": "1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	response, err := service.doUpstream(ctx, cfg, body, cred, false)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	t.Logf("HTTP %d, %d 字节", response.StatusCode, len(response.Body))
	t.Logf("Content-Type: %v", response.Headers["Content-Type"])

	// 上游即使收到 stream=false 也返回 SSE，因此先统一解析出最终响应。
	payload := response.Body
	if trimmed := strings.TrimSpace(string(response.Body)); strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, "data:") {
		t.Log("上游返回 SSE 分帧，解析最终响应")
		final, errParse := parseFinalStreamResponse(response.Body)
		if errParse != nil {
			t.Fatalf("SSE 解析失败: %v", errParse)
		}
		payload = jsonBytes(final)
	}

	var top map[string]any
	if json.Unmarshal(payload, &top) != nil {
		t.Fatalf("响应不是 JSON 对象，前 300 字节: %s", truncateText(string(payload), 300))
	}
	t.Logf("顶层字段: %v", sortedKeys(top))
	for _, key := range []string{"id", "model", "status", "object", "error", "detail"} {
		if value, exists := top[key]; exists {
			t.Logf("  %s = %v", key, truncateText(toString(value), 200))
		}
	}
	output, _ := top["output"].([]any)
	t.Logf("output 项数: %d", len(output))
	for i, value := range output {
		item := objectValue(value)
		if item == nil {
			continue
		}
		t.Logf("  [%d] type=%s role=%s name=%s", i, stringValue(item["type"]), stringValue(item["role"]), stringValue(item["name"]))
		if content, ok := item["content"].([]any); ok {
			for j, part := range content {
				partObj := objectValue(part)
				if partObj == nil {
					continue
				}
				t.Logf("      content[%d] type=%s text=%s", j, stringValue(partObj["type"]), truncateText(stringValue(partObj["text"]), 300))
			}
		}
		if text := stringValue(item["text"]); text != "" {
			t.Logf("      text=%s", truncateText(text, 300))
		}
	}
	if usage := objectValue(top["usage"]); usage != nil {
		t.Logf("usage: %v", usage)
	}
}

// TestCompareModelResponses 比较不同模型的响应是否真的不同。
//
// 若多个模型的响应完全一致，说明它们可能命中了同一个固定响应，
// 需要据此调整模型目录，而不是把无效模型暴露给客户端。
func TestCompareModelResponses(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	models := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-6", "definitely-not-a-real-model-xyz"}
	hashes := map[string]string{}
	for _, model := range models {
		body := map[string]any{
			"model":  model,
			"input":  []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}}}},
			"stream": false,
			"store":  false,
			"metadata": map[string]any{
				"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
				"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
				"agent_iteration": "1",
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		response, err := service.doUpstream(ctx, cfg, body, cred, false)
		cancel()
		if err != nil {
			t.Logf("%-32s -> 失败: %s", model, truncateText(err.Error(), 120))
			continue
		}
		// 只比较结构关键字段，避免时间戳等噪声干扰。
		var top map[string]any
		_ = json.Unmarshal(response.Body, &top)
		signature := shortHash(string(jsonBytes(map[string]any{
			"status": stringValue(top["status"]),
			"model":  stringValue(top["model"]),
			"output": top["output"],
		})))
		hashes[model] = signature
		t.Logf("%-32s -> HTTP %d, 结构指纹 %s, 上游回显 model=%q", model, response.StatusCode, signature[:12], stringValue(top["model"]))
	}
	unique := map[string]bool{}
	for _, hash := range hashes {
		unique[hash] = true
	}
	t.Logf("不同响应结构数: %d / %d", len(unique), len(hashes))
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return string(jsonBytes(value))
	}
}

var _ = os.Getenv
var _ = strings.TrimSpace
