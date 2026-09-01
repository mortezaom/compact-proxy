package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type UpstreamError struct {
	Status    int
	Message   string
	ErrorType string
	Code      string
}

func newUpstreamError(status int, message, errorType, code string) *UpstreamError {
	return &UpstreamError{Status: status, Message: message, ErrorType: errorType, Code: code}
}

func (e *UpstreamError) Error() string { return e.Message }

func (e *UpstreamError) jsonBody() map[string]any {
	var code any
	if e.Code != "" {
		code = e.Code
	}
	return map[string]any{"error": map[string]any{"message": e.Message, "type": e.ErrorType, "code": code}}
}

func sendJSON(ctx context.Context, state *AppState, clientHeaders http.Header, path string, body any, acceptSSE bool) (*http.Response, *UpstreamError) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, newUpstreamError(http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "")
	}
	return sendBytes(ctx, state, clientHeaders, path, data, acceptSSE)
}

func sendBytes(ctx context.Context, state *AppState, clientHeaders http.Header, path string, body []byte, acceptSSE bool) (*http.Response, *UpstreamError) {
	accounts, err := loadAndRefreshAuthCandidates(state.Config)
	if err != nil {
		return nil, newUpstreamError(http.StatusUnauthorized, err.Error(), "authentication_error", "")
	}
	session := sessionKeyFromHeaders(clientHeaders)
	affinityHit := state.AccountAffinity.order(session, accounts)
	if session != nil {
		if affinityHit {
			state.Metrics.CacheAffinityHitsTotal.Add(1)
		} else {
			state.Metrics.CacheAffinityMissesTotal.Add(1)
		}
	}
	endpoint := upstreamBase + "/" + path
	var lastError *UpstreamError
	for index := range accounts {
		account := &accounts[index]
		hasNext := index+1 < len(accounts)
		alias := account.accountAlias()
		response, requestErr := sendOnce(ctx, state, clientHeaders, account, endpoint, body, acceptSSE)
		if requestErr != nil {
			state.Metrics.UpstreamErrorsTotal.Add(1)
			logError("upstream connection failed for account %s: %v", alias, requestErr)
			if hasNext && isConnectionError(requestErr) {
				state.Metrics.AccountFailoversTotal.Add(1)
				continue
			}
			return nil, newUpstreamError(http.StatusBadGateway, "upstream request failed: "+requestErr.Error(), "upstream_error", "")
		}
		if response.StatusCode == http.StatusUnauthorized {
			logInfo("upstream returned 401 for account %s; refreshing once", alias)
			if refreshed, refreshErr := refreshExistingToken(*account, state.Config); refreshErr == nil {
				state.Metrics.AuthRefreshTotal.Add(1)
				response.Body.Close()
				response, requestErr = sendOnce(ctx, state, clientHeaders, &refreshed, endpoint, body, acceptSSE)
				if requestErr != nil {
					return nil, newUpstreamError(http.StatusBadGateway, "upstream retry failed: "+requestErr.Error(), "upstream_error", "")
				}
			} else {
				logWarn("token refresh failed for account %s: %v", alias, refreshErr)
			}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			state.AccountAffinity.remember(session, account)
			logInfo("upstream account selected: account=%s session=%s affinity_hit=%t", alias, sessionLogID(session), affinityHit)
			return response, nil
		}
		state.Metrics.UpstreamErrorsTotal.Add(1)
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		parsed := parseUpstreamError(response.StatusCode, string(raw))
		logWarn("upstream rejected request for account %s: status=%d", alias, response.StatusCode)
		if hasNext && shouldFallbackStatus(response.StatusCode) {
			state.Metrics.AccountFailoversTotal.Add(1)
			lastError = parsed
			continue
		}
		return nil, parsed
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, newUpstreamError(http.StatusUnauthorized, "no configured auth accounts are usable", "authentication_error", "")
}

func sessionLogID(session *SessionKey) string {
	if session == nil {
		return "none"
	}
	return session.logID()
}

func sendOnce(ctx context.Context, state *AppState, clientHeaders http.Header, account *AuthTokens, endpoint string, body []byte, acceptSSE bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header = buildAuthHeaders(*account, state.CodexUserAgent(), state.ClientVersion())
	request.Header.Set("openai-beta", "responses=experimental")
	if acceptSSE {
		request.Header.Set("Accept", "text/event-stream")
	}
	copyCodexPassthroughHeaders(clientHeaders, request.Header)
	return state.HTTP.Do(request)
}

func isConnectionError(err error) bool {
	return err != nil && !strings.Contains(strings.ToLower(err.Error()), "context canceled") && !strings.Contains(strings.ToLower(err.Error()), "invalid")
}

func parseUpstreamError(status int, raw string) *UpstreamError {
	var payload map[string]any
	_ = json.Unmarshal([]byte(raw), &payload)
	var detail map[string]any
	if value, ok := payload["error"].(map[string]any); ok {
		detail = value
	}
	message := ""
	if detail != nil {
		message, _ = detail["message"].(string)
	}
	if message == "" {
		message, _ = payload["detail"].(string)
	}
	if message == "" {
		message = raw
		if message == "" {
			message = "upstream request failed"
		}
	}
	errorType := ""
	if detail != nil {
		errorType, _ = detail["type"].(string)
	}
	if errorType == "" {
		switch {
		case status == http.StatusUnauthorized:
			errorType = "authentication_error"
		case status == http.StatusForbidden:
			errorType = "permission_error"
		case status == http.StatusNotFound:
			errorType = "not_found_error"
		case status == http.StatusTooManyRequests:
			errorType = "rate_limit_error"
		case status >= 500:
			errorType = "upstream_error"
		default:
			errorType = "invalid_request_error"
		}
	}
	code := ""
	if detail != nil {
		code, _ = detail["code"].(string)
	}
	return newUpstreamError(status, message, errorType, code)
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }
