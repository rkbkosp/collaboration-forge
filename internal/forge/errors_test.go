package forge

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeForgeErrorProducesCanonicalSafeEnvelope(t *testing.T) {
	response := &toolResponse{status: 409, header: make(map[string][]string), body: []byte(`{"status":500,"error":{"code":"claim_denied","message":"safe","hint":"retry","data":{"cleanup_pending":false,"execution_token":"secret"}}}`)}
	w := httptest.NewRecorder()
	response.serve(w)
	if w.Code != 409 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	var envelope forgeErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != 409 || envelope.Error.Code != "claim_denied" || envelope.Error.Message != "safe" || envelope.Error.Hint != "retry" {
		t.Fatalf("unexpected envelope: %s", w.Body.String())
	}
	if _, ok := envelope.Error.Data["execution_token"]; ok {
		t.Fatal("secret error field was retained")
	}
}

func TestNormalizeForgeErrorDoesNotForwardPlaintextOrInvalidCodes(t *testing.T) {
	for _, raw := range []string{
		"private-token should not echo",
		`{"status":500,"error":{"code":"Invalid Code","message":"private-token"}}`,
	} {
		body := normalizeForgeError(502, []byte(raw))
		if strings.Contains(string(body), "private-token") || strings.Contains(string(body), "Invalid Code") {
			t.Fatalf("unsafe response forwarded: %s", body)
		}
		var envelope forgeErrorEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Status != 502 || envelope.Error.Code != "upstream_error" {
			t.Fatalf("unexpected fallback: %s", body)
		}
	}
}
