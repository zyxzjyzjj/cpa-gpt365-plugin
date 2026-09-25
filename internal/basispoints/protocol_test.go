package basispoints

import (
	"encoding/base64"
	"strings"
	"testing"
)

func base64URL(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// TestTransformResponseBodyRewritesTransportCall 验证上游原生调用被还原成客户端工具调用。
func TestTransformResponseBodyRewritesTransportCall(t *testing.T) {
	source := map[string]any{
		"tools": []any{
			map[string]any{
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
	outer := `{"summary":"weather","code":` + jsonQuote(inner) + `,"destructive":false,"references":[]}`
	upstream := map[string]any{
		"id":     "resp_1",
		"status": "completed",
		"output": []any{
			map[string]any{
				"type":      "function_call",
				"id":        "fc_1",
				"call_id":   "call_1",
				"name":      transportName,
				"arguments": outer,
			},
		},
	}
	transformed, response, changed, err := transformResponseBody(jsonBytes(upstream), source)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if !changed {
		t.Fatal("应报告已发生改写")
	}
	output, _ := response["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output 项数 = %d", len(output))
	}
	item := objectValue(output[0])
	if stringValue(item["name"]) != "get_weather" {
		t.Errorf("name = %q，期望 get_weather", stringValue(item["name"]))
	}
	if stringValue(item["type"]) != "function_call" {
		t.Errorf("type = %q", stringValue(item["type"]))
	}
	// 被拦截的原生调用必须记住，供结果回放时还原身份。
	if rememberedNativeCall("call_1") == nil {
		t.Error("原生调用应被缓存以便回放")
	}
	if len(transformed) == 0 {
		t.Error("改写后的响应体不应为空")
	}
}

// TestTransformResponseBodyRejectsUnknownTool 上游返回目录外工具时必须报错。
func TestTransformResponseBodyRejectsUnknownTool(t *testing.T) {
	source := map[string]any{"tools": []any{map[string]any{"type": "function", "name": "known"}}}
	outer := `{"code":` + jsonQuote(`{"tool":"unknown","args":{}}`) + `}`
	upstream := map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{"type": "function_call", "id": "fc_1", "call_id": "c1", "name": transportName, "arguments": outer},
		},
	}
	if _, _, _, err := transformResponseBody(jsonBytes(upstream), source); err == nil {
		t.Fatal("目录外的工具调用应被拒绝")
	}
}

// TestTransformResponseBodyRejectsDuplicateCallIDs 重复 call_id 必须拒绝。
func TestTransformResponseBodyRejectsDuplicateCallIDs(t *testing.T) {
	source := map[string]any{"tools": []any{map[string]any{"type": "function", "name": "t"}}}
	outer := `{"code":` + jsonQuote(`{"tool":"t","args":{}}`) + `}`
	item := map[string]any{"type": "function_call", "id": "fc_1", "call_id": "same", "name": transportName, "arguments": outer}
	upstream := map[string]any{"status": "completed", "output": []any{item, cloneObject(item)}}
	if _, _, _, err := transformResponseBody(jsonBytes(upstream), source); err == nil {
		t.Fatal("重复的 call_id 应被拒绝")
	}
}

// TestTransformResponseBodyPassThrough 无工具调用时保持原文。
func TestTransformResponseBodyPassThrough(t *testing.T) {
	source := map[string]any{}
	upstream := map[string]any{
		"id":     "resp_2",
		"status": "completed",
		"output": []any{map[string]any{"type": "message", "role": "assistant"}},
	}
	transformed, _, changed, err := transformResponseBody(jsonBytes(upstream), source)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if changed {
		t.Error("无工具调用时不应报告改写")
	}
	if len(transformed) == 0 {
		t.Error("原文应被返回")
	}
}

// TestSyntheticStreamIsWellFormed 合成流必须包含完整事件序列与终止标记。
func TestSyntheticStreamIsWellFormed(t *testing.T) {
	response := map[string]any{
		"id":     "resp_3",
		"status": "completed",
		"output": []any{
			map[string]any{"type": "function_call", "id": "fc_9", "call_id": "c9", "name": "get_weather", "arguments": `{"city":"Tokyo"}`},
		},
	}
	stream := string(syntheticStream(response))
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.function_call_arguments.delta",
		"event: response.function_call_arguments.done",
		"event: response.output_item.done",
		"event: response.completed",
		"data: [DONE]",
	} {
		if !strings.Contains(stream, want) {
			t.Errorf("合成流缺少 %q", want)
		}
	}
	// 工具参数在 SSE 的 JSON 文本中是转义形式，因此按转义后的字面量校验。
	if !strings.Contains(stream, `\"city\":\"Tokyo\"`) {
		t.Error("合成流应携带工具参数")
	}
}

