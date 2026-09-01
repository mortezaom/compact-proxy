package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	crushSchemaURL         = "https://charm.land/crush.json"
	defaultCrushMaxTokens  = uint64(128000)
	crushModelProviderID   = "codex-proxy"
	crushModelProviderName = "Codex (Proxy)"
)

type crushModelsResponse struct {
	Data []crushDiscoveredModel `json:"data"`
}

type crushDiscoveredModel struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	ContextWindow          *uint64  `json:"context_window"`
	DefaultMaxTokens       *uint64  `json:"default_max_tokens"`
	CanReason              bool     `json:"can_reason"`
	ReasoningLevels        []string `json:"reasoning_levels"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort"`
	SupportsAttachments    *bool    `json:"supports_attachments"`
}

type crushConfig struct {
	Schema    string                   `json:"$schema"`
	Providers map[string]crushProvider `json:"providers"`
}

type crushProvider struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Type           string             `json:"type"`
	BaseURL        string             `json:"base_url"`
	APIKey         string             `json:"api_key"`
	DiscoverModels bool               `json:"discover_models"`
	Models         []crushConfigModel `json:"models"`
}

type crushConfigModel struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	CostPer1MIn            float64  `json:"cost_per_1m_in"`
	CostPer1MOut           float64  `json:"cost_per_1m_out"`
	CostPer1MInCached      float64  `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached     float64  `json:"cost_per_1m_out_cached"`
	ContextWindow          *uint64  `json:"context_window,omitempty"`
	DefaultMaxTokens       uint64   `json:"default_max_tokens"`
	CanReason              bool     `json:"can_reason"`
	ReasoningLevels        []string `json:"reasoning_levels"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	SupportsAttachments    bool     `json:"supports_attachments"`
}

func discoverCrushModels(baseURL, apiKey string) ([]crushDiscoveredModel, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("request %s returned %s", endpoint, response.Status)
	}
	var payload crushModelsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode model metadata: %w", err)
	}
	return payload.Data, nil
}

func marshalCrushConfig(baseURL, apiKey string, models []crushDiscoveredModel) ([]byte, error) {
	configModels := make([]crushConfigModel, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		configModels = append(configModels, makeCrushConfigModel(model))
	}
	config := crushConfig{
		Schema: crushSchemaURL,
		Providers: map[string]crushProvider{
			crushModelProviderID: {
				ID:             crushModelProviderID,
				Name:           crushModelProviderName,
				Type:           "openai",
				BaseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
				APIKey:         apiKey,
				DiscoverModels: true,
				Models:         configModels,
			},
		},
	}
	return json.MarshalIndent(config, "", "  ")
}

func marshalCrushrcModels(models []crushDiscoveredModel) string {
	lines := make([]string, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		configModel := makeCrushConfigModel(model)
		parts := []string{
			"model",
			"add",
			shellQuote(crushModelProviderID + "/" + configModel.ID),
			"--name",
			shellQuote(configModel.Name),
			"--default-max-tokens",
			fmt.Sprintf("%d", configModel.DefaultMaxTokens),
			"--can-reason",
			fmt.Sprintf("%t", configModel.CanReason),
			"--supports-images",
			fmt.Sprintf("%t", configModel.SupportsAttachments),
		}
		if configModel.ContextWindow != nil {
			parts = append(parts, "--context-window", fmt.Sprintf("%d", *configModel.ContextWindow))
		}
		if isCrushReasoningEffort(configModel.DefaultReasoningEffort) {
			parts = append(parts, "--reasoning-effort", configModel.DefaultReasoningEffort)
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

func isCrushReasoningEffort(value string) bool {
	switch value {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func makeCrushConfigModel(model crushDiscoveredModel) crushConfigModel {
	name := strings.TrimSpace(model.Name)
	if name == "" {
		name = strings.TrimSpace(model.ID)
	}
	maxTokens := defaultCrushMaxTokens
	if model.DefaultMaxTokens != nil && *model.DefaultMaxTokens > 0 {
		maxTokens = *model.DefaultMaxTokens
	}
	levels := append([]string(nil), model.ReasoningLevels...)
	if levels == nil {
		levels = []string{}
	}
	canReason := model.CanReason || len(levels) > 0 || strings.TrimSpace(model.DefaultReasoningEffort) != ""
	var contextWindow *uint64
	if model.ContextWindow != nil && *model.ContextWindow > 0 {
		value := *model.ContextWindow
		contextWindow = &value
	}
	return crushConfigModel{
		ID:                     strings.TrimSpace(model.ID),
		Name:                   name,
		CostPer1MIn:            0,
		CostPer1MOut:           0,
		CostPer1MInCached:      0,
		CostPer1MOutCached:     0,
		ContextWindow:          contextWindow,
		DefaultMaxTokens:       maxTokens,
		CanReason:              canReason,
		ReasoningLevels:        levels,
		DefaultReasoningEffort: strings.TrimSpace(model.DefaultReasoningEffort),
		SupportsAttachments:    model.SupportsAttachments != nil && *model.SupportsAttachments,
	}
}
