// abicheck 模拟 CPA 宿主通过 dlopen 载入插件，调用 init，
// 再经 JSON-over-C-ABI 调用若干方法，确认 ABI 契约成立。
//
// 构建：go build -o abicheck.exe .
// 用法：./abicheck.exe <path-to-gpt365.dll>
package main

import (
	"encoding/json"
	"fmt"
	"os"
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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: abicheck <path-to-plugin.dll>")
		os.Exit(2)
	}
	dllPath := os.Args[1]
	dll, err := windows.LoadDLL(dllPath)
	if err != nil {
		fmt.Printf("载入失败: %v\n", err)
		os.Exit(1)
	}
	defer dll.Release()
	fmt.Printf("已载入 %s\n", dllPath)

	initProc, err := dll.FindProc("cliproxy_plugin_init")
	if err != nil {
		fmt.Printf("找不到 cliproxy_plugin_init: %v\n", err)
		os.Exit(1)
	}
	callProc, err := dll.FindProc("cliproxyPluginCall")
	if err != nil {
		fmt.Printf("找不到 cliproxyPluginCall: %v\n", err)
		os.Exit(1)
	}
	freeProc, err := dll.FindProc("cliproxyPluginFree")
	if err != nil {
		fmt.Printf("找不到 cliproxyPluginFree: %v\n", err)
		os.Exit(1)
	}

	// 第一步：宿主回调缺失时，init 必须拒绝初始化。
	host := hostAPI{abiVersion: 1}
	var plugin pluginAPI
	rc, _, _ := initProc.Call(uintptr(unsafe.Pointer(&host)), uintptr(unsafe.Pointer(&plugin)))
	fmt.Printf("cliproxy_plugin_init 返回 %d（期望 1：宿主回调为空时应拒绝初始化）\n", rc)

	// 第二步：提供最小可用宿主回调，验证注册路径。
	hostCallback := windows.NewCallback(func(_ uintptr, method *byte, _ *byte, _ uintptr, resp *cliproxyBuffer) uintptr {
		payload := []byte(`{"ok":true,"result":{}}`)
		if goString(method) == "" {
			payload = []byte(`{"ok":false,"error":{"code":"bad","message":"no method"}}`)
		}
		buf := cBytes(payload)
		resp.ptr = uintptr(unsafe.Pointer(buf))
		resp.len = uintptr(len(payload))
		return 0
	})
	freeCallback := windows.NewCallback(func(_ uintptr, _ uintptr) uintptr {
		return 0
	})
	host.call = hostCallback
	host.freeBuffer = freeCallback

	var plugin2 pluginAPI
	rc2, _, _ := initProc.Call(uintptr(unsafe.Pointer(&host)), uintptr(unsafe.Pointer(&plugin2)))
	fmt.Printf("带宿主回调初始化返回 %d（期望 0）\n", rc2)
	if rc2 != 0 {
		fmt.Println("初始化失败，终止验证")
		os.Exit(1)
	}
	fmt.Printf("插件声明 ABI 版本 %d\n", plugin2.abiVersion)

	// 第三步：逐个调用关键方法，校验 JSON 信封。
	cases := []struct {
		method string
		body   string
	}{
		{"auth.identifier", `{}`},
		{"executor.identifier", `{}`},
		{"model.register", `{}`},
		{"plugin.register", `{"config_yaml":"dGVzdDogMQ=="}`},
	}
	for _, tc := range cases {
		raw, errCall := callPlugin(callProc, freeProc, tc.method, []byte(tc.body))
		if errCall != nil {
			fmt.Printf("  %-22s -> 调用失败: %v\n", tc.method, errCall)
			continue
		}
		var env envelope
		if json.Unmarshal(raw, &env) != nil {
			fmt.Printf("  %-22s -> 应答非 JSON: %s\n", tc.method, truncate(string(raw), 120))
			continue
		}
		if env.OK {
			fmt.Printf("  %-22s -> ok, result=%s\n", tc.method, truncate(string(env.Result), 140))
		} else {
			msg := ""
			if env.Error != nil {
				msg = env.Error.Code + ": " + env.Error.Message
			}
			fmt.Printf("  %-22s -> 错误 %s\n", tc.method, truncate(msg, 140))
		}
	}

	// 第四步：未知方法必须被明确拒绝，而不是静默成功。
	raw, _ := callPlugin(callProc, freeProc, "no.such.method", []byte(`{}`))
	fmt.Printf("  %-22s -> %s\n", "no.such.method", truncate(string(raw), 140))

	fmt.Println("加载验证完成")
}

func callPlugin(callProc, freeProc *windows.Proc, method string, body []byte) ([]byte, error) {
	cMethod := cString(method)
	var resp cliproxyBuffer
	var bodyPtr uintptr
	if len(body) > 0 {
		buf := cBytes(body)
		bodyPtr = uintptr(unsafe.Pointer(buf))
	}
	rc, _, _ := callProc.Call(
		uintptr(unsafe.Pointer(cMethod)),
		bodyPtr,
		uintptr(len(body)),
		uintptr(unsafe.Pointer(&resp)),
	)
	// 非零返回码表示调用失败，但插件仍可能写出了错误应答；
	// 必须读取并释放该缓冲区，否则既看不到错误原因又会泄漏内存。
	if resp.ptr != 0 && resp.len > 0 {
		out := unsafe.Slice((*byte)(unsafe.Pointer(resp.ptr)), resp.len)
		copied := make([]byte, len(out))
		copy(copied, out)
		freeProc.Call(resp.ptr, resp.len)
		return copied, nil
	}
	if rc != 0 {
		return nil, fmt.Errorf("返回码 %d 且无应答缓冲区", rc)
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
	for {
		if *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(length))) == 0 {
			break
		}
		length++
	}
	return string(unsafe.Slice(p, length))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
