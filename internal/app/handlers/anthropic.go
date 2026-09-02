package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type MessagesRequest struct {
	Model        string             `json:"model"`
	Messages     []anthropicMessage `json:"messages"`
	MaxTokens    uint64             `json:"max_tokens"`
	Stream       bool               `json:"stream,omitempty"`
	System       any                `json:"system,omitempty"`
	Tools        []anthropicTool    `json:"tools,omitempty"`
	ToolChoice   any                `json:"tool_choice,omitempty"`
	StopSequence []string           `json:"stop_sequences,omitempty"`
	OutputConfig any                `json:"output_config,omitempty"`
}
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}
type anthropicTool struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	InputSchema any     `json:"input_schema"`
}

func handleMessages(c *gin.Context, state *AppState) {
	var request MessagesRequest
	if err := decodeJSON(c.Request.Body, &request); err != nil {
		logRequestWarn(c, "anthropic request rejected: reason=invalid_json error=%v", err)
		anthropicError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error())
		return
	}
	logRequestDebug(c, "anthropic request received: model=%s stream=%t messages=%d tools=%d max_tokens=%d", request.Model, request.Stream, len(request.Messages), len(request.Tools), request.MaxTokens)
	if request.MaxTokens == 0 {
		logRequestWarn(c, "anthropic request rejected: reason=max_tokens_missing")
		anthropicError(c, http.StatusBadRequest, "invalid_request_error", "max_tokens must be greater than zero")
		return
	}
	translated := translateAnthropicRequest(&request)
	data, _ := json.Marshal(translated)
	normalized, responseWritten := normalizeResponsesBody(c, state, c.Request.Header, data, true)
	if responseWritten {
		return
	}
	logRequestInfo(c, "forwarding endpoint=anthropic_messages model_requested=%s model=%s model_source=%s stream_requested=%t stream_forwarded=%t messages=%d translated_input_items=%d tools=%d reasoning_source=%s reasoning_effort=%s cache_key_source=%s cache_key_id=%s", request.Model, stringValue(normalized.Model), normalized.ModelSource, request.Stream, normalized.StreamForwarded, len(request.Messages), lenAnySlice(normalized.Body["input"]), len(request.Tools), normalized.ReasoningSource, stringValue(normalized.ReasoningEffort), normalized.CacheKeySource, normalized.CacheKeyID)
	upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses", normalized.Body, true)
	if upstreamErr != nil {
		logRequestWarn(c, "anthropic upstream failed: status=%d type=%s", upstreamErr.Status, upstreamErr.ErrorType)
		anthropicError(c, upstreamErr.Status, upstreamErr.ErrorType, upstreamErr.Message)
		return
	}
	if request.Stream {
		logRequestDebug(c, "anthropic response mode=stream")
		streamAnthropic(c, upstream, request.Model, state.Metrics)
		return
	}
	logRequestDebug(c, "anthropic response mode=collect")
	collectAnthropic(c, upstream, request.Model)
}

func translateAnthropicRequest(request *MessagesRequest) map[string]any {
	var input []any
	for _, message := range request.Messages {
		translateAnthropicMessage(message, &input)
	}
	body := map[string]any{"model": request.Model, "instructions": extractAnthropicText(request.System), "input": input, "stream": true, "store": false}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema})
		}
		body["tools"] = tools
	}
	if choice, ok := request.ToolChoice.(map[string]any); ok {
		switch choice["type"] {
		case "none":
			body["tools"] = []any{}
		case "tool":
			if name, ok := choice["name"].(string); ok {
				body["tool_choice"] = map[string]any{"type": "function", "name": name}
			}
		}
	}
	if len(request.StopSequence) > 0 {
		body["stop"] = request.StopSequence
	}
	if config, ok := request.OutputConfig.(map[string]any); ok {
		if effort, ok := config["effort"].(string); ok {
			body["reasoning_effort"] = effort
		}
	}
	return body
}

func translateAnthropicMessage(message anthropicMessage, output *[]any) {
	var blocks []any
	switch content := message.Content.(type) {
	case []any:
		blocks = content
	case string:
		blocks = []any{map[string]any{"type": "text", "text": content}}
	default:
		blocks = []any{map[string]any{"type": "text", "text": extractAnthropicText(content)}}
	}
	var content []any
	flush := func() {
		if len(content) > 0 {
			*output = append(*output, map[string]any{"type": "message", "role": message.Role, "content": content})
			content = nil
		}
	}
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			kind := "input_text"
			if message.Role == "assistant" {
				kind = "output_text"
			}
			content = append(content, map[string]any{"type": kind, "text": stringFromAny(block["text"])})
		case "image":
			if message.Role == "user" {
				if imageURL := anthropicImageURL(block); imageURL != "" {
					content = append(content, map[string]any{"type": "input_image", "image_url": imageURL})
				}
			}
		case "tool_use":
			if message.Role == "assistant" {
				flush()
				arguments, _ := json.Marshal(block["input"])
				if block["input"] == nil {
					arguments = []byte("{}")
				}
				*output = append(*output, map[string]any{"type": "function_call", "call_id": block["id"], "name": block["name"], "arguments": string(arguments)})
			}
		case "tool_result":
			if message.Role == "user" {
				flush()
				*output = append(*output, map[string]any{"type": "function_call_output", "call_id": block["tool_use_id"], "output": extractAnthropicText(block["content"])})
			}
		}
	}
	flush()
}

