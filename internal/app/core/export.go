package core

var (
	DecodeJSON              = decodeJSON
	ReadAll                 = readAll
	LogDebug                = logDebug
	LogInfo                 = logInfo
	LogWarn                 = logWarn
	SetLogLevel             = setLogLevel
	WithRequestID           = withRequestID
	WriteJSONError          = writeJSONError
	WriteUpstreamError      = writeUpstreamError
	ConstantTimeEqual       = constantTimeEqual
	RequestID               = requestID
	HexEncode               = hexEncode
	InvalidRequest          = invalidRequest
	GetProxyAPIKey          = getProxyAPIKey
	DefaultReasoningEffort  = defaultReasoningEffort
	ReasoningEfforts        = reasoningEfforts
	MakeState               = makeState
	SpawnVersionRefresher   = spawnVersionRefresher
	LoadAuthToken           = loadAuthToken
	LoadAllAuthTokens       = loadAllAuthTokens
	DeleteAuthToken         = deleteAuthToken
	RevokeToken             = revokeToken
	LoginFlow               = loginFlow
	DeviceLoginFlow         = deviceLoginFlow
	SendJSON                = sendJSON
	StreamSSEBody           = streamSSEBody
	SSEData                 = sseData
	InjectPromptCacheKey    = injectPromptCacheKey
	PromptCacheKeyLogID     = promptCacheKeyLogID
	HandleModels            = handleModels
	HandleCapabilities      = handleCapabilities
	HandleModelCapabilities = handleModelCapabilities
)

func NewStreamGuard(metrics *Metrics) *StreamGuard { return newStreamGuard(metrics) }

func (g *StreamGuard) Complete() { g.complete() }
func (g *StreamGuard) Close()    { g.close() }

func (m *Metrics) Render() string { return m.render() }

func (c *ModelsCache) Capability(model string) *ModelCapabilities { return c.capability(model) }

func (t AuthTokens) AccountAlias() string            { return t.accountAlias() }
func (t AuthTokens) IsExpired() bool                 { return t.isExpired() }
func (t AuthTokens) SavePrimary(config Config) error { return t.savePrimary(config) }
