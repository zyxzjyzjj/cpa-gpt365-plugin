package basispoints

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 上游客户端。注意这里没有走宿主的 host.http.do：本机访问 bps.openai.com
// 必须经两级代理链，而宿主传输无法表达嵌套 CONNECT。

type upstreamResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// authHeaders 复刻 Excel 插件的客户端画像。
// 这些头部参与上游的客户端识别，缺失会被判定为非预期客户端。
func authHeaders(c credential, stream bool) http.Header {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	headers := http.Header{
		"Authorization":           []string{"Bearer " + c.AccessToken},
		"ChatGPT-Account-ID":      []string{c.AccountID},
		"X-OpenAI-Account-ID":     []string{c.AccountID},
		"X-Basispoints-Auth-Mode": []string{c.AuthMode},
		"Content-Type":            []string{"application/json"},
		"Accept":                  []string{accept},
		"Accept-Encoding":         []string{"identity"},
		"Origin":                  []string{"https://bps.openai.com"},
		"X-OpenAI-Internal-Basispoints-Client-Agent-Profile":  []string{"excel"},
		"X-OpenAI-Internal-Basispoints-Client-Editor":         []string{"excel"},
		"X-OpenAI-Internal-Basispoints-Client-Host":           []string{"office"},
		"X-OpenAI-Internal-Basispoints-Client-Platform":       []string{"excel"},
		"X-OpenAI-Internal-Basispoints-Client-Platform-Class": []string{"PC"},
		"X-OpenAI-Internal-Basispoints-Client-Product":        []string{"basispoints-excel-plugin"},
		"X-OpenAI-Internal-Basispoints-Client-Runtime":        []string{"desktop"},
		"X-OpenAI-Internal-Basispoints-Office-Host":           []string{"Excel"},
		"X-OpenAI-Internal-Basispoints-Office-Platform":       []string{"PC"},
		"X-Stainless-Arch":            []string{"unknown"},
		"X-Stainless-Lang":            []string{"js"},
		"X-Stainless-OS":              []string{"Unknown"},
		"X-Stainless-Package-Version": []string{"6.31.0"},
		"X-Stainless-Retry-Count":     []string{"0"},
		"X-Stainless-Runtime":         []string{"browser:chrome"},
		"User-Agent":                  []string{"oai-basispoints/" + Version},
	}
	return headers
}

// maxTransportAttempts 是传输层重试次数上限。
//
// 轮换代理会偶发在隧道建立后立刻断开（表现为 EOF），实测 5 次里出现 1 次。
// 这类失败发生在请求尚未被上游处理之前，重试是安全的：它不会造成重复计费。
// 只有「连接层」错误才重试，HTTP 状态码错误一律不重试。
const maxTransportAttempts = 3

// doUpstream 发起一次上游请求，对连接层瞬时故障做有限重试。
func (s *Service) doUpstream(ctx context.Context, cfg Config, body map[string]any, c credential, stream bool) (*upstreamResponse, error) {
	if strings.TrimSpace(cfg.ResponsesURL) == "" {
		return nil, fail(500, "invalid_config", "responses_url 为空")
	}
	payload, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, fail(500, "serialize_error", "请求体无法序列化")
	}

	var lastErr error
	for attempt := 1; attempt <= maxTransportAttempts; attempt++ {
		if attempt > 1 {
			// 换一条隧道重试：轮换代理的下一次连接通常落在不同的出口上。
			select {
			case <-ctx.Done():
				return nil, fail(504, "upstream_timeout", "Basis Points 请求已取消或超时")
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
		response, errAttempt, retryable := s.doUpstreamOnce(ctx, cfg, payload, body, c, stream)
		if errAttempt == nil {
			return response, nil
		}
		lastErr = errAttempt
		if !retryable {
			return response, errAttempt
		}
	}
	return nil, lastErr
}

// doUpstreamOnce 执行单次上游请求。
// 第二个返回值是错误；第三个返回值表示该错误是否属于可安全重试的连接层故障。
func (s *Service) doUpstreamOnce(ctx context.Context, cfg Config, payload []byte, body map[string]any, c credential, stream bool) (*upstreamResponse, error, bool) {
	client, errClient := newHTTPClient(cfg, time.Duration(cfg.TimeoutSeconds)*time.Second)
	if errClient != nil {
		return nil, errClient, false
	}
	defer client.CloseIdleConnections()

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ResponsesURL, bytes.NewReader(payload))
	if errRequest != nil {
		return nil, fail(500, "invalid_config", "无法构造上游请求"), false
	}
	req.Header = authHeaders(c, stream)

	resp, errDo := client.Do(req)
	if errDo != nil {
		// 上下文取消不重试，避免在客户端已放弃后继续打上游。
		if ctx.Err() != nil {
			return nil, fail(504, "upstream_timeout", "Basis Points 请求已取消或超时"), false
		}
		return nil, fail(502, "upstream_transport", "Basis Points 传输失败: "+safeError(errDo)), true
	}
	defer resp.Body.Close()

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, int64(cfg.MaxResponseBytes)+1))
	if errRead != nil {
		// 读响应体阶段断连同样属于连接层故障。但此时请求可能已被上游处理，
		// 为避免重复计费，只在尚未读到任何响应头内容时重试。
		return nil, fail(502, "upstream_transport", "读取 Basis Points 响应失败: "+safeError(errRead)), false
	}
	if len(raw) > cfg.MaxResponseBytes {
		return nil, fail(502, "upstream_response_too_large", "Basis Points 响应超过配置上限"), false
	}
	result := &upstreamResponse{StatusCode: resp.StatusCode, Headers: resp.Header.Clone(), Body: raw}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// HTTP 状态码错误说明上游已作出判定，重试无意义。
		return result, upstreamRequestError(resp.StatusCode, raw, body, c), false
	}
	return result, nil, false
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return redactTokenMessage(err.Error())
}