func anthropicImageURL(block map[string]any) string {
	source, ok := block["source"].(map[string]any)
	if !ok {
		return ""
	}
	switch source["type"] {
	case "url":
		return stringFromAny(source["url"])
	case "base64":
		return "data:" + stringFromAny(source["media_type"]) + ";base64," + stringFromAny(source["data"])
	default:
		return ""
	}
}

func extractAnthropicText(value any) string {
	if value == nil {
		return ""
	}
	switch content := value.(type) {
	case string:
		return content
	case []any:
		var result strings.Builder
		for _, raw := range content {
			if block, ok := raw.(map[string]any); ok {
				result.WriteString(stringFromAny(block["text"]))
			}
		}
		return result.String()
	default:
		data, _ := json.Marshal(value)
		return string(data)
	}
}

func collectAnthropic(c *gin.Context, upstream *http.Response, model string) {
	started := time.Now()
	eventCount := 0
	invalidEventCount := 0
	textDeltaCount := 0
	reasoningDeltaCount := 0
	toolCount := 0
	outcome := "error"
	text, thinking := "", ""
	var tools []any
	defer func() {
		logRequestDebug(c, "anthropic collection ended: outcome=%s model=%s status=%d events=%d invalid_events=%d text_deltas=%d reasoning_deltas=%d tool_calls=%d text_bytes=%d thinking_bytes=%d duration_ms=%d", outcome, model, upstream.StatusCode, eventCount, invalidEventCount, textDeltaCount, reasoningDeltaCount, toolCount, len(text), len(thinking), time.Since(started).Milliseconds())
	}()
	defer upstream.Body.Close()
	raw, err := io.ReadAll(upstream.Body)
	if err != nil {
		logRequestWarn(c, "anthropic collection read failed: error=%v", err)
		anthropicError(c, http.StatusBadGateway, "api_error", "failed to read upstream response: "+err.Error())
		return
	}
	id := "msg_" + randomHex(12)
	usage := map[string]any(nil)
	stopReason := "end_turn"
	for _, data := range dataLines(string(raw)) {
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			invalidEventCount++
			continue
		}
		eventCount++
		switch stringFromAny(event["type"]) {
		case "response.created":
			if response, ok := event["response"].(map[string]any); ok && stringFromAny(response["id"]) != "" {
				id = stringFromAny(response["id"])
			}
		case "response.output_text.delta":
			textDeltaCount++
			text += stringFromAny(event["delta"])
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoningDeltaCount++
			thinking += stringFromAny(event["delta"])
		case "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			if item["type"] == "function_call" {
				toolCount++
				input := map[string]any{}
				_ = json.Unmarshal([]byte(stringFromAny(item["arguments"])), &input)
				idValue := item["call_id"]
				if idValue == nil {
					idValue = item["id"]
				}
				tools = append(tools, map[string]any{"type": "tool_use", "id": idValue, "name": item["name"], "input": input})
			}
		case "response.completed":
			if response, ok := event["response"].(map[string]any); ok {
				usage = mapAnthropicUsage(response["usage"])
			}
			if len(tools) > 0 {
				stopReason = "tool_use"
			}
		case "response.incomplete":
			if response, ok := event["response"].(map[string]any); ok {
				usage = mapAnthropicUsage(response["usage"])
			}
			stopReason = "max_tokens"
		}
	}
	content := make([]any, 0, 2+len(tools))
	if thinking != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": thinking})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	content = append(content, tools...)
	if usage == nil {
		usage = map[string]any{"input_tokens": 0, "output_tokens": 0}
	}
	c.JSON(http.StatusOK, map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": stopReason, "stop_sequence": nil, "usage": usage})
	outcome = "completed"
}

func dataLines(raw string) []string {
	var result []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if value != "[DONE]" {
				result = append(result, value)
			}
		}
	}
	return result
}

