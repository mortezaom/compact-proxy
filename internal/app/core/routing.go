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
		return CacheKeyNone
	}
	if _, exists := object["prompt_cache_key"]; exists {
		return CacheKeyClient
	}
	session := sessionKeyFromHeaders(headers)
	if session == nil {
		return CacheKeyNone
	}
	object["prompt_cache_key"] = session.promptCacheKey()
	return CacheKeySession
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
