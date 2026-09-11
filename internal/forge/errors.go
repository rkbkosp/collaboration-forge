package forge

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var forgeBearerSecret = regexp.MustCompile(`(?i)Bearer\s+[^\s"']+`)
var forgeHexSecret = regexp.MustCompile(`(?i)\b[a-f0-9]{32,}\b`)

// forgeErrorEnvelope is the stable Forge/Kata wire shape. Keep this local:
// kata/internal/api is intentionally not importable from the host module.
type forgeErrorEnvelope struct {
	Status int               `json:"status"`
	Error  forgeErrorDetails `json:"error"`
}

type forgeErrorDetails struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func forgeErrorBody(status int, code, message string, hint string, data map[string]any) []byte {
	if !validForgeErrorCode(code) {
		code = "internal"
	}
	body, err := json.Marshal(forgeErrorEnvelope{
		Status: status,
		Error: forgeErrorDetails{
			Code:    code,
			Message: sanitizeForgeErrorText(message, "Forge request failed"),
			Hint:    sanitizeForgeErrorText(hint, ""),
			Data:    sanitizeForgeErrorMap(data),
		},
	})
	if err != nil {
		return []byte(`{"status":500,"error":{"code":"internal","message":"internal Forge error"}}`)
	}
	return body
}

func writeForgeError(w http.ResponseWriter, status int, code, message string) {
	writeForgeErrorBody(w, status, forgeErrorBody(status, code, message, "", nil))
}

func writeForgeErrorBody(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// forwardForgeError preserves a trusted downstream error's stable fields while
// replacing malformed bodies with a safe local error. The HTTP status always
// remains the status of the response crossing this boundary.
func forwardForgeError(w http.ResponseWriter, status int, raw []byte, fallbackCode, fallbackMessage string) {
	var envelope forgeErrorEnvelope
	if json.Unmarshal(raw, &envelope) == nil && validForgeError(envelope.Error) {
		envelope.Status = status
		envelope.Error.Message = sanitizeForgeErrorText(envelope.Error.Message, "Forge request failed")
		envelope.Error.Hint = sanitizeForgeErrorText(envelope.Error.Hint, "")
		envelope.Error.Data = sanitizeForgeErrorMap(envelope.Error.Data)
		if body, err := json.Marshal(envelope); err == nil && len(body) <= toolResponseLimit {
			writeForgeErrorBody(w, status, body)
			return
		}
	}
	writeForgeError(w, status, fallbackCode, fallbackMessage)
}

// normalizeForgeError ensures every non-success response crossing the Forge
// façade has a bounded, safe JSON envelope. Kata is trusted in-process, but a
// malformed/plain response must not become an unstructured host response.
func normalizeForgeError(status int, raw []byte) []byte {
	var envelope forgeErrorEnvelope
	if json.Unmarshal(raw, &envelope) == nil && validForgeError(envelope.Error) {
		envelope.Status = status
		envelope.Error.Message = sanitizeForgeErrorText(envelope.Error.Message, "Forge request failed")
		envelope.Error.Hint = sanitizeForgeErrorText(envelope.Error.Hint, "")
		envelope.Error.Data = sanitizeForgeErrorMap(envelope.Error.Data)
		if body, err := json.Marshal(envelope); err == nil && len(body) <= toolResponseLimit {
			return body
		}
	}
	return forgeErrorBody(status, "upstream_error", "Forge service returned an invalid error response", "", nil)
}

func validForgeError(errorBody forgeErrorDetails) bool {
	return validForgeErrorCode(errorBody.Code) && strings.TrimSpace(errorBody.Message) != ""
}

func sanitizeForgeErrorText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 {
		return fallback
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"execution_token", "worker_token", "admin_token", "attempt_id", "execution_id", "claim_uid", "credential", "secret", "token"} {
		if strings.Contains(lower, marker) {
			return fallback
		}
	}
	value = forgeBearerSecret.ReplaceAllString(value, "Bearer [REDACTED]")
	return forgeHexSecret.ReplaceAllString(value, "[REDACTED]")
}

func validForgeErrorCode(code string) bool {
	if code == "" || len(code) > 100 || code[0] < 'a' || code[0] > 'z' {
		return false
	}
	for _, c := range code[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func sanitizeForgeErrorMap(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	clean, ok := sanitizeForgeErrorValue(data).(map[string]any)
	if !ok {
		return nil
	}
	return clean
}

func sanitizeForgeErrorValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(value))
		for key, child := range value {
			lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			compact := strings.ReplaceAll(lower, "_", "")
			if lower == "authorization" || lower == "bearer" || strings.Contains(compact, "token") || strings.Contains(compact, "forgeexecution") || strings.Contains(compact, "attempt") || strings.Contains(compact, "executionid") || strings.Contains(compact, "claimuid") {
				continue
			}
			clean[key] = sanitizeForgeErrorValue(child)
		}
		return clean
	case []any:
		clean := make([]any, len(value))
		for i, child := range value {
			clean[i] = sanitizeForgeErrorValue(child)
		}
		return clean
	case string:
		return sanitizeForgeErrorText(value, "[REDACTED]")
	default:
		return value
	}
}
