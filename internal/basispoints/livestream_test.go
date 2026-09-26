package basispoints

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveAsyncStreaming 验证异步流式转发的端到端行为。
//
// 这是修复「对话卡住」后的核心验证：确认插件会通过 host.stream.emit
// 逐块推送数据，而不是把整个响应缓冲到结束。
//
//	BP_LIVE=1 BP_TOKEN=<token> BP_ACCOUNT_ID=<id> \
//	  go test ./internal/basispoints/ -run TestLiveAsyncStreaming -v
func TestLiveAsyncStreaming(t *testing.T) {
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

	// 用假宿主记录所有回调，模拟 CPA 的流桥。
	var mu sync.Mutex
	var emitted [][]byte
	var closed bool
	var closeErr string

	service.SetHost(func(method string, payload any, out any) error {
		switch method {
		case "host.stream.emit":
			fields := objectValue(payload)
			if fields != nil {
				// payload 是 []byte，直接取用无需 base64 解码。
				if raw, ok := fields["payload"].([]byte); ok {
					mu.Lock()
					emitted = append(emitted, raw)
					mu.Unlock()
				}
			}
		case "host.stream.close":
			fields := objectValue(payload)
			mu.Lock()
			closed = true
			if fields != nil {
				closeErr = stringValue(fields["error"])
			}
			mu.Unlock()
		}
		return nil
	})

	credentialJSON := jsonBytes(map[string]any{"access_token": token, "account_id": accountID})
	requestBody := jsonBytes(map[string]any{
		"model":  cfg.Models[0],
		"stream": true,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": "请从 1 数到 20，每个数字一行。",
			}},
		}},
	})
	// 注意：ExecutorRequest 的 JSON tag 是 snake_case，字段名必须完全匹配
	// （Go 的 JSON 解码区分大小写）。
	executorRequest := jsonBytes(map[string]any{
		"AuthID":          "live-stream-async",
		"Model":           cfg.Models[0],
		"Format":          "openai-response",
		"Stream":          true,
		"stream_id":       "stream-live-1",
		"OriginalRequest": requestBody,
		"StorageJSON":     credentialJSON,
	})

	start := time.Now()
	result, err := service.execute(json.RawMessage(executorRequest), true)
	callElapsed := time.Since(start)
	if err != nil {
		skipIfRateLimited(t, err)
		t.Fatalf("流式执行失败: %v", err)
	}

	// execute 会等上游返回响应头后才交还控制权。
	//
	// 这是必要的：401/403/429 这类状态必须在这里同步返回，宿主才能冷却账号
	// 并换下一个。等的是「响应头」而不是「整个响应体」，因此耗时取决于上游
	// 的首次响应时间，而不是生成总时长。
	//
	// 实测：上游首次响应约 6 秒，整个响应 298KB / 47 块。
	t.Logf("execute 返回，耗时 %v", callElapsed)
	if callElapsed > 30*time.Second {
		t.Errorf("execute 耗时 %v，上游首包时间异常", callElapsed)
	}

	payload, _ := result.(map[string]any)
	if payload["Headers"] == nil {
		t.Error("应返回响应头以建立下游流")
	}

	// 等待转发完成。
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := closed
		count := len(emitted)
		mu.Unlock()
		if done {
			t.Logf("流转发完成，共 %d 块", count)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if !closed {
		t.Fatal("流未关闭，转发可能卡住")
	}
	if closeErr != "" {
		t.Errorf("流以错误结束: %s", closeErr)
	}
	if len(emitted) == 0 {
		t.Fatal("未收到任何数据块")
	}

	// 拼接所有块，验证内容完整。
	var joined strings.Builder
	for _, chunk := range emitted {
		joined.Write(chunk)
	}
	text := joined.String()
	t.Logf("总字节: %d", len(text))

	if !strings.Contains(text, "event:") {
		t.Errorf("转发内容不是 SSE 格式，前 200 字节: %s", clipText(text, 200))
	}
	if !strings.Contains(text, "response.completed") {
		t.Error("缺少 response.completed 事件")
	}
	// 分块转发应有多个块，而不是一整块。
	if len(emitted) < 3 {
		t.Logf("警告：只有 %d 块，可能仍偏向缓冲", len(emitted))
	}
	t.Logf("块数: %d，平均块大小: %d 字节", len(emitted), len(text)/len(emitted))
}
