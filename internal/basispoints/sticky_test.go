package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---- 代理池 -----------------------------------------------------------------

// TestPoolAssignsStableProxyPerAccount 同一账号必须始终拿到同一出口。
//
// 这是代理池的核心承诺：出口随账号固定，不随请求次数变化，
// 否则同一账号在多个 IP 间跳变，风控特征比共用出口更明显。
func TestPoolAssignsStableProxyPerAccount(t *testing.T) {
	pool := ProxyPoolConfig{
		Enabled: true,
		Entries: []ProxyPoolEntry{
			{Name: "jp-1", URL: "http://user:pass@jp1.example.com:10000"},
			{Name: "jp-2", URL: "http://user:pass@jp2.example.com:10000"},
			{Name: "jp-3", URL: "http://user:pass@jp3.example.com:10000"},
		},
	}
	if err := pool.normalize(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	for _, account := range []string{"acct-a", "acct-b", "acct-c", "acct-d"} {
		first, ok := pool.pickForAccount(account)
		if !ok {
			t.Fatalf("账号 %s 应分配到出口", account)
		}
		for i := 0; i < 20; i++ {
			again, _ := pool.pickForAccount(account)
			if again.URL != first.URL {
				t.Errorf("账号 %s 的出口不稳定: %q != %q", account, again.URL, first.URL)
			}
		}
	}
}

// TestPoolDistributesAcrossAccounts 不同账号应尽量落在不同出口。
func TestPoolDistributesAcrossAccounts(t *testing.T) {
	pool := ProxyPoolConfig{
		Enabled: true,
		Entries: []ProxyPoolEntry{
			{Name: "a", URL: "http://a.example.com:1"},
			{Name: "b", URL: "http://b.example.com:1"},
			{Name: "c", URL: "http://c.example.com:1"},
		},
	}
	_ = pool.normalize()
	used := map[string]bool{}
	for i := 0; i < 60; i++ {
		account := "account-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		entry, ok := pool.pickForAccount(account)
		if !ok {
			t.Fatal("应分配到出口")
		}
		used[entry.Name] = true
	}
	if len(used) < 2 {
		t.Errorf("60 个账号只用到 %d 个出口，分配过于集中", len(used))
	}
}

// TestPoolOrderIndependent 条目顺序不应影响分配结果。
func TestPoolOrderIndependent(t *testing.T) {
	entries := []ProxyPoolEntry{
		{Name: "x", URL: "http://x.example.com:1"},
		{Name: "y", URL: "http://y.example.com:1"},
		{Name: "z", URL: "http://z.example.com:1"},
	}
	first := ProxyPoolConfig{Enabled: true, Entries: entries}
	_ = first.normalize()

	reversed := []ProxyPoolEntry{entries[2], entries[1], entries[0]}
	second := ProxyPoolConfig{Enabled: true, Entries: reversed}
	_ = second.normalize()

	for _, account := range []string{"a1", "b2", "c3", "d4", "e5"} {
		one, _ := first.pickForAccount(account)
		two, _ := second.pickForAccount(account)
		if one.URL != two.URL {
			t.Errorf("账号 %s 的分配随条目顺序变化: %q != %q", account, one.URL, two.URL)
		}
	}
}

// TestPoolRejectsInvalidEntry 非法出口地址必须在校验期被拒绝。
func TestPoolRejectsInvalidEntry(t *testing.T) {
	pool := ProxyPoolConfig{
		Enabled: true,
		Entries: []ProxyPoolEntry{{Name: "bad", URL: "socks5://127.0.0.1:1080"}},
	}
	if err := pool.normalize(); err == nil {
		t.Fatal("非 http/https 出口应被拒绝")
	}
}

// TestPoolRejectsDuplicateNames 重名会让分配结果不可预期。
func TestPoolRejectsDuplicateNames(t *testing.T) {
	pool := ProxyPoolConfig{
		Enabled: true,
		Entries: []ProxyPoolEntry{
			{Name: "same", URL: "http://a.example.com:1"},
			{Name: "same", URL: "http://b.example.com:1"},
		},
	}
	if err := pool.normalize(); err == nil {
		t.Fatal("重名出口应被拒绝")
	}
}

// TestPoolStrictWithoutEntries 严格模式下空池必须直接报错。
func TestPoolStrictWithoutEntries(t *testing.T) {
	pool := ProxyPoolConfig{Enabled: true, Strict: true}
	if err := pool.normalize(); err == nil {
		t.Fatal("strict 模式下空池应被拒绝")
	}
}

// TestEffectiveChainKeepsLocalHop 账号代理只能替换远程段，本地跳必须保留。
func TestEffectiveChainKeepsLocalHop(t *testing.T) {
	base := ProxyChainConfig{
		Enabled:        true,
		LocalProxy:     "http://127.0.0.1:15732",
		RemoteProxy:    "http://global.example.com:10000",
		RemoteUsername: "global-user",
		RemotePassword: "global-pass",
	}
	merged, err := effectiveProxyChain(base, "http://acct:secret@acct-proxy.example.com:9000")
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if merged.LocalProxy != base.LocalProxy {
		t.Errorf("本地跳被改动: %q", merged.LocalProxy)
	}
	if merged.RemoteProxy != "http://acct:secret@acct-proxy.example.com:9000" {
		t.Errorf("远程段未替换: %q", merged.RemoteProxy)
	}
	// 账号代理自带凭据时，必须清掉全局凭据，避免把凭据发给别的出口。
	if merged.RemoteUsername != "" || merged.RemotePassword != "" {
		t.Errorf("应清空全局代理凭据，实际 user=%q pass=%q", merged.RemoteUsername, merged.RemotePassword)
	}
}

// TestEffectiveChainWithoutAccountProxy 未分配出口时保持原链路。
func TestEffectiveChainWithoutAccountProxy(t *testing.T) {
	base := ProxyChainConfig{Enabled: true, LocalProxy: "http://127.0.0.1:15732", RemoteProxy: "http://r:1"}
	merged, err := effectiveProxyChain(base, "")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if merged != base {
		t.Errorf("无账号代理时链路不应变化: %+v", merged)
	}
}

// TestBindProxyToAuthJSON 代理必须写进凭据，重启后分配才不丢。
func TestBindProxyToAuthJSON(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"type": Provider, "access_token": "tok"})
	bound, err := bindProxyToAuthJSON(raw, "http://p.example.com:1")
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	var root map[string]any
	_ = json.Unmarshal(bound, &root)
	if stringValue(root[attrProxyURL]) != "http://p.example.com:1" {
		t.Errorf("proxy_url = %q", stringValue(root[attrProxyURL]))
	}
	if stringValue(root["type"]) != Provider {
		t.Errorf("type 必须保持为 %q，实际 %q", Provider, stringValue(root["type"]))
	}
}

