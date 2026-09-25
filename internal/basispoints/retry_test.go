package basispoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件验证传输层重试的边界：只重试连接层故障，绝不重试 HTTP 状态码错误。

// TestUpstreamRetriesTransportFailure 首次连接失败后应重试并成功。
func TestUpstreamRetriesTransportFailure(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count == 1 {
			// 模拟轮换代理在隧道建立后立刻断开：直接掐掉连接。
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("测试服务器不支持连接劫持")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok","status":"completed","output":[]}`))
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.ResponsesURL = server.URL
	cfg.ProxyChain.Enabled = false
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}

	service := NewService()
	cred := credential{AccessToken: "t", AccountID: "a", AuthMode: "chatgpt"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := service.doUpstream(ctx, cfg, map[string]any{"model": "m", "input": []any{}}, cred, false)
	if err != nil {
		t.Fatalf("重试后应成功，实际失败: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d", response.StatusCode)
	}
	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Errorf("应至少尝试 2 次，实际 %d", got)
	}
}

// TestUpstreamDoesNotRetryHTTPError HTTP 状态码错误必须立即返回，不得重试。
//
// 这类错误说明上游已作出判定，重试既无意义又可能加重限流。
func TestUpstreamDoesNotRetryHTTPError(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit","message":"Rate limit exceeded"}}`))
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.ResponsesURL = server.URL
	cfg.ProxyChain.Enabled = false
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}

	service := NewService()
	cred := credential{AccessToken: "t", AccountID: "a", AuthMode: "chatgpt"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := service.doUpstream(ctx, cfg, map[string]any{"model": "m", "input": []any{}}, cred, false)
	if err == nil {
		t.Fatal("429 应返回错误")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("HTTP 错误不应重试，实际尝试 %d 次", got)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("错误信息应包含状态码: %v", err)
	}
}

// TestUpstreamGivesUpAfterMaxAttempts 持续连接失败应在上限后放弃。
func TestUpstreamGivesUpAfterMaxAttempts(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("测试服务器不支持连接劫持")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.ResponsesURL = server.URL
	cfg.ProxyChain.Enabled = false
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}

	service := NewService()
	cred := credential{AccessToken: "t", AccountID: "a", AuthMode: "chatgpt"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := service.doUpstream(ctx, cfg, map[string]any{"model": "m", "input": []any{}}, cred, false)
	if err == nil {
		t.Fatal("持续失败应返回错误")
	}
	if got := atomic.LoadInt32(&attempts); got != maxTransportAttempts {
		t.Errorf("应尝试恰好 %d 次，实际 %d", maxTransportAttempts, got)
	}
}

// TestEnvOverridesProxyChain 环境变量必须能提供凭据（仓库不含默认凭据）。
func TestEnvOverridesProxyChain(t *testing.T) {
	t.Setenv(envRemoteProxy, "http://remote.example.com:10000")
	t.Setenv(envRemoteUsername, "user-from-env")
	t.Setenv(envRemotePassword, "pass-from-env")
	t.Setenv(envLocalProxy, "http://127.0.0.1:9999")

	cfg := Config{}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	if cfg.ProxyChain.RemoteProxy != "http://remote.example.com:10000" {
		t.Errorf("remote_proxy 未被环境变量覆盖: %q", cfg.ProxyChain.RemoteProxy)
	}
	if cfg.ProxyChain.RemoteUsername != "user-from-env" {
		t.Errorf("remote_username 未被环境变量覆盖: %q", cfg.ProxyChain.RemoteUsername)
	}
	if cfg.ProxyChain.RemotePassword != "pass-from-env" {
		t.Errorf("remote_password 未被环境变量覆盖: %q", cfg.ProxyChain.RemotePassword)
	}
	if cfg.ProxyChain.LocalProxy != "http://127.0.0.1:9999" {
		t.Errorf("local_proxy 未被环境变量覆盖: %q", cfg.ProxyChain.LocalProxy)
	}
}

// TestDefaultConfigHasNoCredentials 默认配置绝不能内置任何凭据。
//
// 本仓库是公开的，一旦默认值里带上口令就等于公开泄露。
func TestDefaultConfigHasNoCredentials(t *testing.T) {
	cfg := defaultConfig()
	if cfg.ProxyChain.RemotePassword != "" {
		t.Error("默认配置不得包含远程代理口令")
	}
	if cfg.ProxyChain.RemoteUsername != "" {
		t.Error("默认配置不得包含远程代理用户名")
	}
	if cfg.ProxyChain.RemoteProxy != "" {
		t.Error("默认配置不得包含远程代理地址")
	}
}

// TestPersistedSettingsDropRemotePassword 持久化时必须剔除口令。
func TestPersistedSettingsDropRemotePassword(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = dir
	cfg.ProxyChain.RemoteProxy = "http://remote.example.com:10000"
	cfg.ProxyChain.RemoteUsername = "user"
	cfg.ProxyChain.RemotePassword = "super-secret"
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}

	service := NewService()
	configYAML, _ := json.Marshal(map[string]any{"data_dir": dir})
	// 直接走 configure 路径，验证落盘内容。
	request, _ := json.Marshal(map[string]any{"config_yaml": []byte(
		"data_dir: " + dir + "\n" +
			"proxy_chain:\n" +
			"  enabled: true\n" +
			"  local_proxy: \"http://127.0.0.1:15732\"\n" +
			"  remote_proxy: \"http://remote.example.com:10000\"\n" +
			"  remote_username: \"user\"\n" +
			"  remote_password: \"super-secret\"\n",
	)})
	_ = configYAML
	if _, err := service.Handle("plugin.register", request); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	data, errRead := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errRead != nil {
		t.Fatalf("读取设置文件失败: %v", errRead)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Error("持久化文件中不得出现远程代理口令")
	}
}
