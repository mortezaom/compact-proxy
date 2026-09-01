package handlers

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type ChatRequest struct {
	Model               string         `json:"model"`
	Messages            []ChatMessage  `json:"messages"`
	Stream              bool           `json:"stream,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	MaxTokens           *uint64        `json:"max_tokens,omitempty"`
	MaxCompletionTokens *uint64        `json:"max_completion_tokens,omitempty"`
	Stop                any            `json:"stop,omitempty"`
	N                   *uint64        `json:"n,omitempty"`
	StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
	Tools               []ToolDef      `json:"tools,omitempty"`
	ToolChoice          any            `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool          `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort     *string        `json:"reasoning_effort,omitempty"`
	PromptCacheKey      *string        `json:"prompt_cache_key,omitempty"`
	FrequencyPenalty    *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64       `json:"presence_penalty,omitempty"`
	Logprobs            *bool          `json:"logprobs,omitempty"`
	ResponseFormat      any            `json:"response_format,omitempty"`
	Seed                *int64         `json:"seed,omitempty"`
	User                *string        `json:"user,omitempty"`
	ServiceTier         *string        `json:"service_tier,omitempty"`
}

type StreamOptions struct {
	IncludeUsage *bool `json:"include_usage,omitempty"`
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string    `json:"tool_call_id,omitempty"`
	Name       *string    `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	CallType string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct{ Name, Arguments string }

func (f FunctionCall) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{f.Name, f.Arguments})
}
func (f *FunctionCall) UnmarshalJSON(data []byte) error {
	var value struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Name, f.Arguments = value.Name, value.Arguments
	return nil
}

type ToolDef struct {
	ToolType string       `json:"type"`
	Function *FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Parameters  any     `json:"parameters,omitempty"`
}

type chatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created uint64        `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        uint32  `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

type delta struct {
	Role             *string         `json:"role,omitempty"`
	Content          *string         `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        []deltaToolCall `json:"tool_calls,omitempty"`
}

type deltaToolCall struct {
	Index    uint32         `json:"index"`
	ID       *string        `json:"id,omitempty"`
	CallType *string        `json:"type,omitempty"`
	Function *deltaFunction `json:"function,omitempty"`
}

type deltaFunction struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

type Usage struct {
	PromptTokens            uint64                  `json:"prompt_tokens"`
	CompletionTokens        uint64                  `json:"completion_tokens"`
	TotalTokens             uint64                  `json:"total_tokens"`
	PromptTokensDetails     *PromptTokenDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokenDetails `json:"completion_tokens_details,omitempty"`
}

type PromptTokenDetails struct {
	CachedTokens uint64 `json:"cached_tokens"`
}
type CompletionTokenDetails struct {
	ReasoningTokens uint64 `json:"reasoning_tokens"`
}

type chatCompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created uint64             `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   Usage              `json:"usage"`
}
type completionChoice struct {
	Index        uint32          `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}
type responseMessage struct {
	Role             string             `json:"role"`
	Content          *string            `json:"content"`
	ReasoningContent *string            `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCallResponse `json:"tool_calls,omitempty"`
}
type toolCallResponse struct {
	ID       string           `json:"id"`
	CallType string           `json:"type"`
	Function functionResponse `json:"function"`
}
type functionResponse struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func handleChatCompletions(c *gin.Context, state *AppState) {
	var request ChatRequest
	if err := decodeJSON(c.Request.Body, &request); err != nil {
		invalidRequest(c, "invalid JSON request: "+err.Error())
		return
	}
	if validateChatRequest(c, &request) {
		return
	}
	streamIncludeUsage := request.StreamOptions != nil && request.StreamOptions.IncludeUsage != nil && *request.StreamOptions.IncludeUsage
	responseID := "chatcmpl-" + randomHex(8)
	translated := buildChatResponsesBody(&request)
	data, _ := json.Marshal(translated)
	normalized, responseWritten := normalizeResponsesBody(c, state, c.Request.Header, data, true)
	if responseWritten {
		return
	}
	logInfo("forwarding Chat Completions request: model=%s stream=%t reasoning_effort=%s cache_key=%s", request.Model, request.Stream, stringValue(normalized.ReasoningEffort), normalized.CacheKeySource)
	upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses", normalized.Body, true)
	if upstreamErr != nil {
		writeUpstreamError(c, upstreamErr)
		return
	}
	if request.Stream {
		streamChat(c, upstream, responseID, request.Model, streamIncludeUsage, state.Metrics)
		return
	}
	collectChat(c, upstream, responseID, request.Model)
}

