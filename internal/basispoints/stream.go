package basispoints

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// 本文件实现流式转发。
//
// 实测依据（同一账号、同一提示）：
//
//	上游流式响应 298 KB / 47 个事件，全部收完耗时 6.4 秒。
//	真实对话生成更长，可达 60 秒以上。
//
// 早期实现把整个流读完、解析、再合成事件序列一次性返回，客户端在这期间
// 收不到任何字节，表现为「对话一直卡住」，并常在 60 秒左右被客户端放弃
// （HTTP 499）。
//
// 现在的策略是「正文边收边发，工具调用延后到完整响应」：
//
//   - 文本、思考等正文事件逐条立即转发，客户端立刻开始显示内容。
//   - 涉及原生运输调用（run_officejs）的事件先缓存，等流结束、拿到完整
//     item 后再还原成客户端工具调用并补发。
//
// 这样既消除了等待，又保住了工具偷渡方案对完整 item 的依赖。

// streamForwarder 把上游 SSE 边读边推给宿主。
type streamForwarder struct {
	service  *Service
	streamID string
	source   map[string]any

	// emitFailed 记录推送是否已失败（通常是客户端断开）。
	emitFailed bool

	// raw 累积完整响应，供结束时还原工具调用。
	raw strings.Builder

	// pending 缓存与工具调用相关的事件，延后决定是否转发。
	pending []string

	// sequence 用于补发事件时继续递增序号。
	sequence int
}

// executeStreamAsync 启动异步转发并立即返回响应头。
func (s *Service) executeStreamAsync(
	request ExecutorRequest,
	cfg Config,
	source map[string]any,
	body map[string]any,
	c credential,
) (any, error) {
	streamID := strings.TrimSpace(request.StreamID)
	if streamID == "" {
		return nil, fail(500, "stream_id_missing", "流式执行缺少 stream_id")
	}

	payload, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, fail(500, "serialize_error", "请求体无法序列化")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration(cfg))
	client, errClient := newHTTPClient(cfg, timeoutDuration(cfg))
	if errClient != nil {
		cancel()
		return nil, errClient
	}

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ResponsesURL, strings.NewReader(string(payload)))
	if errRequest != nil {
		cancel()
		client.CloseIdleConnections()
		return nil, fail(500, "invalid_config", "无法构造上游请求")
	}
	req.Header = authHeaders(c, true)

	// 先把上游连接建立起来，让 401/403/429 这类状态能在本次调用内同步返回。
	// 宿主据此冷却该账号并换下一个；若先返回成功再异步请求，宿主永远看不到
	// 真实状态，只会看到一个卡住的流。
	resp, errDo := client.Do(req)
	if errDo != nil {
		cancel()
		client.CloseIdleConnections()
		if ctx.Err() != nil {
			return nil, fail(504, "upstream_timeout", "Basis Points 请求已取消或超时")
		}
		return nil, fail(502, "upstream_transport", "Basis Points 传输失败: "+safeError(errDo))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		cancel()
		client.CloseIdleConnections()
		return nil, upstreamRequestError(resp.StatusCode, raw, body, c)
	}

	forwarder := &streamForwarder{service: s, streamID: streamID, source: source}
	go func() {
		defer cancel()
		defer client.CloseIdleConnections()
		defer resp.Body.Close()
		forwarder.run(ctx, resp.Body)
	}()

	// 空 chunk 列表告诉宿主去消费异步流桥。
	return map[string]any{
		"Headers": map[string][]string{
			"Content-Type":  {"text/event-stream"},
			"Cache-Control": {"no-cache"},
		},
	}, nil
}

// run 读取上游 SSE 并转发。
func (f *streamForwarder) run(ctx context.Context, body io.Reader) {
	reader := bufio.NewReaderSize(body, 64<<10)
	decoder := newSSEDecoder()
	var eventBuffer strings.Builder

	for {
		if ctx.Err() != nil {
			f.finish("")
			return
		}
		chunk := make([]byte, 32<<10)
		n, errRead := reader.Read(chunk)
		if n > 0 {
			f.raw.Write(chunk[:n])
			// 按 SSE 分帧解析，逐事件决定立即转发还是延后。
			errFeed := decoder.feed(chunk[:n], func(event, data string) error {
				eventBuffer.Reset()
				if event != "" {
					eventBuffer.WriteString("event: ")
					eventBuffer.WriteString(event)
					eventBuffer.WriteString("\n")
				}
				eventBuffer.WriteString("data: ")
				eventBuffer.WriteString(data)
				eventBuffer.WriteString("\n\n")
				return f.handleEvent(event, data, eventBuffer.String())
			})
			if errFeed != nil {
				f.finish(safeError(errFeed))
				return
			}
		}
		if errRead != nil {
			if errRead != io.EOF {
				f.finish(safeError(errRead))
				return
			}
			break
		}
	}
	f.finish("")
}

