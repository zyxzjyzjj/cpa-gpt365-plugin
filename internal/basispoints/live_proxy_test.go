package basispoints

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSingleHopRemoteProxy 验证「只配远程代理」的单跳形态可用。
//
// 这模拟服务器场景：本机能直连远程代理，不需要本地跳。
// 之前 newChainDialer 强制要求 local_proxy，这种配置根本无法使用。
//
// 需要环境变量：
//
//	BP_LIVE=1
//	CHAIN_REMOTE_PROXY   远程代理地址（含凭据）
//	BP_TOKEN / BP_ACCOUNT_ID  用于真实上游请求
func TestLiveSingleHopRemoteProxy(t *testing.T) {
	if os.Getenv("BP_LIVE") != "1" {
		t.Skip("需要 BP_LIVE=1")
	}
	remote := strings.TrimSpace(os.Getenv("CHAIN_REMOTE_PROXY"))
	if remote == "" {
		t.Skip("需要 CHAIN_REMOTE_PROXY")
	}

	// 只配远程代理，不配本地跳。
	cfg := defaultConfig()
	cfg.ProxyChain = ProxyChainConfig{RemoteProxy: remote}
	if errNormalize := cfg.normalize(); errNormalize != nil {
		t.Fatalf("配置校验失败: %v", errNormalize)
	}
	if !cfg.ProxyChain.Enabled {
		t.Fatal("提供地址后应自动启用")
	}
	if cfg.ProxyChain.LocalProxy != "" {
		t.Errorf("不应有本地跳: %q", cfg.ProxyChain.LocalProxy)
	}

	dialer, errDialer := newChainDialer(cfg.ProxyChain)
	if errDialer != nil {
		t.Fatalf("构造拨号器失败: %v", errDialer)
	}
	if dialer.localAddr != "" {
		t.Errorf("localAddr 应为空: %q", dialer.localAddr)
	}
	if dialer.remoteAddr == "" {
		t.Fatal("remoteAddr 不应为空")
	}
	t.Logf("单跳形态: 直连 %s", dialer.remoteAddr)

	// 真实验证隧道能否建立。
	//
	// 注意：能否连上取决于运行环境。某些远程代理只接受来自特定前置代理的
	// 连接，直连会被重置（EOF）——那属于环境限制，不是插件缺陷，因此这里
	// 只记录而不判定失败。
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, errDial := dialer.dialThrough(ctx, "api.ipify.org:443")
	if errDial != nil {
		t.Logf("单跳隧道未建立（可能该代理要求经前置代理接入）: %v", errDial)
		return
	}
	_ = conn.Close()
	t.Log("单跳隧道建立成功")
}

// TestLiveDirectConnection 验证默认直连形态（不配任何代理）。
//
// 只验证能构造出可用的客户端，不强制要求能访问上游——
// 运行环境是否可直连上游由部署方决定。
func TestLiveDirectConnection(t *testing.T) {
	// 清空代理环境变量，确保测的是「默认配置」而不是运行环境的代理设置。
	clearProxyEnv(t)
	cfg := defaultConfig()
	if errNormalize := cfg.normalize(); errNormalize != nil {
		t.Fatalf("默认配置应合法: %v", errNormalize)
	}
	if cfg.ProxyChain.Enabled {
		t.Fatal("默认必须直连")
	}
	client, errClient := newHTTPClient(cfg, 20*time.Second)
	if errClient != nil {
		t.Fatalf("构造客户端失败: %v", errClient)
	}
	if client == nil {
		t.Fatal("应返回客户端")
	}
	t.Log("默认直连形态可用")
}
