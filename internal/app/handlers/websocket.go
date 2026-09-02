package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var websocketUpgrader = websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }, ReadBufferSize: 32 * 1024, WriteBufferSize: 32 * 1024}

type wsMessage struct {
	messageType int
	payload     []byte
	err         error
}
type websocketSession struct{ lastRequest map[string]any }

func handleResponsesWebSocket(c *gin.Context, state *AppState) {
	started := time.Now()
	messageCount := 0
	requestCount := 0
	appendCount := 0
	outcome := "closed"
	connection, err := websocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logRequestWarn(c, "responses WebSocket upgrade failed: error=%v", err)
		return
	}
	logRequestInfo(c, "responses WebSocket connected")
	defer func() {
		logRequestDebug(c, "responses WebSocket closed: outcome=%s messages=%d requests=%d appends=%d duration_ms=%d", outcome, messageCount, requestCount, appendCount, time.Since(started).Milliseconds())
		_ = connection.Close()
	}()
	incoming := make(chan wsMessage, 8)
	go func() {
		defer close(incoming)
		for {
			messageType, payload, readErr := connection.ReadMessage()
			incoming <- wsMessage{messageType: messageType, payload: payload, err: readErr}
			if readErr != nil {
				return
			}
		}
	}()
	session := websocketSession{}
	for message := range incoming {
		messageCount++
		if message.err != nil || message.messageType == websocket.CloseMessage {
			outcome = "client_disconnected"
			if message.err != nil {
				logRequestDebug(c, "responses WebSocket read ended: error=%v", message.err)
			}
			return
		}
		if message.messageType == websocket.PingMessage {
			logRequestDebug(c, "responses WebSocket ping received")
			_ = connection.WriteControl(websocket.PongMessage, message.payload, time.Time{})
			continue
		}
		if message.messageType != websocket.TextMessage {
			outcome = "protocol_error"
			logRequestWarn(c, "responses WebSocket rejected frame: message_type=%d payload_bytes=%d", message.messageType, len(message.payload))
			if sendWSError(connection, "invalid_request_error", "WebSocket requests must be UTF-8 JSON text frames") != nil {
				return
			}
			continue
		}
		request, parseErr := parseWSRequest(message.payload, session.lastRequest)
		if parseErr != nil {
			outcome = "protocol_error"
			logRequestWarn(c, "responses WebSocket request rejected: payload_bytes=%d error=%v", len(message.payload), parseErr)
			if sendWSError(connection, "invalid_request_error", parseErr.Error()) != nil {
				return
			}
			continue
		}
		requestCount++
		messageType := stringFromAny(messageTypeValue(message.payload))
		if messageType == "response.append" {
			appendCount++
		}
		logRequestDebug(c, "responses WebSocket request accepted: message_type=%s payload_bytes=%d fields=%d", messageType, len(message.payload), len(request))
		if value, ok := request["generate"].(bool); ok && !value {
			outcome = "protocol_error"
			logRequestWarn(c, "responses WebSocket request rejected: reason=generate_false")
			if sendWSError(connection, "invalid_request_error", "generate=false is not supported by the HTTP-backed WebSocket bridge") != nil {
				return
			}
			continue
		}
		upstream, upstreamErr := sendJSON(c.Request.Context(), state, c.Request.Header, "responses", request, true)
		if upstreamErr != nil {
			outcome = "upstream_error"
			logRequestWarn(c, "responses WebSocket upstream failed: status=%d type=%s", upstreamErr.Status, upstreamErr.ErrorType)
			if sendWSError(connection, errorCode(upstreamErr), upstreamErr.Message) != nil {
				return
			}
			continue
		}
		session.lastRequest = request
		if err := streamWebSocketEvents(connection, upstream, incoming, state.Metrics, requestLogID(c)); err != nil {
			outcome = "stream_error"
			logRequestWarn(c, "responses WebSocket stream ended with error: error=%v", err)
			return
		}
		outcome = "completed"
	}
}

func messageTypeValue(data []byte) any {
	var message map[string]any
	if json.Unmarshal(data, &message) != nil {
		return "unknown"
	}
	return message["type"]
}

func errorCode(errorValue *UpstreamError) string {
	if errorValue.Code != "" {
		return errorValue.Code
	}
	return "upstream_error"
}

