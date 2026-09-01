package app

import (
	"github.com/mortezaom/compact-proxy/internal/app/core"
	"github.com/mortezaom/compact-proxy/internal/app/handlers"
)

type AppState = core.AppState
type Config = core.Config

var (
	loginFlow                = core.LoginFlow
	deviceLoginFlow          = core.DeviceLoginFlow
	loadConfig               = core.LoadConfig
	loadAuthToken            = core.LoadAuthToken
	revokeToken              = core.RevokeToken
	deleteAuthToken          = core.DeleteAuthToken
	logInfo                  = core.LogInfo
	logWarn                  = core.LogWarn
	makeState                = core.MakeState
	spawnVersionRefresher    = core.SpawnVersionRefresher
	getProxyAPIKey           = core.GetProxyAPIKey
	requestID                = core.RequestID
	constantTimeEqual        = core.ConstantTimeEqual
	writeJSONError           = core.WriteJSONError
	loadAllAuthTokens        = core.LoadAllAuthTokens
	handleUsage              = handlers.HandleUsage
	handleModels             = core.HandleModels
	handleCapabilities       = core.HandleCapabilities
	handleModelCapabilities  = core.HandleModelCapabilities
	handleResponses          = handlers.HandleResponses
	handleResponsesWebSocket = handlers.HandleResponsesWebSocket
	handleCompact            = handlers.HandleCompact
	handleChatCompletions    = handlers.HandleChatCompletions
	handleImagesGenerations  = handlers.HandleImagesGenerations
	handleImagesEdits        = handlers.HandleImagesEdits
	handleMessages           = handlers.HandleMessages
)
