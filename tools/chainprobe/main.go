// chainprobe 验证两级代理链路是否真实成立：
//
//	本进程 --CONNECT--> 本地代理 --CONNECT--> 远程轮换代理 --> 目标
//
// curl 的 --preproxy/--proxy 组合在部分平台不生效（实测错误口令仍返回 200），
// 因此这里手写 CONNECT 嵌套，并带负向对照（错误口令必须失败）。
//
// 凭据一律从环境变量读取，仓库内不含任何真实凭据：
//
//	CHAIN_LOCAL_PROXY    第一跳本地代理，默认 http://127.0.0.1:15732
//	CHAIN_REMOTE_PROXY   第二跳远程轮换代理，例如 http://host:10000
//	CHAIN_REMOTE_USER    远程代理用户名
//	CHAIN_REMOTE_PASS    远程代理口令
//	CHAIN_TARGET         探测目标，默认 https://api.ipify.org
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// chainEnv 保存从环境变量读出的链路参数。
type chainEnv struct {
	localProxy  string
	remoteProxy string
	remoteUser  string
	remotePass  string
	target      string
}

// loadEnv 读取链路参数；缺少必要项时给出明确提示而不是静默使用默认凭据。
func loadEnv() (chainEnv, error) {
	env := chainEnv{
		localProxy:  strings.TrimSpace(os.Getenv("CHAIN_LOCAL_PROXY")),
		remoteProxy: strings.TrimSpace(os.Getenv("CHAIN_REMOTE_PROXY")),
		remoteUser:  strings.TrimSpace(os.Getenv("CHAIN_REMOTE_USER")),
		remotePass:  strings.TrimSpace(os.Getenv("CHAIN_REMOTE_PASS")),
		target:      strings.TrimSpace(os.Getenv("CHAIN_TARGET")),
	}
	if env.localProxy == "" {
		env.localProxy = "http://127.0.0.1:15732"
	}
	if env.target == "" {
		env.target = "https://api.ipify.org"
	}
	if env.remoteProxy == "" {
		return env, fmt.Errorf("缺少 CHAIN_REMOTE_PROXY（第二跳远程代理地址）")
	}
	if env.remoteUser == "" || env.remotePass == "" {
		return env, fmt.Errorf("缺少 CHAIN_REMOTE_USER 或 CHAIN_REMOTE_PASS（远程代理凭据）")
	}
	return env, nil
}

// hostPort 从 http(s)://host:port 形式的地址中取出 host:port。
func hostPort(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("地址缺少主机名: %s", raw)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port), nil
}

// readConnectReply 手动读取 CONNECT 应答。不能使用 http.ReadResponse：
// 它会把隧道后续数据当作 body 吞掉，导致上层 TLS 握手读到空流。
func readConnectReply(br *bufio.Reader) (int, string, error) {
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return 0, "", fmt.Errorf("读取状态行失败: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(statusLine))
	if len(fields) < 2 {
		return 0, statusLine, fmt.Errorf("状态行格式异常: %q", statusLine)
	}
	var code int
	if _, errScan := fmt.Sscanf(fields[1], "%d", &code); errScan != nil {
		return 0, statusLine, fmt.Errorf("状态码格式异常: %q", fields[1])
	}
	for {
		line, errRead := br.ReadString('\n')
		if errRead != nil {
			return code, statusLine, fmt.Errorf("读取响应头失败: %w", errRead)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return code, statusLine, nil
}

// connectThrough 向 proxyAddr 发 CONNECT target，返回已建立的隧道连接。
// 返回的 bufio.Reader 可能已缓冲隧道数据，调用方必须继续从它读取。
func connectThrough(proxyAddr, target, authHeader string, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 %s 失败: %w", proxyAddr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	br := bufio.NewReader(conn)

	var sb strings.Builder
	fmt.Fprintf(&sb, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if authHeader != "" {
		fmt.Fprintf(&sb, "Proxy-Authorization: %s\r\n", authHeader)
	}
	sb.WriteString("Proxy-Connection: Keep-Alive\r\n\r\n")
	if _, errWrite := conn.Write([]byte(sb.String())); errWrite != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("向 %s 写 CONNECT 失败: %w", proxyAddr, errWrite)
	}
	code, line, errReply := readConnectReply(br)
	if errReply != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("CONNECT %s 经 %s 失败: %w", target, proxyAddr, errReply)
	}
	if code != 200 {
		conn.Close()
		return nil, nil, fmt.Errorf("CONNECT %s 经 %s 被拒: %s", target, proxyAddr, strings.TrimSpace(line))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, nil
}

// bufferedConn 保留 CONNECT 阶段已读入缓冲的数据。
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// chainDialer 建立两级隧道：本地代理 -> 远程轮换代理 -> 目标。
type chainDialer struct {
	timeout    time.Duration
	localAddr  string // host:port，第一跳
	remoteAddr string // host:port，第二跳
	user       string
	pass       string
	viaRemote  bool
}

func (d *chainDialer) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	// 第一跳：本地代理，隧道通向远程代理本身。
	hop1, br1, err := connectThrough(d.localAddr, d.remoteAddr, "", d.timeout)
	if err != nil {
		return nil, err
	}
	if !d.viaRemote {
		return &bufferedConn{Conn: hop1, br: br1}, nil
	}
	// 第二跳：在隧道内向远程代理发 CONNECT，并附上其认证头。
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(d.user+":"+d.pass))
	var sb strings.Builder
	fmt.Fprintf(&sb, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", addr, addr, auth)
	_ = hop1.SetDeadline(time.Now().Add(d.timeout))
	if _, errWrite := hop1.Write([]byte(sb.String())); errWrite != nil {
		hop1.Close()
		return nil, fmt.Errorf("向远程代理写 CONNECT 失败: %w", errWrite)
	}
	code, line, errReply := readConnectReply(br1)
	if errReply != nil {
		hop1.Close()
		return nil, fmt.Errorf("远程 CONNECT %s 失败: %w", addr, errReply)
	}
	if code != 200 {
		hop1.Close()
		return nil, fmt.Errorf("远程代理拒绝 %s: %s", addr, strings.TrimSpace(line))
	}
	_ = hop1.SetDeadline(time.Time{})
	return &bufferedConn{Conn: hop1, br: br1}, nil
}

func probe(label string, dialer *chainDialer, url string) {
	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		ForceAttemptHTTP2:   false,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 30 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: 45 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("  %-22s 失败: %v\n", label, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	fmt.Printf("  %-22s HTTP %d  %s\n", label, resp.StatusCode, strings.TrimSpace(string(body)))
}

func main() {
	env, errEnv := loadEnv()
	if errEnv != nil {
		fmt.Printf("配置不完整: %v\n\n", errEnv)
		fmt.Println("请先设置环境变量：")
		fmt.Println("  CHAIN_REMOTE_PROXY   第二跳远程轮换代理，例如 http://host:10000")
		fmt.Println("  CHAIN_REMOTE_USER    远程代理用户名")
		fmt.Println("  CHAIN_REMOTE_PASS    远程代理口令")
		fmt.Println("可选：")
		fmt.Println("  CHAIN_LOCAL_PROXY    第一跳本地代理，默认 http://127.0.0.1:15732")
		fmt.Println("  CHAIN_TARGET         探测目标，默认 https://api.ipify.org")
		os.Exit(2)
	}

	localAddr, errLocal := hostPort(env.localProxy)
	if errLocal != nil {
		fmt.Printf("本地代理地址无效: %v\n", errLocal)
		os.Exit(2)
	}
	remoteAddr, errRemote := hostPort(env.remoteProxy)
	if errRemote != nil {
		fmt.Printf("远程代理地址无效: %v\n", errRemote)
		os.Exit(2)
	}
	targetAddr, errTarget := hostPort(env.target)
	if errTarget != nil {
		fmt.Printf("探测目标地址无效: %v\n", errTarget)
		os.Exit(2)
	}

	fmt.Printf("本地代理   : %s\n", localAddr)
	fmt.Printf("远程代理   : %s\n", remoteAddr)
	fmt.Printf("探测目标   : %s\n\n", env.target)

	fmt.Println("=== 1) 对照组：仅经本地代理 ===")
	probe("local-only", &chainDialer{
		timeout: 30 * time.Second, localAddr: localAddr,
		remoteAddr: remoteAddr, viaRemote: false,
	}, env.target)

	fmt.Println("=== 2) 实验组：经本地代理 -> 远程轮换代理 ===")
	probe("chain(local->remote)", &chainDialer{
		timeout: 30 * time.Second, localAddr: localAddr,
		remoteAddr: remoteAddr, user: env.remoteUser, pass: env.remotePass, viaRemote: true,
	}, env.target)

	fmt.Println("=== 3) 负向对照：口令故意写错，必须失败 ===")
	probe("chain(bad-pass)", &chainDialer{
		timeout: 30 * time.Second, localAddr: localAddr,
		remoteAddr: remoteAddr, user: env.remoteUser, pass: "definitely-wrong", viaRemote: true,
	}, env.target)

	fmt.Println("=== 4) 轮换行为：经远程代理连续 6 次请求 ===")
	rot := &chainDialer{
		timeout: 30 * time.Second, localAddr: localAddr,
		remoteAddr: remoteAddr, user: env.remoteUser, pass: env.remotePass, viaRemote: true,
	}
	for i := 1; i <= 6; i++ {
		probe(fmt.Sprintf("rotating-%d", i), rot, env.target)
	}

	fmt.Println("=== 5) 出口归属 ===")
	tr := &http.Transport{
		DialContext:         rot.DialContext,
		ForceAttemptHTTP2:   false,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 30 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: 45 * time.Second}
	resp, err := client.Get("http://ip-api.com/json/?fields=country,city,isp,org,as,query")
	if err != nil {
		fmt.Printf("  失败: %v\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var pretty map[string]any
	if json.Unmarshal(body, &pretty) == nil {
		out, _ := json.MarshalIndent(pretty, "  ", "  ")
		fmt.Printf("  %s\n", out)
	} else {
		fmt.Printf("  %s\n", body)
	}
	_ = targetAddr
}
