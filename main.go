// Command gpt365 构建 CLIProxyAPI 原生动态库插件。
//
// 构建（CGO 是必需的，插件 ABI 是 C ABI）：
//
//	CGO_ENABLED=1 go build -buildmode=c-shared -o gpt365.dll .
//
// 产物是标准动态库：CLIProxyAPI 通过 dlopen 载入，调用 cliproxy_plugin_init，
// 之后只经由 JSON-over-C-ABI 的 call 函数表交互。
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static cliproxy_host_api stored_host;
static int host_ready;

static void store_host_api(const cliproxy_host_api* host) {
	if (host == NULL) {
		host_ready = 0;
		return;
	}
	stored_host = *host;
	host_ready = 1;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (!host_ready || stored_host.call == NULL) {
		return 1;
	}
	return stored_host.call(stored_host.host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (host_ready && stored_host.free_buffer != NULL && ptr != NULL) {
		stored_host.free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"unsafe"

	"github.com/zyxzjyzjj/cpa-gpt365-plugin/internal/basispoints"
)

// abiVersion 必须与宿主声明的 ABI 版本一致。
const abiVersion = 1

type rpcEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	ErrorCode  string `json:"code"`
	Message    string `json:"message"`
	Retry      bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *rpcError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

func (e *rpcError) Code() string {
	if e == nil {
		return ""
	}
	return e.ErrorCode
}

func (e *rpcError) Retryable() bool {
	return e != nil && e.Retry
}

var (
	service  *basispoints.Service
	mu       sync.RWMutex
	callback sync.RWMutex
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) (rc C.int) {
	mu.Lock()
	defer mu.Unlock()
	defer func() {
		if recover() != nil {
			rc = 1
		}
	}()
	if host == nil || host.abi_version != C.uint32_t(abiVersion) ||
		host.call == nil || host.free_buffer == nil || plugin == nil {
		return 1
	}
	if service != nil {
		return 1
	}
	callback.Lock()
	C.store_host_api(host)
	callback.Unlock()
	service = basispoints.NewService()
	service.SetHost(callHost)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (rc C.int) {
	mu.RLock()
	defer mu.RUnlock()
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	// 任何 panic 都必须转成错误应答，绝不能穿透 C 边界。
	defer func() {
		if recover() != nil {
			writeResponse(response, failure(&rpcError{
				ErrorCode:  "plugin_panic",
				Message:    "插件回调发生 panic",
				HTTPStatus: http.StatusInternalServerError,
			}))
			rc = 1
		}
	}()
	if method == nil || strings.TrimSpace(C.GoString(method)) == "" {
		writeResponse(response, failure(&rpcError{ErrorCode: "invalid_method", Message: "缺少方法名", HTTPStatus: http.StatusBadRequest}))
		return 1
	}
	if requestLen > C.size_t(math.MaxInt32) || (requestLen > 0 && request == nil) {
		writeResponse(response, failure(&rpcError{ErrorCode: "invalid_request", Message: "请求缓冲区无效", HTTPStatus: http.StatusBadRequest}))
		return 1
	}
	var raw json.RawMessage
	if requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
		if !json.Valid(raw) {
			writeResponse(response, failure(&rpcError{ErrorCode: "invalid_request", Message: "请求 JSON 无效", HTTPStatus: http.StatusBadRequest}))
			return 1
		}
	}
	if service == nil {
		writeResponse(response, failure(&rpcError{ErrorCode: "plugin_stopped", Message: "插件尚未初始化", HTTPStatus: http.StatusServiceUnavailable}))
		return 1
	}
	result, err := service.Handle(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, failure(errorDetails(err)))
		return 1
	}
	successResponse, errMarshal := success(result)
	if errMarshal != nil {
		writeResponse(response, failure(&rpcError{ErrorCode: "serialization_error", Message: "插件响应无法编码", HTTPStatus: http.StatusInternalServerError}))
		return 1
	}
	writeResponse(response, successResponse)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	// 该导出在宿主卸载动态库前调用。此处刻意不触碰 Go 运行时状态
	// （锁、channel、goroutine 同步），避免宿主正在收尾时崩溃。
	// 真正的清理在 plugin.quiesce 中完成。
}

func success(result any) ([]byte, error) {
	if result == nil {
		result = struct{}{}
	}
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(rpcEnvelope{OK: true, Result: raw})
}

func failure(details *rpcError) []byte {
	raw, errMarshal := json.Marshal(rpcEnvelope{OK: false, Error: details})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"serialization_error","message":"plugin error cannot be encoded","http_status":500}}`)
	}
	return raw
}

// errorDetails 把业务错误映射成宿主可理解的状态码与类别。
func errorDetails(err error) *rpcError {
	details := &rpcError{ErrorCode: "plugin_error", Message: err.Error(), HTTPStatus: http.StatusInternalServerError}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() >= 400 && status.StatusCode() <= 599 {
		details.HTTPStatus = status.StatusCode()
	}
	var code interface{ Code() string }
	if errors.As(err, &code) && strings.TrimSpace(code.Code()) != "" {
		details.ErrorCode = code.Code()
	}
	var retry interface{ Retryable() bool }
	if errors.As(err, &retry) {
		details.Retry = retry.Retryable()
	}
	return details
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}

// callHost 是插件调用宿主方法的唯一通道。
func callHost(method string, payload any, out any) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("宿主回调 %s 发生 panic", method)
		}
	}()
	if strings.TrimSpace(method) == "" {
		return errors.New("宿主回调方法名不能为空")
	}
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return fmt.Errorf("编码宿主回调 %s 失败: %w", method, errMarshal)
	}
	if len(rawPayload) > math.MaxInt32 {
		return fmt.Errorf("宿主回调 %s 请求过大", method)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cPayload := C.CBytes(rawPayload)
	defer C.free(cPayload)

	callback.RLock()
	var response C.cliproxy_buffer
	callCode := C.call_host_api(cMethod, (*C.uint8_t)(cPayload), C.size_t(len(rawPayload)), &response)
	if response.ptr == nil || response.len > C.size_t(math.MaxInt32) {
		if response.ptr != nil {
			C.free_host_buffer(response.ptr, response.len)
		}
		callback.RUnlock()
		return fmt.Errorf("宿主回调 %s 返回了无效缓冲区，code=%d", method, int(callCode))
	}
	rawResponse := C.GoBytes(response.ptr, C.int(response.len))
	C.free_host_buffer(response.ptr, response.len)
	callback.RUnlock()

	if callCode != 0 {
		return fmt.Errorf("宿主回调 %s 返回 code=%d", method, int(callCode))
	}
	var envelope rpcEnvelope
	if errUnmarshal := json.Unmarshal(rawResponse, &envelope); errUnmarshal != nil {
		return fmt.Errorf("解析宿主回调 %s 失败: %w", method, errUnmarshal)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return envelope.Error
		}
		return fmt.Errorf("宿主回调 %s 失败", method)
	}
	if out != nil && len(envelope.Result) > 0 {
		if errUnmarshal := json.Unmarshal(envelope.Result, out); errUnmarshal != nil {
			return fmt.Errorf("解析宿主回调 %s 结果失败: %w", method, errUnmarshal)
		}
	}
	return nil
}
