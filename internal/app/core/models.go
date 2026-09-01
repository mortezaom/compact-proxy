package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type OpenAIModel struct {
	ID                     string   `json:"id"`
	Object                 string   `json:"object"`
	Created                uint64   `json:"created"`
	OwnedBy                string   `json:"owned_by"`
	Name                   string   `json:"name,omitempty"`
	ContextWindow          *uint64  `json:"context_window,omitempty"`
	DefaultMaxTokens       *uint64  `json:"default_max_tokens,omitempty"`
	CanReason              bool     `json:"can_reason,omitempty"`
	ReasoningLevels        []string `json:"reasoning_levels,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	SupportsAttachments    *bool    `json:"supports_attachments,omitempty"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

type ModelCapabilities struct {
	ID                      string   `json:"id"`
	ContextWindow           *uint64  `json:"context_window,omitempty"`
	MaxOutputTokens         *uint64  `json:"max_output_tokens,omitempty"`
	Reasoning               *bool    `json:"reasoning,omitempty"`
	ReasoningEfforts        []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort  string   `json:"default_reasoning_effort,omitempty"`
	SupportsTools           *bool    `json:"supports_tools,omitempty"`
	SupportsParallelTools   *bool    `json:"supports_parallel_tools,omitempty"`
	SupportsImages          *bool    `json:"supports_images,omitempty"`
	SupportsWebSearch       *bool    `json:"supports_web_search,omitempty"`
	SupportsImageGeneration *bool    `json:"supports_image_generation,omitempty"`
	SupportsPromptCache     *bool    `json:"supports_prompt_cache,omitempty"`
	SupportsResponsesWS     *bool    `json:"supports_responses_ws,omitempty"`
}

type modelCatalog struct {
	Models       []OpenAIModel
	Capabilities []ModelCapabilities
}

type cacheEntry struct {
	catalog   modelCatalog
	fetchedAt time.Time
}

type ModelsCache struct {
	mu    sync.RWMutex
	entry *cacheEntry
}

func newModelsCache() *ModelsCache { return &ModelsCache{} }

func (c *ModelsCache) capability(model string) *ModelCapabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entry == nil {
		return nil
	}
	for index := range c.entry.catalog.Capabilities {
		if c.entry.catalog.Capabilities[index].ID == model {
			result := c.entry.catalog.Capabilities[index]
			return &result
		}
	}
	return nil
}

