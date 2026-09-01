package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func decodeJSON(reader io.Reader, target any) error { return json.NewDecoder(reader).Decode(target) }
func readAll(reader io.Reader) ([]byte, error)      { return io.ReadAll(reader) }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func logInfo(format string, args ...any)  { log.Printf("INFO "+format, args...) }
func logWarn(format string, args ...any)  { log.Printf("WARN "+format, args...) }
func logError(format string, args ...any) { log.Printf("ERROR "+format, args...) }

func writeJSONError(c *gin.Context, status int, errorType, message, parameter string) {
	detail := map[string]any{"message": message, "type": errorType}
	if parameter != "" {
		detail["param"] = parameter
	}
	c.JSON(status, map[string]any{"error": detail})
}

func writeUpstreamError(c *gin.Context, err *UpstreamError) {
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
