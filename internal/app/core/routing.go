package core

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

type SessionKey struct{ hash string }

func sessionKeyFromHeaders(headers http.Header) *SessionKey {
	value := strings.TrimSpace(headers.Get("x-session-id"))
	if value == "" {
		value = strings.TrimSpace(headers.Get("x-session-affinity"))
	}
	if value == "" {
		return nil
	}
	digest := sha256.Sum256([]byte(value))
	return &SessionKey{hash: hex.EncodeToString(digest[:])}
}

func (s *SessionKey) promptCacheKey() string { return "agent:" + s.hash[:48] }
func (s *SessionKey) logID() string          { return s.hash[:12] }

type CacheKeySource string

const (
	CacheKeyClient  CacheKeySource = "client"
	CacheKeySession CacheKeySource = "session"
	CacheKeyNone    CacheKeySource = "none"
)

func injectPromptCacheKey(headers http.Header, body any) CacheKeySource {
	object, ok := body.(map[string]any)
	if !ok {
		logDebug("prompt cache key decision: request_id=%s source=none reason=non_object_body", requestIDFromHeaders(headers))
		return CacheKeyNone
	}
	if value, exists := object["prompt_cache_key"]; exists {
		logDebug("prompt cache key decision: request_id=%s source=client key_id=%s", requestIDFromHeaders(headers), promptCacheKeyLogID(value))
		return CacheKeyClient
	}
	session := sessionKeyFromHeaders(headers)
	if session == nil {
		logDebug("prompt cache key decision: request_id=%s source=none session=none", requestIDFromHeaders(headers))
		return CacheKeyNone
	}
	cacheKey := session.promptCacheKey()
	object["prompt_cache_key"] = cacheKey
	logDebug("prompt cache key decision: request_id=%s source=session session=%s key_id=%s", requestIDFromHeaders(headers), session.logID(), promptCacheKeyLogID(cacheKey))
	return CacheKeySession
}

func promptCacheKeyLogID(value any) string {
	if value == nil {
		return "none"
	}
	text, ok := value.(string)
	if !ok {
		return "non_string"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "empty"
	}
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:6])
}

type affinityEntry struct {
	accountKey string
	touchedAt  time.Time
}

type AccountAffinity struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
}

func (a *AccountAffinity) order(session *SessionKey, accounts []AuthTokens) bool {
	if session == nil {
		return false
	}
	a.mu.Lock()
	if a.entries == nil {
		a.entries = make(map[string]affinityEntry)
	}
	now := time.Now()
	for key, entry := range a.entries {
		if now.Sub(entry.touchedAt) >= 4*time.Hour {
			delete(a.entries, key)
		}
	}
	entry, ok := a.entries[session.hash]
	if ok {
		entry.touchedAt = now
		a.entries[session.hash] = entry
	}
	a.mu.Unlock()
	if !ok {
		return false
	}
	for index := range accounts {
		if accounts[index].routingKey() == entry.accountKey {
			accounts[0], accounts[index] = accounts[index], accounts[0]
			return true
		}
	}
	logDebug("account affinity entry not usable: session=%s", session.logID())
	return false
}

func (a *AccountAffinity) remember(session *SessionKey, account *AuthTokens) {
	if session == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = make(map[string]affinityEntry)
	}
	now := time.Now()
	for key, entry := range a.entries {
		if now.Sub(entry.touchedAt) >= 4*time.Hour {
			delete(a.entries, key)
		}
	}
	if len(a.entries) >= 4096 {
		if _, exists := a.entries[session.hash]; !exists {
			var oldestKey string
			var oldest time.Time
			for key, entry := range a.entries {
				if oldestKey == "" || entry.touchedAt.Before(oldest) {
					oldestKey, oldest = key, entry.touchedAt
				}
			}
			delete(a.entries, oldestKey)
		}
	}
	a.entries[session.hash] = affinityEntry{accountKey: account.routingKey(), touchedAt: now}
}
