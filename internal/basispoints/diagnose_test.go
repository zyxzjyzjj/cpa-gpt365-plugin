package basispoints

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 开发期诊断测试，仅在显式开启时运行。这些用例依赖真实网络与真实凭据，
// 因此不作为常规测试套件的一部分。

func diagnoseEnv(t *testing.T) (Config, credential, string) {
	t.Helper()
	if os.Getenv("BP_DIAGNOSE") != "1" {
		t.Skip("需要 BP_DIAGNOSE=1")
	}
	token := strings.TrimSpace(os.Getenv("BP_TOKEN"))
	if token == "" {
		t.Skip("需要 BP_TOKEN")
	}
	cfg := liveConfig(t)
	cred := credential{
		AccessToken: token,
		AccountID:   strings.TrimSpace(os.Getenv("BP_ACCOUNT_ID")),
		AuthMode:    "chatgpt",
	}
	return cfg, cred, token
}

// TestDiagnoseBodyField 逐字段定位上游请求体的必需项。
//
// 实测结论：缺少 metadata 时上游返回 422；带上
// task_id / turn_id / agent_iteration 后返回 200。
func TestDiagnoseBodyField(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	userMessage := map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
	}
	developerMessage := messageItem("developer", "You are a helpful assistant.")

	variants := []struct {
		name string
		body map[string]any
	}{
		{"V1-minimal", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{userMessage}, "stream": false,
		}},
		{"V2-store", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{userMessage}, "stream": false, "store": false,
		}},
		{"V3-model_selection", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{userMessage}, "stream": false,
			"store": false, "model_selection": "explicit",
		}},
		{"V4-effort", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{userMessage}, "stream": false,
			"store": false, "reasoning_effort": "low",
		}},
		{"V5-developer-msg", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{developerMessage, userMessage},
			"stream": false, "store": false,
		}},
		{"V6-metadata", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{userMessage}, "stream": false, "store": false,
			"metadata": map[string]any{
				"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
				"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
				"agent_iteration": "1",
			},
		}},
		{"V7-dev+metadata", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{developerMessage, userMessage},
			"stream": false, "store": false,
			"metadata": map[string]any{
				"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
				"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
				"agent_iteration": "1",
			},
		}},
		{"V8-full+effort", map[string]any{
			"model": cfg.UpstreamModel, "input": []any{developerMessage, userMessage},
			"stream": false, "store": false, "model_selection": "explicit",
			"reasoning_effort": "low",
			"metadata": map[string]any{
				"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
				"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
				"agent_iteration": "1",
			},
		}},
	}

	for _, variant := range variants {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		response, err := service.doUpstream(ctx, cfg, variant.body, cred, false)
		cancel()
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Logf("%-20s -> HTTP %d: %s", variant.name, status, truncateText(err.Error(), 160))
			continue
		}
		t.Logf("%-20s -> HTTP %d OK (%d bytes)", variant.name, response.StatusCode, len(response.Body))
	}
}

// TestDiagnoseModelAvailability 探测该账号实际可用的上游模型。
//
// 不同账号的可用模型集合不同，因此这里逐一尝试并报告结果，
// 而不是假定某个模型一定可用。
func TestDiagnoseModelAvailability(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	candidates := []string{}
	if list := strings.TrimSpace(os.Getenv("BP_MODELS")); list != "" {
		candidates = strings.Split(list, ",")
	} else {
		candidates = []string{
			"gpt-6-astra", "gpt-5.6-sol", "gpt-6-sol", "gpt-6-astra-preview",
			"gpt-5.6-astra", "gpt-5-6-sol", "gpt-5.6", "gpt-6",
		}
	}

	for _, model := range candidates {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
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
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		response, err := service.doUpstream(ctx, cfg, body, cred, false)
		cancel()
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Logf("%-24s -> HTTP %d: %s", model, status, truncateText(err.Error(), 120))
			continue
		}
		t.Logf("%-24s -> HTTP %d OK (%d bytes)  <<< 可用", model, response.StatusCode, len(response.Body))
	}
}

// TestDiagnoseFullPluginBody 用插件真实改写后的请求体探测上游反应。
func TestDiagnoseFullPluginBody(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()
	// 使用配置中的真实别名，避免硬编码与默认值脱节。
	alias := cfg.Models[0]
	if override := strings.TrimSpace(os.Getenv("BP_MODEL")); override != "" {
		cfg.UpstreamModel = override
		cfg.ModelMappings = map[string]string{alias: override}
	}

	source := map[string]any{
		"model": alias,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}}},
		},
	}
	body, err := prepareResponsesBody(source, cfg)
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}
	t.Logf("别名 %s -> 上游模型 %s", alias, stringValue(body["model"]))
	t.Logf("插件请求体字段: %v", sortedKeys(body))
	if _, exists := body["model_selection"]; exists {
		t.Errorf("默认不应发送 model_selection，实际为 %v", body["model_selection"])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	response, errUpstream := service.doUpstream(ctx, cfg, body, cred, false)
	if errUpstream != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Logf("完整请求体 -> HTTP %d: %s", status, truncateText(errUpstream.Error(), 240))
		return
	}
	t.Logf("完整请求体 -> HTTP %d OK (%d bytes)", response.StatusCode, len(response.Body))

	// 复现生产路径：上游返回 SSE，先解析出最终响应再转换。
	payload := response.Body
	if isEventStreamBody(response.Body) {
		final, errParse := parseFinalStreamResponse(response.Body)
		if errParse != nil {
			t.Fatalf("SSE 解析失败: %v", errParse)
		}
		payload = jsonBytes(final)
	}
	transformed, parsed, changed, errTransform := transformResponseBody(payload, source)
	if errTransform != nil {
		t.Fatalf("响应转换失败: %v", errTransform)
	}
	t.Logf("响应转换: changed=%v, 输出 %d 字节, status=%s, 上游回显 model=%s",
		changed, len(transformed), stringValue(parsed["status"]), stringValue(parsed["model"]))
	if output, ok := parsed["output"].([]any); ok {
		for i, value := range output {
			item := objectValue(value)
			if item == nil {
				continue
			}
			t.Logf("  output[%d] type=%s role=%s", i, stringValue(item["type"]), stringValue(item["role"]))
		}
	}
	stream := syntheticStream(parsed)
	t.Logf("合成 SSE 流: %d 字节, 含 [DONE]=%v", len(stream), strings.Contains(string(stream), "[DONE]"))
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
