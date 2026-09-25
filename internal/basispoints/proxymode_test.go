package basispoints

import (
	"strings"
	"testing"
)

// 本文件锁定代理配置的默认行为与各形态的可用性。
//
// 背景：插件曾把「本地代理 127.0.0.1:15732」写成默认值并把 proxy_chain 默认
// 打开。那只是开发机上的调试端口，部署到能直连上游的服务器后，每个请求都会
// 先连一个不存在的本地端口，全部失败。默认必须是直连。

// TestDefaultIsDirect 默认配置必须直连，不带任何代理。
func TestDefaultIsDirect(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	if err := cfg.normalize(); err != nil {
		t.Fatalf("默认配置校验失败: %v", err)
	}
	if cfg.ProxyChain.Enabled {
		t.Error("默认应关闭代理链")
	}
	if cfg.ProxyChain.LocalProxy != "" {
		t.Errorf("默认不应带本地代理地址: %q", cfg.ProxyChain.LocalProxy)
	}
	if cfg.ProxyChain.RemoteProxy != "" {
		t.Errorf("默认不应带远程代理地址: %q", cfg.ProxyChain.RemoteProxy)
	}
}

// TestDirectModeBuildsNoProxyClient 直连模式下不应构造代理拨号器。
func TestDirectModeBuildsNoProxyClient(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	if err := cfg.normalize(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	client, errClient := newHTTPClient(cfg, 30_000_000_000)
	if errClient != nil {
		t.Fatalf("构造客户端失败: %v", errClient)
	}
	if client == nil {
		t.Fatal("应返回可用的客户端")
	}
}

// TestRemoteOnlyProxyIsValid 只配远程代理必须可用（服务器常见形态）。
//
// 之前 newChainDialer 会先解析 local_proxy，空地址直接报错，
// 导致「服务器只填一个远程代理」这种最自然的配置根本无法使用。
func TestRemoteOnlyProxyIsValid(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ProxyChain = ProxyChainConfig{
		RemoteProxy: "http://user:pass@proxy.example.com:10000",
	}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("只配远程代理应合法: %v", err)
	}
	if !cfg.ProxyChain.Enabled {
		t.Error("提供了地址应自动视为启用")
	}
	dialer, errDialer := newChainDialer(cfg.ProxyChain)
	if errDialer != nil {
		t.Fatalf("应能构造拨号器: %v", errDialer)
	}
	if dialer.remoteAddr != "proxy.example.com:10000" {
		t.Errorf("remoteAddr = %q", dialer.remoteAddr)
	}
	if dialer.localAddr != "" {
		t.Errorf("无本地跳时 localAddr 应为空: %q", dialer.localAddr)
	}
	if dialer.remoteAuth == "" {
		t.Error("URL 中的凭据应被解析为认证头")
	}
}

// TestLocalOnlyProxyIsValid 只配本地代理同样可用。
func TestLocalOnlyProxyIsValid(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ProxyChain = ProxyChainConfig{
		LocalProxy: "http://127.0.0.1:15732",
	}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("只配本地代理应合法: %v", err)
	}
	dialer, errDialer := newChainDialer(cfg.ProxyChain)
	if errDialer != nil {
		t.Fatalf("应能构造拨号器: %v", errDialer)
	}
	if dialer.localAddr != "127.0.0.1:15732" {
		t.Errorf("localAddr = %q", dialer.localAddr)
	}
	if dialer.remoteAddr != "" {
		t.Errorf("无远程跳时 remoteAddr 应为空: %q", dialer.remoteAddr)
	}
}

// TestTwoHopProxyIsValid 两级隧道形态仍要可用（本机开发场景）。
func TestTwoHopProxyIsValid(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ProxyChain = ProxyChainConfig{
		LocalProxy:     "http://127.0.0.1:15732",
		RemoteProxy:    "http://proxy.example.com:10000",
		RemoteUsername: "user",
		RemotePassword: "pass",
	}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("两级形态应合法: %v", err)
	}
	dialer, errDialer := newChainDialer(cfg.ProxyChain)
	if errDialer != nil {
		t.Fatalf("应能构造拨号器: %v", errDialer)
	}
	if dialer.localAddr == "" || dialer.remoteAddr == "" {
		t.Errorf("两级都应就位: local=%q remote=%q", dialer.localAddr, dialer.remoteAddr)
	}
	if dialer.remoteAuth == "" {
		t.Error("远程代理凭据应被解析")
	}
}