func validateChatRequest(c *gin.Context, request *ChatRequest) bool {
	if request.N != nil && *request.N != 1 {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "only n=1 is supported", "n")
		return true
	}
	for _, tool := range request.Tools {
		if tool.ToolType != "function" || tool.Function == nil || strings.TrimSpace(tool.Function.Name) == "" || (tool.Function.Parameters != nil && !isJSONObject(tool.Function.Parameters)) {
			writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "each tool must be a named function with an object parameters schema", "tools")
			return true
		}
	}
	return false
}

func isJSONObject(value any) bool { _, ok := value.(map[string]any); return ok }

func buildChatResponsesBody(request *ChatRequest) map[string]any {
	model, suffixEffort := parseReasoningSuffix(request.Model)
	instructions, input := translateMessages(request.Messages)
	body := map[string]any{"model": model, "instructions": instructions, "input": input, "stream": true, "store": false}
	if request.ReasoningEffort != nil {
		body["reasoning"] = map[string]any{"effort": *request.ReasoningEffort}
	} else if suffixEffort != nil {
		body["reasoning"] = map[string]any{"effort": *suffixEffort}
	}
	if request.PromptCacheKey != nil {
		body["prompt_cache_key"] = *request.PromptCacheKey
	}
	switch stop := request.Stop.(type) {
	case string:
		body["stop"] = []string{stop}
	case []any:
		body["stop"] = stop
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Function == nil {
				continue
			}
			parameters := tool.Function.Parameters
			if parameters == nil {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"type": "function", "name": tool.Function.Name, "description": tool.Function.Description, "parameters": parameters})
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}
	switch choice := request.ToolChoice.(type) {
	case string:
		if choice == "none" {
			body["tools"] = []any{}
		}
	case map[string]any:
		if choice["type"] == "function" {
			if function, ok := choice["function"].(map[string]any); ok {
				if name, ok := function["name"].(string); ok {
					body["tool_choice"] = map[string]any{"type": "function", "name": name}
				}
			}
		}
	}
	if request.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *request.ParallelToolCalls
	}
	return body
}

func translateMessages(messages []ChatMessage) (string, []any) {
	var instructions []string
	var input []any
	for _, message := range messages {
		switch message.Role {
		case "system", "developer":
			instructions = append(instructions, extractChatText(message.Content))
		case "user":
			input = append(input, map[string]any{"type": "message", "role": "user", "content": translateContent(message.Content, "input_text")})
		case "assistant":
			if text := extractChatText(message.Content); text != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": extractChatText(message.Content)})
		default:
			logWarn("unknown message role %s, treating as user", message.Role)
			input = append(input, map[string]any{"type": "message", "role": "user", "content": translateContent(message.Content, "input_text")})
		}
	}
	return strings.Join(instructions, "\n\n"), input
}

func extractChatText(content any) string {
	if content == nil {
		return ""
	}
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var result strings.Builder
		for _, item := range value {
			if object, ok := item.(map[string]any); ok {
				if text, ok := object["text"].(string); ok {
					result.WriteString(text)
				}
			}
		}
		return result.String()
	default:
		data, _ := json.Marshal(value)
		return string(data)
	}
}

func translateContent(content any, textType string) []any {
	if content == nil {
		return []any{map[string]any{"type": textType, "text": ""}}
	}
	if text, ok := content.(string); ok {
		return []any{map[string]any{"type": textType, "text": text}}
	}
	if parts, ok := content.([]any); ok {
		result := make([]any, 0, len(parts))
		for _, part := range parts {
			object, ok := part.(map[string]any)
			if !ok {
				result = append(result, part)
				continue
			}
			switch object["type"] {
			case "text":
				result = append(result, map[string]any{"type": textType, "text": stringFromAny(object["text"])})
			case "image_url":
				imageURL := ""
				if image, ok := object["image_url"].(map[string]any); ok {
					imageURL = stringFromAny(image["url"])
				}
				result = append(result, map[string]any{"type": "input_image", "image_url": imageURL})
			default:
				result = append(result, part)
			}
		}
		return result
	}
	return []any{map[string]any{"type": textType, "text": extractChatText(content)}}
}

type chatToolState struct {
	Index        uint32
	SawArguments bool
}
type chatStreamState struct {
	ID            string
	Model         string
	IncludeUsage  bool
	Tools         map[string]chatToolState
	LastTool      string
	NextToolIndex uint32
	SawToolCall   bool
	Completed     bool
}

func streamChat(c *gin.Context, upstream *http.Response, responseID, model string, includeUsage bool, metrics *Metrics) {
	defer upstream.Body.Close()
	guard := newStreamGuard(metrics)
	defer guard.close()
	state := &chatStreamState{ID: responseID, Model: model, IncludeUsage: includeUsage, Tools: make(map[string]chatToolState)}
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	flusher, _ := c.Writer.(http.Flusher)
	write := func(value string) error {
		if _, err := c.Writer.Write([]byte(value)); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	err := streamSSEBody(upstream.Body, func(frame string) error {
		output := translateChatFrame(frame, state)
		if output != "" {
			return write(output)
		}
		return nil
	})
	if err != nil {
		guard.complete()
		_ = write("data: {\"error\":{\"message\":\"stream error: " + escapeJSONString(err.Error()) + "\"}}\n\ndata: [DONE]\n\n")
		return
	}
	if !state.Completed {
		state.Completed = true
		reason := "stop"
		if state.SawToolCall {
			reason = "tool_calls"
		}
		_ = write(finalChatStreamChunk(state.ID, state.Model, nil, reason, state.IncludeUsage))
	}
	guard.complete()
}

func translateChatFrame(frame string, state *chatStreamState) string {
	var output strings.Builder
	for _, data := range sseData(frame) {
		if data == "[DONE]" {
			state.Completed = true
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		eventType := stringFromAny(event["type"])
		switch eventType {
		case "response.created":
			role := "assistant"
			output.WriteString(chatChunkJSON(state.ID, state.Model, delta{Role: &role}))
		case "response.output_item.added":
			item, _ := event["item"].(map[string]any)
			if stringFromAny(item["type"]) != "function_call" {
				continue
			}
			state.SawToolCall = true
			name := stringFromAny(item["name"])
			id := stringFromAny(item["call_id"])
			if id == "" {
				id = stringFromAny(item["id"])
			}
			index := state.NextToolIndex
			state.NextToolIndex++
			tool := chatToolState{Index: index}
			state.Tools[id] = tool
			if itemID := stringFromAny(item["id"]); itemID != "" {
				state.Tools[itemID] = tool
			}
			state.LastTool = id
			callType := "function"
			output.WriteString(chatChunkJSON(state.ID, state.Model, delta{ToolCalls: []deltaToolCall{{Index: index, ID: stringPointer(id), CallType: &callType, Function: &deltaFunction{Name: stringPointer(name), Arguments: stringPointer("")}}}}))
		case "response.output_text.delta":
			text := stringFromAny(event["delta"])
			output.WriteString(chatChunkJSON(state.ID, state.Model, delta{Content: stringPointer(text)}))
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			text := stringFromAny(event["delta"])
			output.WriteString(chatChunkJSON(state.ID, state.Model, delta{ReasoningContent: stringPointer(text)}))
		case "response.function_call_arguments.delta":
			state.SawToolCall = true
			args := stringFromAny(event["delta"])
			key := stringFromAny(event["call_id"])
			if key == "" {
				key = stringFromAny(event["item_id"])
			}
			if key == "" {
				key = state.LastTool
			}
			tool, ok := state.Tools[key]
			index := uint32(0)
			if ok {
				index = tool.Index
				for key, candidate := range state.Tools {
					if candidate.Index == index {
						candidate.SawArguments = true
						state.Tools[key] = candidate
					}
				}
			}
			callType := "function"
			output.WriteString(chatChunkJSON(state.ID, state.Model, delta{ToolCalls: []deltaToolCall{{Index: index, CallType: &callType, Function: &deltaFunction{Arguments: stringPointer(args)}}}}))
		case "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			if stringFromAny(item["type"]) != "function_call" {
				continue
			}
			state.SawToolCall = true
			id := stringFromAny(item["call_id"])
			if id == "" {
				id = stringFromAny(item["id"])
			}
			tool, exists := state.Tools[id]
			index := state.NextToolIndex
			if exists {
				index = tool.Index
			} else {
				state.NextToolIndex++
			}
			if exists && tool.SawArguments {
				continue
			}
			name, args := stringFromAny(item["name"]), stringFromAny(item["arguments"])
			callType := "function"
			call := deltaToolCall{Index: index, CallType: &callType, Function: &deltaFunction{Arguments: stringPointer(args)}}
			if !exists {
				call.ID, call.Function.Name = stringPointer(id), stringPointer(name)
			}
			output.WriteString(chatChunkJSON(state.ID, state.Model, delta{ToolCalls: []deltaToolCall{call}}))
		case "response.completed":
			state.Completed = true
			reason := "stop"
			if state.SawToolCall {
				reason = "tool_calls"
			}
			var usage any
			if response, ok := event["response"].(map[string]any); ok {
				usage = response["usage"]
			}
			output.WriteString(finalChatStreamChunk(state.ID, state.Model, usage, reason, state.IncludeUsage))
		case "response.incomplete":
			state.Completed = true
			var usage any
			if response, ok := event["response"].(map[string]any); ok {
				usage = response["usage"]
			}
			output.WriteString(finalChatStreamChunk(state.ID, state.Model, usage, "length", state.IncludeUsage))
		}
	}
	return output.String()
}

func chatChunkJSON(id, model string, change delta) string {
	chunk := chatCompletionChunk{ID: id, Object: "chat.completion.chunk", Created: nowEpoch(), Model: model, Choices: []chunkChoice{{Index: 0, Delta: change}}}
	data, _ := json.Marshal(chunk)
	return "data: " + string(data) + "\n\n"
}

func finalChatStreamChunk(id, model string, rawUsage any, finishReason string, includeUsage bool) string {
	reason := finishReason
	finish := chatCompletionChunk{ID: id, Object: "chat.completion.chunk", Created: nowEpoch(), Model: model, Choices: []chunkChoice{{Index: 0, Delta: delta{}, FinishReason: &reason}}}
	finishData, _ := json.Marshal(finish)
	if !includeUsage {
		return "data: " + string(finishData) + "\n\ndata: [DONE]\n\n"
	}
	usage := mapUsage(rawUsage)
	usageChunk := chatCompletionChunk{ID: id, Object: "chat.completion.chunk", Created: nowEpoch(), Model: model, Choices: []chunkChoice{}, Usage: usage}
	usageData, _ := json.Marshal(usageChunk)
	return "data: " + string(finishData) + "\n\ndata: " + string(usageData) + "\n\ndata: [DONE]\n\n"
}

func collectChat(c *gin.Context, upstream *http.Response, responseID, model string) {
	defer upstream.Body.Close()
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		body, _ := io.ReadAll(upstream.Body)
		c.Data(upstream.StatusCode, "application/json", []byte(`{"error":{"message":`+jsonString(string(body))+`}}`))
		return
	}
	raw, err := io.ReadAll(upstream.Body)
	if err != nil {
		writeJSONError(c, http.StatusBadGateway, "upstream_error", "failed to read upstream: "+err.Error(), "")
		return
	}
	content, reasoning := "", ""
	var toolCalls []toolCallResponse
	indexes := make(map[string]int)
	var lastToolIndex *int
	var usage *Usage
	finishReason := "stop"
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		switch stringFromAny(event["type"]) {
		case "response.output_text.delta":
			content += stringFromAny(event["delta"])
		case "response.output_item.added":
			item, _ := event["item"].(map[string]any)
			if stringFromAny(item["type"]) != "function_call" {
				continue
			}
			id := stringFromAny(item["call_id"])
			if id == "" {
				id = stringFromAny(item["id"])
			}
			index := len(toolCalls)
			toolCalls = append(toolCalls, toolCallResponse{ID: id, CallType: "function", Function: functionResponse{Name: stringFromAny(item["name"])}})
			indexes[id] = index
			if itemID := stringFromAny(item["id"]); itemID != "" {
				indexes[itemID] = index
			}
			lastToolIndex = &index
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning += stringFromAny(event["delta"])
		case "response.function_call_arguments.delta":
			key := stringFromAny(event["call_id"])
			if key == "" {
				key = stringFromAny(event["item_id"])
			}
			index, ok := indexes[key]
			if !ok && lastToolIndex != nil {
				index, ok = *lastToolIndex, true
			}
			if ok {
				toolCalls[index].Function.Arguments += stringFromAny(event["delta"])
			}
		case "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			if stringFromAny(item["type"]) != "function_call" {
				continue
			}
			id := stringFromAny(item["call_id"])
			if id == "" {
				id = stringFromAny(item["id"])
			}
			index, ok := indexes[id]
			if !ok {
				if itemID := stringFromAny(item["id"]); itemID != "" {
					index, ok = indexes[itemID]
				}
			}
			if ok && toolCalls[index].Function.Arguments == "" {
				toolCalls[index].Function.Arguments = stringFromAny(item["arguments"])
			}
		case "response.completed":
			if response, ok := event["response"].(map[string]any); ok {
				usage = mapUsage(response["usage"])
			}
			if len(toolCalls) > 0 {
				finishReason = "tool_calls"
			}
		case "response.incomplete":
			if response, ok := event["response"].(map[string]any); ok {
				usage = mapUsage(response["usage"])
			}
			finishReason = "length"
		}
	}
	message := responseMessage{Role: "assistant"}
	if content != "" {
		message.Content = &content
	}
	if reasoning != "" {
		message.ReasoningContent = &reasoning
	}
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
	}
	if usage == nil {
		usage = &Usage{}
	}
	c.JSON(http.StatusOK, chatCompletionResponse{ID: responseID, Object: "chat.completion", Created: nowEpoch(), Model: model, Choices: []completionChoice{{Index: 0, Message: message, FinishReason: finishReason}}, Usage: *usage})
}

