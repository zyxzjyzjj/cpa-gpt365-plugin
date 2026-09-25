package basispoints

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 本文件实现「本机 -> 本地代理 -> 远程轮换代理 -> 目标」的两级 HTTP 隧道。
//
// 为什么不使用宿主 host.http.do：宿主的 HTTP 传输只接受单一代理设置，
// 而本机的远程轮换代理无法直连，必须先经本地代理（如 127.0.0.1:15732）
// 转发。标准库的 http.ProxyURL 也只支持一跳，无法在已建立的隧道内再发一次
// CONNECT，因此这里手写嵌套 CONNECT。
//
// 实测依据（同一台机器）：
//   - 直连远程轮换代理：失败（连接被重置）。
//   - 经本地代理嵌套 CONNECT：成功，且错误密码会被远程代理以 407 拒绝。
//   - 连续请求出口 IP 不同，确认轮换生效。

const connectHandshakeTimeout = 30 * time.Second

// chainDialer 建立两级隧道连接。
type chainDialer struct {
	localAddr   string // host:port，第一跳
	localAuth   string // 第一跳的 Proxy-Authorization 值
	localIsTLS  bool   // 本地代理是否以 TLS 提供服务
	remoteAddr  string // host:port，第二跳；为空时退化为单跳
	remoteAuth  string // 第二跳的 Proxy-Authorization 值
	remoteIsTLS bool   // 远程代理是否以 TLS 提供服务
	connectTO   time.Duration
}