func parseWSRequest(data []byte, previous map[string]any) (map[string]any, error) {
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, fmt.Errorf("message must be valid JSON: %w", err)
	}
	if message == nil {
		return nil, errors.New("message must be a JSON object")
	}
	switch messageType := stringFromAny(message["type"]); messageType {
	case "response.create":
		return responsePayload(message)
	case "response.append":
		if previous == nil {
			return nil, errors.New("response.append requires an earlier response.create")
		}
		patch, err := responsePayload(message)
		if err != nil {
			return nil, err
		}
		merged := cloneObject(previous)
		mergeWSObjects(merged, patch)
		return merged, nil
	case "":
		return nil, errors.New("message is missing `type`")
	default:
		return nil, fmt.Errorf("unsupported WebSocket message type `%s`", messageType)
	}
}

func responsePayload(message map[string]any) (map[string]any, error) {
	if response, ok := message["response"]; ok {
		object, valid := response.(map[string]any)
		if !valid {
			return nil, errors.New("response must be a JSON object")
		}
		return cloneObject(object), nil
	}
	result := cloneObject(message)
	delete(result, "type")
	if len(result) == 0 {
		return nil, errors.New("message is missing a response payload")
	}
	return result, nil
}

func cloneObject(value map[string]any) map[string]any {
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}
func mergeWSObjects(target, patch map[string]any) {
	for key, value := range patch {
		current, exists := target[key]
		if key == "input" {
			currentArray, currentOK := current.([]any)
			patchArray, patchOK := value.([]any)
			if exists && currentOK && patchOK {
				target[key] = append(currentArray, patchArray...)
				continue
			}
		}
		currentObject, currentOK := current.(map[string]any)
		patchObject, patchOK := value.(map[string]any)
		if currentOK && patchOK {
			mergeWSObjects(currentObject, patchObject)
			continue
		}
		target[key] = value
	}
}

func streamWebSocketEvents(connection *websocket.Conn, upstream *http.Response, incoming <-chan wsMessage, metrics *Metrics, requestID string) error {
	started := time.Now()
	chunkCount := 0
	eventCount := 0
	outcome := "client_disconnect"
	defer func() {
		logDebug("request_id=%s responses WebSocket stream ended: outcome=%s chunks=%d events=%d duration_ms=%d", requestID, outcome, chunkCount, eventCount, time.Since(started).Milliseconds())
	}()
	defer upstream.Body.Close()
	guard := newStreamGuard(metrics)
	defer guard.close()
	chunks := make(chan []byte, 2)
	readErrors := make(chan error, 1)
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			count, err := upstream.Body.Read(buffer)
			if count > 0 {
				chunk := append([]byte(nil), buffer[:count]...)
				chunks <- chunk
			}
			if err != nil {
				if err != io.EOF {
					readErrors <- err
				}
				close(chunks)
				return
			}
		}
	}()
	var pending []byte
	forward := func(line []byte) error {
		trimmed := strings.TrimSpace(string(line))
		if !strings.HasPrefix(trimmed, "data:") {
			return nil
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "" || data == "[DONE]" {
			return nil
		}
		var event any
		if json.Unmarshal([]byte(data), &event) != nil {
			return errors.New("upstream SSE event was not JSON")
		}
		eventCount++
		return connection.WriteMessage(websocket.TextMessage, []byte(data))
	}
	for chunks != nil {
		select {
		case message, ok := <-incoming:
			if !ok || message.err != nil {
				outcome = "client_disconnected"
				return errors.New("WebSocket client disconnected")
			}
			if message.messageType == websocket.PingMessage {
				if err := connection.WriteControl(websocket.PongMessage, message.payload, time.Time{}); err != nil {
					return err
				}
				continue
			}
			outcome = "concurrent_request"
			return errors.New("concurrent WebSocket requests are not supported")
		case err := <-readErrors:
			outcome = "upstream_read_error"
			return fmt.Errorf("failed to read upstream stream: %w", err)
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				outcome = "completed"
				continue
			}
			chunkCount++
			pending = append(pending, chunk...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				line := pending[:index]
				pending = pending[index+1:]
				line = bytes.TrimSuffix(line, []byte{'\r'})
				if err := forward(line); err != nil {
					outcome = "stream_error"
					return err
				}
			}
		}
	}
	if len(pending) > 0 {
		chunkCount++
		if err := forward(bytes.TrimSuffix(pending, []byte{'\r'})); err != nil {
			outcome = "stream_error"
			return err
		}
	}
	if outcome == "client_disconnect" {
		outcome = "completed"
	}
	guard.complete()
	return nil
}

func sendWSError(connection *websocket.Conn, code, message string) error {
	return connection.WriteJSON(map[string]any{"type": "error", "code": code, "message": message, "sequence_number": 0})
}