func mapUsage(value any) *Usage {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	prompt := uint64FromAny(object["input_tokens"])
	completion := uint64FromAny(object["output_tokens"])
	total := uint64FromAny(object["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}
	usage := &Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
	if details, ok := object["input_tokens_details"].(map[string]any); ok {
		if cached := uint64FromAny(details["cached_tokens"]); cached > 0 {
			usage.PromptTokensDetails = &PromptTokenDetails{CachedTokens: cached}
		}
	}
	if details, ok := object["output_tokens_details"].(map[string]any); ok {
		if reasoning := uint64FromAny(details["reasoning_tokens"]); reasoning > 0 {
			usage.CompletionTokensDetails = &CompletionTokenDetails{ReasoningTokens: reasoning}
		}
	}
	return usage
}

func nowEpoch() uint64 { return uint64(time.Now().Unix()) }
func randomHex(size int) string {
	data := make([]byte, size)
	_, _ = rand.Read(data)
	return hexEncode(data)
}

func stringPointer(value string) *string { return &value }
func stringFromAny(value any) string     { result, _ := value.(string); return result }
func uint64FromAny(value any) uint64 {
	switch number := value.(type) {
	case float64:
		if number > 0 {
			return uint64(number)
		}
	case json.Number:
		n, _ := number.Int64()
		if n > 0 {
			return uint64(n)
		}
	}
	return 0
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func jsonString(value string) string       { data, _ := json.Marshal(value); return string(data) }
func escapeJSONString(value string) string { return strings.Trim(string(jsonString(value)), `"`) }
