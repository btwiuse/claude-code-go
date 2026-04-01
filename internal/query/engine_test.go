package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anthropics/claude-code-go/internal/api"
	"github.com/anthropics/claude-code-go/internal/cost"
	"github.com/anthropics/claude-code-go/internal/tools"
	"github.com/anthropics/claude-code-go/internal/types"
)

// makeStreamingResponse builds a raw SSE response body that the API client can parse.
// If toolCall is true, the response includes a tool_use block so the engine continues looping.
func makeStreamingResponse(toolCall bool) string {
	events := ""

	// message_start
	events += `event: message_start` + "\n"
	events += `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}` + "\n\n"

	// text block
	events += `event: content_block_start` + "\n"
	events += `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"
	events += `event: content_block_delta` + "\n"
	events += `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Working on it."}}` + "\n\n"
	events += `event: content_block_stop` + "\n"
	events += `data: {"type":"content_block_stop","index":0}` + "\n\n"

	if toolCall {
		// tool_use block – triggers next turn in the engine loop
		events += `event: content_block_start` + "\n"
		events += `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"Bash","input":{}}}` + "\n\n"
		events += `event: content_block_delta` + "\n"
		events += `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hello\"}"}}` + "\n\n"
		events += `event: content_block_stop` + "\n"
		events += `data: {"type":"content_block_stop","index":1}` + "\n\n"
	}

	// message_delta + stop
	stopReason := "end_turn"
	if toolCall {
		stopReason = "tool_use"
	}
	events += `event: message_delta` + "\n"
	events += fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":"%s"},"usage":{"output_tokens":5}}`, stopReason) + "\n\n"
	events += `event: message_stop` + "\n"
	events += `data: {"type":"message_stop"}` + "\n\n"

	return events
}

// newTestEngine creates an Engine backed by a mock streaming API server.
// requestCount is incremented for every API request.
// alwaysToolCall controls whether every response includes a tool_use block.
func newTestEngine(t *testing.T, maxTurns int, alwaysToolCall bool, requestCount *atomic.Int32) *Engine {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(makeStreamingResponse(alwaysToolCall)))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	client, err := api.NewClient(api.WithAPIKey("test-key"), api.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	// Minimal tool registry with a stub Bash tool that always succeeds
	registry := tools.NewRegistry()
	registry.Register(&stubTool{name: "Bash"})

	return NewEngine(EngineConfig{
		Client:       client,
		Registry:     registry,
		CostTracker:  cost.NewTracker(),
		ToolCtx:      &tools.ToolContext{CWD: t.TempDir()},
		SystemPrompt: "test",
		MaxTurns:     maxTurns,
	})
}

// stubTool implements tools.Tool for testing.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string  { return "stub" }
func (s *stubTool) IsReadOnly() bool     { return true }
func (s *stubTool) IsEnabled() bool      { return true }
func (s *stubTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type:       "object",
		Properties: map[string]types.ToolPropertySchema{},
	}
}
func (s *stubTool) UserFacingName(_ json.RawMessage) string { return s.name }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage, _ *tools.ToolContext) (*tools.ToolResult, error) {
	return &tools.ToolResult{Content: "ok"}, nil
}

func TestSubmitReturnsErrorOnMaxTurnsExhausted(t *testing.T) {
	var reqCount atomic.Int32
	maxTurns := 3

	engine := newTestEngine(t, maxTurns, true /* every response triggers tool use */, &reqCount)

	err := engine.Submit(t.Context(), "do something complex")
	if err == nil {
		t.Fatal("expected ErrMaxTurnsReached, got nil")
	}
	if !errors.Is(err, ErrMaxTurnsReached) {
		t.Fatalf("expected ErrMaxTurnsReached, got: %v", err)
	}

	// Verify the engine actually ran maxTurns iterations
	if int(reqCount.Load()) != maxTurns {
		t.Errorf("expected %d API requests, got %d", maxTurns, reqCount.Load())
	}
}

func TestSubmitReturnsNilOnNormalCompletion(t *testing.T) {
	var reqCount atomic.Int32

	engine := newTestEngine(t, 10, false /* no tool calls – completes on first turn */, &reqCount)

	err := engine.Submit(t.Context(), "just say hello")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Should have made exactly 1 API request
	if reqCount.Load() != 1 {
		t.Errorf("expected 1 API request, got %d", reqCount.Load())
	}
}

func TestOnErrorCallbackOnMaxTurns(t *testing.T) {
	var reqCount atomic.Int32
	var callbackErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(makeStreamingResponse(true)))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)

	client, err := api.NewClient(api.WithAPIKey("test-key"), api.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	registry := tools.NewRegistry()
	registry.Register(&stubTool{name: "Bash"})

	engine := NewEngine(EngineConfig{
		Client:       client,
		Registry:     registry,
		CostTracker:  cost.NewTracker(),
		ToolCtx:      &tools.ToolContext{CWD: t.TempDir()},
		SystemPrompt: "test",
		MaxTurns:     2,
		OnError: func(err error) {
			callbackErr = err
		},
	})

	submitErr := engine.Submit(t.Context(), "complex task")
	if !errors.Is(submitErr, ErrMaxTurnsReached) {
		t.Fatalf("expected ErrMaxTurnsReached, got: %v", submitErr)
	}

	// The onError callback should also have been called
	if callbackErr == nil {
		t.Fatal("expected onError callback to be called")
	}
	if !errors.Is(callbackErr, ErrMaxTurnsReached) {
		t.Fatalf("expected ErrMaxTurnsReached in callback, got: %v", callbackErr)
	}
}

func TestSubmitSingleTurnMaxTurns(t *testing.T) {
	var reqCount atomic.Int32

	engine := newTestEngine(t, 1, true, &reqCount)

	err := engine.Submit(t.Context(), "one turn only")
	if !errors.Is(err, ErrMaxTurnsReached) {
		t.Fatalf("expected ErrMaxTurnsReached with maxTurns=1, got: %v", err)
	}
	if reqCount.Load() != 1 {
		t.Errorf("expected 1 API request, got %d", reqCount.Load())
	}
}
