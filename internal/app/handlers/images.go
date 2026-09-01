package handlers

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	defaultImagesMainModel = "gpt-5.4-mini"
	defaultImagesToolModel = "gpt-image-2"
)

type ImagesGenerationsRequest struct {
	Prompt            string  `json:"prompt"`
	Model             *string `json:"model,omitempty"`
	N                 *uint64 `json:"n,omitempty"`
	Size              *string `json:"size,omitempty"`
	Quality           *string `json:"quality,omitempty"`
	Background        *string `json:"background,omitempty"`
	OutputFormat      *string `json:"output_format,omitempty"`
	OutputCompression *uint64 `json:"output_compression,omitempty"`
	ResponseFormat    *string `json:"response_format,omitempty"`
	Stream            *bool   `json:"stream,omitempty"`
	PartialImages     *uint64 `json:"partial_images,omitempty"`
	Moderation        *string `json:"moderation,omitempty"`
}
type ImagesEditsJSONRequest struct {
	Prompt            string       `json:"prompt"`
	Images            []InputImage `json:"images"`
	Model             *string      `json:"model,omitempty"`
	Size              *string      `json:"size,omitempty"`
	Quality           *string      `json:"quality,omitempty"`
	Background        *string      `json:"background,omitempty"`
	OutputFormat      *string      `json:"output_format,omitempty"`
	OutputCompression *uint64      `json:"output_compression,omitempty"`
	InputFidelity     *string      `json:"input_fidelity,omitempty"`
	ResponseFormat    *string      `json:"response_format,omitempty"`
	Stream            *bool        `json:"stream,omitempty"`
	PartialImages     *uint64      `json:"partial_images,omitempty"`
	Moderation        *string      `json:"moderation,omitempty"`
	Mask              *MaskInput   `json:"mask,omitempty"`
}
type InputImage struct {
	ImageURL string `json:"image_url"`
}
type MaskInput struct {
	ImageURL *string `json:"image_url,omitempty"`
}

type imageToolParams struct {
	Size, Quality, Background, OutputFormat *string
	OutputCompression, PartialImages        *uint64
	InputFidelity, Moderation, MaskImageURL *string
}
type imageCallResult struct{ Result, RevisedPrompt, OutputFormat, Size, Background, Quality string }

func resolveImageModel(value *string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return strings.TrimSpace(*value)
	}
	return defaultImagesToolModel
}
func resolveMainImageModel(imageModel string) string {
	if index := strings.LastIndex(imageModel, "/"); index >= 0 && strings.TrimSpace(imageModel[:index]) != "" {
		return strings.TrimSpace(imageModel[:index]) + "/" + defaultImagesMainModel
	}
	return defaultImagesMainModel
}
func resolveImageResponseFormat(value *string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return strings.ToLower(strings.TrimSpace(*value))
	}
	return "b64_json"
}

func buildImageTool(params imageToolParams) map[string]any {
	format := "png"
	if params.OutputFormat != nil {
		format = *params.OutputFormat
	}
	tool := map[string]any{"type": "image_generation", "output_format": format}
	if params.Size != nil {
		tool["size"] = *params.Size
	}
	if params.Quality != nil {
		tool["quality"] = *params.Quality
	}
	if params.Background != nil {
		tool["background"] = *params.Background
	}
	if params.OutputCompression != nil {
		tool["output_compression"] = *params.OutputCompression
	}
	if params.PartialImages != nil {
		tool["partial_images"] = *params.PartialImages
	}
	if params.InputFidelity != nil {
		tool["input_fidelity"] = *params.InputFidelity
	}
	if params.Moderation != nil {
		tool["moderation"] = *params.Moderation
	}
	if params.MaskImageURL != nil {
		tool["input_image_mask"] = map[string]any{"image_url": *params.MaskImageURL}
	}
	return tool
}

func buildImageResponsesRequest(prompt string, images []string, tool map[string]any, model string) map[string]any {
	content := []any{map[string]any{"type": "input_text", "text": prompt}}
	for _, image := range images {
		if strings.TrimSpace(image) != "" {
			content = append(content, map[string]any{"type": "input_image", "image_url": image})
		}
	}
	return map[string]any{"model": model, "instructions": "", "input": []any{map[string]any{"type": "message", "role": "user", "content": content}}, "tools": []any{tool}, "tool_choice": map[string]any{"type": "image_generation"}, "parallel_tool_calls": true, "include": []string{"reasoning.encrypted_content"}, "reasoning": map[string]any{"effort": "medium", "summary": "auto"}, "stream": true, "store": false}
}

