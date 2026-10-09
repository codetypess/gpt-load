package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

type firstResponseKey struct{}

type firstResponseCallback struct {
	mu       sync.Mutex
	notify   func()
	upstream bool
}

// FirstResponsePending 供独立观测器在已取得首响或请求结束后停止读取副本。
func FirstResponsePending(ctx context.Context) bool {
	callback, _ := ctx.Value(firstResponseKey{}).(*firstResponseCallback)
	if callback == nil {
		return false
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	return callback.notify != nil
}

// WithFirstResponseObserver 在一次尝试内仅通知一次。停止后，SDK 后台读取不能再修改请求日志。
// 观察不参与提交、超时、重试或响应内容处理。
func WithFirstResponseObserver(ctx context.Context, notify func()) (context.Context, func()) {
	if notify == nil {
		return ctx, func() {}
	}
	callback := &firstResponseCallback{notify: notify}
	return context.WithValue(ctx, firstResponseKey{}, callback), func() {
		callback.mu.Lock()
		defer callback.mu.Unlock()
		callback.notify = nil
	}
}

// NewFirstResponseSSEObserver 识别首条 semantic SSE 事件，不等待完整帧或正文。
// semantic 首响会跳过 response.created、response.in_progress 和保活事件，
// 但不会要求事件已经包含可见文字；推理、工具调用等模型事件同样算首响。
func NewFirstResponseSSEObserver(ctx context.Context) func([]byte) {
	return newFirstResponseSSEObserver(ctx, true)
}

// NewFirstResponseSSEFallback 仅供没有上游读取器的执行器；不能把 SDK 合成事件误记为上游首响。
func NewFirstResponseSSEFallback(ctx context.Context) func([]byte) {
	return newFirstResponseSSEObserver(ctx, false)
}

// FirstResponseDataIsSemantic exposes the same semantic boundary to transports
// that deliver one decoded JSON event at a time (for example WebSocket).
func FirstResponseDataIsSemantic(eventName string, payload []byte) bool {
	return firstResponseSSEDataIsSemantic([]byte(eventName), payload)
}

func newFirstResponseSSEObserver(ctx context.Context, upstream bool) func([]byte) {
	callback, _ := ctx.Value(firstResponseKey{}).(*firstResponseCallback)
	if callback != nil && upstream {
		callback.mu.Lock()
		callback.upstream = true
		callback.mu.Unlock()
	}
	var line []byte
	var eventName []byte
	done := callback == nil
	return func(chunk []byte) {
		for !done && len(chunk) > 0 {
			end := bytes.IndexAny(chunk, "\r\n")
			part := chunk
			if end >= 0 {
				part = chunk[:end]
			}
			if len(line)+len(part) > OpenAIImagesSSEEventLimitBytes {
				done, line = true, nil
				return
			}
			line = append(line, part...)
			if end < 0 {
				return
			}
			trimmed := bytes.TrimSpace(line)
			switch {
			case len(trimmed) == 0:
				// A blank line terminates the current SSE event. Keep eventName
				// while processing data lines, then clear it for the next event.
				eventName = eventName[:0]
			case bytes.HasPrefix(line, []byte("event:")):
				eventName = append(eventName[:0], bytes.TrimSpace(line[len("event:"):])...)
			case bytes.HasPrefix(line, []byte("data:")):
				payload := bytes.TrimSpace(line[len("data:"):])
				if bytes.HasPrefix(payload, []byte("[DONE]")) {
					done, line = true, nil
					return
				}
				if firstResponseSSEDataIsSemantic(eventName, payload) {
					callback.mu.Lock()
					if callback.notify != nil && (upstream || !callback.upstream) {
						callback.notify()
						callback.notify = nil
					}
					callback.mu.Unlock()
					done, line = true, nil
					return
				}
			}
			line = line[:0]
			chunk = chunk[end+1:]
		}
	}
}

// firstResponseSSEDataIsSemantic implements the semantic TTFT boundary. The
// payload is intentionally not inspected for visible content: an empty text
// delta, reasoning event, or tool-call event is still the first semantic
// response from the model. Unknown/opaque payloads remain compatible with the
// old behavior and count when they are non-empty.
func firstResponseSSEDataIsSemantic(eventName, payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
	}

	eventType := strings.ToLower(strings.TrimSpace(string(eventName)))
	payloadType := ""
	var object struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &object); err == nil {
		payloadType = strings.ToLower(strings.TrimSpace(object.Type))
	}
	if isFirstResponseLifecycleEvent(eventType) || isFirstResponseLifecycleEvent(payloadType) {
		return false
	}
	if eventType == "" && payloadType == "" {
		// Plain non-JSON data is used by several compatible providers. Preserve
		// the historical non-empty-data behavior for those streams, except for
		// textual keepalive markers.
		return !isFirstResponseLifecycleEvent(strings.ToLower(strings.TrimSpace(string(payload))))
	}
	return true
}

func isFirstResponseLifecycleEvent(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "response.created", "response.in_progress", "ping", "keepalive", "keep-alive", "heartbeat":
		return true
	default:
		return false
	}
}

// ObserveFirstResponseReader 保持读取字节和错误不变，在协议转换前采样。
func ObserveFirstResponseReader(ctx context.Context, reader io.Reader) io.Reader {
	if _, ok := ctx.Value(firstResponseKey{}).(*firstResponseCallback); !ok {
		return reader
	}
	return &firstResponseReader{Reader: reader, observe: NewFirstResponseSSEObserver(ctx)}
}

type firstResponseReader struct {
	io.Reader
	observe func([]byte)
}

func (reader *firstResponseReader) Read(buffer []byte) (int, error) {
	n, err := reader.Reader.Read(buffer)
	reader.observe(buffer[:n])
	if err == io.EOF {
		reader.observe([]byte{'\n'})
	}
	return n, err
}
