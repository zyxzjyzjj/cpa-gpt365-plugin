package basispoints

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveStreamTiming 实测流式请求的时间分布。
//
// 目的：确认「对话卡住」是否因为插件把整个响应缓冲完才返回。
// 输出：首个字节到达时间、总耗时、事件数。
//
//	BP_LIVE=1 BP_TOKEN=<token> BP_ACCOUNT_ID=<id> \
//	  go test ./internal/basispoints/ -run TestLiveStreamTiming -v
func TestLiveStreamTiming(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1")
	}
	token := strings.TrimSpace(os.Getenv("BP_TOKEN"))
	if token == "" {
		t.Skip("需要 BP_TOKEN")
	}
	cfg := liveConfig(t)
	service := NewService()
	cred := credential{
		AccessToken: token,
		AccountID:   strings.TrimSpace(os.Getenv("BP_ACCOUNT_ID")),
		AuthMode:    "chatgpt",
	}

	meta := map[string]any{
		"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
		"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
		"agent_iteration": "1",
	}
	// 用需要一定生成时间的提示，模拟真实对话。
	body := map[string]any{
		"model":  cfg.UpstreamModel,
		"stream": true,
		"store":  false,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": "请从 1 数到 20，每个数字一行。",
			}},
		}},
		"metadata": cloneObject(meta),
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	response, err := service.doUpstream(ctx, cfg, body, cred, true)
	if err != nil {
		t.Skipf("上游不可用（可能是限流）: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("=== 上游流式响应 ===")
	t.Logf("总耗时: %v", elapsed)
	t.Logf("响应大小: %d 字节", len(response.Body))
	t.Logf("Content-Type: %v", response.Headers["Content-Type"])

	// 统计 SSE 事件数，确认内容完整。
	bodyText := string(response.Body)
	events := strings.Count(bodyText, "event:")
	dataLines := strings.Count(bodyText, "data:")
	t.Logf("事件数: %d, data 行数: %d", events, dataLines)

	if events == 0 {
		t.Errorf("未发现 SSE 事件，响应前 300 字节: %s", clipText(bodyText, 300))
	}

	// 关键指标：插件当前是「全缓冲」，所以客户端要等这么久才看到第一个字节。
	// 若这个时间很长（>10s），说明必须改成边收边发。
	if elapsed > 10*time.Second {
		t.Logf("⚠ 上游耗时 %v，客户端在此期间收不到任何数据 —— 需要异步流式转发", elapsed)
	}

	// 确认最终响应能被解析。
	final, errParse := parseFinalStreamResponse(response.Body)
	if errParse != nil {
		t.Errorf("解析失败: %v", errParse)
	} else {
		t.Logf("最终状态: %v, 模型: %v",
			stringValue(final["status"]), stringValue(final["model"]))
		encoded, _ := json.Marshal(final["usage"])
		t.Logf("用量: %s", clipText(string(encoded), 200))
	}
}
