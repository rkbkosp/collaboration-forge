package main

import (
	"errors"
	"testing"

	"github.com/rkbkosp/collaboration-forge/internal/checkout"
)

func TestForgedErrorPreservesStructuredCheckoutFieldsSafely(t *testing.T) {
	err := &checkout.Error{
		Status:    503,
		Code:      "claim_denied",
		Message:   "lease is held",
		Hint:      "wait before retrying",
		Data:      map[string]any{"cleanup_pending": false, "execution_token": "private"},
		Ambiguous: true,
	}
	converted := forgedErrorFrom(err)
	if converted.code != "claim_denied" || converted.status != 503 || converted.hint != "wait before retrying" || !converted.ambiguous {
		t.Fatalf("structured fields lost: %+v", converted)
	}
	if _, ok := converted.data["execution_token"]; ok {
		t.Fatal("execution token escaped checkout error")
	}
}

func TestForgedErrorDoesNotExposeNativeCause(t *testing.T) {
	converted := forgedErrorFrom(errors.New("open /private/token-file: private-token"))
	if converted.code != "forged_error" || converted.message == "" || converted.message == "open /private/token-file: private-token" {
		t.Fatalf("native cause exposed: %+v", converted)
	}
}
