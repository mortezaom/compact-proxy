package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func handleResponses(c *gin.Context, state *AppState) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logRequestWarn(c, "responses request body read failed: error=%v", err)
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "failed to read request body", "")
		return
	}
	normalized, responseWritten := normalizeResponsesBody(c, state, c.Request.Header, body, true)
	if responseWritten {
		return
	}
	logRequestInfo(c, "forwarding endpoint=responses model_requested=%s model=%s model_source=%s stream_requested=%t stream_forwarded=%t conversation_mode=%s reasoning_source=%s reasoning_effort=%s cache_key_source=%s cache_key_id=%s", normalized.RequestedModel, stringValue(normalized.Model), normalized.ModelSource, normalized.StreamRequested, normalized.StreamForwarded, normalized.ConversationMode, normalized.ReasoningSource, stringValue(normalized.ReasoningEffort), normalized.CacheKeySource, normalized.CacheKeyID)
	upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses", normalized.Body, true)
	if upstreamErr != nil {
		logRequestWarn(c, "responses upstream failed: status=%d type=%s", upstreamErr.Status, upstreamErr.ErrorType)
		writeUpstreamError(c, upstreamErr)
		return
	}
	if normalized.StreamRequested {
		logRequestDebug(c, "responses response mode=stream")
		streamResponse(c, upstream, state.Metrics)
		return
	}
	logRequestDebug(c, "responses response mode=collect")
	collectSSEToJSON(c, upstream)
}

func handleCompact(c *gin.Context, state *AppState) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logRequestWarn(c, "compact request body read failed: error=%v", err)
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "failed to read request body", "")
		return
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		logRequestWarn(c, "compact request rejected: reason=invalid_json error=%v", err)
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error(), "")
		return
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		logRequestWarn(c, "compact request rejected: reason=body_not_object")
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object", "")
		return
	}
	delete(object, "conversation_mode")
	delete(object, "stream")
	cacheSource := injectPromptCacheKey(c.Request.Header, object)
	logRequestInfo(c, "forwarding endpoint=responses/compact model=%s cache_key_source=%s cache_key_id=%s", stringFromAny(object["model"]), cacheSource, promptCacheKeyLogID(object["prompt_cache_key"]))
	upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses/compact", object, false)
	if upstreamErr != nil {
		logRequestWarn(c, "compact upstream failed: status=%d type=%s", upstreamErr.Status, upstreamErr.ErrorType)
		writeUpstreamError(c, upstreamErr)
		return
	}
	passthroughResponse(c, upstream)
}

func streamResponse(c *gin.Context, upstream *http.Response, metrics *Metrics) {
	started := time.Now()
	bytesWritten := 0
	outcome := "client_disconnect"
	defer func() {
		logRequestDebug(c, "responses stream ended: outcome=%s bytes=%d duration_ms=%d", outcome, bytesWritten, time.Since(started).Milliseconds())
	}()
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
				outcome = "downstream_write_error"
				logRequestWarn(c, "responses stream write failed: bytes=%d error=%v", count, writeErr)
				return
			}
			bytesWritten += count
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			outcome = "completed"
			guard.complete()
			return
		}
		if err != nil {
			outcome = "upstream_read_error"
			logRequestWarn(c, "responses stream read failed: error=%v", err)
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
	bytesWritten, err := io.Copy(c.Writer, upstream.Body)
	logRequestDebug(c, "upstream response passed through: status=%d bytes=%d error=%v", upstream.StatusCode, bytesWritten, err)
}

func collectSSEToJSON(c *gin.Context, upstream *http.Response) {
	started := time.Now()
	eventCount := 0
	invalidEventCount := 0
	outputItemCount := 0
	defer func() {
		logRequestDebug(c, "responses collection ended: status=%d events=%d invalid_events=%d output_items=%d duration_ms=%d", upstream.StatusCode, eventCount, invalidEventCount, outputItemCount, time.Since(started).Milliseconds())
	}()
	defer upstream.Body.Close()
	raw, err := io.ReadAll(upstream.Body)
	if err != nil {
		logRequestWarn(c, "responses collection read failed: error=%v", err)
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
			invalidEventCount++
			continue
		}
		eventCount++
		if event["type"] == "response.output_item.done" {
			if item, ok := event["item"]; ok {
				outputItems = append(outputItems, item)
				outputItemCount++
			}
		}
		if response, ok := event["response"]; ok {
			lastResponse = response
		}
	}
	if lastResponse == nil {
		logRequestWarn(c, "responses collection produced no final response")
		writeJSONError(c, upstream.StatusCode, "upstream_error", "upstream did not return a response", "")
		return
	}
	response, ok := lastResponse.(map[string]any)
	if !ok {
		logRequestWarn(c, "responses collection final response had invalid shape")
		writeJSONError(c, upstream.StatusCode, "upstream_error", "upstream response was invalid", "")
		return
	}
	if output, ok := response["output"].([]any); !ok || len(output) == 0 {
		if len(outputItems) > 0 {
			response["output"] = outputItems
		}
	}
	logRequestDebug(c, "responses collection final response found: output_items=%d", outputItemCount)
	c.JSON(upstream.StatusCode, response)
}

