package basispoints

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDiagnoseIsolateFields 在 metadata 存在的前提下，逐字段隔离出引发 403 的选项。
//
// 前序诊断结论：metadata 是必需字段（缺失即 422）；在此之上，
// model_selection 与 reasoning_effort 的组合会触发 403
// "Model access has changed"。本测试确认到底是哪一个字段。
func TestDiagnoseIsolateFields(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	meta := map[string]any{
		"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
		"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
		"agent_iteration": "1",
	}
	user := map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
	}
	base := func() map[string]any {
		return map[string]any{
			"model":    cfg.UpstreamModel,
			"input":    []any{user},
			"stream":   false,
			"store":    false,
			"metadata": cloneObject(meta),
		}
	}

	variants := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"A-meta-only", func(m map[string]any) {}},
		{"B+model_selection", func(m map[string]any) { m["model_selection"] = "explicit" }},
		{"C+effort-low", func(m map[string]any) { m["reasoning_effort"] = "low" }},
		{"D+effort-medium", func(m map[string]any) { m["reasoning_effort"] = "medium" }},
		{"E+effort-high", func(m map[string]any) { m["reasoning_effort"] = "high" }},
		{"F+both", func(m map[string]any) {
			m["model_selection"] = "explicit"
			m["reasoning_effort"] = "low"
		}},
		{"G+service_tier", func(m map[string]any) { m["service_tier"] = "auto" }},
		{"H+dev-msg", func(m map[string]any) {
			m["input"] = []any{messageItem("developer", "You are a helpful assistant."), user}
		}},
	}

	for _, variant := range variants {
		body := base()
		variant.mutate(body)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		response, err := service.doUpstream(ctx, cfg, body, cred, false)
		cancel()
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Logf("%-20s -> HTTP %d: %s", variant.name, status, truncateText(err.Error(), 130))
			continue
		}
		echoed := ""
		if final, errParse := parseFinalStreamResponse(response.Body); errParse == nil {
			echoed = stringValue(final["model"])
		}
		t.Logf("%-20s -> HTTP %d OK, 上游回显 model=%q", variant.name, response.StatusCode, echoed)
	}
}

// TestDiagnoseModelWithSelection 验证「显式选择」对模型可用性的影响。
func TestDiagnoseModelWithSelection(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	meta := map[string]any{
		"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
		"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
		"agent_iteration": "1",
	}
	user := map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
	}

	for _, selection := range []string{"", "explicit", "auto"} {
		for _, model := range []string{"gpt-6-astra", "gpt-5.6-luna", "gpt-5.6-sol"} {
			body := map[string]any{
				"model":    model,
				"input":    []any{user},
				"stream":   false,
				"store":    false,
				"metadata": cloneObject(meta),
			}
			if selection != "" {
				body["model_selection"] = selection
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			response, err := service.doUpstream(ctx, cfg, body, cred, false)
			cancel()
			label := "sel=(none)"
			if selection != "" {
				label = "sel=" + selection
			}
			if err != nil {
				status := 0
				if response != nil {
					status = response.StatusCode
				}
				t.Logf("%-14s %-16s -> HTTP %d: %s", label, model, status, truncateText(err.Error(), 90))
				continue
			}
			echoed := ""
			if final, errParse := parseFinalStreamResponse(response.Body); errParse == nil {
				echoed = stringValue(final["model"])
			}
			t.Logf("%-14s %-16s -> HTTP %d OK, 回显 %q", label, model, response.StatusCode, echoed)
		}
	}
}

// TestDiagnoseMetadataShape 探测 metadata 的最小必需形态。
func TestDiagnoseMetadataShape(t *testing.T) {
	cfg, cred, _ := diagnoseEnv(t)
	service := NewService()

	user := map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
	}
	cases := []struct {
		name     string
		metadata any
	}{
		{"empty-object", map[string]any{}},
		{"only-task_id", map[string]any{"task_id": "3f2504e0-4f89-51d3-9a0c-0305e82c3301"}},
		{"task+turn", map[string]any{
			"task_id": "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
			"turn_id": "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
		}},
		{"task+turn+iter-str", map[string]any{
			"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
			"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
			"agent_iteration": "1",
		}},
		{"task+turn+iter-int", map[string]any{
			"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
			"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
			"agent_iteration": 1,
		}},
		{"non-uuid-ids", map[string]any{
			"task_id":         "task-abc",
			"turn_id":         "turn-abc",
			"agent_iteration": "1",
		}},
	}

	for _, tc := range cases {
		body := map[string]any{
			"model":    cfg.UpstreamModel,
			"input":    []any{user},
			"stream":   false,
			"store":    false,
			"metadata": tc.metadata,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		response, err := service.doUpstream(ctx, cfg, body, cred, false)
		cancel()
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Logf("%-22s -> HTTP %d: %s", tc.name, status, truncateText(err.Error(), 110))
			continue
		}
		t.Logf("%-22s -> HTTP %d OK", tc.name, response.StatusCode)
	}
}

var _ = json.Marshal
var _ = strings.TrimSpace