type anthropicToolState struct {
	Index                 uint32
	SawArguments, Stopped bool
}
type anthropicStreamState struct {
	Model, ID                     string
	Tools                         map[string]anthropicToolState
	NextIndex                     uint32
	TextIndex, ThinkingIndex      *uint32
	Started, Completed            bool
	Pending                       string
	Frames, Events                int
	InvalidEvents                 int
	TextDeltas, ThinkingDeltas    int
	ToolCalls, ToolArgumentDeltas int
}

func streamAnthropic(c *gin.Context, upstream *http.Response, model string, metrics *Metrics) {
	started := time.Now()
	defer upstream.Body.Close()
	guard := newStreamGuard(metrics)
	defer guard.close()
	state := &anthropicStreamState{Model: model, ID: "msg_" + randomHex(12), Tools: make(map[string]anthropicToolState)}
	outcome := "client_disconnect"
	defer func() {
		reason := "end_turn"
		if len(state.Tools) > 0 {
			reason = "tool_use"
		}
		logRequestDebug(c, "anthropic stream ended: outcome=%s model=%s frames=%d events=%d invalid_events=%d text_deltas=%d thinking_deltas=%d tool_calls=%d tool_argument_deltas=%d stop_reason=%s duration_ms=%d", outcome, model, state.Frames, state.Events, state.InvalidEvents, state.TextDeltas, state.ThinkingDeltas, state.ToolCalls, state.ToolArgumentDeltas, reason, time.Since(started).Milliseconds())
	}()
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	flusher, _ := c.Writer.(http.Flusher)
	write := func(data string) error {
		if _, err := c.Writer.Write([]byte(data)); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	err := streamSSEBody(upstream.Body, func(frame string) error { return write(translateAnthropicFrame(frame, state)) })
	if err != nil {
		outcome = "stream_error"
		logRequestWarn(c, "anthropic stream failed: error=%v", err)
		guard.complete()
		_ = write(anthropicEvent("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": err.Error()}}))
		return
	}
	if !state.Completed {
		outcome = "completed_with_synthetic_finish"
		reason := "end_turn"
		if len(state.Tools) > 0 {
			reason = "tool_use"
		}
		finishAnthropicStream(state, reason, nil)
		_ = write(statePending(state))
	}
	if outcome == "client_disconnect" {
		outcome = "completed"
	}
	guard.complete()
}

// translateAnthropicFrame returns all events generated for one upstream SSE frame.
func translateAnthropicFrame(frame string, state *anthropicStreamState) string {
	state.Frames++
	var output strings.Builder
	for _, data := range sseData(frame) {
		if data == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			state.InvalidEvents++
			continue
		}
		state.Events++
		switch stringFromAny(event["type"]) {
		case "response.created":
			if response, ok := event["response"].(map[string]any); ok && stringFromAny(response["id"]) != "" {
				state.ID = stringFromAny(response["id"])
			}
			ensureAnthropicMessageStart(state, &output)
		case "response.output_text.delta":
			state.TextDeltas++
			ensureAnthropicMessageStart(state, &output)
			index := ensureAnthropicTextBlock(state, false, &output)
			output.WriteString(anthropicEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": event["delta"]}}))
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			state.ThinkingDeltas++
			ensureAnthropicMessageStart(state, &output)
			index := ensureAnthropicTextBlock(state, true, &output)
			output.WriteString(anthropicEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": event["delta"]}}))
		case "response.output_item.added":
			if item, ok := event["item"].(map[string]any); ok && item["type"] == "function_call" {
				state.ToolCalls++
			}
			startAnthropicTool(state, event["item"], &output)
		case "response.function_call_arguments.delta":
			state.ToolArgumentDeltas++
			key := stringFromAny(event["call_id"])
			if key == "" {
				key = stringFromAny(event["item_id"])
			}
			if tool, ok := state.Tools[key]; ok {
				for name, candidate := range state.Tools {
					if candidate.Index == tool.Index {
						candidate.SawArguments = true
						state.Tools[name] = candidate
					}
				}
				output.WriteString(anthropicEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.Index, "delta": map[string]any{"type": "input_json_delta", "partial_json": event["delta"]}}))
			}
		case "response.output_item.done":
			finishAnthropicTool(state, event["item"], &output)
		case "response.completed":
			var usage any
			if response, ok := event["response"].(map[string]any); ok {
				usage = mapAnthropicUsage(response["usage"])
			}
			reason := "end_turn"
			if len(state.Tools) > 0 {
				reason = "tool_use"
			}
			finishAnthropicStream(state, reason, usage)
			output.WriteString(statePending(state))
		case "response.incomplete":
			var usage any
			if response, ok := event["response"].(map[string]any); ok {
				usage = mapAnthropicUsage(response["usage"])
			}
			finishAnthropicStream(state, "max_tokens", usage)
			output.WriteString(statePending(state))
		}
	}
	return output.String()
}

