// Package responsemodel observes provider-declared model identities without
// changing response bytes.
package responsemodel

import (
	"bytes"
	"strings"

	"gpt-load/internal/dialect"
	"gpt-load/internal/protocol"
)

// Observer extracts model identities from complete JSON responses or
// incrementally delivered SSE events.
type Observer struct {
	inspector     dialect.ResponseModelInspector
	maxEventBytes int
	pending       []byte
	model         string
}

// New returns an observer for the client-visible response protocol.
func New(value protocol.Protocol, maxEventBytes int) *Observer {
	var inspector dialect.ResponseModelInspector
	switch value {
	case protocol.OpenAIResponses:
		inspector = dialect.NewOpenAIResponses()
	case protocol.Anthropic:
		inspector = dialect.NewAnthropic()
	case protocol.Gemini:
		inspector = dialect.NewGemini()
	case protocol.Decisions:
		inspector = dialect.NewDecisions()
	case protocol.OpenAICompletions, protocol.OpenAIImages,
		protocol.OpenAIEmbeddings, protocol.Rerank:
		inspector = dialect.NewOpenAI()
	}
	return &Observer{inspector: inspector, maxEventBytes: maxEventBytes}
}

// Observe accepts either one complete JSON payload or an arbitrary SSE chunk.
func (observer *Observer) Observe(payload []byte) {
	if observer == nil || observer.inspector == nil || len(payload) == 0 {
		return
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		observer.observeJSON(trimmed)
		return
	}

	observer.pending = append(observer.pending, payload...)
	if observer.maxEventBytes > 0 && len(observer.pending) > observer.maxEventBytes {
		observer.pending = nil
		return
	}
	for {
		end, ok := completeEventEnd(observer.pending)
		if !ok {
			return
		}
		observer.observeSSEEvent(observer.pending[:end])
		observer.pending = observer.pending[end:]
	}
}

// Finish observes a final SSE event that is not followed by a blank line.
func (observer *Observer) Finish() {
	if observer == nil || len(bytes.TrimSpace(observer.pending)) == 0 {
		return
	}
	observer.observeSSEEvent(observer.pending)
	observer.pending = nil
}

// Model returns the last non-empty model declared by the upstream response.
func (observer *Observer) Model() string {
	if observer == nil {
		return ""
	}
	return observer.model
}

func (observer *Observer) observeJSON(payload []byte) {
	for _, model := range observer.inspector.InspectResponseModels(payload) {
		if model = strings.TrimSpace(model); model != "" {
			observer.model = model
		}
	}
}

func (observer *Observer) observeSSEEvent(event []byte) {
	var data bytes.Buffer
	for start := 0; start < len(event); {
		end := start
		for end < len(event) && event[end] != '\r' && event[end] != '\n' {
			end++
		}
		line := event[start:end]
		if bytes.Equal(line, []byte("data")) || bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data"))
			value = bytes.TrimPrefix(value, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			if data.Len() > 0 {
				_ = data.WriteByte('\n')
			}
			_, _ = data.Write(value)
		}
		if end == len(event) {
			break
		}
		start = end + 1
		if event[end] == '\r' && start < len(event) && event[start] == '\n' {
			start++
		}
	}
	observer.observeJSON(bytes.TrimSpace(data.Bytes()))
}

func completeEventEnd(data []byte) (int, bool) {
	lineStart := 0
	for index := 0; index < len(data); {
		if data[index] != '\r' && data[index] != '\n' {
			index++
			continue
		}
		lineEnd := index
		index++
		if data[lineEnd] == '\r' && index < len(data) && data[index] == '\n' {
			index++
		}
		if lineEnd == lineStart {
			return index, true
		}
		lineStart = index
	}
	return 0, false
}
