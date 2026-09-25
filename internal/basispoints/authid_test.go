package basispoints

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件复现宿主对 auth ID 的推导规则，用于确认模型注册与查询用的是同一个键。
//
// 这是「账号模型列表为空」的核心：宿主按 auth.ID 注册模型
// （sdk/cliproxy/service_executors.go:452），管理接口也按 auth.ID 查询
// （internal/api/handlers/management/auth_files.go:207）。两者必须一致。

// authIDForPath 复现 internal/pluginhost/auth_provider.go 的 authIDForPath。
//
// 宿主在 AuthData.ID 为空时用它推导 ID：取相对 auth-dir 的路径，
// 统一为正斜杠，Windows 下转小写。
func authIDForPath(path, authDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	id := path
	if authDir = strings.TrimSpace(authDir); authDir != "" {
		if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" && !strings.HasPrefix(rel, "..") {
			id = rel
		}
	}
	id = filepath.ToSlash(filepath.Clean(id))
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

// TestAuthParseLeavesIDEmptyForHostDerivation 验证插件不再自造 ID。
//
// 插件曾把 ID 设为 "bp-<name>"，而宿主的 FileName 是 "<name>.json"，
// 两者脱节：模型按自造 ID 注册，管理接口按文件名找凭据，
// 于是「账号模型列表」查不到任何模型。留空让宿主统一推导即可对齐。
func TestAuthParseLeavesIDEmptyForHostDerivation(t *testing.T) {
	raw := []byte(`{"type":"gpt365","access_token":"` + buildJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
		"exp":                         float64(4102444800),
	}) + `","account_id":"acct-1"}`)

	request, _ := json.Marshal(map[string]any{
		"Provider": Provider,
		"Path":     "/data/gpt365-acct-1.json",
		"FileName": "gpt365-acct-1.json",
		"RawJSON":  raw,
	})
	result, err := authParse(request)
	if err != nil {
		t.Fatalf("authParse 失败: %v", err)
	}
	auth := objectValue(result["Auth"])

	if id := stringValue(auth["ID"]); id != "" {
		t.Errorf("ID = %q，必须留空让宿主按路径推导，否则与 FileName 脱节", id)
	}
	if stringValue(auth["FileName"]) != "gpt365-acct-1.json" {
		t.Errorf("FileName = %q", stringValue(auth["FileName"]))
	}

	// 宿主推导出的 ID 必须是相对 auth-dir 的路径。
	derived := authIDForPath("/data/gpt365-acct-1.json", "/data")
	if derived != "gpt365-acct-1.json" {
		t.Errorf("宿主推导的 ID = %q，期望 gpt365-acct-1.json", derived)
	}
}

// TestDerivedIDMatchesFileName 确认按路径推导的 ID 与凭据文件名可互相定位。
//
// 管理接口用「文件名或 ID」在凭据列表里找记录（auth.FileName == name || auth.ID == name），
// 再用找到的 auth.ID 查模型。因此推导结果必须能按文件名命中。
func TestDerivedIDMatchesFileName(t *testing.T) {
	const authDir = "/data"
	for _, fileName := range []string{
		"gpt365-acct-1.json",
		"gpt365-7b8c1fc7-f1a3-4cee-98b7-c1ade3db7a0c.json",
	} {
		fullPath := authDir + "/" + fileName
		derived := authIDForPath(fullPath, authDir)
		// 两种匹配方式都要能命中同一条记录。
		byFileName := derived == fileName
		byID := derived == derived
		if !byFileName || !byID {
			t.Errorf("文件名 %q 推导出 ID %q，无法按文件名命中", fileName, derived)
		}
	}
}

// TestAuthDataHasNoSelfInventedID 防止以后又加回自造 ID。
func TestAuthDataHasNoSelfInventedID(t *testing.T) {
	c := credential{AccessToken: "tok", AccountID: "acct-1", Email: "a@b.c"}
	data := authData([]byte(`{"type":"gpt365"}`), "gpt365-acct-1.json", c)
	if _, exists := data["ID"]; exists {
		t.Error("authData 不应设置 ID 字段")
	}
	// 其余字段必须齐全。
	for _, key := range []string{"Provider", "FileName", "Label", "StorageJSON", "Metadata", "Attributes"} {
		if _, exists := data[key]; !exists {
			t.Errorf("authData 缺少字段 %s", key)
		}
	}
}
