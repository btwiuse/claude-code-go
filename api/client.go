// Package api provides the Claude API client with streaming support.
// It wraps the official github.com/anthropics/anthropic-sdk-go SDK.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/btwiuse/claude-code-go/config"
	"github.com/btwiuse/claude-code-go/constants"
	"github.com/btwiuse/claude-code-go/types"
)

// Client is the Claude API client, wrapping the official Anthropic SDK.
type Client struct {
	sdkClient anthropic.Client
	model     string
	maxTokens int
}

// clientConfig holds intermediate configuration before building the SDK client.
type clientConfig struct {
	apiKey     string
	baseURL    string
	model      string
	maxTokens  int
	sessionID  string
	httpClient *http.Client
}

// ClientOption configures a Client.
type ClientOption func(*clientConfig)

// WithAPIKey sets the API key.
func WithAPIKey(key string) ClientOption {
	return func(c *clientConfig) {
		c.apiKey = key
	}
}

// WithBaseURL sets the API base URL.
func WithBaseURL(url string) ClientOption {
	return func(c *clientConfig) {
		c.baseURL = url
	}
}

// WithModel sets the default model.
func WithModel(model string) ClientOption {
	return func(c *clientConfig) {
		c.model = model
	}
}

// WithMaxTokens sets the default max tokens.
func WithMaxTokens(tokens int) ClientOption {
	return func(c *clientConfig) {
		c.maxTokens = tokens
	}
}

// WithSessionID sets the session ID for request tracking.
func WithSessionID(id string) ClientOption {
	return func(c *clientConfig) {
		c.sessionID = id
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *clientConfig) {
		c.httpClient = httpClient
	}
}

// NewClient creates a new Claude API client using the official Anthropic SDK.
func NewClient(opts ...ClientOption) (*Client, error) {
	cfg := &clientConfig{
		model:     getModel(),
		maxTokens: constants.DefaultMaxTokens,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// Resolve API key: explicit option > config file > env (handled by SDK)
	apiKey := cfg.apiKey
	if apiKey == "" {
		apiKey = config.GetAPIKey()
	}
	if apiKey == "" {
		return nil, fmt.Errorf("no API key configured; set ANTHROPIC_API_KEY environment variable or configure via 'claude-code config'")
	}

	// Build SDK options
	sdkOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}

	if cfg.baseURL != "" {
		sdkOpts = append(sdkOpts, option.WithBaseURL(cfg.baseURL))
	}
	if cfg.httpClient != nil {
		sdkOpts = append(sdkOpts, option.WithHTTPClient(cfg.httpClient))
	}

	// Set standard headers
	sdkOpts = append(sdkOpts,
		option.WithHeader("anthropic-beta", "prompt-caching-2024-07-31,interleaved-thinking-2025-05-14"),
		option.WithHeader("User-Agent", fmt.Sprintf("claude-code-go/%s", constants.Version)),
		option.WithHeader("x-app", "cli"),
	)

	if cfg.sessionID != "" {
		sdkOpts = append(sdkOpts, option.WithHeader("X-Claude-Code-Session-Id", cfg.sessionID))
	}

	// Parse custom headers from environment
	if customHeaders := os.Getenv("ANTHROPIC_CUSTOM_HEADERS"); customHeaders != "" {
		for _, line := range strings.Split(customHeaders, "\n") {
			line = strings.TrimSpace(line)
			if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
				sdkOpts = append(sdkOpts, option.WithHeader(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])))
			}
		}
	}

	sdkClient := anthropic.NewClient(sdkOpts...)

	return &Client{
		sdkClient: sdkClient,
		model:     cfg.model,
		maxTokens: cfg.maxTokens,
	}, nil
}

func getModel() string {
	if model := os.Getenv("ANTHROPIC_MODEL"); model != "" {
		return model
	}
	return constants.DefaultModel
}

// GetModel returns the configured model name.
func (c *Client) GetModel() string {
	return c.model
}

