package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type UsageResponse struct {
	Object     string              `json:"object"`
	RateLimits []RateLimitSnapshot `json:"rate_limits"`
}

type RateLimitSnapshot struct {
	LimitID   string           `json:"limit_id"`
	LimitName string           `json:"limit_name,omitempty"`
	Primary   *RateLimitWindow `json:"primary,omitempty"`
	Secondary *RateLimitWindow `json:"secondary,omitempty"`
	Credits   *CreditsSnapshot `json:"credits,omitempty"`
}

type RateLimitWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes *int64  `json:"window_minutes,omitempty"`
	ResetsAt      *int64  `json:"resets_at,omitempty"`
}

type CreditsSnapshot struct {
	HasCredits bool   `json:"has_credits"`
	Unlimited  bool   `json:"unlimited"`
	Balance    string `json:"balance,omitempty"`
}

func handleUsage(c *gin.Context, state *AppState) {
	model := state.Config.UsageModel
	body := map[string]any{"model": model, "instructions": "", "input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}}}, "store": false, "stream": true}
	response, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses?client_version="+state.ClientVersion(), body, true)
	if upstreamErr != nil {
		c.String(upstreamErr.Status, upstreamErr.Message)
		return
	}
	defer response.Body.Close()
	snapshots := parseAllRateLimits(response.Header)
	raw, err := readAll(response.Body)
	if err != nil {
		c.String(http.StatusBadGateway, "failed to read upstream: "+err.Error())
		return
	}
	snapshots = append(snapshots, parseRateLimitEvents(string(raw))...)
	dedupeRateLimits(&snapshots)
	c.JSON(http.StatusOK, UsageResponse{Object: "codex.usage", RateLimits: snapshots})
}

func parseAllRateLimits(headers http.Header) []RateLimitSnapshot {
	snapshots := []RateLimitSnapshot{parseRateLimitForLimit(headers, "")}
	ids := make(map[string]bool)
	for name := range headers {
		if limitID := headerNameToLimitID(strings.ToLower(name)); limitID != "" && limitID != "codex" {
			ids[limitID] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		snapshot := parseRateLimitForLimit(headers, id)
		if hasRateLimitData(snapshot) {
			snapshots = append(snapshots, snapshot)
		}
	}
	return snapshots
}

func parseRateLimitForLimit(headers http.Header, limitID string) RateLimitSnapshot {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(limitID), "_", "-"))
	if normalized == "" {
		normalized = "codex"
	}
	prefix := "x-" + normalized
	return RateLimitSnapshot{LimitID: strings.ReplaceAll(normalized, "-", "_"), LimitName: strings.TrimSpace(headerValue(headers, prefix+"-limit-name")), Primary: parseRateLimitWindow(headers, prefix+"-primary-used-percent", prefix+"-primary-window-minutes", prefix+"-primary-reset-at"), Secondary: parseRateLimitWindow(headers, prefix+"-secondary-used-percent", prefix+"-secondary-window-minutes", prefix+"-secondary-reset-at"), Credits: parseCreditsSnapshot(headers)}
}

func parseRateLimitWindow(headers http.Header, usedName, windowName, resetName string) *RateLimitWindow {
	used, ok := parseHeaderFloat(headers, usedName)
	if !ok {
		return nil
	}
	window, windowOK := parseHeaderInt(headers, windowName)
	reset, resetOK := parseHeaderInt(headers, resetName)
	if used == 0 && (!windowOK || window == 0) && !resetOK {
		return nil
	}
	result := &RateLimitWindow{UsedPercent: used}
	if windowOK {
		result.WindowMinutes = &window
	}
	if resetOK {
		result.ResetsAt = &reset
	}
	return result
}

func parseCreditsSnapshot(headers http.Header) *CreditsSnapshot {
	hasCredits, ok := parseHeaderBool(headers, "x-codex-credits-has-credits")
	if !ok {
		return nil
	}
	unlimited, ok := parseHeaderBool(headers, "x-codex-credits-unlimited")
	if !ok {
		return nil
	}
	return &CreditsSnapshot{HasCredits: hasCredits, Unlimited: unlimited, Balance: strings.TrimSpace(headerValue(headers, "x-codex-credits-balance"))}
}

func parseRateLimitEvents(raw string) []RateLimitSnapshot {
	var result []RateLimitSnapshot
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil || event["type"] != "codex.rate_limits" {
			continue
		}
		result = append(result, parseRateLimitEvent(event))
	}
	return result
}

func parseRateLimitEvent(event map[string]any) RateLimitSnapshot {
	var details map[string]any
	if value, ok := event["rate_limits"].(map[string]any); ok {
		details = value
	}
	var primary, secondary *RateLimitWindow
	if details != nil {
		primary = mapEventWindow(details["primary"])
		secondary = mapEventWindow(details["secondary"])
	}
	var credits *CreditsSnapshot
	if value, ok := event["credits"].(map[string]any); ok {
		has, hasOK := value["has_credits"].(bool)
		unlimited, unlimitedOK := value["unlimited"].(bool)
		if hasOK && unlimitedOK {
			balance, _ := value["balance"].(string)
			credits = &CreditsSnapshot{HasCredits: has, Unlimited: unlimited, Balance: balance}
		}
	}
	limitID := "codex"
	if value, ok := event["metered_limit_name"].(string); ok {
		limitID = normalizeLimitID(value)
	} else if value, ok := event["limit_name"].(string); ok {
		limitID = normalizeLimitID(value)
	}
	return RateLimitSnapshot{LimitID: limitID, Primary: primary, Secondary: secondary, Credits: credits}
}

func mapEventWindow(value any) *RateLimitWindow {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	used, ok := object["used_percent"].(float64)
	if !ok {
		return nil
	}
	result := &RateLimitWindow{UsedPercent: used}
	if value, ok := object["window_minutes"].(float64); ok {
		n := int64(value)
		result.WindowMinutes = &n
	}
	if value, ok := object["reset_at"].(float64); ok {
		n := int64(value)
		result.ResetsAt = &n
	}
	return result
}

func dedupeRateLimits(snapshots *[]RateLimitSnapshot) {
	seen := make(map[string]bool)
	result := (*snapshots)[:0]
	for _, snapshot := range *snapshots {
		if !seen[snapshot.LimitID] {
			seen[snapshot.LimitID] = true
			result = append(result, snapshot)
		}
	}
	*snapshots = result
}

func headerValue(headers http.Header, name string) string {
	return headers.Get(name)
}

func parseHeaderFloat(headers http.Header, name string) (float64, bool) {
	value, err := strconv.ParseFloat(headerValue(headers, name), 64)
	return value, err == nil
}

func parseHeaderInt(headers http.Header, name string) (int64, bool) {
	value, err := strconv.ParseInt(headerValue(headers, name), 10, 64)
	return value, err == nil
}

func parseHeaderBool(headers http.Header, name string) (bool, bool) {
	raw := strings.TrimSpace(headerValue(headers, name))
	switch {
	case strings.EqualFold(raw, "true") || raw == "1":
		return true, true
	case strings.EqualFold(raw, "false") || raw == "0":
		return false, true
	default:
		return false, false
	}
}

func hasRateLimitData(snapshot RateLimitSnapshot) bool {
	return snapshot.Primary != nil || snapshot.Secondary != nil || snapshot.Credits != nil
}

func headerNameToLimitID(name string) string {
	prefix := strings.TrimSuffix(name, "-primary-used-percent")
	if prefix == name || !strings.HasPrefix(prefix, "x-") {
		return ""
	}
	return normalizeLimitID(strings.TrimPrefix(prefix, "x-"))
}

func normalizeLimitID(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
}
