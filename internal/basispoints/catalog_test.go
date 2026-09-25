package basispoints

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveModelCatalog 探测某账号实际可用的上游模型。
//
// 用途：确认 plus / free 账号各自能用哪些模型，据此决定默认注册哪些别名。
// 插件此前只注册配置里的一个别名，导致「模型列表只有 luna」。
//
//	BP_LIVE=1 BP_TOKEN=<token> BP_ACCOUNT_ID=<id> \
//	  go test ./internal/basispoints/ -run TestLiveModelCatalog -v
func TestLiveModelCatalog(t *testing.T) {
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

	// 候选模型名。上游对无权限的名称返回 403，对存在的名称返回 200。
	candidates := []string{}
	if list := strings.TrimSpace(os.Getenv("BP_MODELS")); list != "" {
		candidates = strings.Split(list, ",")
	} else {
		candidates = []string{
			"gpt-5.6-luna", "gpt-6-astra", "gpt-5.6-sol", "gpt-6-sol",
			"gpt-6-astra-preview", "gpt-5.6", "gpt-6", "gpt-5.6-luna-preview",
		}
	}

	meta := map[string]any{
		"task_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3301",
		"turn_id":         "3f2504e0-4f89-51d3-9a0c-0305e82c3302",
		"agent_iteration": "1",
	}
	user := map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly: PONG"}},
	}

	for _, model := range candidates {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		body := map[string]any{
			"model": model, "input": []any{user},
			"stream": false, "store": false,
			"metadata": cloneObject(meta),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		response, err := service.doUpstream(ctx, cfg, body, cred, false)
		cancel()
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Logf("%-24s -> HTTP %d: %s", model, status, clipText(err.Error(), 100))
			continue
		}
		echoed := ""
		if final, errParse := parseFinalStreamResponse(response.Body); errParse == nil {
			echoed = stringValue(final["model"])
		}
		t.Logf("%-24s -> HTTP %d OK  上游回显=%q  <<< 可用", model, response.StatusCode, echoed)
	}
}