// CreateMessageRequest contains the parameters for creating a message.
type CreateMessageRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	Messages      []types.Message        `json:"messages"`
	System        []types.SystemBlock    `json:"system,omitempty"`
	Tools         []types.ToolDefinition `json:"tools,omitempty"`
	Stream        bool                   `json:"stream"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	Thinking      *types.ThinkingConfig  `json:"thinking,omitempty"`
}

// buildParams converts a CreateMessageRequest to SDK MessageNewParams.
func (c *Client) buildParams(req *CreateMessageRequest) anthropic.MessageNewParams {
	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
		Messages:  convertMessages(req.Messages),
	}

	if len(req.System) > 0 {
		params.System = convertSystemBlocks(req.System)
	}
	if len(req.Tools) > 0 {
		params.Tools = convertTools(req.Tools)
	}
	if len(req.StopSequences) > 0 {
		params.StopSequences = req.StopSequences
	}
	if req.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Temperature)
	}
	if req.Thinking != nil {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(req.Thinking.BudgetTokens))
	}

	return params
}

// CreateMessage sends a non-streaming message request.
func (c *Client) CreateMessage(ctx context.Context, req *CreateMessageRequest) (*types.APIResponse, error) {
	params := c.buildParams(req)

	msg, err := c.sdkClient.Messages.New(ctx, params)
	if err != nil {
		return nil, convertSDKError(err)
	}

	return convertMessageToAPIResponse(msg), nil
}

// StreamMessage sends a streaming message request and returns a channel of events.
func (c *Client) StreamMessage(ctx context.Context, req *CreateMessageRequest) (<-chan StreamResult, error) {
	params := c.buildParams(req)

	stream := c.sdkClient.Messages.NewStreaming(ctx, params)

	ch := make(chan StreamResult, 64)
	go func() {
		defer close(ch)
		defer stream.Close()
		processSDKStream(ctx, stream, ch)
	}()

	return ch, nil
}

// StreamResult wraps either a stream event or an error.
type StreamResult struct {
	Event *types.StreamEvent
	Error error
}

// processSDKStream reads events from the SDK stream and sends them as StreamResults.
func processSDKStream(ctx context.Context, stream *ssestream.Stream[anthropic.MessageStreamEventUnion], ch chan<- StreamResult) {
	for stream.Next() {
		select {
		case <-ctx.Done():
			ch <- StreamResult{Error: ctx.Err()}
			return
		default:
		}

		sdkEvent := stream.Current()
		event := convertStreamEvent(sdkEvent)
		if event != nil {
			ch <- StreamResult{Event: event}
		}
	}

	if err := stream.Err(); err != nil {
		ch <- StreamResult{Error: convertSDKError(err)}
	}
}

// convertStreamEvent converts an SDK stream event to our internal StreamEvent type.
func convertStreamEvent(sdkEvent anthropic.MessageStreamEventUnion) *types.StreamEvent {
	switch sdkEvent.Type {
	case "message_start":
		e := sdkEvent.AsMessageStart()
		event := &types.StreamEvent{
			Type: "message_start",
			Message: &types.MessageStartData{
				ID:    e.Message.ID,
				Type:  string(e.Message.Type),
				Role:  types.RoleAssistant,
				Model: string(e.Message.Model),
			},
		}
		if e.Message.Usage.InputTokens > 0 || e.Message.Usage.OutputTokens > 0 {
			event.Message.Usage = &types.Usage{
				InputTokens:              int(e.Message.Usage.InputTokens),
				OutputTokens:             int(e.Message.Usage.OutputTokens),
				CacheReadInputTokens:     int(e.Message.Usage.CacheReadInputTokens),
				CacheCreationInputTokens: int(e.Message.Usage.CacheCreationInputTokens),
			}
		}
		return event

	case "content_block_start":
		e := sdkEvent.AsContentBlockStart()
		block := convertContentBlockStartToContentBlock(e.ContentBlock)
		return &types.StreamEvent{
			Type:         "content_block_start",
			Index:        int(e.Index),
			ContentBlock: &block,
		}

	case "content_block_delta":
		e := sdkEvent.AsContentBlockDelta()
		delta := convertDeltaToRaw(e.Delta)
		return &types.StreamEvent{
			Type:  "content_block_delta",
			Index: int(e.Index),
			Delta: delta,
		}

	case "content_block_stop":
		e := sdkEvent.AsContentBlockStop()
		return &types.StreamEvent{
			Type:  "content_block_stop",
			Index: int(e.Index),
		}

	case "message_delta":
		e := sdkEvent.AsMessageDelta()
		return &types.StreamEvent{
			Type:  "message_delta",
			Delta: marshalDelta(map[string]string{"stop_reason": string(e.Delta.StopReason)}),
			Usage: &types.Usage{
				OutputTokens: int(e.Usage.OutputTokens),
			},
		}

	case "message_stop":
		return &types.StreamEvent{
			Type: "message_stop",
		}

	default:
		// Skip unknown event types (e.g. ping)
		return nil
	}
}

// convertContentBlockStartToContentBlock converts the SDK content block start data.
func convertContentBlockStartToContentBlock(cb anthropic.ContentBlockStartEventContentBlockUnion) types.ContentBlock {
	block := types.ContentBlock{
		Type: types.ContentType(cb.Type),
	}
	switch cb.Type {
	case "text":
		block.Text = cb.Text
	case "tool_use":
		block.ID = cb.ID
		block.Name = cb.Name
	case "thinking":
		block.Thinking = cb.Thinking
	}
	return block
}

// marshalDelta is a helper that marshals a map to JSON for stream delta conversion.
// Marshaling simple string maps cannot fail in practice, so errors are not propagated.
func marshalDelta(m map[string]string) json.RawMessage {
	raw, _ := json.Marshal(m)
	return json.RawMessage(raw)
}

// convertDeltaToRaw converts the SDK delta union to raw JSON.
func convertDeltaToRaw(delta anthropic.RawContentBlockDeltaUnion) json.RawMessage {
	switch delta.Type {
	case "text_delta":
		return marshalDelta(map[string]string{
			"type": "text_delta",
			"text": delta.Text,
		})
	case "input_json_delta":
		return marshalDelta(map[string]string{
			"type":         "input_json_delta",
			"partial_json": delta.PartialJSON,
		})
	case "thinking_delta":
		return marshalDelta(map[string]string{
			"type":     "thinking_delta",
			"thinking": delta.Thinking,
		})
	default:
		return marshalDelta(map[string]string{
			"type": delta.Type,
		})
	}
}

// convertMessages converts internal messages to SDK MessageParams.
func convertMessages(msgs []types.Message) []anthropic.MessageParam {
	params := make([]anthropic.MessageParam, 0, len(msgs))
	for _, msg := range msgs {
		blocks := convertContentBlocks(msg.Content)
		param := anthropic.MessageParam{
			Role:    anthropic.MessageParamRole(msg.Role),
			Content: blocks,
		}
		params = append(params, param)
	}
	return params
}

// convertContentBlocks converts internal content blocks to SDK ContentBlockParamUnion.
func convertContentBlocks(blocks []types.ContentBlock) []anthropic.ContentBlockParamUnion {
	params := make([]anthropic.ContentBlockParamUnion, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case types.ContentTypeText:
			params = append(params, anthropic.NewTextBlock(block.Text))
		case types.ContentTypeToolUse:
			params = append(params, anthropic.NewToolUseBlock(block.ID, json.RawMessage(block.Input), block.Name))
		case types.ContentTypeToolResult:
			content := ""
			if len(block.Content) > 0 {
				content = block.Content[0].Text
			}
			params = append(params, anthropic.NewToolResultBlock(block.ToolUseID, content, block.IsError))
		case types.ContentTypeThinking:
			params = append(params, anthropic.NewThinkingBlock("", block.Thinking))
		case types.ContentTypeImage:
			if block.Source != nil {
				params = append(params, anthropic.NewImageBlockBase64(block.Source.MediaType, block.Source.Data))
			}
		}
	}
	return params
}

// convertSystemBlocks converts internal system blocks to SDK TextBlockParams.
func convertSystemBlocks(blocks []types.SystemBlock) []anthropic.TextBlockParam {
	params := make([]anthropic.TextBlockParam, 0, len(blocks))
	for _, block := range blocks {
		tb := anthropic.TextBlockParam{
			Text: block.Text,
		}
		if block.CacheControl != nil {
			tb.CacheControl = anthropic.CacheControlEphemeralParam{
				// SDK uses the default "ephemeral" type
			}
		}
		params = append(params, tb)
	}
	return params
}

// convertTools converts internal tool definitions to SDK ToolUnionParams.
func convertTools(tools []types.ToolDefinition) []anthropic.ToolUnionParam {
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		toolParam := anthropic.ToolParam{
			Name:        tool.Name,
			Description: anthropic.String(tool.Description),
			InputSchema: convertToolInputSchema(tool.InputSchema),
		}
		params = append(params, anthropic.ToolUnionParam{OfTool: &toolParam})
	}
	return params
}

// convertToolInputSchema converts an internal ToolInputSchema to the SDK format.
func convertToolInputSchema(schema types.ToolInputSchema) anthropic.ToolInputSchemaParam {
	props := make(map[string]any, len(schema.Properties))
	for name, prop := range schema.Properties {
		propMap := map[string]any{
			"type": prop.Type,
		}
		if prop.Description != "" {
			propMap["description"] = prop.Description
		}
		if len(prop.Enum) > 0 {
			propMap["enum"] = prop.Enum
		}
		if prop.Default != nil {
			propMap["default"] = prop.Default
		}
		if prop.Items != nil {
			itemMap := map[string]any{
				"type": prop.Items.Type,
			}
			if prop.Items.Description != "" {
				itemMap["description"] = prop.Items.Description
			}
			propMap["items"] = itemMap
		}
		props[name] = propMap
	}

	return anthropic.ToolInputSchemaParam{
		Properties: props,
		Required:   schema.Required,
	}
}

// convertMessageToAPIResponse converts an SDK Message to our internal APIResponse.
func convertMessageToAPIResponse(msg *anthropic.Message) *types.APIResponse {
	resp := &types.APIResponse{
		ID:         msg.ID,
		Type:       string(msg.Type),
		Role:       types.RoleAssistant,
		Model:      string(msg.Model),
		StopReason: string(msg.StopReason),
	}

	for _, block := range msg.Content {
		cb := types.ContentBlock{
			Type: types.ContentType(block.Type),
		}
		switch block.Type {
		case "text":
			cb.Text = block.Text
		case "tool_use":
			cb.ID = block.ID
			cb.Name = block.Name
			cb.Input = block.Input
		case "thinking":
			cb.Thinking = block.Thinking
		}
		resp.Content = append(resp.Content, cb)
	}

	resp.Usage = &types.Usage{
		InputTokens:              int(msg.Usage.InputTokens),
		OutputTokens:             int(msg.Usage.OutputTokens),
		CacheReadInputTokens:     int(msg.Usage.CacheReadInputTokens),
		CacheCreationInputTokens: int(msg.Usage.CacheCreationInputTokens),
	}

	return resp
}

// APIError represents an error response from the Claude API.
type APIError struct {
	StatusCode int
	Type       string `json:"type"`
	ErrorInfo  struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d): %s - %s", e.StatusCode, e.ErrorInfo.Type, e.ErrorInfo.Message)
}

// IsOverloaded returns true if the API is overloaded.
func (e *APIError) IsOverloaded() bool {
	return e.StatusCode == 529
}

// IsRateLimited returns true if the request was rate limited.
func (e *APIError) IsRateLimited() bool {
	return e.StatusCode == 429
}

// IsAuthError returns true if there was an authentication error.
func (e *APIError) IsAuthError() bool {
	return e.StatusCode == 401 || e.StatusCode == 403
}

// convertSDKError converts an SDK error to our APIError if applicable.
func convertSDKError(err error) error {
	var sdkErr *anthropic.Error
	if errors.As(err, &sdkErr) {
		apiErr := &APIError{
			StatusCode: sdkErr.StatusCode,
		}
		// Try to parse the error body for type/message info.
		// Re-set StatusCode after unmarshal since the JSON body doesn't contain it.
		if raw := sdkErr.RawJSON(); raw != "" {
			_ = json.Unmarshal([]byte(raw), apiErr)
			apiErr.StatusCode = sdkErr.StatusCode
		}
		return apiErr
	}
	return err
}
