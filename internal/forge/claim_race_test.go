package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.kenn.io/kata"
)

func TestClaimDoesNotGrantExecutionAfterConcurrentHostClose(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Host closes between resolution and acquire")
	native := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/lease/actions/acquire") {
			closeReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/issues/%s/actions/close", f.project.ID, uid), strings.NewReader(`{"reason":"wontfix","message":"The host cancelled this work before a worker execution could begin."}`))
			closeReq.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(closeReq.Context(), toolsSeedKey{}, true)
			ctx = kata.WithPrincipal(ctx, kata.Principal{Subject: "host", Actor: "Human"})
			closed := httptest.NewRecorder()
			f.native.ServeHTTP(closed, closeReq.WithContext(ctx))
			toolsSuccess(t, closed)
		}
		f.native.ServeHTTP(w, r)
	})
	handler := newToolHandler(native, f.project, f.signer)
	result := toolsRequest(handler, "issue_claim", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q}`, uid, toolsAttemptA))
	if result.Code != http.StatusConflict {
		t.Fatalf("closed issue granted execution: %d", result.Code)
	}
	if toolsJSON(t, result)["execution_token"] != nil {
		t.Fatal("closed issue received execution credential")
	}
	shown := toolsJSON(t, toolsRequest(f.handler, "issue_get", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q}`, uid)))
	if lease := shown["lease"]; lease != nil && string(lease) != "null" {
		t.Fatal("abandoned closed-issue lease was not released")
	}
}
