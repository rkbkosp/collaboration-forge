package forge

import (
	"fmt"
	"net/http"
	"testing"
)

func TestMountedWorkerLifecycleAndRestartReceipt(t *testing.T) {
	dir := t.TempDir()
	config := Config{DataDir: dir, ProjectName: "lifecycle", AdminToken: testToken, WorkerToken: testWorkerToken}
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	_, created := createIssue(t, s)
	uid := created.Issue.UID
	worker := func() http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+testWorkerToken)
			s.Handler().ServeHTTP(w, r)
		})
	}
	claim := toolsRequest(worker(), "issue_claim", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q}`, uid, toolsAttemptA))
	toolsSuccess(t, claim)
	token := toolsString(t, toolsJSON(t, claim), "execution_token")
	conflict := toolsRequest(worker(), "issue_claim", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q}`, uid, toolsAttemptB))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same Pi session new execution should conflict: %d", conflict.Code)
	}
	toolsSuccess(t, toolsRequest(worker(), "issue_comment", toolsRuntimeB, "", fmt.Sprintf(`{"ref":%q,"body":"Other runtime can still contribute while work is held."}`, uid)))
	body := fmt.Sprintf(`{"ref":%q,"reason":"done","message":"Implemented the requested change and verified its behavior.","evidence":[{"type":"test","command":"go test ./..."}]}`, uid)
	close := toolsRequest(worker(), "issue_close", toolsRuntimeA, token, body, "Idempotency-Key", "restart-receipt")
	toolsSuccess(t, close)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	replay := toolsRequest(worker(), "issue_close", toolsRuntimeA, token, body, "Idempotency-Key", "restart-receipt")
	toolsSuccess(t, replay)
	if string(toolsJSON(t, replay)["reused"]) != "true" {
		t.Fatal("restart did not restore original receipt")
	}
	unauth := toolsRequest(s.Handler(), "issue_list", toolsRuntimeA, "", `{}`)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated tools accepted")
	}
}
