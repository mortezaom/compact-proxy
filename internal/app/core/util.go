package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

type logLevel uint32

const (
	logLevelDebug logLevel = iota
	logLevelInfo
	logLevelWarn
	logLevelError
)

var configuredLogLevel atomic.Uint32

type requestIDContextKey struct{}

func init() { configuredLogLevel.Store(uint32(logLevelInfo)) }

func decodeJSON(reader io.Reader, target any) error { return json.NewDecoder(reader).Decode(target) }
func readAll(reader io.Reader) ([]byte, error)      { return io.ReadAll(reader) }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func setLogLevel(value string) error {
	level, err := parseLogLevel(value)
	if err != nil {
		return err
	}
	configuredLogLevel.Store(uint32(level))
	return nil
}

func parseLogLevel(value string) (logLevel, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return logLevelDebug, nil
	case "info", "":
		return logLevelInfo, nil
	case "warn", "warning":
		return logLevelWarn, nil
	case "error":
		return logLevelError, nil
	default:
		return logLevelInfo, fmt.Errorf("unsupported log level %q (use debug, info, warn, or error)", value)
	}
}

func logAt(level logLevel, label, format string, args ...any) {
	if level < logLevel(configuredLogLevel.Load()) {
		return
	}
	log.Printf(label+" "+format, args...)
}

func logDebug(format string, args ...any) { logAt(logLevelDebug, "DEBUG", format, args...) }
func logInfo(format string, args ...any)  { logAt(logLevelInfo, "INFO", format, args...) }
func logWarn(format string, args ...any)  { logAt(logLevelWarn, "WARN", format, args...) }
func logError(format string, args ...any) { logAt(logLevelError, "ERROR", format, args...) }

func requestIDFromHeaders(headers http.Header) string {
	if value := strings.TrimSpace(headers.Get("x-request-id")); value != "" {
		return value
	}
	return "unknown"
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

func requestIDFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDContextKey{}).(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return "unknown"
}

func writeJSONError(c *gin.Context, status int, errorType, message, parameter string) {
	logDebug("request error response: request_id=%s status=%d type=%s parameter=%s", requestIDFromHeaders(c.Request.Header), status, errorType, parameter)
	detail := map[string]any{"message": message, "type": errorType}
	if parameter != "" {
		detail["param"] = parameter
	}
	c.JSON(status, map[string]any{"error": detail})
}

func writeUpstreamError(c *gin.Context, err *UpstreamError) {
	logDebug("upstream error response: request_id=%s status=%d type=%s code=%s", requestIDFromHeaders(c.Request.Header), err.Status, err.ErrorType, err.Code)
	c.JSON(err.Status, err.jsonBody())
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func requestID(c *gin.Context) string {
	value := c.GetHeader("x-request-id")
	if value == "" {
		value = c.GetHeader("x-client-request-id")
	}
	if value == "" {
		bytes := make([]byte, 16)
		_, _ = rand.Read(bytes)
		return hexEncode(bytes)
	}
	return value
}

func hexEncode(bytes []byte) string {
	return hex.EncodeToString(bytes)
}

func invalidRequest(c *gin.Context, message string) {
	writeJSONError(c, http.StatusBadRequest, "invalid_request_error", message, "")
}