// TestTranslateInputItemsReplaysNativeIdentity 结果回放必须还原原生运输身份。
func TestTranslateInputItemsReplaysNativeIdentity(t *testing.T) {
	// 先登记一次被拦截的原生调用。
	native := map[string]any{
		"type":      "function_call",
		"id":        "fc_rt",
		"call_id":   "call_rt",
		"name":      transportName,
		"arguments": `{"code":"{\"tool\":\"get_weather\",\"args\":{\"city\":\"Tokyo\"}}"}`,
	}
	rememberNativeCall(native)

	allowed := map[string]toolSpec{"get_weather": {Key: "get_weather", Name: "get_weather", Type: "function"}}
	items := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "天气"}}},
		// 客户端回传的其实是真实工具名，但必须被还原成原生运输身份。
		map[string]any{"type": "function_call", "call_id": "call_rt", "name": "get_weather", "arguments": `{"city":"Tokyo"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_rt", "output": "18C"},
	}
	result := translateInputItems(items, allowed)
	if len(result) != 3 {
		t.Fatalf("项数 = %d，期望 3", len(result))
	}
	replayed := objectValue(result[1])
	if stringValue(replayed["name"]) != transportName {
		t.Errorf("回放的工具名 = %q，期望 %q", stringValue(replayed["name"]), transportName)
	}
	outputItem := objectValue(result[2])
	if stringValue(outputItem["type"]) != "function_call_output" {
		t.Errorf("结果项 type = %q", stringValue(outputItem["type"]))
	}
	if _, exists := outputItem["name"]; exists {
		t.Error("结果项不应携带 name 字段")
	}
}

// TestTranslateInputItemsWrapsClientToolCall 历史中的客户端工具调用被包装成原生调用。
func TestTranslateInputItemsWrapsClientToolCall(t *testing.T) {
	allowed := map[string]toolSpec{"exec_command": {Key: "exec_command", Name: "exec_command", Type: "function"}}
	items := []any{
		map[string]any{"type": "function_call", "call_id": "c_wrap", "name": "exec_command", "arguments": `{"cmd":"pwd"}`},
	}
	result := translateInputItems(items, allowed)
	if len(result) != 1 {
		t.Fatalf("项数 = %d", len(result))
	}
	wrapped := objectValue(result[0])
	if stringValue(wrapped["name"]) != transportName {
		t.Errorf("应被包装成 %q，实际 %q", transportName, stringValue(wrapped["name"]))
	}
	// 内层 code 必须是可解析的 JSON 文本，且带真实工具名。
	arguments := parseArgumentsObject(wrapped["arguments"])
	inner := parseArgumentsObject(arguments["code"])
	if stringValue(inner["tool"]) != "exec_command" {
		t.Errorf("内层工具名 = %q", stringValue(inner["tool"]))
	}
}

// TestTranslateInputItemsDropsReasoningWithoutCiphertext 无密文的 reasoning 必须丢弃。
func TestTranslateInputItemsDropsReasoningWithoutCiphertext(t *testing.T) {
	items := []any{
		map[string]any{"type": "reasoning", "summary": []any{}},
		map[string]any{"type": "reasoning", "encrypted_content": "abc"},
	}
	result := translateInputItems(items, nil)
	if len(result) != 1 {
		t.Fatalf("项数 = %d，期望只保留带密文的 reasoning", len(result))
	}
}

// TestCallableClientToolSpecsHonorsToolChoice tool_choice 必须约束可调用集合。
func TestCallableClientToolSpecsHonorsToolChoice(t *testing.T) {
	base := []any{
		map[string]any{"type": "function", "name": "a"},
		map[string]any{"type": "function", "name": "b"},
	}
	all := callableClientToolSpecs(map[string]any{"tools": base})
	if len(all) != 2 {
		t.Errorf("默认应允许全部工具，实际 %d", len(all))
	}
	none := callableClientToolSpecs(map[string]any{"tools": base, "tool_choice": "none"})
	if len(none) != 0 {
		t.Errorf("tool_choice=none 应禁用全部工具，实际 %d", len(none))
	}
	one := callableClientToolSpecs(map[string]any{
		"tools":       base,
		"tool_choice": map[string]any{"type": "function", "name": "a"},
	})
	if len(one) != 1 {
		t.Errorf("应只允许 a，实际 %d", len(one))
	}
	if _, ok := one["a"]; !ok {
		t.Error("应选中 a")
	}
}

// TestClientToolCallRequired 覆盖 required 的多种写法。
func TestClientToolCallRequired(t *testing.T) {
	cases := []struct {
		choice any
		want   bool
	}{
		{"required", true},
		{"auto", false},
		{"none", false},
		{map[string]any{"type": "function", "name": "x"}, true},
		{map[string]any{"type": "allowed_tools", "mode": "required"}, true},
		{map[string]any{"type": "allowed_tools", "mode": "auto"}, false},
	}
	for _, tc := range cases {
		if got := clientToolCallRequired(map[string]any{"tool_choice": tc.choice}); got != tc.want {
			t.Errorf("tool_choice=%v => %v，期望 %v", tc.choice, got, tc.want)
		}
	}
}

func jsonQuote(s string) string {
	return string(jsonBytes(s))
}
