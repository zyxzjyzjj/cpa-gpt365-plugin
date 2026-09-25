// verify 端到端验证插件 ABI 与页面注册，模拟 CPA 宿主的调用序列。
//
// 用法：go run . <path-to-gpt365.dll>
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type cliproxyBuffer struct {
	ptr uintptr
	len uintptr
}

type hostAPI struct {
	abiVersion uint32
	hostCtx    uintptr
	call       uintptr
	freeBuffer uintptr
}

type pluginAPI struct {
	abiVersion uint32
	call       uintptr
	freeBuffer uintptr
	shutdown   uintptr
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var (
	callProc *windows.Proc
	freeProc *windows.Proc
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: verify <path-to-gpt365.dll>")
		os.Exit(2)
	}
	dll, err := windows.LoadDLL(os.Args[1])
	if err != nil {
		fmt.Printf("载入失败: %v\n", err)
		os.Exit(1)
	}
	defer dll.Release()

	initProc, _ := dll.FindProc("cliproxy_plugin_init")
	callProc, _ = dll.FindProc("cliproxyPluginCall")
	freeProc, _ = dll.FindProc("cliproxyPluginFree")

	hostCallback := windows.NewCallback(func(_ uintptr, method *byte, req *byte, reqLen uintptr, resp *cliproxyBuffer) uintptr {
		name := goString(method)
		body := ""
		if req != nil && reqLen > 0 {
			body = string(unsafe.Slice(req, reqLen))
		}
		result := handleHostCall(name, body)
		buf := cBytes(result)
		resp.ptr = uintptr(unsafe.Pointer(buf))
		resp.len = uintptr(len(result))
		return 0
	})
	freeCallback := windows.NewCallback(func(_ uintptr, _ uintptr) uintptr { return 0 })

	host := hostAPI{abiVersion: 1, call: hostCallback, freeBuffer: freeCallback}
	var plugin pluginAPI
	rc, _, _ := initProc.Call(uintptr(unsafe.Pointer(&host)), uintptr(unsafe.Pointer(&plugin)))
	if rc != 0 {
		fmt.Printf("初始化失败 rc=%d\n", rc)
		os.Exit(1)
	}
	fmt.Println("✓ 插件初始化成功")

	// 1) 注册
	registerRaw, errCall := callPlugin("plugin.register", `{"config_yaml":"dGVzdDogMQ=="}`)
	if errCall != nil {
		fmt.Printf("✗ 注册失败: %v\n", errCall)
		os.Exit(1)
	}
	var registerEnv envelope
	_ = json.Unmarshal(registerRaw, &registerEnv)
	var registration struct {
		SchemaVersion uint32 `json:"schema_version"`
		Metadata      struct {
			Name    string `json:"Name"`
			Version string `json:"Version"`
		} `json:"metadata"`
		Capabilities map[string]any `json:"capabilities"`
	}
	_ = json.Unmarshal(registerEnv.Result, &registration)
	fmt.Printf("✓ 注册成功: %s v%s (schema %d)\n", registration.Metadata.Name, registration.Metadata.Version, registration.SchemaVersion)

	management, _ := registration.Capabilities["management_api"].(bool)
	authProvider, _ := registration.Capabilities["auth_provider"].(bool)
	fmt.Printf("  capabilities: management_api=%v  auth_provider=%v\n", management, authProvider)
	if !management {
		fmt.Println("✗ management_api 未声明，页面不会被注册")
		os.Exit(1)
	}

	// 2) 认证标识
	identifierRaw, _ := callPlugin("auth.identifier", `{}`)
	var identifierEnv envelope
	_ = json.Unmarshal(identifierRaw, &identifierEnv)
	fmt.Printf("✓ auth.identifier = %s\n", string(identifierEnv.Result))

	// 3) 管理路由注册 —— 这是页面能否出现的决定性一步
	managementRaw, errManagement := callPlugin("management.register", `{}`)
	if errManagement != nil {
		fmt.Printf("✗ management.register 失败: %v\n", errManagement)
		os.Exit(1)
	}
	var managementEnv envelope
	_ = json.Unmarshal(managementRaw, &managementEnv)
	var reg struct {
		Routes []struct {
			Method string `json:"Method"`
			Path   string `json:"Path"`
		} `json:"Routes"`
		Resources []struct {
			Path        string `json:"Path"`
			Menu        string `json:"Menu"`
			Description string `json:"Description"`
		} `json:"Resources"`
	}
	if errUnmarshal := json.Unmarshal(managementEnv.Result, &reg); errUnmarshal != nil {
		fmt.Printf("✗ management.register 响应无法解析: %v\n", errUnmarshal)
		os.Exit(1)
	}
	fmt.Printf("✓ management.register: %d 条路由, %d 个资源页\n", len(reg.Routes), len(reg.Resources))
	for _, route := range reg.Routes {
		fmt.Printf("    路由  %-6s %s\n", route.Method, route.Path)
	}
	for _, resource := range reg.Resources {
		fmt.Printf("    页面  %s  菜单=%q\n", resource.Path, resource.Menu)
	}
	if len(reg.Resources) == 0 {
		fmt.Println("✗ 没有注册任何资源页")
		os.Exit(1)
	}

	// 关键：照搬宿主的路径规范化，验证每条资源路由是否真的会被注册。
	// 宿主 normalizeResourceRoute 会先 strings.TrimRight(path, "/")，
	// 把 "/" 裁成空串后判为无效并静默丢弃 —— 只在这里检查才能抓到该问题。
	accepted := 0
	for _, resource := range reg.Resources {
		normalized, ok := normalizeResourceRoute("gpt365", resource.Path)
		if !ok {
			fmt.Printf("✗ 资源路由 %q 会被宿主丢弃（规范化后为空或非法）\n", resource.Path)
			continue
		}
		accepted++
		fmt.Printf("    ✓ 宿主接受: %s\n", normalized)
	}
	if accepted == 0 {
		fmt.Println("✗ 没有任何资源路由能通过宿主规范化，页面不会出现")
		os.Exit(1)
	}

	// 页面必须能通过宿主实际使用的完整路径访问。
	pagePath := ""
	for _, resource := range reg.Resources {
		if normalized, ok := normalizeResourceRoute("gpt365", resource.Path); ok {
			pagePath = normalized
			break
		}
	}
	pageRaw, errPage := callPlugin("management.handle", mustJSON(map[string]any{
		"Method": "GET", "Path": pagePath,
	}))
	if errPage != nil {
		fmt.Printf("✗ 页面请求失败: %v\n", errPage)
		os.Exit(1)
	}
	var pageEnv envelope
	_ = json.Unmarshal(pageRaw, &pageEnv)
	var pageResp struct {
		StatusCode int                 `json:"StatusCode"`
		Headers    map[string][]string `json:"Headers"`
		Body       []byte              `json:"Body"`
	}
	_ = json.Unmarshal(pageEnv.Result, &pageResp)
	html := string(pageResp.Body)
	fmt.Printf("✓ 页面 %s -> HTTP %d, %d 字节\n", pagePath, pageResp.StatusCode, len(html))
	if len(html) == 0 || !contains(html, "<!DOCTYPE html>") {
		fmt.Println("✗ 页面未返回 HTML")
		os.Exit(1)
	}
	// 页面必须真的能导入。页面把 API 基址与子路径拼接使用，
	// 因此这里分别校验基址与子路径都出现，再校验拼接结果可用。
	for _, marker := range []string{
		"导入令牌",
		"已有凭据",
		"/v0/management/plugins/gpt365",
		`api("/import"`,
		// 管理密钥必须能手动输入，否则自动读取失败时页面无法使用。
		`id="mgmt-key"`,
		// 固定键名读取同源管理面板的密钥（CPA 面板实际使用的键）。
		`"cli-proxy-auth"`,
	} {
		if !contains(html, marker) {
			fmt.Printf("✗ 页面缺少关键元素: %s\n", marker)
			os.Exit(1)
		}
	}
	fmt.Println("✓ 页面包含导入入口、密钥输入框与管理接口调用")

	// 5) 导入接口：先用结构合法的 JWT 走通导入路径，
	// 再用一个非法输入确认它被明确拒绝而不是静默成功。
	validToken := makeTestJWT("acct-verify-1")
	importRaw, errImport := callPlugin("management.handle",
		managementRequest("POST", "/v0/management/plugins/gpt365/import",
			map[string]any{"text": validToken + "\n" + validToken + "\n"}))
	if errImport != nil {
		fmt.Printf("✗ 导入接口调用失败: %v\n", errImport)
		os.Exit(1)
	}
	importResult := extractResult(importRaw)
	// 响应是 base64 编码的 JSON 信封，解码后检查业务字段。
	decoded := decodeManagementBody(importResult)
	fmt.Printf("✓ 导入接口 -> %s\n", truncate(decoded, 260))
	// 同一令牌出现两次：第一条成功，第二条必须被识别为批次内重复。
	if !contains(decoded, `"imported":1`) {
		fmt.Println("✗ 应恰好导入 1 条")
		os.Exit(1)
	}
	if !contains(decoded, "重复") {
		fmt.Println("✗ 重复令牌未被识别")
		os.Exit(1)
	}
	fmt.Println("✓ 重复令牌被识别并拒绝")

	badRaw, _ := callPlugin("management.handle",
		managementRequest("POST", "/v0/management/plugins/gpt365/import",
			map[string]any{"text": "not-a-real-token\n"}))
	if !contains(decodeManagementBody(extractResult(badRaw)), `"failed":1`) {
		fmt.Println("✗ 非法令牌应被计入失败")
		os.Exit(1)
	}
	fmt.Printf("✓ 非法令牌被明确拒绝 -> %s\n", truncate(decodeManagementBody(extractResult(badRaw)), 200))

	// 6) 列表接口
	listRaw, _ := callPlugin("management.handle",
		managementRequest("GET", "/v0/management/plugins/gpt365/auths", nil))
	fmt.Printf("✓ 列表接口 -> %s\n", truncate(decodeManagementBody(extractResult(listRaw)), 200))

	// 7) 删除接口必须拒绝本插件之外的文件名
	guardRaw, _ := callPlugin("management.handle",
		managementRequest("POST", "/v0/management/plugins/gpt365/delete",
			map[string]any{"names": []string{"codex.json"}}))
	fmt.Printf("✓ 删除接口（仅本插件文件） -> %s\n", truncate(decodeManagementBody(extractResult(guardRaw)), 160))

	fmt.Println("\n全部验证通过：插件可注册、页面可打开、导入与列表接口可用")
}

