package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// OpenAIEncryptedHistoryError is a request-history rejection, not evidence that
// an account is unhealthy. It deliberately carries no request body or ciphertext.
// Recognizing this error does not authorize deleting tool results or replaying a
// request whose execution/usage is unknown.
type OpenAIEncryptedHistoryError struct {
	Code string
}

func (e *OpenAIEncryptedHistoryError) Error() string {
	return "upstream rejected encrypted request history: " + e.Code
}

// IsOpenAIEncryptedHistoryError also recognizes errors wrapped by stream readers.
func IsOpenAIEncryptedHistoryError(err error) bool {
	var historyErr *OpenAIEncryptedHistoryError
	return errors.As(err, &historyErr)
}

func openAIEncryptedHistoryError(account *Account, payload []byte) *OpenAIEncryptedHistoryError {
	if account == nil || account.Platform != PlatformOpenAI || !gjson.ValidBytes(payload) {
		return nil
	}
	errorObject := openAIEncryptedHistoryErrorObject(payload)
	code := errorObject.Get("code")
	if code.Type != gjson.String {
		return nil
	}
	switch code.String() {
	case "invalid_encrypted_content":
		return &OpenAIEncryptedHistoryError{Code: code.String()}
	case "thinking_signature_invalid":
		message := errorObject.Get("message")
		if message.Type == gjson.String &&
			strings.Contains(message.String(), "The encrypted content") &&
			strings.Contains(message.String(), "could not be verified") &&
			strings.Contains(message.String(), "could not be decrypted or parsed") {
			return &OpenAIEncryptedHistoryError{Code: code.String()}
		}
	}
	return nil
}

func openAIEncryptedHistoryErrorObject(payload []byte) gjson.Result {
	root := gjson.ParseBytes(payload)
	if nested := root.Get("response.error"); nested.IsObject() {
		return nested
	}
	if nested := root.Get("error"); nested.IsObject() {
		return nested
	}
	// Native Responses error events may put code/message/param at the top level.
	if root.IsObject() && root.Get("type").String() == "error" {
		return root
	}
	return gjson.Result{}
}

// Called only with payloads accepted by openAIEncryptedHistoryError. Preserve
// error fields while removing a native SSE event's framing metadata.
func openAIEncryptedHistoryErrorJSON(payload []byte) []byte {
	errorObject := openAIEncryptedHistoryErrorObject(payload)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(errorObject.Raw), &fields); err != nil {
		return nil // Only called after structured classification succeeds.
	}
	if errorObject.Get("type").String() == "error" {
		delete(fields, "type") // SSE discriminator is not an error category.
		delete(fields, "sequence_number")
		delete(fields, "event_id")
	}
	if _, ok := fields["type"]; !ok {
		fields["type"] = json.RawMessage(`"invalid_request_error"`)
	}
	encoded, _ := json.Marshal(map[string]any{"error": fields}) // Validated RawMessages.
	return encoded
}

// Keep code, param, and extension fields when an existing Codex reader converts
// a bare error to response.failed. The generic builder only keeps code/message.
func buildOpenAIEncryptedHistoryFailedSSE(responseID, model string, payload []byte) string {
	encoded := openAIEncryptedHistoryErrorJSON(payload)
	if responseID = strings.TrimSpace(responseID); responseID == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	response := gin.H{
		"id": responseID, "object": "response", "status": "failed", "output": []any{},
		"error": json.RawMessage(gjson.GetBytes(encoded, "error").Raw),
	}
	if model = strings.TrimSpace(model); model != "" {
		response["model"] = model
	}
	data, _ := json.Marshal(gin.H{"type": "response.failed", "response": response})
	return "event: response.failed\ndata: " + string(data) + "\n\n"
}

// Preserve the request error instead of returning a generic protocol-error 502.
// If a heartbeat already committed HTTP 200, finish that SSE response.
func writeOpenAIEncryptedHistoryJSON(c *gin.Context, payload []byte) {
	encoded := openAIEncryptedHistoryErrorJSON(payload)
	committedSSE := openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c)
	MarkResponseCommitted(c)
	if committedSSE {
		_, _ = c.Writer.WriteString(buildOpenAIEncryptedHistoryFailedSSE("", "", payload))
		c.Writer.Flush()
		return
	}
	status := http.StatusBadRequest
	if c.Writer.Written() {
		status = c.Writer.Status()
	}
	c.Data(status, "application/json; charset=utf-8", encoded)
}
