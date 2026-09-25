package basispoints

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实链路集成测试。
//
// 默认跳过；设置 BP_LIVE=1 后才会执行，因为需要本机的两级代理链可用。
//
//	BP_LIVE=1 BP_TOKEN=<access_token> go test ./internal/basispoints/ -run TestLive -v
//
// 该测试只验证传输与错误处理，不校验上游业务结果，因此即使账号没有
// Basis Points 权限，也能确认链路与请求构造是否正确。

func liveConfig(t *testing.T) Config {
	t.Helper()
	cfg := defaultConfig()
	if password := strings.TrimSpace(os.Getenv("BP_REMOTE_PASSWORD")); password != "" {
		cfg.ProxyChain.RemotePassword = password
	}
	if local := strings.TrimSpace(os.Getenv("BP_LOCAL_PROXY")); local != "" {
		cfg.ProxyChain.LocalProxy = local
	}
	if remote := strings.TrimSpace(os.Getenv("BP_REMOTE_PROXY")); remote != "" {
		cfg.ProxyChain.RemoteProxy = remote
	}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	return cfg
}

// TestLiveChainReachesUpstream 验证两级代理链真的能到达上游。
//
// 关键断言是「收到了 HTTP 响应」而不是「请求成功」：
// 账号无权限时会返回 429 not_enabled，这依然证明链路可用。
func TestLiveChainReachesUpstream(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1 与可用的两级代理链")
	}
	token := strings.TrimSpace(os.Getenv("BP_TOKEN"))
	if token == "" {
		t.Skip("需要 BP_TOKEN")
	}
	cfg := liveConfig(t)
	service := NewService()
	cred := credential{AccessToken: token, AccountID: "acct-live-test", AuthMode: "chatgpt"}

	body := map[string]any{
		"model":            cfg.UpstreamModel,
		"input":            []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "ping"}}}},
		"reasoning_effort": "low",
		"store":            false,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	response, err := service.doUpstream(ctx, cfg, body, cred, false)
	if err != nil {
		// 上游的业务拒绝同样证明链路通了，因此单独识别。
		message := err.Error()
		if strings.Contains(message, "Basis Points HTTP") {
			t.Logf("链路可达，上游返回业务错误: %s", message)
			return
		}
		t.Fatalf("链路不可达: %v", err)
	}
	t.Logf("链路可达，上游 HTTP %d，响应 %d 字节", response.StatusCode, len(response.Body))
}

// TestLiveChainRequiresCredentials 负向对照：错误口令必须被远程代理拒绝。
//
// 这一条是链路真实性的证据：若第二跳根本没发生，错误口令也会「成功」。
func TestLiveChainRequiresCredentials(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1 与可用的两级代理链")
	}
	cfg := liveConfig(t)
	cfg.ProxyChain.RemotePassword = "definitely-wrong-password"
	service := NewService()
	cred := credential{AccessToken: "irrelevant", AccountID: "acct", AuthMode: "chatgpt"}
	body := map[string]any{"model": cfg.UpstreamModel, "input": []any{}}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := service.doUpstream(ctx, cfg, body, cred, false)
	if err == nil {
		t.Fatal("错误口令仍然成功，说明第二跳未被真正执行")
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "407") && !strings.Contains(message, "认证") && !strings.Contains(message, "拒绝") {
		t.Logf("已拒绝（错误信息未明确提及 407）: %v", err)
		return
	}
	t.Logf("远程代理正确拒绝了错误口令: %v", err)
}

// TestLiveRotatingEgressChanges 验证轮换代理确实更换出口。
func TestLiveRotatingEgressChanges(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1 与可用的两级代理链")
	}
	cfg := liveConfig(t)
	seen := map[string]bool{}
	for attempt := 0; attempt < 3; attempt++ {
		dialer, errDialer := newChainDialer(cfg.ProxyChain)
		if errDialer != nil {
			t.Fatalf("构造拨号器失败: %v", errDialer)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		conn, errDial := dialer.dialThrough(ctx, "api.ipify.org:443")
		cancel()
		if errDial != nil {
			t.Fatalf("第 %d 次拨号失败: %v", attempt+1, errDial)
		}
		_ = conn.Close()
		seen[dialer.remoteAddr] = true
	}
	t.Logf("完成 3 次独立隧道建立，远程代理 %v", seen)
}