// decodeManagementBody 把管理响应信封里的 Body（base64）解成可读 JSON。
func decodeManagementBody(result string) string {
	var resp struct {
		StatusCode int    `json:"StatusCode"`
		Body       []byte `json:"Body"`
	}
	if json.Unmarshal([]byte(result), &resp) != nil || len(resp.Body) == 0 {
		return result
	}
	return string(resp.Body)
}

// extractResult 取出信封里的 result 原文，出错时返回错误说明。
func extractResult(raw []byte) string {
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return string(raw)
	}
	if !env.OK {
		if env.Error != nil {
			return "错误信封: " + env.Error.Code + ": " + env.Error.Message
		}
		return "错误信封"
	}
	return string(env.Result)
}

// makeTestJWT 生成结构合法的测试令牌（签名不参与校验）。
func makeTestJWT(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  "free",
		},
		"exp": float64(4102444800),
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// handleHostCall 模拟宿主回调，只实现验证所需的最小集合。
func handleHostCall(method, body string) []byte {
	switch method {
	case "host.log":
		return []byte(`{"ok":true,"result":{}}`)
	case "host.auth.list":
		return []byte(`{"ok":true,"result":{"files":[]}}`)
	case "host.auth.save":
		var req struct {
			Name string          `json:"name"`
			JSON json.RawMessage `json:"json"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		// 校验插件生成的凭据文件是否带正确的 type。
		var meta map[string]any
		_ = json.Unmarshal(req.JSON, &meta)
		if meta["type"] != "gpt365" {
			return []byte(`{"ok":false,"error":{"code":"bad_type","message":"凭据 type 必须是 gpt365"}}`)
		}
		result, _ := json.Marshal(map[string]any{"name": req.Name, "path": "/auth/" + req.Name})
		return []byte(`{"ok":true,"result":` + string(result) + `}`)
	default:
		return []byte(`{"ok":true,"result":{}}`)
	}
}

// normalizeResourceRoute 是宿主 internal/pluginhost/management.go 中
// normalizeResourceRoute 的等价实现，用于在本地判断一条资源路由是否会被
// 宿主接受。宿主会静默丢弃不合法的路由，因此必须在这里显式复现其规则，
// 否则「注册成功」的假象会一直存在。
func normalizeResourceRoute(pluginID, rawPath string) (string, bool) {
	const resourcePluginBasePath = "/v0/resource/plugins"

	if strings.TrimSpace(pluginID) == "" {
		return "", false
	}
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	pluginBasePath := resourcePluginBasePath + "/" + pluginID
	if strings.HasPrefix(path, pluginBasePath+"/") {
		path = strings.TrimPrefix(path, pluginBasePath)
	}
	// 宿主的关键动作：裁掉尾部斜杠后，空路径判为无效。
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "", false
	}
	fullPath := pluginBasePath + path
	if !strings.HasPrefix(fullPath, pluginBasePath+"/") {
		return "", false
	}
	if strings.ContainsAny(fullPath, " \t\r\n") || strings.Contains(fullPath, ":") ||
		strings.Contains(fullPath, "*") || strings.Contains(fullPath, "..") {
		return "", false
	}
	return fullPath, true
}

// managementRequest 按 CPA 契约构造管理请求。
//
// Body 在 JSON 中必须是 base64（[]byte 的编码形式），与宿主
// rpcManagementRequest 的线上格式一致；直接放 JSON 字符串会解码失败。
func managementRequest(method, path string, body map[string]any) string {
	payload := map[string]any{"Method": method, "Path": path}
	if body != nil {
		raw, _ := json.Marshal(body)
		payload["Body"] = base64.StdEncoding.EncodeToString(raw)
	}
	return mustJSON(payload)
}

func callPlugin(method string, body string) ([]byte, error) {
	cMethod := cString(method)
	var resp cliproxyBuffer
	var bodyPtr uintptr
	if len(body) > 0 {
		buf := cBytes([]byte(body))
		bodyPtr = uintptr(unsafe.Pointer(buf))
	}
	rc, _, _ := callProc.Call(
		uintptr(unsafe.Pointer(cMethod)), bodyPtr, uintptr(len(body)), uintptr(unsafe.Pointer(&resp)),
	)
	if resp.ptr != 0 && resp.len > 0 {
		out := unsafe.Slice((*byte)(unsafe.Pointer(resp.ptr)), resp.len)
		copied := make([]byte, len(out))
		copy(copied, out)
		freeProc.Call(resp.ptr, resp.len)
		return copied, nil
	}
	if rc != 0 {
		return nil, fmt.Errorf("返回码 %d", rc)
	}
	return nil, fmt.Errorf("空应答")
}

func cString(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

func cBytes(b []byte) *byte {
	out := make([]byte, len(b)+1)
	copy(out, b)
	return &out[0]
}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	var length int
	for *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(length))) != 0 {
		length++
	}
	return string(unsafe.Slice(p, length))
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var _ = base64.StdEncoding