type NormalizedRequest struct {
	Body             map[string]any
	StreamRequested  bool
	StreamForwarded  bool
	RequestedModel   string
	Model            *string
	ModelSource      string
	ReasoningEffort  *string
	ReasoningSource  string
	ConversationMode string
	CacheKeySource   CacheKeySource
	CacheKeyID       string
}

func normalizeResponsesBody(c *gin.Context, state *AppState, headers http.Header, body []byte, forceStream bool) (NormalizedRequest, bool) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		logRequestWarn(c, "request normalization rejected: reason=invalid_json error=%v", err)
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error(), "")
		return NormalizedRequest{}, true
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		logRequestWarn(c, "request normalization rejected: reason=body_not_object")
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object", "")
		return NormalizedRequest{}, true
	}
	streamRequested := false
	if raw, exists := object["stream"]; exists {
		value, valid := raw.(bool)
		if !valid {
			logRequestWarn(c, "request normalization rejected: field=stream reason=not_boolean")
			writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "stream must be a boolean", "stream")
			return NormalizedRequest{}, true
		}
		streamRequested = value
	}
	delete(object, "safety_identifier")
	delete(object, "generate")
	delete(object, "max_output_tokens")
	conversationMode := state.Config.ConversationMode
	conversationModeSource := "default"
	if raw, exists := object["conversation_mode"]; exists {
		value, valid := raw.(string)
		if !valid {
			logRequestWarn(c, "request normalization rejected: field=conversation_mode reason=not_string")
			writeJSONError(c, http.StatusBadRequest, "invalid_request_error", "conversation_mode must be a string", "conversation_mode")
			return NormalizedRequest{}, true
		}
		conversationMode = value
		conversationModeSource = "request"
		delete(object, "conversation_mode")
	}
	if conversationMode != "client" && conversationMode != "server" {
		logRequestWarn(c, "request normalization rejected: field=conversation_mode value=%s", conversationMode)
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
	requestedModel := ""
	model := ""
	modelSource := "none"
	var suffixEffort *string
	if value, ok := object["model"].(string); ok {
		requestedModel = value
		modelSource = "client"
		model, suffixEffort = parseReasoningSuffix(value)
		if model != value {
			modelSource = "client_suffix"
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
	reasoningSource := "none"
	if nativeReasoningPresent {
		reasoningSource = "native"
		effort = nativeEffort
	} else if compatibleEffort != nil {
		reasoningSource = "compatible"
		effort = compatibleEffort
	} else if suffixEffort != nil {
		reasoningSource = "model_suffix"
		effort = suffixEffort
	} else if fallback := defaultReasoningEffort(state.Config); fallback != "" {
		reasoningSource = "default"
		effort = &fallback
	}
	if effort != nil {
		if responseWritten := validateReasoningEffort(c, state, model, *effort); responseWritten {
			logRequestWarn(c, "request normalization rejected: model=%s reasoning_effort=%s source=%s", model, *effort, reasoningSource)
			return NormalizedRequest{}, true
		}
		if _, exists := object["reasoning"]; !exists {
			object["reasoning"] = map[string]any{"effort": *effort}
		}
	}
	cacheSource := injectPromptCacheKey(headers, object)
	result := NormalizedRequest{Body: object, StreamRequested: streamRequested, StreamForwarded: forceStream, RequestedModel: requestedModel, Model: modelPtr, ModelSource: modelSource, ReasoningEffort: effort, ReasoningSource: reasoningSource, ConversationMode: conversationMode, CacheKeySource: cacheSource, CacheKeyID: promptCacheKeyLogID(object["prompt_cache_key"])}
	logRequestDebug(c, "request normalized: model_requested=%s model=%s model_source=%s stream_requested=%t stream_forwarded=%t conversation_mode=%s conversation_mode_source=%s reasoning_source=%s reasoning_effort=%s cache_key_source=%s cache_key_id=%s fields=%d body_bytes=%d", result.RequestedModel, stringValue(result.Model), result.ModelSource, result.StreamRequested, result.StreamForwarded, result.ConversationMode, conversationModeSource, result.ReasoningSource, stringValue(result.ReasoningEffort), result.CacheKeySource, result.CacheKeyID, len(object), len(body))
	return result, false
}

func validateReasoningEffort(c *gin.Context, state *AppState, model, effort string) bool {
	if capability := state.ModelsCache.Capability(model); capability != nil && capability.ReasoningEfforts != nil {
		for _, candidate := range capability.ReasoningEfforts {
			if candidate == effort {
				logRequestDebug(c, "reasoning validation: model=%s effort=%s source=catalog result=accepted", model, effort)
				return false
			}
		}
		logRequestDebug(c, "reasoning validation: model=%s effort=%s source=catalog result=rejected", model, effort)
		writeJSONError(c, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("model `%s` does not advertise reasoning effort `%s`", model, effort), "reasoning.effort")
		return true
	}
	for _, supported := range reasoningEfforts {
		if supported == effort {
			logRequestDebug(c, "reasoning validation: model=%s effort=%s source=global result=accepted", model, effort)
			return false
		}
	}
	logRequestDebug(c, "reasoning validation: model=%s effort=%s source=global result=rejected", model, effort)
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