// TestProxyURLFromMetadata 执行阶段必须能从属性里读回代理。
func TestProxyURLFromMetadata(t *testing.T) {
	if got := proxyURLFromMetadata(map[string]string{attrProxyURL: "http://x:1"}, nil); got != "http://x:1" {
		t.Errorf("从 Attributes 读取失败: %q", got)
	}
	if got := proxyURLFromMetadata(nil, map[string]any{attrProxyURL: "http://y:1"}); got != "http://y:1" {
		t.Errorf("从 Metadata 读取失败: %q", got)
	}
	if got := proxyURLFromMetadata(nil, nil); got != "" {
		t.Errorf("无数据时应返回空: %q", got)
	}
}

// TestDescribePoolHidesCredentials 池状态不得回传代理口令。
func TestDescribePoolHidesCredentials(t *testing.T) {
	pool := ProxyPoolConfig{
		Enabled: true,
		Entries: []ProxyPoolEntry{
			{Name: "jp", URL: "http://user:SuperSecret123@jp.example.com:10000"},
		},
	}
	_ = pool.normalize()
	described := pool.describePool()
	encoded, _ := json.Marshal(described)
	if strings.Contains(string(encoded), "SuperSecret123") {
		t.Errorf("池状态泄露了口令: %s", encoded)
	}
	if !strings.Contains(string(encoded), "jp.example.com") {
		t.Errorf("池状态应包含主机名: %s", encoded)
	}
}

