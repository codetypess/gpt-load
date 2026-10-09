package execution

import (
	"io"
	"strings"
	"testing"
)

func TestFirstResponseSkipsLifecycleAndSamplesSemanticEvent(t *testing.T) {
	for _, payload := range []string{`{"type":"response.created"}`, `{"type":"response.in_progress"}`, `{"type":"ping"}`, `{"type":"keepalive"}`} {
		t.Run(payload, func(t *testing.T) {
			calls := 0
			ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
			defer stop()
			observe := NewFirstResponseSSEObserver(ctx)
			observe([]byte(": ping\n\nevent: ping\ndata:\ndata:  \t\r\n"))
			if calls != 0 {
				t.Fatal("control line counted")
			}
			for _, b := range []byte("data: " + payload) {
				observe([]byte{b})
			}
			if calls != 0 {
				t.Fatal("incomplete line counted")
			}
			observe([]byte("\n"))
			observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n"))
			if calls != 1 {
				t.Fatalf("calls = %d, want one", calls)
			}
		})
	}
}

func TestFirstResponseUsesSSEEventNameForLifecycleFiltering(t *testing.T) {
	for _, stream := range []string{
		"event: response.created\ndata: {\"opaque\":true}\n\n",
		"event: response.in_progress\ndata: {\"opaque\":true}\n\n",
		"event: ping\ndata: {\"opaque\":true}\n\n",
		"event: message\ndata: {\"type\":\"response.created\"}\n\n",
		"data: keepalive\n\n",
	} {
		t.Run(strings.ReplaceAll(stream, "\n", "_"), func(t *testing.T) {
			calls := 0
			ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
			defer stop()
			NewFirstResponseSSEObserver(ctx)([]byte(stream))
			if calls != 0 {
				t.Fatalf("lifecycle event counted: %q", stream)
			}
		})
	}

	calls := 0
	ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
	defer stop()
	NewFirstResponseSSEObserver(ctx)([]byte("event: response.created\ndata: {}\n\nevent: response.output_text.delta\ndata: {}\n\n"))
	if calls != 1 {
		t.Fatalf("semantic event count = %d, want 1", calls)
	}
}

func TestFirstResponseSamplesCRAndSplitCRLFLinesBeforeEOF(t *testing.T) {
	const data = `data: {"type":"response.created"}`
	for _, tc := range []struct {
		name   string
		chunks []string
		wantAt int
	}{
		{
			name:   "cr_with_event",
			chunks: []string{"event: response.created\r", data, "\r", "\rdata: later\r\r"},
			wantAt: 3,
		},
		{
			name:   "cr_data_only",
			chunks: []string{data, "\r", "\rdata: later\r\r"},
			wantAt: 2,
		},
		{
			name:   "split_crlf",
			chunks: []string{": ping\r", "\nevent: response.created\r", "\n" + data, "\r", "\n\r\ndata: later\r\n\r\n"},
			wantAt: 4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
			defer stop()
			observe := NewFirstResponseSSEObserver(ctx)
			for index, chunk := range tc.chunks {
				observe([]byte(chunk))
				want := 0
				if index >= tc.wantAt {
					want = 1
				}
				if calls != want {
					t.Fatalf("after chunk %d: calls = %d, want %d", index, calls, want)
				}
			}
		})
	}
}

func TestFirstResponseStopsAtDoneMarker(t *testing.T) {
	for _, done := range []string{"[DONE]", "[DONE]ignored"} {
		calls := 0
		ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
		observe := NewFirstResponseSSEObserver(ctx)
		observe([]byte("data: " + done + "\n\ndata: late\n"))
		stop()
		if calls != 0 {
			t.Fatalf("data after %s counted", done)
		}
	}
}

func TestFirstResponseReaderPreservesBytesAndEOF(t *testing.T) {
	calls := 0
	ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
	body := "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}"
	data, err := io.ReadAll(ObserveFirstResponseReader(ctx, strings.NewReader(body)))
	if err != nil || string(data) != body || calls != 1 {
		t.Fatalf("body=%q calls=%d err=%v", data, calls, err)
	}
	stop()
	observe := NewFirstResponseSSEObserver(ctx)
	observe([]byte("data: late\n"))
	if calls != 1 {
		t.Fatal("callback continued after stop")
	}
}

func TestFirstResponseDoesNotUseSyntheticSDKData(t *testing.T) {
	calls := 0
	ctx, stop := WithFirstResponseObserver(t.Context(), func() { calls++ })
	defer stop()
	fallback := NewFirstResponseSSEFallback(ctx)
	upstream := NewFirstResponseSSEObserver(ctx)
	upstream([]byte("data: [DONE]\n\n"))
	fallback([]byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"))
	if calls != 0 {
		t.Fatal("synthetic SDK completion counted as an upstream response")
	}
}