func (c *ModelsCache) getOrFetch(ctx context.Context, state *AppState, auth AuthTokens) (modelCatalog, error) {
	c.mu.RLock()
	if c.entry != nil && time.Since(c.entry.fetchedAt) < modelsCacheTTL {
		catalog := c.entry.catalog
		c.mu.RUnlock()
		return catalog, nil
	}
	c.mu.RUnlock()
	state.Metrics.ModelDiscoveryRefreshTotal.Add(1)
	catalog, err := fetchModelCatalog(ctx, state, auth)
	if err == nil {
		c.mu.Lock()
		c.entry = &cacheEntry{catalog: catalog, fetchedAt: time.Now()}
		c.mu.Unlock()
		return catalog, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entry != nil {
		logWarn("model discovery failed; serving stale cache: %v", err)
		return c.entry.catalog, nil
	}
	return modelCatalog{}, err
}

type upstreamModel struct {
	Slug                       string   `json:"slug"`
	Name                       string   `json:"name"`
	DisplayName                string   `json:"display_name"`
	Title                      string   `json:"title"`
	SupportedInAPI             *bool    `json:"supported_in_api"`
	Hidden                     *bool    `json:"hidden"`
	Visibility                 string   `json:"visibility"`
	ContextWindow              *uint64  `json:"context_window"`
	MaxContextWindow           *uint64  `json:"max_context_window"`
	MaxOutputTokens            *uint64  `json:"max_output_tokens"`
	SupportedReasoningLevels   []any    `json:"supported_reasoning_levels"`
	SupportedReasoningEfforts  []any    `json:"supported_reasoning_efforts"`
	DefaultReasoningLevel      string   `json:"default_reasoning_level"`
	DefaultReasoningEffort     string   `json:"default_reasoning_effort"`
	SupportsTools              *bool    `json:"supports_tools"`
	SupportsParallelToolCalls  *bool    `json:"supports_parallel_tool_calls"`
	SupportsParallelTools      *bool    `json:"supports_parallel_tools"`
	InputModalities            []string `json:"input_modalities"`
	SupportsSearchTool         *bool    `json:"supports_search_tool"`
	SupportsWebSearch          *bool    `json:"supports_web_search"`
	ExperimentalSupportedTools []string `json:"experimental_supported_tools"`
	SupportsPromptCache        *bool    `json:"supports_prompt_cache"`
	SupportsResponsesWS        *bool    `json:"supports_responses_ws"`
	SupportsWebsocket          *bool    `json:"supports_websocket"`
	SupportsWebsockets         *bool    `json:"supports_websockets"`
}

func fetchModelCatalog(ctx context.Context, state *AppState, auth AuthTokens) (modelCatalog, error) {
	version := state.ClientVersion()
	endpoint := upstreamBase + "/models?client_version=" + version
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return modelCatalog{}, err
	}
	request.Header = buildAuthHeaders(auth, state.CodexUserAgent(), version)
	response, err := state.HTTP.Do(request)
	if err != nil {
		return modelCatalog{}, fmt.Errorf("upstream request failed: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		if refreshed, refreshErr := refreshExistingToken(auth, state.Config); refreshErr == nil {
			state.Metrics.AuthRefreshTotal.Add(1)
			request, _ = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			request.Header = buildAuthHeaders(refreshed, state.CodexUserAgent(), version)
			response, err = state.HTTP.Do(request)
			if err != nil {
				return modelCatalog{}, fmt.Errorf("upstream retry failed: %w", err)
			}
		}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return modelCatalog{}, fmt.Errorf("upstream returned %s", response.Status)
	}
	var payload struct {
		Models []upstreamModel `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return modelCatalog{}, fmt.Errorf("failed to parse upstream models response: %w", err)
	}
	seen := make(map[string]bool)
	catalog := modelCatalog{Models: make([]OpenAIModel, 0, len(payload.Models)), Capabilities: make([]ModelCapabilities, 0, len(payload.Models))}
	for _, model := range payload.Models {
		if strings.TrimSpace(model.Slug) == "" || (model.SupportedInAPI != nil && !*model.SupportedInAPI) || (model.Hidden != nil && *model.Hidden) || strings.EqualFold(model.Visibility, "hide") || strings.EqualFold(model.Visibility, "hidden") || seen[model.Slug] {
			continue
		}
		seen[model.Slug] = true
		reasoningValues := model.SupportedReasoningLevels
		if len(reasoningValues) == 0 {
			reasoningValues = model.SupportedReasoningEfforts
		}
		reasoningEfforts := parseReasoningEfforts(reasoningValues)
		var supportsImages *bool
		if len(model.InputModalities) > 0 {
			value := false
			for _, modality := range model.InputModalities {
				if strings.EqualFold(modality, "image") {
					value = true
				}
			}
			supportsImages = &value
		}
		var supportsImageGeneration *bool
		if len(model.ExperimentalSupportedTools) > 0 {
			value := false
			for _, tool := range model.ExperimentalSupportedTools {
				if tool == "image_generation" {
					value = true
				}
			}
			supportsImageGeneration = &value
		}
		contextWindow := model.ContextWindow
		if contextWindow == nil {
			contextWindow = model.MaxContextWindow
		}
		defaultEffort := model.DefaultReasoningLevel
		if defaultEffort == "" {
			defaultEffort = model.DefaultReasoningEffort
		}
		var reasoning *bool
		if len(reasoningEfforts) > 0 || defaultEffort != "" {
			value := true
			reasoning = &value
		}
		parallel := model.SupportsParallelToolCalls
		if parallel == nil {
			parallel = model.SupportsParallelTools
		}
		search := model.SupportsSearchTool
		if search == nil {
			search = model.SupportsWebSearch
		}
		ws := model.SupportsResponsesWS
		if ws == nil {
			ws = model.SupportsWebsocket
		}
		if ws == nil {
			ws = model.SupportsWebsockets
		}
		var effortList []string
		if len(reasoningEfforts) > 0 {
			effortList = reasoningEfforts
		}
		catalog.Capabilities = append(catalog.Capabilities, ModelCapabilities{ID: model.Slug, ContextWindow: contextWindow, MaxOutputTokens: model.MaxOutputTokens, Reasoning: reasoning, ReasoningEfforts: effortList, DefaultReasoningEffort: defaultEffort, SupportsTools: model.SupportsTools, SupportsParallelTools: parallel, SupportsImages: supportsImages, SupportsWebSearch: search, SupportsImageGeneration: supportsImageGeneration, SupportsPromptCache: model.SupportsPromptCache, SupportsResponsesWS: ws})
		catalog.Models = append(catalog.Models, OpenAIModel{
			ID:                     model.Slug,
			Object:                 "model",
			Created:                0,
			OwnedBy:                "openai",
			Name:                   modelDisplayName(model),
			ContextWindow:          contextWindow,
			DefaultMaxTokens:       model.MaxOutputTokens,
			CanReason:              reasoning != nil && *reasoning,
			ReasoningLevels:        effortList,
			DefaultReasoningEffort: defaultEffort,
			SupportsAttachments:    supportsImages,
		})
	}
	return catalog, nil
}

func modelDisplayName(model upstreamModel) string {
	for _, value := range []string{model.Name, model.DisplayName, model.Title, model.Slug} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parseReasoningEfforts(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
			continue
		}
		if object, ok := value.(map[string]any); ok {
			if text, ok := object["effort"].(string); ok {
				result = append(result, text)
			} else if text, ok := object["reasoning_effort"].(string); ok {
				result = append(result, text)
			}
		}
	}
	return result
}

func catalogForRequest(ctx context.Context, state *AppState) (modelCatalog, error) {
	accounts, err := loadAndRefreshAuthCandidates(state.Config)
	if err != nil {
		return modelCatalog{}, newUpstreamError(http.StatusUnauthorized, err.Error(), "authentication_error", "")
	}
	var lastErr error
	for _, account := range accounts {
		catalog, fetchErr := state.ModelsCache.getOrFetch(ctx, state, account)
		if fetchErr == nil {
			return catalog, nil
		}
		logWarn("models fetch failed for account %s: %v", account.accountAlias(), fetchErr)
		lastErr = fetchErr
	}
	if lastErr == nil {
		lastErr = errors.New("no configured auth accounts are usable")
	}
	return modelCatalog{}, newUpstreamError(http.StatusBadGateway, lastErr.Error(), "upstream_error", "")
}

func buildAuthHeaders(tokens AuthTokens, userAgent, clientVersion string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+tokens.AccessToken)
	if tokens.AccountID != nil {
		headers.Set("ChatGPT-Account-ID", *tokens.AccountID)
	}
	if tokens.ChatGPTAccountIsFedramp {
		headers.Set("X-OpenAI-Fedramp", "true")
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("version", clientVersion)
	headers.Set("User-Agent", userAgent)
	headers.Set("originator", originator)
	return headers
}

func copyCodexPassthroughHeaders(source, destination http.Header) {
	for _, key := range []string{"openai-beta", "openai-organization", "openai-project", "x-request-id", "x-client-request-id", "session_id", "thread_id", "x-openai-subagent"} {
		if value := source.Values(key); len(value) > 0 {
			destination.Del(key)
			for _, item := range value {
				destination.Add(key, item)
			}
		}
	}
}

func handleModels(c *gin.Context, state *AppState) {
	catalog, err := catalogForRequest(c.Request.Context(), state)
	if err != nil {
		writeUpstreamError(c, err.(*UpstreamError))
		return
	}
	c.JSON(http.StatusOK, modelList{Object: "list", Data: catalog.Models})
}

func handleCapabilities(c *gin.Context, state *AppState) {
	catalog, err := catalogForRequest(c.Request.Context(), state)
	if err != nil {
		writeUpstreamError(c, err.(*UpstreamError))
		return
	}
	c.JSON(http.StatusOK, map[string]any{"object": "list", "data": catalog.Capabilities})
}

func handleModelCapabilities(c *gin.Context, state *AppState) {
	catalog, err := catalogForRequest(c.Request.Context(), state)
	if err != nil {
		writeUpstreamError(c, err.(*UpstreamError))
		return
	}
	model := c.Param("model")
	for _, capability := range catalog.Capabilities {
		if capability.ID == model {
			c.JSON(http.StatusOK, capability)
			return
		}
	}
	writeJSONError(c, http.StatusNotFound, "upstream_error", "model not found", "")
}