// ---- 会话粘性 ---------------------------------------------------------------

// TestMergeKeepEntriesRestoresOriginalURL 地址留空的条目必须沿用原地址。
//
// 页面上已有条目的口令不会回传，编辑时地址框留空表示「保持不变」。
// 若这里不还原，保存一次就会把出口地址清空，代理池静默失效。
func TestMergeKeepEntriesRestoresOriginalURL(t *testing.T) {
	previous := []ProxyPoolEntry{
		{Name: "jp-1", URL: "http://user:secret@jp1.example.com:10000"},
		{Name: "jp-2", URL: "http://user:secret@jp2.example.com:10000"},
	}
	next := ProxyPoolConfig{
		Enabled: true,
		// 页面只回传名称，地址为空。
		Entries:     []ProxyPoolEntry{{Name: "jp-1", URL: ""}},
		KeepEntries: []string{"jp-1", "jp-2"},
	}
	next.mergeKeepEntries(previous)
	if err := next.normalize(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(next.Entries) != 2 {
		t.Fatalf("条目数 = %d，期望 2（应保留未出现在页面上的条目）", len(next.Entries))
	}
	byName := map[string]string{}
	for _, entry := range next.Entries {
		byName[entry.Name] = entry.URL
	}
	if byName["jp-1"] != "http://user:secret@jp1.example.com:10000" {
		t.Errorf("jp-1 地址未还原: %q", byName["jp-1"])
	}
	if byName["jp-2"] != "http://user:secret@jp2.example.com:10000" {
		t.Errorf("jp-2 地址未还原: %q", byName["jp-2"])
	}
	// 辅助字段必须被清空，不能留在生效配置里。
	if len(next.KeepEntries) != 0 {
		t.Errorf("KeepEntries 应被清空: %v", next.KeepEntries)
	}
}

// TestMergeKeepEntriesAllowsRealUpdate 显式填写的地址必须覆盖原值。
func TestMergeKeepEntriesAllowsRealUpdate(t *testing.T) {
	previous := []ProxyPoolEntry{{Name: "jp-1", URL: "http://old.example.com:10000"}}
	next := ProxyPoolConfig{
		Enabled:     true,
		Entries:     []ProxyPoolEntry{{Name: "jp-1", URL: "http://new.example.com:20000"}},
		KeepEntries: []string{"jp-1"},
	}
	next.mergeKeepEntries(previous)
	if next.Entries[0].URL != "http://new.example.com:20000" {
		t.Errorf("显式地址应覆盖原值，实际 %q", next.Entries[0].URL)
	}
}

// TestMergeKeepEntriesWithoutPrevious 无历史配置时不应凭空造出条目。
func TestMergeKeepEntriesWithoutPrevious(t *testing.T) {
	next := ProxyPoolConfig{
		Enabled:     true,
		KeepEntries: []string{"ghost"},
	}
	next.mergeKeepEntries(nil)
	if len(next.Entries) != 0 {
		t.Errorf("不应还原不存在的条目: %v", next.Entries)
	}
}

// TestDescribeProxyURLHidesCredentials 池状态只暴露主机名，不回传口令。
func TestDescribeProxyURLHidesCredentials(t *testing.T) {
	host, hasCreds := describeProxyURL("http://user:SuperSecret@jp.example.com:10000")
	if host != "jp.example.com:10000" {
		t.Errorf("host = %q", host)
	}
	if !hasCreds {
		t.Error("应识别出该地址带凭据")
	}
	if strings.Contains(host, "SuperSecret") {
		t.Errorf("主机名中不得含口令: %q", host)
	}

	plainHost, plainCreds := describeProxyURL("http://jp2.example.com:10000")
	if plainHost != "jp2.example.com:10000" || plainCreds {
		t.Errorf("无凭据地址解析错误: %q %v", plainHost, plainCreds)
	}
}

// TestSessionBinderSticksAndExpires 绑定在 TTL 内有效，过期后失效。
func TestSessionBinderSticksAndExpires(t *testing.T) {
	binder := newSessionBinder()
	binder.bind("session-1", "auth-a", 50*time.Millisecond, 10)
	if got, ok := binder.lookup("session-1"); !ok || got != "auth-a" {
		t.Fatalf("绑定未生效: %q %v", got, ok)
	}
	time.Sleep(70 * time.Millisecond)
	if _, ok := binder.lookup("session-1"); ok {
		t.Error("过期绑定应失效")
	}
}

// TestSessionBinderEvictsOldest 超出容量时淘汰最旧记录。
func TestSessionBinderEvictsOldest(t *testing.T) {
	binder := newSessionBinder()
	for i := 0; i < 5; i++ {
		binder.bind("s"+string(rune('0'+i)), "auth-"+string(rune('0'+i)), time.Hour, 3)
	}
	if got := binder.count(); got != 3 {
		t.Errorf("绑定数 = %d，期望 3", got)
	}
	if _, ok := binder.lookup("s0"); ok {
		t.Error("最旧绑定应被淘汰")
	}
	if _, ok := binder.lookup("s4"); !ok {
		t.Error("最新绑定应保留")
	}
}

// TestSessionKeyFromHeaders 会话键必须优先取显式会话标识。
func TestSessionKeyFromHeaders(t *testing.T) {
	cases := []struct {
		name     string
		headers  map[string][]string
		metadata map[string]any
		want     string
	}{
		{"metadata 优先", map[string][]string{"X-Session-Id": {"h"}}, map[string]any{"session_id": "m"}, "meta:m"},
		{"请求头", map[string][]string{"X-Session-Id": {"abc"}}, nil, "hdr:abc"},
		{"大小写不敏感", map[string][]string{"x-session-id": {"abc"}}, nil, "hdr:abc"},
		{"会话 ID 头", map[string][]string{"Session-Id": {"xyz"}}, nil, "hdr:xyz"},
		{"无会话", map[string][]string{"Content-Type": {"application/json"}}, nil, ""},
		{"空值忽略", map[string][]string{"X-Session-Id": {"   "}}, nil, ""},
	}
	for _, tc := range cases {
		if got := sessionKeyFrom(tc.headers, tc.metadata); got != tc.want {
			t.Errorf("%s: sessionKeyFrom = %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestSchedulerPickReturnsBoundAccount 已绑定的会话应返回同一账号。
func TestSchedulerPickReturnsBoundAccount(t *testing.T) {
	service := NewService()
	service.sessions.bind("hdr:abc", "auth-2", time.Hour, 10)

	request, _ := json.Marshal(map[string]any{
		"Options": map[string]any{
			"Headers": map[string][]string{"X-Session-Id": {"abc"}},
		},
		"Candidates": []map[string]any{
			{"ID": "auth-1", "Status": "active"},
			{"ID": "auth-2", "Status": "active"},
			{"ID": "auth-3", "Status": "active"},
		},
	})
	result, err := service.schedulerPick(request)
	if err != nil {
		t.Fatalf("调度失败: %v", err)
	}
	response := objectValue(result)
	if response["Handled"] != true {
		t.Fatal("应接管本次调度")
	}
	if stringValue(response["AuthID"]) != "auth-2" {
		t.Errorf("选中账号 = %q，期望 auth-2", stringValue(response["AuthID"]))
	}
}

// TestSchedulerPickDelegatesWhenUnbound 未绑定的会话交给内置调度器。
func TestSchedulerPickDelegatesWhenUnbound(t *testing.T) {
	service := NewService()
	request, _ := json.Marshal(map[string]any{
		"Options":    map[string]any{"Headers": map[string][]string{"X-Session-Id": {"new"}}},
		"Candidates": []map[string]any{{"ID": "auth-1", "Status": "active"}},
	})
	result, err := service.schedulerPick(request)
	if err != nil {
		t.Fatalf("调度失败: %v", err)
	}
	if objectValue(result)["Handled"] != false {
		t.Error("未绑定时不应接管")
	}
}

// TestSchedulerPickFallsBackWhenBoundAccountMissing 绑定账号不在候选里时回退。
//
// 账号可能被删除或停用，此时必须交给内置调度器，而不是把请求钉死。
func TestSchedulerPickFallsBackWhenBoundAccountMissing(t *testing.T) {
	service := NewService()
	service.sessions.bind("hdr:gone", "auth-removed", time.Hour, 10)
	request, _ := json.Marshal(map[string]any{
		"Options":    map[string]any{"Headers": map[string][]string{"X-Session-Id": {"gone"}}},
		"Candidates": []map[string]any{{"ID": "auth-other", "Status": "active"}},
	})
	result, _ := service.schedulerPick(request)
	if objectValue(result)["Handled"] != false {
		t.Error("绑定账号不在候选中时应回退")
	}
}

// TestSchedulerPickSkipsDisabledCandidate 绑定的账号被停用时必须回退。
func TestSchedulerPickSkipsDisabledCandidate(t *testing.T) {
	service := NewService()
	service.sessions.bind("hdr:dis", "auth-1", time.Hour, 10)
	request, _ := json.Marshal(map[string]any{
		"Options": map[string]any{"Headers": map[string][]string{"X-Session-Id": {"dis"}}},
		"Candidates": []map[string]any{
			{"ID": "auth-1", "Status": "disabled"},
			{"ID": "auth-2", "Status": "active"},
		},
	})
	result, _ := service.schedulerPick(request)
	if objectValue(result)["Handled"] != false {
		t.Error("绑定账号被停用时应回退")
	}
}

// TestSchedulerPickRespectsDisabledSetting 关闭粘性后不应接管调度。
func TestSchedulerPickRespectsDisabledSetting(t *testing.T) {
	service := NewService()
	service.sessions.bind("hdr:off", "auth-1", time.Hour, 10)
	service.mu.Lock()
	service.cfg.StickySession.Enabled = false
	service.mu.Unlock()

	request, _ := json.Marshal(map[string]any{
		"Options":    map[string]any{"Headers": map[string][]string{"X-Session-Id": {"off"}}},
		"Candidates": []map[string]any{{"ID": "auth-1", "Status": "active"}},
	})
	result, _ := service.schedulerPick(request)
	if objectValue(result)["Handled"] != false {
		t.Error("粘性关闭时不应接管调度")
	}
}

// TestSessionKeyFromExecutorBody 会话键要能从请求体兜底推导。
func TestSessionKeyFromExecutorBody(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"prompt_cache_key": "cache-xyz", "input": "hi"})
	request := ExecutorRequest{OriginalRequest: body}
	if got := sessionKeyFromExecutor(request); got != "body:cache-xyz" {
		t.Errorf("sessionKeyFromExecutor = %q，期望 body:cache-xyz", got)
	}

	nested, _ := json.Marshal(map[string]any{
		"input":    "hi",
		"metadata": map[string]any{"session_id": "nested-1"},
	})
	if got := sessionKeyFromExecutor(ExecutorRequest{OriginalRequest: nested}); got != "bodymeta:nested-1" {
		t.Errorf("嵌套会话标识未取到: %q", got)
	}

	empty, _ := json.Marshal(map[string]any{"input": "hi"})
	if got := sessionKeyFromExecutor(ExecutorRequest{OriginalRequest: empty}); got != "" {
		t.Errorf("无会话标识时应返回空: %q", got)
	}
}
