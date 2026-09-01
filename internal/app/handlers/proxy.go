package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func handleResponses(c *gin.Context, state *AppState) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "failed to read request body", "")
		return
	}
	normalized, responseWritten := normalizeResponsesBody(c, state, c.Request.Header, body, true)
	if responseWritten {
		return
	}
	logInfo("forwarding Responses request: model=%s stream=%t reasoning_effort=%s cache_key=%s", stringValue(normalized.Model), normalized.StreamRequested, stringValue(normalized.ReasoningEffort), normalized.CacheKeySource)
	upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses", normalized.Body, true)
	if upstreamErr != nil {
		writeUpstreamError(c, upstreamErr)
		return
	}
	if normalized.StreamRequested {
		streamResponse(c, upstream, state.Metrics)
		return
	}
	collectSSEToJSON(c, upstream)
}

func handleCompact(c *gin.Context, state *AppState) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "failed to read request body", "")
		return
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error(), "")
		return
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object", "")
		return
	}
	delete(object, "conversation_mode")
	delete(object, "stream")
	injectPromptCacheKey(c.Request.Header, object)
	upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses/compact", object, false)
	if upstreamErr != nil {
		writeUpstreamError(c, upstreamErr)
		return
	}
	passthroughResponse(c, upstream)
}

func streamResponse(c *gin.Context, upstream *http.Response, metrics *Metrics) {
	defer upstream.Body.Close()
	guard := newStreamGuard(metrics)
	defer guard.close()
	contentType := upstream.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Status(upstream.StatusCode)
	c.Header("Content-Type", contentType)
	flusher, _ := c.Writer.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		count, err := upstream.Body.Read(buffer)
		if count > 0 {
			if _, writeErr := c.Writer.Write(buffer[:count]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			guard.complete()
			return
		}
		if err != nil {
			return
		}
	}
}

func passthroughResponse(c *gin.Context, upstream *http.Response) {
	defer upstream.Body.Close()
	if contentType := upstream.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Status(upstream.StatusCode)
	_, _ = io.Copy(c.Writer, upstream.Body)
}

func collectSSEToJSON(c *gin.Context, upstream *http.Response) {
	defer upstream.Body.Close()
	raw, err := io.ReadAll(upstream.Body)
	if err != nil {
		writeJSONError(c, http.StatusBadGateway, "upstream_error", "failed to read upstream: "+err.Error(), "")
		return
	}
	var lastResponse any
	var outputItems []any
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
		if event["type"] == "response.output_item.done" {
			if item, ok := event["item"]; ok {
				outputItems = append(outputItems, item)
			}
		}
		if response, ok := event["response"]; ok {
			lastResponse = response
		}
	}
	if lastResponse == nil {
		writeJSONError(c, upstream.StatusCode, "upstream_error", "upstream did not return a response", "")
		return
	}
	response, ok := lastResponse.(map[string]any)
	if !ok {
		writeJSONError(c, upstream.StatusCode, "upstream_error", "upstream response was invalid", "")
		return
	}
	if output, ok := response["output"].([]any); !ok || len(output) == 0 {
		if len(outputItems) > 0 {
			response["output"] = outputItems
		}
	}
	c.JSON(upstream.StatusCode, response)
}

type NormalizedRequest struct {
	Body            map[string]any
	StreamRequested bool
	Model           *string
	ReasoningEffort *string
	CacheKeySource  CacheKeySource
}

func normalizeResponsesBody(c *gin.Context, state *AppState, headers http.Header, body []byte, forceStream bool) (NormalizedRequest, bool) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error(), "")
		return NormalizedRequest{}, true
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object", "")
		return NormalizedRequest{}, true
	}
	streamRequested := false
	if raw, exists := object["stream"]; exists {
		value, valid := raw.(bool)
		if !valid {
			writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "stream must be a boolean", "stream")
			return NormalizedRequest{}, true
		}
		streamRequested = value
	}
	delete(object, "safety_identifier")
	delete(object, "generate")
	delete(object, "max_output_tokens")
	conversationMode := state.Config.ConversationMode
	if raw, exists := object["conversation_mode"]; exists {
		value, valid := raw.(string)
		if !valid {
			writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "conversation_mode must be a string", "conversation_mode")
			return NormalizedRequest{}, true
		}
		conversationMode = value
		delete(object, "conversation_mode")
	}
	if conversationMode != "client" && conversationMode != "server" {
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "conversation_mode must be `client` or `server`", "")
		return NormalizedRequest{}, true
	}
	if _, exists := object["instructions"]; !exists {
		object["instructions"] = ""
	}
	if _, exists := object["store"]; !exists {
		object["store"] = false
	}
	if forceStream {
		object["stream"] = true
	} else {
		delete(object, "stream")
	}
	model := ""
	var suffixEffort *string
	if value, ok := object["model"].(string); ok {
		model, suffixEffort = parseReasoningSuffix(value)
		if model != value {
			object["model"] = model
		}
	}
	var modelPtr *string
	if model != "" {
		modelCopy := model
		modelPtr = &modelCopy
	}
	nativeReasoningPresent := false
	var nativeEffort *string
	if reasoning, exists := object["reasoning"]; exists {
		nativeReasoningPresent = true
		if reasoningObject, ok := reasoning.(map[string]any); ok {
			if effort, ok := reasoningObject["effort"].(string); ok {
				nativeEffort = &effort
			}
		}
	}
	var compatibleEffort *string
	if raw, exists := object["reasoning_effort"]; exists {
		delete(object, "reasoning_effort")
		value, valid := raw.(string)
		if !valid {
			writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "reasoning_effort must be a string", "reasoning_effort")
			return NormalizedRequest{}, true
		}
		compatibleEffort = &value
	}
	var effort *string
	if nativeReasoningPresent {
		effort = nativeEffort
	} else if compatibleEffort != nil {
		effort = compatibleEffort
	} else if suffixEffort != nil {
		effort = suffixEffort
	} else if fallback := defaultReasoningEffort(state.Config); fallback != "" {
		effort = &fallback
	}
	if effort != nil {
		if responseWritten := validateReasoningEffort(c, state, model, *effort); responseWritten {
			return NormalizedRequest{}, true
		}
		if _, exists := object["reasoning"]; !exists {
			object["reasoning"] = map[string]any{"effort": *effort}
		}
	}
	cacheSource := injectPromptCacheKey(headers, object)
	return NormalizedRequest{Body: object, StreamRequested: streamRequested, Model: modelPtr, ReasoningEffort: effort, CacheKeySource: cacheSource}, false
}

func validateReasoningEffort(c *gin.Context, state *AppState, model, effort string) bool {
	if capability := state.ModelsCache.Capability(model); capability != nil && capability.ReasoningEfforts != nil {
		for _, candidate := range capability.ReasoningEfforts {
			if candidate == effort {
				return false
			}
		}
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("model `%s` does not advertise reasoning effort `%s`", model, effort), "reasoning.effort")
		return true
	}
	for _, supported := range reasoningEfforts {
		if supported == effort {
			return false
		}
	}
	writeJSONError(c, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("unsupported reasoning effort `%s`", effort), "reasoning.effort")
	return true
}

func parseReasoningSuffix(model string) (string, *string) {
	for _, effort := range reasoningEfforts {
		suffix := "-" + effort
		if strings.HasSuffix(model, suffix) {
			clean := strings.TrimSuffix(model, suffix)
			value := effort
			return clean, &value
		}
	}
	return model, nil
}