// handleEvent 决定单个事件立即转发还是延后。
func (f *streamForwarder) handleEvent(event, data, rawEvent string) error {
	// 与工具调用无关的事件立即转发，让客户端尽快看到正文。
	if !f.eventMayCarryToolCall(event, data) {
		f.emitText(rawEvent)
		return nil
	}
	// 可能与工具调用相关：先缓存，等完整响应再决定。
	f.pending = append(f.pending, rawEvent)
	return nil
}

// eventMayCarryToolCall 判断事件是否可能涉及工具调用。
//
// 只对 function_call / custom_tool_call / output_item 相关事件延后，
// 文本与思考事件一律立即转发。
func (f *streamForwarder) eventMayCarryToolCall(event, data string) bool {
	lowered := strings.ToLower(event)
	if strings.Contains(lowered, "function_call") ||
		strings.Contains(lowered, "custom_tool_call") ||
		strings.Contains(lowered, "output_item") {
		return true
	}
	// 事件名缺失时按内容判断。
	if strings.Contains(data, `"`+transportName+`"`) {
		return true
	}
	return false
}

// emitText 立即转发一条事件。
func (f *streamForwarder) emitText(rawEvent string) {
	if f.emitFailed {
		return
	}
	if errEmit := f.service.call("host.stream.emit", map[string]any{
		"stream_id": f.streamID,
		"payload":   []byte(rawEvent),
	}, nil); errEmit != nil {
		// 客户端取消会关闭宿主桥，不再重试。
		f.emitFailed = true
	}
}

// finish 结束流：先处理延后的事件，再关闭。
func (f *streamForwarder) finish(errMessage string) {
	if errMessage != "" {
		f.closeWithError(errMessage)
		return
	}
	if f.emitFailed {
		f.closeWithError("")
		return
	}

	// 检查完整响应里是否有需要还原的原生运输调用。
	raw := f.raw.String()
	if len(f.pending) == 0 || !needsToolRewrite(raw) {
		// 无工具调用：延后的事件按原样转发。
		for _, event := range f.pending {
			f.emitText(event)
		}
		f.closeWithError("")
		return
	}

	// 有工具调用：用完整响应合成修正后的事件序列并转发。
	final, errParse := parseFinalStreamResponse([]byte(raw))
	if errParse != nil {
		// 解析失败时退回原样转发，至少不让客户端卡死。
		for _, event := range f.pending {
			f.emitText(event)
		}
		f.closeWithError("")
		return
	}
	if _, response, changed, errTransform := transformResponseBody(jsonBytes(final), f.source); errTransform == nil && changed {
		// 工具调用已还原：补发修正后的 output 事件。
		for _, event := range syntheticOutputEvents(response) {
			f.emitText(event)
		}
	} else {
		for _, event := range f.pending {
			f.emitText(event)
		}
	}
	f.closeWithError("")
}

func (f *streamForwarder) closeWithError(errMessage string) {
	payload := map[string]any{"stream_id": f.streamID}
	if errMessage != "" {
		payload["error"] = errMessage
	}
	_ = f.service.call("host.stream.close", payload, nil)
}

// needsToolRewrite 判断原始流里是否含需要还原的原生运输调用。
func needsToolRewrite(raw string) bool {
	return strings.Contains(raw, `"`+transportName+`"`)
}

// syntheticOutputEvents 为已还原的工具调用生成 output_item 事件。
//
// 只发 output_item.added / done，客户端据此识别工具调用；
// 正文事件已在前面的流程里实时转发过。
func syntheticOutputEvents(response map[string]any) []string {
	output, _ := response["output"].([]any)
	events := make([]string, 0, len(output)*2)
	for index, value := range output {
		item := objectValue(value)
		if item == nil {
			continue
		}
		switch stringValue(item["type"]) {
		case "function_call", "custom_tool_call":
		default:
			continue
		}
		events = append(events,
			formatSSE("response.output_item.added", map[string]any{
				"output_index": index,
				"item":         item,
			}),
			formatSSE("response.output_item.done", map[string]any{
				"output_index": index,
				"item":         item,
			}),
		)
	}
	return events
}

func formatSSE(event string, value map[string]any) string {
	value["type"] = event
	var builder strings.Builder
	builder.WriteString("event: ")
	builder.WriteString(event)
	builder.WriteString("\ndata: ")
	builder.Write(jsonBytes(value))
	builder.WriteString("\n\n")
	return builder.String()
}

var _ = time.Second
