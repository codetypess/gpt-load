package responsemodel

import (
	"testing"

	"gpt-load/internal/protocol"
)

func TestObserverReadsJSONAndSplitSSEModels(t *testing.T) {
	tests := []struct {
		name     string
		protocol protocol.Protocol
		chunks   []string
		want     string
	}{
		{
			name: "JSON", protocol: protocol.OpenAICompletions,
			chunks: []string{`{"model":"served-chat"}`}, want: "served-chat",
		},
		{
			name: "split OpenAI SSE", protocol: protocol.OpenAICompletions,
			chunks: []string{`data: {"model":"served-`, "chat\"}\n\n"}, want: "served-chat",
		},
		{
			name: "Anthropic SSE", protocol: protocol.Anthropic,
			chunks: []string{"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-served\"}}\n\n"},
			want:   "claude-served",
		},
		{
			name: "Responses event without final delimiter", protocol: protocol.OpenAIResponses,
			chunks: []string{"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-served\"}}"},
			want:   "gpt-served",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := New(test.protocol, 1<<20)
			for _, chunk := range test.chunks {
				observer.Observe([]byte(chunk))
			}
			observer.Finish()
			if got := observer.Model(); got != test.want {
				t.Fatalf("Model() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestObserverDoesNotInventMissingModel(t *testing.T) {
	observer := New(protocol.OpenAICompletions, 1<<20)
	observer.Observe([]byte(`{"id":"chat-1"}`))
	observer.Observe([]byte("data: [DONE]\n\n"))
	observer.Finish()
	if got := observer.Model(); got != "" {
		t.Fatalf("Model() = %q, want empty", got)
	}
}
