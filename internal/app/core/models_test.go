package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestFetchModelCatalogIncludesCrushReasoningMetadata(t *testing.T) {
	state := newAppState(Config{}, "test-version")
	state.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/backend-api/codex/models" || request.URL.Query().Get("client_version") != "test-version" {
			t.Fatalf("models request URL = %s, want Codex models endpoint", request.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body: io.NopCloser(strings.NewReader(`{"models":[{
				"slug":"gpt-5.6-sol",
				"name":"GPT-5.6 Sol",
				"supported_in_api":true,
				"context_window":400000,
				"max_output_tokens":128000,
				"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}],
				"default_reasoning_level":"medium",
				"input_modalities":["text","image"]
			}]}`)),
			Header: make(http.Header),
		}, nil
	})}

	catalog, err := fetchModelCatalog(context.Background(), state, AuthTokens{AccessToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("catalog models = %d, want 1", len(catalog.Models))
	}

	model := catalog.Models[0]
	if model.Name != "GPT-5.6 Sol" || model.ContextWindow == nil || *model.ContextWindow != 400000 || model.DefaultMaxTokens == nil || *model.DefaultMaxTokens != 128000 {
		t.Fatalf("model metadata = %#v", model)
	}
	if !model.CanReason || !reflect.DeepEqual(model.ReasoningLevels, []string{"low", "medium", "high"}) || model.DefaultReasoningEffort != "medium" {
		t.Fatalf("model reasoning metadata = %#v", model)
	}
	if model.SupportsAttachments == nil || !*model.SupportsAttachments {
		t.Fatalf("model attachment metadata = %#v, want true", model.SupportsAttachments)
	}

	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"name":                     "GPT-5.6 Sol",
		"context_window":           float64(400000),
		"default_max_tokens":       float64(128000),
		"can_reason":               true,
		"reasoning_levels":         []any{"low", "medium", "high"},
		"default_reasoning_effort": "medium",
		"supports_attachments":     true,
	} {
		if !reflect.DeepEqual(payload[key], want) {
			t.Errorf("serialized model[%q] = %#v, want %#v", key, payload[key], want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