// bufferedConn 保留 CONNECT 应答阶段已经读入缓冲的字节。
//
// 这一层不能省：bufio.Reader 可能已经把隧道的第一批数据读进内存，
// 若直接返回裸连接，上层 TLS 握手会读到空流并失败。
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// readConnectReply 手动解析 CONNECT 应答。
//
// 不能使用 http.ReadResponse：它会把隧道后续数据当成响应体吞掉，
// 导致上层协议读到不完整的数据流。
func readConnectReply(reader *bufio.Reader) (int, string, error) {
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, "", fmt.Errorf("读取状态行失败: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(statusLine))
	if len(fields) < 2 {
		return 0, statusLine, fmt.Errorf("状态行格式异常: %q", strings.TrimSpace(statusLine))
	}
	var code int
	if _, errScan := fmt.Sscanf(fields[1], "%d", &code); errScan != nil {
		return 0, statusLine, fmt.Errorf("状态码格式异常: %q", fields[1])
	}
	// 逐行读到空行为止；上游可能返回任意数量的响应头。
	for {
		line, errRead := reader.ReadString('\n')
		if errRead != nil {
			return code, statusLine, fmt.Errorf("读取响应头失败: %w", errRead)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return code, statusLine, nil
}

// establishTunnel 在 raw 之上向 proxyAddr 发起 CONNECT，隧道目标是 target。
// 返回隧道连接以及可能已缓冲数据的 reader。
func establishTunnel(raw net.Conn, proxyAddr, target, authHeader string, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	reader := bufio.NewReader(raw)
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if authHeader != "" {
		fmt.Fprintf(&request, "Proxy-Authorization: %s\r\n", authHeader)
	}
	request.WriteString("Proxy-Connection: Keep-Alive\r\n\r\n")

	_ = raw.SetDeadline(time.Now().Add(timeout))
	if _, errWrite := raw.Write([]byte(request.String())); errWrite != nil {
		return nil, nil, fmt.Errorf("向代理 %s 发送 CONNECT 失败: %w", proxyAddr, errWrite)
	}
	code, line, errReply := readConnectReply(reader)
	if errReply != nil {
		return nil, nil, fmt.Errorf("代理 %s 的 CONNECT 应答异常: %w", proxyAddr, errReply)
	}
	if code != http.StatusOK {
		return nil, nil, fmt.Errorf("代理 %s 拒绝隧道到 %s: %s", proxyAddr, target, strings.TrimSpace(line))
	}
	_ = raw.SetDeadline(time.Time{})
	return raw, reader, nil
}

// dialThrough 建立一条到 target 的（可能两级）隧道连接。
func (d *chainDialer) dialThrough(ctx context.Context, target string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: d.connectTO, KeepAlive: 30 * time.Second}

	// 第一跳：本机 -> 本地代理，隧道指向远程代理（或直接指向目标）。
	firstTarget := target
	if d.remoteAddr != "" {
		firstTarget = d.remoteAddr
	}
	hop1, err := dialer.DialContext(ctx, "tcp", d.localAddr)
	if err != nil {
		return nil, fmt.Errorf("连接本地代理 %s 失败: %w", d.localAddr, err)
	}
	if d.localIsTLS {
		tlsHop, errTLS := d.wrapTLS(ctx, hop1, d.localAddr)
		if errTLS != nil {
			hop1.Close()
			return nil, fmt.Errorf("本地代理 TLS 握手失败: %w", errTLS)
		}
		hop1 = tlsHop
	}
	tunnel, reader, errTunnel := establishTunnel(hop1, d.localAddr, firstTarget, d.localAuth, d.connectTO)
	if errTunnel != nil {
		hop1.Close()
		return nil, errTunnel
	}
	conn := net.Conn(&bufferedConn{Conn: tunnel, reader: reader})

	// 单跳模式：本地代理已经打通到目标。
	if d.remoteAddr == "" {
		return conn, nil
	}

	// 第二跳：在已建立的隧道内，向远程轮换代理再发一次 CONNECT。
	if d.remoteIsTLS {
		tlsRemote, errTLS := d.wrapTLS(ctx, conn, d.remoteAddr)
		if errTLS != nil {
			conn.Close()
			return nil, fmt.Errorf("远程代理 TLS 握手失败: %w", errTLS)
		}
		conn = tlsRemote
	}
	final, finalReader, errFinal := establishTunnel(conn, d.remoteAddr, target, d.remoteAuth, d.connectTO)
	if errFinal != nil {
		conn.Close()
		return nil, errFinal
	}
	return &bufferedConn{Conn: final, reader: finalReader}, nil
}

func (d *chainDialer) wrapTLS(ctx context.Context, conn net.Conn, serverName string) (net.Conn, error) {
	host := serverName
	if h, _, errSplit := net.SplitHostPort(serverName); errSplit == nil {
		host = h
	}
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		return nil, errHandshake
	}
	return tlsConn, nil
}

// DialContext 适配 http.Transport 的拨号签名。
func (d *chainDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	_ = network
	return d.dialThrough(ctx, addr)
}

// newHTTPClient 依据配置构造带两级代理链的 HTTP 客户端。
//
// 关闭 HTTP/2：轮换代理在连接复用下的行为不可预期，显式使用 HTTP/1.1
// 可确保每次请求都经过新的 CONNECT，从而使轮换生效。
func newHTTPClient(cfg Config, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     true,
		MaxIdleConns:          0,
		TLSHandshakeTimeout:   connectHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		Proxy:                 nil,
	}

	if cfg.ProxyChain.Enabled {
		dialer, errDialer := newChainDialer(cfg.ProxyChain)
		if errDialer != nil {
			return nil, errDialer
		}
		transport.DialContext = dialer.DialContext
		// 走代理链时由我们自己完成 TLS，标准库仍按 https 处理即可。
	} else {
		// 未启用代理链：使用标准库的直连与系统代理行为。
		transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		transport.Proxy = http.ProxyFromEnvironment
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// newChainDialer 把配置解析成可用的拨号器。
func newChainDialer(cfg ProxyChainConfig) (*chainDialer, error) {
	local, errLocal := parseProxyEndpoint(cfg.LocalProxy)
	if errLocal != nil {
		return nil, fail(400, "invalid_config", "本地代理地址无效: "+errLocal.Error())
	}
	timeout := time.Duration(cfg.ConnectTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = DefaultConnectTimeout * time.Second
	}
	dialer := &chainDialer{
		localAddr:  local.address,
		localAuth:  local.auth,
		localIsTLS: local.tls,
		connectTO:  timeout,
	}

	if strings.TrimSpace(cfg.RemoteProxy) == "" {
		// 只配置本地代理：单跳，本地代理自身的凭据在第一跳发送。
		return dialer, nil
	}

	remote, errRemote := parseProxyEndpoint(cfg.RemoteProxy)
	if errRemote != nil {
		return nil, fail(400, "invalid_config", "远程代理地址无效: "+errRemote.Error())
	}
	dialer.remoteAddr = remote.address
	dialer.remoteIsTLS = remote.tls
	switch {
	case cfg.RemoteUsername != "" || cfg.RemotePassword != "":
		dialer.remoteAuth = basicAuth(cfg.RemoteUsername, cfg.RemotePassword)
	case remote.auth != "":
		// 允许把凭据直接写在 remote_proxy 的 URL 里。
		dialer.remoteAuth = remote.auth
	}
	return dialer, nil
}

type proxyEndpoint struct {
	address string
	auth    string
	tls     bool
}

// parseProxyEndpoint 解析形如 http://user:pass@host:port 的代理地址。
func parseProxyEndpoint(raw string) (proxyEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return proxyEndpoint{}, fmt.Errorf("地址为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return proxyEndpoint{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return proxyEndpoint{}, fmt.Errorf("仅支持 http/https，收到 %q", u.Scheme)
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" {
		return proxyEndpoint{}, fmt.Errorf("缺少主机名")
	}
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	endpoint := proxyEndpoint{
		address: net.JoinHostPort(host, port),
		tls:     u.Scheme == "https",
	}
	if u.User != nil {
		password, _ := u.User.Password()
		endpoint.auth = basicAuth(u.User.Username(), password)
	}
	return endpoint, nil
}

func basicAuth(username, password string) string {
	if username == "" && password == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// drainAndClose 读取并丢弃剩余响应体，使连接可以被安全关闭。
func drainAndClose(body io.ReadCloser, limit int64) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, limit))
	_ = body.Close()
}
