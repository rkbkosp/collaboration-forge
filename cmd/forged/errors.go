package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/rkbkosp/collaboration-forge/internal/checkout"
)

type forgedError struct {
	code      string
	message   string
	status    int
	hint      string
	data      map[string]any
	ambiguous bool
}

func (e *forgedError) Error() string { return e.message }

func newForgedError(code, message string) *forgedError {
	return &forgedError{code: code, message: message}
}

func forgedErrorFrom(err error) *forgedError {
	if err == nil {
		return nil
	}
	var typed *forgedError
	if errors.As(err, &typed) {
		return typed
	}
	var checkoutErr *checkout.Error
	if errors.As(err, &checkoutErr) {
		code := checkoutErr.Code
		if !validForgedCode(code) {
			code = "checkout_error"
		}
		message := sanitizeForgedText(checkoutErr.Message, "Checkout request failed")
		return &forgedError{code: code, message: message, status: checkoutErr.Status, hint: sanitizeForgedText(checkoutErr.Hint, ""), data: sanitizeForgedData(checkoutErr.Data), ambiguous: checkoutErr.Ambiguous}
	}
	// Do not print native causes: they may contain local paths or request data.
	message := "forged operation failed; inspect command configuration and server state"
	nativeMessage := err.Error()
	if strings.HasPrefix(nativeMessage, "usage:") || strings.Contains(nativeMessage, " usage:") ||
		strings.Contains(nativeMessage, "invalid value for") || strings.Contains(nativeMessage, "flag provided but not defined") || strings.Contains(nativeMessage, "flag needs an argument") {
		return newForgedError("usage", message)
	}
	return newForgedError("forged_error", message)
}

func (e *forgedError) exitCode() int {
	if e.ambiguous {
		return 3
	}
	if e.code == "usage" {
		return 2
	}
	return 1
}

func (e *forgedError) envelope() map[string]any {
	body := map[string]any{
		"code":      e.code,
		"message":   e.message,
		"ambiguous": e.ambiguous,
	}
	if e.hint != "" {
		body["hint"] = e.hint
	}
	if e.data != nil {
		body["data"] = e.data
	}
	out := map[string]any{"error": body}
	if e.status > 0 {
		out["status"] = e.status
	}
	return out
}

func writeForgedError(err error) int {
	typed := forgedErrorFrom(err)
	body, marshalErr := json.Marshal(typed.envelope())
	if marshalErr != nil {
		body = []byte(`{"error":{"code":"forged_error","message":"forged operation failed","ambiguous":false}}`)
	}
	_, _ = os.Stderr.Write(append(body, '\n'))
	return typed.exitCode()
}

func parseForgedHTTPError(status int, raw []byte, fallbackCode, fallbackMessage string) *forgedError {
	var envelope struct {
		Status int `json:"status"`
		Error  struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Hint      string         `json:"hint"`
			Data      map[string]any `json:"data"`
			Ambiguous bool           `json:"ambiguous"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && validForgedCode(envelope.Error.Code) && strings.TrimSpace(envelope.Error.Message) != "" {
		return &forgedError{
			code:      envelope.Error.Code,
			message:   sanitizeForgedText(envelope.Error.Message, "The Forge request failed"),
			status:    status,
			hint:      sanitizeForgedText(envelope.Error.Hint, ""),
			data:      sanitizeForgedData(envelope.Error.Data),
			ambiguous: envelope.Error.Ambiguous,
		}
	}
	return &forgedError{code: fallbackCode, message: fallbackMessage, status: status}
}

func sanitizeForgedText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return fallback
	}
	for _, marker := range []string{"Bearer ", "worker-token", "admin-token", "execution_token", "attempt_id", "claim_uid", "token", "credential", "secret"} {
		if strings.Contains(strings.ToLower(value), strings.ToLower(marker)) {
			return fallback
		}
	}
	return value
}

func sanitizeForgedData(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, child := range value {
		lower := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
		compact := strings.ReplaceAll(lower, "_", "")
		if strings.Contains(compact, "token") || strings.Contains(compact, "attempt") || strings.Contains(compact, "claimuid") || strings.Contains(compact, "credential") || strings.Contains(compact, "secret") {
			continue
		}
		switch nested := child.(type) {
		case map[string]any:
			if safe := sanitizeForgedData(nested); safe != nil {
				out[key] = safe
			}
		case []any:
			items := make([]any, 0, len(nested))
			for _, item := range nested {
				if itemMap, ok := item.(map[string]any); ok {
					items = append(items, sanitizeForgedData(itemMap))
				} else if text, ok := item.(string); ok {
					if safe := sanitizeForgedText(text, ""); safe != "" {
						items = append(items, safe)
					}
				} else {
					items = append(items, item)
				}
			}
			out[key] = items
		case string:
			if safe := sanitizeForgedText(nested, ""); safe != "" {
				out[key] = safe
			}
		default:
			out[key] = child
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validForgedCode(code string) bool {
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

func writeForgedHTTPError(w http.ResponseWriter, status int, code, message string) {
	writeForgedHTTPFailure(w, status, &forgedError{code: code, message: message, status: status})
}

func writeForgedHTTPFailure(w http.ResponseWriter, status int, failure *forgedError) {
	if failure == nil {
		failure = &forgedError{code: "execution_tool_error", message: "Execution tool request was rejected"}
	}
	if failure.status <= 0 {
		failure.status = status
	}
	body, _ := json.Marshal(failure.envelope())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(failure.status)
	_, _ = w.Write(body)
}