func ensureAnthropicMessageStart(state *anthropicStreamState, output *strings.Builder) {
	if state.Started {
		return
	}
	state.Started = true
	output.WriteString(anthropicEvent("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": state.ID, "type": "message", "role": "assistant", "model": state.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}}))
}

func ensureAnthropicTextBlock(state *anthropicStreamState, thinking bool, output *strings.Builder) uint32 {
	existing := state.TextIndex
	if thinking {
		existing = state.ThinkingIndex
	}
	if existing != nil {
		return *existing
	}
	index := state.NextIndex
	state.NextIndex++
	if thinking {
		state.ThinkingIndex = &index
	} else {
		state.TextIndex = &index
	}
	block := map[string]any{"type": "text", "text": ""}
	if thinking {
		block = map[string]any{"type": "thinking", "thinking": ""}
	}
	output.WriteString(anthropicEvent("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": block}))
	return index
}

func startAnthropicTool(state *anthropicStreamState, raw any, output *strings.Builder) {
	item, ok := raw.(map[string]any)
	if !ok || item["type"] != "function_call" {
		return
	}
	ensureAnthropicMessageStart(state, output)
	id := stringFromAny(item["call_id"])
	if id == "" {
		id = stringFromAny(item["id"])
	}
	if _, exists := state.Tools[id]; exists {
		return
	}
	index := state.NextIndex
	state.NextIndex++
	tool := anthropicToolState{Index: index}
	state.Tools[id] = tool
	if itemID := stringFromAny(item["id"]); itemID != "" {
		state.Tools[itemID] = tool
	}
	output.WriteString(anthropicEvent("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": id, "name": item["name"], "input": map[string]any{}}}))
}

func finishAnthropicTool(state *anthropicStreamState, raw any, output *strings.Builder) {
	item, ok := raw.(map[string]any)
	if !ok || item["type"] != "function_call" {
		return
	}
	id := stringFromAny(item["call_id"])
	if id == "" {
		id = stringFromAny(item["id"])
	}
	if _, exists := state.Tools[id]; !exists {
		startAnthropicTool(state, item, output)
	}
	tool, ok := state.Tools[id]
	if !ok {
		return
	}
	if !tool.SawArguments {
		output.WriteString(anthropicEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.Index, "delta": map[string]any{"type": "input_json_delta", "partial_json": item["arguments"]}}))
	}
	if !tool.Stopped {
		output.WriteString(anthropicEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": tool.Index}))
		for name, candidate := range state.Tools {
			if candidate.Index == tool.Index {
				candidate.Stopped = true
				state.Tools[name] = candidate
			}
		}
	}
}

func finishAnthropicStream(state *anthropicStreamState, reason string, usage any) {
	state.Completed = true
	var output strings.Builder
	ensureAnthropicMessageStart(state, &output)
	indexes := map[uint32]bool{}
	if state.TextIndex != nil {
		indexes[*state.TextIndex] = true
		state.TextIndex = nil
	}
	if state.ThinkingIndex != nil {
		indexes[*state.ThinkingIndex] = true
		state.ThinkingIndex = nil
	}
	for _, tool := range state.Tools {
		if !tool.Stopped {
			indexes[tool.Index] = true
		}
	}
	for index := range indexes {
		output.WriteString(anthropicEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index}))
	}
	if usage == nil {
		usage = map[string]any{"output_tokens": 0}
	}
	output.WriteString(anthropicEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": usage}))
	output.WriteString(anthropicEvent("message_stop", map[string]any{"type": "message_stop"}))
	state.Pending = output.String()
}

func statePending(state *anthropicStreamState) string {
	value := state.Pending
	state.Pending = ""
	return value
}
func anthropicEvent(name string, value any) string {
	data, _ := json.Marshal(value)
	return "event: " + name + "\ndata: " + string(data) + "\n\n"
}
func mapAnthropicUsage(value any) map[string]any {
	usage := mapUsage(value)
	result := map[string]any{"input_tokens": uint64(0), "output_tokens": uint64(0), "cache_read_input_tokens": uint64(0)}
	if usage != nil {
		result["input_tokens"], result["output_tokens"] = usage.PromptTokens, usage.CompletionTokens
		if usage.PromptTokensDetails != nil {
			result["cache_read_input_tokens"] = usage.PromptTokensDetails.CachedTokens
		}
	}
	return result
}
func anthropicError(c *gin.Context, status int, errorType, message string) {
	logRequestDebug(c, "anthropic error response: status=%d type=%s", status, errorType)
	c.JSON(status, map[string]any{"type": "error", "error": map[string]any{"type": errorType, "message": message}})
}
