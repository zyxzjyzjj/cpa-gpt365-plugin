package basispoints

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveTimeToFirstByte 测量上游的首包时间。
//
// 目的：区分「插件缓冲」与「上游本身慢」。
// 早期实现是整个流读完才返回，客户端等的是总时长；
// 现在是等首包，之后边收边发。两者相差数倍。
//
//	BP_LIVE=1 BP_TOKEN=<token> BP_ACCOUNT_ID=<id> \
//	  go test ./internal/basispoints/ -run TestLiveTimeToFirstByte -v
func TestLiveTimeToFirstByte(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1")
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

	meta := map[string]any{
		"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3401",
		"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3402",
		"agent_iteration": "1",
	}
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
	payload, _ := json.Marshal(body)

	client, errClient := newHTTPClient(cfg, 120*time.Second)
	if errClient != nil {
		t.Fatalf("构造客户端失败: %v", errClient)
	}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ResponsesURL, strings.NewReader(string(payload)))
	req.Header = authHeaders(cred, true)

	start := time.Now()
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Skipf("上游不可用（可能限流）: %v", errDo)
	}
	firstByte := time.Since(start)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw := make([]byte, 512)
		n, _ := resp.Body.Read(raw)
		t.Skipf("上游返回 %d: %s", resp.StatusCode, clipText(string(raw[:n]), 200))
	}
	t.Logf("首包时间（响应头）: %v", firstByte)

	// 继续读第一块数据，看真正的内容何时到达。
	buf := make([]byte, 4096)
	n, errRead := resp.Body.Read(buf)
	if errRead != nil {
		t.Fatalf("读取失败: %v", errRead)
	}
	t.Logf("首个数据块: %v 后到达，%d 字节", time.Since(start), n)
	t.Logf("首块内容: %s", clipText(string(buf[:n]), 200))

	if firstByte > 20*time.Second {
		t.Errorf("首包耗时 %v，客户端等待时间过长", firstByte)
	}
}
