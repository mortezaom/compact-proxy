package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDiscoverCrushModelsReadsProxyModelList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer proxy-key" {
			t.Fatalf("model discovery request = %s %q", request.URL.String(), request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"object":"list","data":[{"id":"gpt-5.6-sol","name":"GPT-5.6-Sol","context_window":272000,"can_reason":true,"reasoning_levels":["low","medium","high","xhigh","max","ultra"],"default_reasoning_effort":"low","supports_attachments":true}]}`)
	}))
	defer server.Close()

	models, err := discoverCrushModels(server.URL+"/v1/", "proxy-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-sol" || models[0].ContextWindow == nil || *models[0].ContextWindow != 272000 {
		t.Fatalf("discovered models = %#v", models)
	}
	if !models[0].CanReason || !reflect.DeepEqual(models[0].ReasoningLevels, []string{"low", "medium", "high", "xhigh", "max", "ultra"}) {
		t.Fatalf("discovered reasoning metadata = %#v", models[0])
	}
}

func TestMarshalCrushConfigIncludesExplicitModelCapabilities(t *testing.T) {
	contextWindow := uint64(272000)
	attachments := true
	models := []crushDiscoveredModel{
		{
			ID:                     "gpt-5.6-sol",
			Name:                   "GPT-5.6-Sol",
			ContextWindow:          &contextWindow,
			CanReason:              true,
			ReasoningLevels:        []string{"low", "medium", "high", "xhigh", "max", "ultra"},
			DefaultReasoningEffort: "low",
			SupportsAttachments:    &attachments,
		},
	}

	data, err := marshalCrushConfig("http://127.0.0.1:8080/v1/", "proxy-key", models)
	if err != nil {
		t.Fatal(err)
	}
	var config crushConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	provider, ok := config.Providers[crushModelProviderID]
	if !ok {
		t.Fatalf("providers = %#v, want %q", config.Providers, crushModelProviderID)
	}
	if config.Schema != crushSchemaURL || provider.BaseURL != "http://127.0.0.1:8080/v1" || provider.APIKey != "proxy-key" || !provider.DiscoverModels {
		t.Fatalf("config/provider metadata = %#v / %#v", config, provider)
	}
	if len(provider.Models) != 1 {
		t.Fatalf("provider models = %d, want 1", len(provider.Models))
	}

	model := provider.Models[0]
	if model.ID != "gpt-5.6-sol" || model.Name != "GPT-5.6-Sol" || model.ContextWindow == nil || *model.ContextWindow != 272000 {
		t.Fatalf("model identity/context = %#v", model)
	}
	if model.DefaultMaxTokens != defaultCrushMaxTokens || !model.CanReason || model.DefaultReasoningEffort != "low" || !model.SupportsAttachments {
		t.Fatalf("model capability metadata = %#v", model)
	}
	if !reflect.DeepEqual(model.ReasoningLevels, models[0].ReasoningLevels) {
		t.Fatalf("reasoning levels = %#v, want %#v", model.ReasoningLevels, models[0].ReasoningLevels)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["providers"]; !ok {
		t.Fatalf("serialized Crush config omitted providers: %s", data)
	}
}

func TestMarshalCrushrcModelsIncludesSupportedScalarMetadata(t *testing.T) {
	contextWindow := uint64(272000)
	attachments := true
	output := marshalCrushrcModels([]crushDiscoveredModel{{
		ID:                     "gpt-5.6-sol",
		Name:                   "GPT-5.6-Sol",
		ContextWindow:          &contextWindow,
		DefaultReasoningEffort: "low",
		ReasoningLevels:        []string{"low", "medium", "high", "xhigh"},
		SupportsAttachments:    &attachments,
	}})
	want := "model add 'codex-proxy/gpt-5.6-sol' --name 'GPT-5.6-Sol' --default-max-tokens 128000 --can-reason true --supports-images true --context-window 272000 --reasoning-effort low"
	if output != want {
		t.Fatalf("crushrc model commands = %q, want %q", output, want)
	}
}
