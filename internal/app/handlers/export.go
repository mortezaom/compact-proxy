package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/mortezaom/compact-proxy/internal/app/core"
)

type AppState = core.AppState
type CacheKeySource = core.CacheKeySource
type Metrics = core.Metrics
type UpstreamError = core.UpstreamError

var (
	decodeJSON             = core.DecodeJSON
	readAll                = core.ReadAll
	logDebug               = core.LogDebug
	logInfo                = core.LogInfo
	logWarn                = core.LogWarn
	writeJSONError         = core.WriteJSONError
	writeUpstreamError     = core.WriteUpstreamError
	invalidRequest         = core.InvalidRequest
	hexEncode              = core.HexEncode
	defaultReasoningEffort = core.DefaultReasoningEffort
	reasoningEfforts       = core.ReasoningEfforts
	sendJSON               = core.SendJSON
	streamSSEBody          = core.StreamSSEBody
	sseData                = core.SSEData
	injectPromptCacheKey   = core.InjectPromptCacheKey
	promptCacheKeyLogID    = core.PromptCacheKeyLogID
)

type streamGuard struct {
	inner *core.StreamGuard
}

func newStreamGuard(metrics *Metrics) *streamGuard {
	return &streamGuard{inner: core.NewStreamGuard(metrics)}
}

func (g *streamGuard) complete() { g.inner.Complete() }
func (g *streamGuard) close()    { g.inner.Close() }

func requestLogID(c *gin.Context) string {
	if c == nil {
		return "unknown"
	}
	if value := c.GetHeader("x-request-id"); value != "" {
		return value
	}
	return "unknown"
}

func logRequestDebug(c *gin.Context, format string, args ...any) {
	logDebug("request_id=%s "+format, append([]any{requestLogID(c)}, args...)...)
}

func logRequestInfo(c *gin.Context, format string, args ...any) {
	logInfo("request_id=%s "+format, append([]any{requestLogID(c)}, args...)...)
}

func logRequestWarn(c *gin.Context, format string, args ...any) {
	logWarn("request_id=%s "+format, append([]any{requestLogID(c)}, args...)...)
}

func HandleResponses(c *gin.Context, state *AppState) { handleResponses(c, state) }
func HandleCompact(c *gin.Context, state *AppState)   { handleCompact(c, state) }
func HandleChatCompletions(c *gin.Context, state *AppState) {
	handleChatCompletions(c, state)
}
func HandleImagesGenerations(c *gin.Context, state *AppState) { handleImagesGenerations(c, state) }
func HandleImagesEdits(c *gin.Context, state *AppState)       { handleImagesEdits(c, state) }
func HandleMessages(c *gin.Context, state *AppState)          { handleMessages(c, state) }
func HandleUsage(c *gin.Context, state *AppState)             { handleUsage(c, state) }
func HandleResponsesWebSocket(c *gin.Context, state *AppState) {
	handleResponsesWebSocket(c, state)
}