// TestEnabledWithoutAddressIsRejected 启用但无地址必须报错，而不是静默直连。
//
// 「以为在用代理，其实直连」是危险的：出口 IP 与预期不符，账号更容易被
// 关联风控，而且从外部看不出来。宁可在配置校验阶段就失败。
func TestEnabledWithoutAddressIsRejected(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ProxyChain = ProxyChainConfig{Enabled: true}
	if err := cfg.normalize(); err == nil {
		t.Fatal("启用代理链但未提供任何地址时应报错")
	}
}

// TestDisabledProxyClearsAddresses 关闭时代理应被清空，避免半配置残留。
func TestDisabledProxyClearsAddresses(t *testing.T) {
	clearProxyEnv(t)
	cfg := defaultConfig()
	cfg.ProxyChain = ProxyChainConfig{
		Enabled:     false,
		LocalProxy:  "http://127.0.0.1:15732",
		RemoteProxy: "http://proxy.example.com:10000",
	}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	// 提供了地址会被视为启用，这是刻意的：避免「配了却不生效」。
	if !cfg.ProxyChain.Enabled {
		t.Error("提供了地址应视为启用")
	}
}

// TestEnvProxyEnablesChain 环境变量提供地址时同样生效。
func TestEnvProxyEnablesChain(t *testing.T) {
	t.Setenv(envLocalProxy, "")
	t.Setenv(envRemoteProxy, "http://proxy.example.com:10000")
	t.Setenv(envRemoteUsername, "u")
	t.Setenv(envRemotePassword, "p")
	cfg := Config{}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if !cfg.ProxyChain.Enabled {
		t.Error("环境变量提供地址时应启用代理链")
	}
	if cfg.ProxyChain.RemoteProxy != "http://proxy.example.com:10000" {
		t.Errorf("remote_proxy = %q", cfg.ProxyChain.RemoteProxy)
	}
}

// TestAccountProxyEnablesChainFromDirect 直连配置下，账号代理仍要生效。
//
// 场景：全局不配代理（服务器能直连），但某个账号需要通过指定出口。
// 此时账号代理必须把代理链打开，否则请求会直连，出口与预期不符。
func TestAccountProxyEnablesChainFromDirect(t *testing.T) {
	base := ProxyChainConfig{Enabled: false}
	merged, err := effectiveProxyChain(base, "http://user:pass@acct-proxy.example.com:9000")
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if !merged.Enabled {
		t.Error("指定账号代理后必须启用代理链")
	}
	if merged.RemoteProxy != "http://user:pass@acct-proxy.example.com:9000" {
		t.Errorf("remote_proxy = %q", merged.RemoteProxy)
	}
	// 能据此构造出拨号器。
	if _, errDialer := newChainDialer(merged); errDialer != nil {
		t.Errorf("应能构造拨号器: %v", errDialer)
	}
}

// TestAccountProxyRejectsInvalid 非法账号代理必须报错，而不是静默直连。
func TestAccountProxyRejectsInvalid(t *testing.T) {
	if _, err := effectiveProxyChain(ProxyChainConfig{}, "socks5://127.0.0.1:1080"); err == nil {
		t.Error("非 http/https 的账号代理应被拒绝")
	}
}

// TestNoHardcodedDebugPort 源码与示例配置都不得出现调试用的本地端口。
//
// 这是防回归：该端口只属于开发机，出现在默认值里会让直连环境的请求全部失败。
func TestNoHardcodedDebugPort(t *testing.T) {
	cfg := defaultConfig()
	blob := strings.Join([]string{
		cfg.ProxyChain.LocalProxy,
		cfg.ProxyChain.RemoteProxy,
		cfg.ResponsesURL,
	}, "|")
	if strings.Contains(blob, "15732") {
		t.Errorf("默认配置不得包含调试端口: %s", blob)
	}
}