func handleImagesGenerations(c *gin.Context, state *AppState) {
	var request ImagesGenerationsRequest
	if err := decodeJSON(c.Request.Body, &request); err != nil {
		imageError(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		imageError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	imageModel := resolveImageModel(request.Model)
	responseFormat := resolveImageResponseFormat(request.ResponseFormat)
	stream := request.Stream != nil && *request.Stream
	params := imageToolParams{Size: request.Size, Quality: request.Quality, Background: request.Background, OutputFormat: request.OutputFormat, OutputCompression: request.OutputCompression, PartialImages: request.PartialImages, Moderation: request.Moderation}
	upstream, err := sendImageRequest(c, state, buildImageResponsesRequest(prompt, nil, buildImageTool(params), resolveMainImageModel(imageModel)))
	if err != nil {
		writeUpstreamError(c, err)
		return
	}
	if stream {
		streamImages(c, upstream, responseFormat, "image_generation", state.Metrics)
	} else {
		collectImages(c, upstream, responseFormat)
	}
}

func handleImagesEdits(c *gin.Context, state *AppState) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 10*1024*1024+1))
	if err != nil {
		imageError(c, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}
	if len(body) > 10*1024*1024 {
		imageError(c, http.StatusBadRequest, "request body is too large")
		return
	}
	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		var request ImagesEditsJSONRequest
		if err := json.Unmarshal(body, &request); err != nil {
			imageError(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		handleImagesEditsJSON(c, state, request)
		return
	}
	handleImagesEditsMultipart(c, state, body, contentType)
}

func handleImagesEditsJSON(c *gin.Context, state *AppState, request ImagesEditsJSONRequest) {
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		imageError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	var images []string
	for _, image := range request.Images {
		if value := strings.TrimSpace(image.ImageURL); value != "" {
			images = append(images, value)
		}
	}
	if len(images) == 0 {
		imageError(c, http.StatusBadRequest, "images[].image_url is required")
		return
	}
	mask := (*string)(nil)
	if request.Mask != nil && request.Mask.ImageURL != nil && strings.TrimSpace(*request.Mask.ImageURL) != "" {
		value := strings.TrimSpace(*request.Mask.ImageURL)
		mask = &value
	}
	params := imageToolParams{Size: request.Size, Quality: request.Quality, Background: request.Background, OutputFormat: request.OutputFormat, OutputCompression: request.OutputCompression, PartialImages: request.PartialImages, InputFidelity: request.InputFidelity, Moderation: request.Moderation, MaskImageURL: mask}
	imageModel := resolveImageModel(request.Model)
	upstream, err := sendImageRequest(c, state, buildImageResponsesRequest(prompt, images, buildImageTool(params), resolveMainImageModel(imageModel)))
	if err != nil {
		writeUpstreamError(c, err)
		return
	}
	if request.Stream != nil && *request.Stream {
		streamImages(c, upstream, resolveImageResponseFormat(request.ResponseFormat), "image_edit", state.Metrics)
	} else {
		collectImages(c, upstream, resolveImageResponseFormat(request.ResponseFormat))
	}
}

func handleImagesEditsMultipart(c *gin.Context, state *AppState, body []byte, contentType string) {
	_, mediaParams, err := mime.ParseMediaType(contentType)
	if err != nil || mediaParams["boundary"] == "" {
		imageError(c, http.StatusBadRequest, "invalid Content-Type: missing multipart boundary")
		return
	}
	reader := multipart.NewReader(strings.NewReader(string(body)), mediaParams["boundary"])
	fields := make(map[string]string)
	for {
		part, readErr := reader.NextPart()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			imageError(c, http.StatusBadRequest, "failed to parse multipart: "+readErr.Error())
			return
		}
		data, _ := io.ReadAll(io.LimitReader(part, 10*1024*1024+1))
		if len(data) > 10*1024*1024 {
			imageError(c, http.StatusBadRequest, "multipart field is too large")
			return
		}
		if part.FileName() != "" {
			mediaType := part.Header.Get("Content-Type")
			if mediaType == "" {
				mediaType = "application/octet-stream"
			}
			fields[part.FormName()] = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
		} else {
			fields[part.FormName()] = string(data)
		}
	}
	prompt := strings.TrimSpace(fields["prompt"])
	if prompt == "" {
		imageError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	var images []string
	if image := fields["image[]"]; image != "" {
		images = append(images, image)
	}
	if image := fields["image"]; image != "" {
		images = append(images, image)
	}
	if len(images) == 0 {
		imageError(c, http.StatusBadRequest, "image is required")
		return
	}
	imageModel := resolveImageModel(pointerIfNonEmpty(fields["model"]))
	responseFormat := resolveImageResponseFormat(pointerIfNonEmpty(fields["response_format"]))
	params := imageToolParams{Size: pointerIfNonEmpty(fields["size"]), Quality: pointerIfNonEmpty(fields["quality"]), Background: pointerIfNonEmpty(fields["background"]), OutputFormat: pointerIfNonEmpty(fields["output_format"]), OutputCompression: parseUintPointer(fields["output_compression"]), PartialImages: parseUintPointer(fields["partial_images"]), InputFidelity: pointerIfNonEmpty(fields["input_fidelity"]), Moderation: pointerIfNonEmpty(fields["moderation"]), MaskImageURL: pointerIfNonEmpty(fields["mask"])}
	upstream, upstreamErr := sendImageRequest(c, state, buildImageResponsesRequest(prompt, images, buildImageTool(params), resolveMainImageModel(imageModel)))
	if upstreamErr != nil {
		writeUpstreamError(c, upstreamErr)
		return
	}
	if strings.EqualFold(fields["stream"], "true") {
		streamImages(c, upstream, responseFormat, "image_edit", state.Metrics)
	} else {
		collectImages(c, upstream, responseFormat)
	}
}

func pointerIfNonEmpty(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	value = strings.TrimSpace(value)
	return &value
}
func parseUintPointer(value string) *uint64 {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func sendImageRequest(c *gin.Context, state *AppState, body map[string]any) (*http.Response, *UpstreamError) {
	injectPromptCacheKey(c.Request.Header, body)
	return sendJSON(c.Request.Context(), state, c.Request.Header, "responses", body, true)
}

func imageResultFromItem(item map[string]any) (imageCallResult, bool) {
	result := stringFromAny(item["result"])
	if strings.TrimSpace(result) == "" {
		return imageCallResult{}, false
	}
	return imageCallResult{Result: result, RevisedPrompt: stringFromAny(item["revised_prompt"]), OutputFormat: stringFromAny(item["output_format"]), Size: stringFromAny(item["size"]), Background: stringFromAny(item["background"]), Quality: stringFromAny(item["quality"])}, true
}
func imageResultsFromCompleted(event map[string]any) []imageCallResult {
	response, _ := event["response"].(map[string]any)
	output, _ := response["output"].([]any)
	var result []imageCallResult
	for _, raw := range output {
		if item, ok := raw.(map[string]any); ok && item["type"] == "image_generation_call" {
			if image, ok := imageResultFromItem(item); ok {
				result = append(result, image)
			}
		}
	}
	return result
}
func imageUsageFromEvent(event map[string]any) any {
	response, _ := event["response"].(map[string]any)
	if usage, ok := response["tool_usage"].(map[string]any); ok {
		return usage["image_gen"]
	}
	return nil
}

func collectImages(c *gin.Context, upstream *http.Response, responseFormat string) {
	defer upstream.Body.Close()
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		body, _ := io.ReadAll(upstream.Body)
		imageError(c, upstream.StatusCode, string(body))
		return
	}
	raw, err := io.ReadAll(upstream.Body)
	if err != nil {
		imageError(c, http.StatusBadGateway, "failed to read upstream: "+err.Error())
		return
	}
	results := []imageCallResult{}
	created := nowEpoch()
	var usage any
	for _, data := range dataLines(string(raw)) {
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		switch event["type"] {
		case "response.completed":
			if response, ok := event["response"].(map[string]any); ok {
				if value, ok := response["created_at"].(float64); ok {
					created = uint64(value)
				}
			}
			if completed := imageResultsFromCompleted(event); len(completed) > 0 {
				results = completed
			}
			usage = imageUsageFromEvent(event)
		case "response.output_item.done":
			if item, ok := event["item"].(map[string]any); ok && item["type"] == "image_generation_call" {
				if image, ok := imageResultFromItem(item); ok {
					results = append(results, image)
				}
			}
		}
	}
	if len(results) == 0 {
		imageError(c, http.StatusBadGateway, "upstream did not return image output")
		return
	}
	c.JSON(http.StatusOK, buildImagesResponse(results, responseFormat, created, usage))
}

func mimeTypeForImage(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
func buildImagesResponse(results []imageCallResult, responseFormat string, created uint64, usage any) map[string]any {
	response := map[string]any{"created": created, "data": []any{}}
	data := make([]any, 0, len(results))
	for _, image := range results {
		item := map[string]any{}
		if responseFormat == "url" {
			item["url"] = "data:" + mimeTypeForImage(image.OutputFormat) + ";base64," + image.Result
		} else {
			item["b64_json"] = image.Result
		}
		if image.RevisedPrompt != "" {
			item["revised_prompt"] = image.RevisedPrompt
		}
		data = append(data, item)
	}
	response["data"] = data
	if len(results) > 0 {
		first := results[0]
		if first.Background != "" {
			response["background"] = first.Background
		}
		if first.OutputFormat != "" {
			response["output_format"] = first.OutputFormat
		}
		if first.Quality != "" {
			response["quality"] = first.Quality
		}
		if first.Size != "" {
			response["size"] = first.Size
		}
	}
	if usage != nil {
		response["usage"] = usage
	}
	return response
}

func streamImages(c *gin.Context, upstream *http.Response, responseFormat, prefix string, metrics *Metrics) {
	defer upstream.Body.Close()
	guard := newStreamGuard(metrics)
	defer guard.close()
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	flusher, _ := c.Writer.(http.Flusher)
	write := func(value string) error {
		if _, err := c.Writer.Write([]byte(value)); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	err := streamSSEBody(upstream.Body, func(frame string) error {
		var output strings.Builder
		for _, data := range sseData(frame) {
			var event map[string]any
			if json.Unmarshal([]byte(data), &event) != nil {
				continue
			}
			switch event["type"] {
			case "response.image_generation_call.partial_image":
				b64 := stringFromAny(event["partial_image_b64"])
				if b64 == "" {
					continue
				}
				format := stringFromAny(event["output_format"])
				index := uint64FromAny(event["partial_image_index"])
				name := prefix + ".partial_image"
				payload := map[string]any{"type": name, "partial_image_index": index}
				if responseFormat == "url" {
					payload["url"] = "data:" + mimeTypeForImage(format) + ";base64," + b64
				} else {
					payload["b64_json"] = b64
				}
				output.WriteString("event: " + name + "\ndata: " + mustJSON(payload) + "\n\n")
			case "response.output_item.done":
				item, _ := event["item"].(map[string]any)
				if item["type"] != "image_generation_call" {
					continue
				}
				image, ok := imageResultFromItem(item)
				if !ok {
					continue
				}
				name := prefix + ".completed"
				payload := map[string]any{"type": name}
				if responseFormat == "url" {
					payload["url"] = "data:" + mimeTypeForImage(image.OutputFormat) + ";base64," + image.Result
				} else {
					payload["b64_json"] = image.Result
				}
				if image.RevisedPrompt != "" {
					payload["revised_prompt"] = image.RevisedPrompt
				}
				output.WriteString("event: " + name + "\ndata: " + mustJSON(payload) + "\n\n")
			case "response.completed":
				results := imageResultsFromCompleted(event)
				if len(results) == 0 {
					continue
				}
				usage := imageUsageFromEvent(event)
				name := prefix + ".completed"
				for _, image := range results {
					payload := map[string]any{"type": name}
					if responseFormat == "url" {
						payload["url"] = "data:" + mimeTypeForImage(image.OutputFormat) + ";base64," + image.Result
					} else {
						payload["b64_json"] = image.Result
					}
					if image.RevisedPrompt != "" {
						payload["revised_prompt"] = image.RevisedPrompt
					}
					if usage != nil {
						payload["usage"] = usage
					}
					output.WriteString("event: " + name + "\ndata: " + mustJSON(payload) + "\n\n")
				}
			}
		}
		return write(output.String())
	})
	if err != nil {
		_ = write("event: error\ndata: " + mustJSON(map[string]any{"error": map[string]any{"message": err.Error()}}) + "\n\n")
		return
	}
	guard.complete()
}

func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
func imageError(c *gin.Context, status int, message string) {
	c.JSON(status, map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
}