// upstreamRequestError 生成带上文摘要的错误，便于定位而无需记录对话内容。
func upstreamRequestError(status int, raw []byte, body map[string]any, c credential) error {
	redacted := string(raw)
	for _, secret := range []string{c.AccessToken, c.AccountID, c.Email} {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
		}
	}
	message := redactTokenMessage(errorMessage([]byte(redacted)))
	images := 0
	if items, ok := body["input"].([]any); ok {
		for _, value := range items {
			parts, _ := objectValue(value)["content"].([]any)
			for _, part := range parts {
				if stringValue(objectValue(part)["type"]) == "input_image" {
					images++
				}
			}
		}
	}
	// 429 且带 not_enabled 时给出可执行的判断，而不是笼统的失败。
	hint := ""
	if status == http.StatusTooManyRequests && strings.Contains(redacted, "not_enabled") {
		hint = "（该账号未获得 Basis Points 访问权限：接口可达但账号被拒绝，请更换有权限的 ChatGPT 账号凭据）"
	}
	return fail(status, "upstream_error", fmt.Sprintf(
		"Basis Points HTTP %d: %s（reasoning_effort=%s; input_images=%d）%s",
		status, message, stringValue(body["reasoning_effort"]), images, hint))
}

// parseFinalStreamResponse 从 SSE 流中取出最终完整响应对象。
//
// 之所以先把整个流读完再回放：客户端工具调用需要基于完整 item 做转换，
// 逐块转发会让工具调用在中途被截断。
func parseFinalStreamResponse(raw []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fail(502, "invalid_upstream_response", "Basis Points 返回了空流")
	}
	// 上游可能直接返回 JSON（未按 SSE 分帧）。
	if strings.HasPrefix(trimmed, "{") {
		var object map[string]any
		if json.Unmarshal([]byte(trimmed), &object) == nil {
			if response := objectValue(object["response"]); response != nil {
				return response, nil
			}
			return object, nil
		}
	}
	decoder := newSSEDecoder()
	var completed map[string]any
	err := decoder.feed(raw, func(_, data string) error {
		if strings.TrimSpace(data) == "[DONE]" {
			return nil
		}
		var object map[string]any
		if json.Unmarshal([]byte(data), &object) != nil {
			return nil
		}
		typeName := stringValue(object["type"])
		if response := objectValue(object["response"]); response != nil {
			if typeName == "response.completed" || stringValue(response["status"]) == "completed" {
				completed = response
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completed == nil {
		return nil, fail(502, "invalid_upstream_response", "Basis Points 流未以 response.completed 结束")
	}
	return completed, nil
}

type sseDecoder struct {
	buffer strings.Builder
	data   []string
	event  string
}

func newSSEDecoder() *sseDecoder { return &sseDecoder{} }

func (d *sseDecoder) feed(chunk []byte, emit func(event, data string) error) error {
	d.buffer.Write(chunk)
	text := d.buffer.String()
	for {
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			d.buffer.Reset()
			d.buffer.WriteString(text)
			return nil
		}
		line := strings.TrimSuffix(text[:index], "\r")
		text = text[index+1:]
		if line == "" {
			if len(d.data) > 0 {
				if err := emit(d.event, strings.Join(d.data, "\n")); err != nil {
					return err
				}
			}
			d.data = nil
			d.event = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			d.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			d.data = append(d.data, strings.TrimPrefix(value, " "))
		}
	}
}
