package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件验证流式转发的关键行为。
//
// 背景：早期实现把整个上游流读完再一次性返回，客户端在数十秒内收不到任何
// 字节，表现为「对话卡住」并被主动放弃（HTTP 499）。现在改为异步转发：
// 正文事件立即推送，工具调用相关事件延后到完整响应再还原。

// TestEventMayCarryToolCall 判断哪些事件需要延后。
func TestEventMayCarryToolCall(t *testing.T) {
	forwarder := &streamForwarder{}
	cases := []struct {
		event string
		data  string
		want  bool
	}{
		// 正文与思考事件必须立即转发，否则客户端仍要等。
		{"response.output_text.delta", `{"delta":"你好"}`, false},
		{"response.reasoning_summary_text.delta", `{"delta":"思考"}`, false},
		{"response.created", `{"response":{}}`, false},
		{"response.completed", `{"response":{"status":"completed"}}`, false},
		// 工具调用相关事件延后。
		{"response.function_call_arguments.delta", `{"delta":"{}"}`, true},
		{"response.output_item.added", `{"item":{"type":"function_call"}}`, true},
		{"response.output_item.done", `{"item":{}}`, true},
		{"response.custom_tool_call_input.delta", `{}`, true},
	}
	for _, tc := range cases {
		if got := forwarder.eventMayCarryToolCall(tc.event, tc.data); got != tc.want {
			t.Errorf("eventMayCarryToolCall(%q) = %v，期望 %v", tc.event, got, tc.want)
		}
	}
}

// TestEventWithTransportNameIsDeferred 事件名缺失时按内容判断。
func TestEventWithTransportNameIsDeferred(t *testing.T) {
	forwarder := &streamForwarder{}
	data := `{"item":{"name":"` + transportName + `"}}`
	if !forwarder.eventMayCarryToolCall("", data) {
		t.Error("含运输工具名的事件应被延后")
	}
}

// TestNeedsToolRewrite 只有真正出现运输调用时才需要还原。
func TestNeedsToolRewrite(t *testing.T) {
	if needsToolRewrite(`{"output":[{"type":"message"}]}`) {
		t.Error("无运输调用时不应触发还原")
	}
	if !needsToolRewrite(`{"output":[{"name":"` + transportName + `"}]}`) {
		t.Error("含运输调用时应触发还原")
	}
}

// TestSyntheticOutputEvents 为还原后的工具调用生成事件。
func TestSyntheticOutputEvents(t *testing.T) {
	response := map[string]any{
		"output": []any{
			map[string]any{"type": "message", "role": "assistant"},
			map[string]any{
				"type": "function_call", "id": "fc_1", "call_id": "c1",
				"name": "get_weather", "arguments": `{"city":"Tokyo"}`,
			},
		},
	}
	events := syntheticOutputEvents(response)
	if len(events) != 2 {
		t.Fatalf("事件数 = %d，期望 2（added + done）", len(events))
	}
	joined := strings.Join(events, "")
	if !strings.Contains(joined, "response.output_item.added") {
		t.Error("缺少 added 事件")
	}
	if !strings.Contains(joined, "response.output_item.done") {
		t.Error("缺少 done 事件")
	}
	if !strings.Contains(joined, "get_weather") {
		t.Error("事件应携带客户端工具名")
	}
	// 纯文本 item 不应生成事件。
	onlyText := syntheticOutputEvents(map[string]any{
		"output": []any{map[string]any{"type": "message", "role": "assistant"}},
	})
	if len(onlyText) != 0 {
		t.Errorf("纯文本响应不应生成工具事件，实际 %d 条", len(onlyText))
	}
}

// TestFormatSSEIsWellFormed 合成事件必须是合法 SSE 帧。
func TestFormatSSEIsWellFormed(t *testing.T) {
	frame := formatSSE("response.output_item.done", map[string]any{"output_index": 0})
	if !strings.HasPrefix(frame, "event: response.output_item.done\n") {
		t.Errorf("事件行格式错误: %q", clipText(frame, 80))
	}
	if !strings.Contains(frame, "\ndata: ") {
		t.Error("缺少 data 行")
	}
	if !strings.HasSuffix(frame, "\n\n") {
		t.Error("SSE 帧必须以空行结束")
	}
	// data 行必须是合法 JSON。
	line := strings.TrimPrefix(strings.SplitN(frame, "\ndata: ", 2)[1], "")
	line = strings.TrimSuffix(line, "\n\n")
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal([]byte(line), &parsed); errUnmarshal != nil {
		t.Errorf("data 行不是合法 JSON: %v", errUnmarshal)
	}
	if stringValue(parsed["type"]) != "response.output_item.done" {
		t.Errorf("type 字段 = %q", stringValue(parsed["type"]))
	}
}
