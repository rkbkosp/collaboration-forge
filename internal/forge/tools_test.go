package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.kenn.io/kata"
)

const (
	toolsRuntimeA  = "b0d4ec6b-c5c7-4cb4-9ec1-a406ad42719a"
	toolsRuntimeB  = "8a5318f0-f523-4fb3-b029-f237543a0026"
	toolsAttemptA  = "75ad1c34-a802-4077-8c94-5437ea394fe2"
	toolsAttemptB  = "30aa284d-b5b1-466a-b328-559ee5efc7ad"
	toolsAttemptV7 = "01965f47-3f80-7901-8b5e-adc5dc509c88"
)

type toolsSeedKey struct{}
type toolsTestAccess struct{}

func (toolsTestAccess) Authorize(ctx context.Context, req kata.AccessRequest) (kata.AccessDecision, error) {
	if seed, _ := ctx.Value(toolsSeedKey{}).(bool); seed {
		return kata.AccessDecision{TransactionFence: func(ctx context.Context, _ kata.Transaction) error { return ctx.Err() }}, nil
	}
	return authorizeTool(ctx, req)
}

type toolsFixture struct {
	handler http.Handler
	native  http.Handler
	project kata.Project
	signer  executionSigner
	service *kata.Service
}

func newToolsFixture(t *testing.T) *toolsFixture {
	t.Helper()
	svc, err := kata.New(context.Background(), kata.Config{
		DSN: filepath.Join(t.TempDir(), "kata.db"), Profile: kata.EmbeddingProfileRestricted,
		Access: toolsTestAccess{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		WorkerTransactionFence: func(ctx context.Context, _ kata.Transaction) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Error(err)
		}
	})
	p, err := svc.EnsureProject(context.Background(), kata.ProjectSpec{UID: "01K00000000000000000000001", Name: "forge"})
	if err != nil {
		t.Fatal(err)
	}
	signer := executionSigner{key: bytes.Repeat([]byte{9}, 32)}
	return &toolsFixture{handler: newToolHandler(svc.Handler(), p.Project, signer), native: svc.Handler(), project: p.Project, signer: signer, service: svc}
}

func toolsRequest(h http.Handler, operation, runtime, token, body string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/forge/v1/tools/"+operation, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if runtime != "" {
		r.Header.Set("X-Forge-Session", runtime)
	}
	if token != "" {
		r.Header.Set("X-Forge-Execution", token)
	}
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func toolsJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var result map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid response JSON: %v", err)
	}
	return result
}

func toolsSuccess(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code < 200 || w.Code >= 300 {
		var e struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		t.Fatalf("HTTP %d: %s: %s", w.Code, e.Error.Code, e.Error.Message)
	}
}

func toolsString(t *testing.T, m map[string]json.RawMessage, key string) string {
	t.Helper()
	var result string
	if err := json.Unmarshal(m[key], &result); err != nil {
		t.Fatalf("missing/invalid %s: %v", key, err)
	}
	return result
}

func toolsIssue(t *testing.T, w *httptest.ResponseRecorder) (uid, shortID string) {
	t.Helper()
	toolsSuccess(t, w)
	var result struct {
		Issue struct {
			UID     string `json:"uid"`
			ShortID string `json:"short_id"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Issue.UID == "" || result.Issue.ShortID == "" {
		t.Fatal("missing canonical issue identity")
	}
	return result.Issue.UID, result.Issue.ShortID
}

func toolsCreate(t *testing.T, h http.Handler, title string) (string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"title": title, "body": "A concrete integration-test issue body."})
	return toolsIssue(t, toolsRequest(h, "issue_create", toolsRuntimeA, "", string(body)))
}

func toolsClaim(t *testing.T, f *toolsFixture, uid, runtime, attempt string) (string, string, string) {
	t.Helper()
	w := toolsRequest(f.handler, "issue_claim", runtime, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q,"purpose":"integration test"}`, uid, attempt))
	toolsSuccess(t, w)
	m := toolsJSON(t, w)
	var granted bool
	_ = json.Unmarshal(m["granted"], &granted)
	if !granted {
		t.Fatal("claim unexpectedly denied")
	}
	var lease struct {
		ClaimUID, ClaimKind string
		ExpiresAt           time.Time `json:"expires_at"`
		AcquiredAt          time.Time `json:"acquired_at"`
	}
	var l map[string]json.RawMessage
	if err := json.Unmarshal(m["lease"], &l); err != nil {
		t.Fatal(err)
	}
	lease.ClaimUID = toolsString(t, l, "claim_uid")
	lease.ClaimKind = toolsString(t, l, "claim_kind")
	_ = json.Unmarshal(l["expires_at"], &lease.ExpiresAt)
	_ = json.Unmarshal(l["acquired_at"], &lease.AcquiredAt)
	if lease.ClaimKind != "timed" || lease.ExpiresAt.Sub(lease.AcquiredAt) != 300*time.Second {
		t.Fatal("claim must default to a timed 300s lease")
	}
	var now time.Time
	if err := json.Unmarshal(m["server_now"], &now); err != nil || now.IsZero() {
		t.Fatal("missing server clock")
	}
	return toolsString(t, m, "execution_token"), toolsString(t, m, "execution_id"), lease.ClaimUID
}

func TestToolsContributionWithoutClaim(t *testing.T) {
	f := newToolsFixture(t)
	uid, shortID := toolsCreate(t, f.handler, "Implement scoped contribution")
	other, _ := toolsCreate(t, f.handler, "Document related contribution")
	get := toolsRequest(f.handler, "issue_get", toolsRuntimeB, "", fmt.Sprintf(`{"ref":%q}`, shortID))
	toolsSuccess(t, get)
	// Kata's current show wire does not emit an ETag; controlled-dispatch
	// coverage below checks preservation whenever the upstream supplies one.
	for _, tc := range []struct{ op, body string }{
		{"issue_list", `{"status":"open","limit":20}`},
		{"issue_comment", fmt.Sprintf(`{"ref":%q,"body":"Another runtime contributes without holding the execution lease."}`, uid)},
		{"issue_link", fmt.Sprintf(`{"ref":%q,"type":"related","to_ref":%q}`, uid, other)},
		{"issue_graph", fmt.Sprintf(`{"ref":%q,"depth":2}`, uid)},
	} {
		t.Run(tc.op, func(t *testing.T) { toolsSuccess(t, toolsRequest(f.handler, tc.op, toolsRuntimeB, "", tc.body)) })
	}
	// Contributions remain possible while somebody else owns execution.
	toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	toolsSuccess(t, toolsRequest(f.handler, "issue_comment", toolsRuntimeB, "", fmt.Sprintf(`{"ref":%q,"body":"Knowledge remains multi-writer even during an active lease."}`, uid)))
	shown := toolsJSON(t, toolsRequest(f.handler, "issue_get", toolsRuntimeB, "", fmt.Sprintf(`{"ref":%q}`, uid)))
	var issue map[string]json.RawMessage
	_ = json.Unmarshal(shown["issue"], &issue)
	var owner *string
	_ = json.Unmarshal(issue["owner"], &owner)
	if owner != nil {
		t.Fatal("claim must not assign long-term owner")
	}
}

func TestToolsClaimRetryConflictAndRuntimeIsolation(t *testing.T) {
	f := newToolsFixture(t)
	uid, shortID := toolsCreate(t, f.handler, "Exercise independent execution attempts")
	token, subject, claim := toolsClaim(t, f, shortID, toolsRuntimeA, toolsAttemptA)
	token2, subject2, claim2 := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	if token != token2 || subject != subject2 || claim != claim2 {
		t.Fatal("logical acquire retry changed its execution or tenure")
	}
	for _, tc := range []struct{ runtime, attempt string }{{toolsRuntimeB, toolsAttemptA}, {toolsRuntimeA, toolsAttemptB}} {
		w := toolsRequest(f.handler, "issue_claim", tc.runtime, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q}`, uid, tc.attempt))
		if w.Code != http.StatusConflict {
			t.Fatalf("unexpected claim conflict HTTP %d", w.Code)
		}
		m := toolsJSON(t, w)
		var granted bool
		_ = json.Unmarshal(m["granted"], &granted)
		if granted || m["execution_token"] != nil {
			t.Fatal("competing execution acquired a credential")
		}
	}
	for _, op := range []string{"issue_renew", "issue_release", "issue_close"} {
		for _, tc := range []struct{ runtime, token, ref string }{
			{toolsRuntimeB, token, uid}, {toolsRuntimeA, token + "tampered", uid},
			{toolsRuntimeA, "", uid}, {toolsRuntimeA, token, shortID},
		} {
			w := toolsRequest(f.handler, op, tc.runtime, tc.token, fmt.Sprintf(`{"ref":%q}`, tc.ref), "Idempotency-Key", "isolation-close")
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("%s accepted wrong runtime/proof/ref: HTTP %d", op, w.Code)
			}
		}
	}
	other, _ := toolsCreate(t, f.handler, "Different issue cannot borrow proof")
	otherToken, _, _ := toolsClaim(t, f, other, toolsRuntimeA, toolsAttemptB)
	for _, op := range []string{"issue_renew", "issue_release"} {
		w := toolsRequest(f.handler, op, toolsRuntimeA, otherToken, fmt.Sprintf(`{"ref":%q}`, uid))
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("%s accepted another issue proof", op)
		}
	}
	renew := toolsRequest(f.handler, "issue_renew", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q,"ttl_seconds":60}`, uid))
	toolsSuccess(t, renew)
	if toolsJSON(t, renew)["server_now"] == nil {
		t.Fatal("renew missing server_now")
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_release", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q,"reason":"handoff"}`, uid)))
	toolsClaim(t, f, uid, toolsRuntimeB, toolsAttemptB)
	for _, op := range []string{"issue_renew", "issue_release"} {
		w := toolsRequest(f.handler, op, toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q}`, uid))
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("old holder %s unexpectedly succeeded: HTTP %d", op, w.Code)
		}
	}
}

func TestToolsStrictCloseExactReceiptAndEvidence(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Complete with exact lease receipt")
	token, _, _ := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	body := fmt.Sprintf(`{"ref":%q,"reason":"done","message":"Implemented the scoped facade and verified the integration tests pass with exact lease receipt replay.","evidence":[{"type":"test","command":"go test ./..."},{"type":"commit","sha":"abcdef1234567"}]}`, uid)
	missingKey := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, body)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key: HTTP %d", missingKey.Code)
	}
	proof, err := f.signer.verify(token, toolsRuntimeA)
	if err != nil {
		t.Fatal(err)
	}
	wrongSubject := proof
	wrongSubject.Subject = f.signer.identity(toolsRuntimeA, toolsAttemptB, uid)
	principalDenied := toolsRequest(f.handler, "issue_close", toolsRuntimeA, f.signer.sign(wrongSubject), body, "Idempotency-Key", "wrong-principal")
	if principalDenied.Code != http.StatusConflict {
		t.Fatalf("same actor, wrong exact principal: HTTP %d", principalDenied.Code)
	}
	invalidEvidence := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q,"reason":"done","message":"A sufficiently detailed conclusion still cannot omit typed evidence for an agent close.","evidence":[{"type":"commit","sha":"not-a-sha"}]}`, uid), "Idempotency-Key", "invalid-evidence")
	if invalidEvidence.Code != http.StatusBadRequest {
		t.Fatalf("Kata evidence validation bypassed: HTTP %d", invalidEvidence.Code)
	}
	wrong := proof
	wrong.ClaimUID = "01K00000000000000000000009"
	denied := toolsRequest(f.handler, "issue_close", toolsRuntimeA, f.signer.sign(wrong), body, "Idempotency-Key", "wrong-tenure")
	if denied.Code != http.StatusConflict {
		t.Fatalf("wrong exact claim UID: HTTP %d", denied.Code)
	}
	closed := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, body, "Idempotency-Key", "stable-logical-close")
	toolsSuccess(t, closed)
	shown := toolsJSON(t, toolsRequest(f.handler, "issue_get", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q}`, uid)))
	if lease := shown["lease"]; lease != nil && string(lease) != "null" {
		t.Fatal("close did not release lease")
	}
	var closedIssue struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(shown["issue"], &closedIssue)
	if closedIssue.Status != "closed" {
		t.Fatal("issue not closed")
	}
	// K7: never preflight a live lease; the committed receipt survives release.
	retry := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, body, "Idempotency-Key", "stable-logical-close")
	toolsSuccess(t, retry)
	first, replay := toolsJSON(t, closed), toolsJSON(t, retry)
	var original, recovered map[string]json.RawMessage
	_ = json.Unmarshal(first["event"], &original)
	_ = json.Unmarshal(replay["original_event"], &recovered)
	if string(replay["reused"]) != "true" || toolsString(t, original, "uid") != toolsString(t, recovered, "uid") || toolsString(t, original, "payload") != toolsString(t, recovered, "payload") {
		t.Fatal("close retry did not recover Kata's exact original receipt")
	}
	var payload struct {
		Evidence []struct{ Type, Command, SHA string } `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(toolsString(t, recovered, "payload")), &payload); err != nil || len(payload.Evidence) != 2 || payload.Evidence[0].Command != "go test ./..." || payload.Evidence[1].SHA != "abcdef1234567" {
		t.Fatal("typed evidence was not preserved in Kata's receipt")
	}
	if closed.Header().Get("ETag") != retry.Header().Get("ETag") {
		t.Fatal("receipt ETag changed")
	}
	mismatch := toolsRequest(f.handler, "issue_close", toolsRuntimeA, f.signer.sign(wrong), body, "Idempotency-Key", "stable-logical-close")
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("different tenure reused receipt: HTTP %d", mismatch.Code)
	}
	noReceipt := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, body, "Idempotency-Key", "new-close-after-release")
	if noReceipt.Code != http.StatusConflict {
		t.Fatalf("strict close without live lease: HTTP %d", noReceipt.Code)
	}
	closedClaim := toolsRequest(f.handler, "issue_claim", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q}`, uid, toolsAttemptB))
	if closedClaim.Code != http.StatusConflict {
		t.Fatalf("claimed closed issue: HTTP %d", closedClaim.Code)
	}
}

func TestToolsUUIDv7AttemptAndSameSessionNewExecution(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Accept per-claim UUIDv7 logical tenure")
	firstToken, firstSubject, firstClaim := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptV7)
	retryToken, retrySubject, retryClaim := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptV7)
	if firstToken != retryToken || firstSubject != retrySubject || firstClaim != retryClaim {
		t.Fatal("UUIDv7 attempt retry lost tenure identity")
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_release", toolsRuntimeA, firstToken, fmt.Sprintf(`{"ref":%q}`, uid)))
	_, nextSubject, nextClaim := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	if firstSubject == nextSubject || firstClaim == nextClaim {
		t.Fatal("new logical attempt reused old execution")
	}
	// Session IDs remain v4 even though logical-acquire attempts may be v7.
	w := toolsRequest(f.handler, "issue_list", toolsAttemptV7, "", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("v7 session accepted: HTTP %d", w.Code)
	}
}

func TestToolsRejectUnsafeDTOsAndBounds(t *testing.T) {
	f := newToolsFixture(t)
	for _, tc := range []struct{ op, body string }{
		{"issue_list", `{"actor":"Human"}`}, {"issue_get", `{"ref":"abcd","owner":"Human"}`},
		{"issue_create", `{"title":"bad","actor":"Human"}`}, {"issue_create", `{"title":"bad","owner":"Human"}`},
		{"issue_create", `{"title":"bad","metadata":{"admin":true}}`}, {"issue_create", `{"title":"bad","force_new":true}`},
		{"issue_comment", `{"ref":"abcd","body":"note","actor":"Human"}`},
		{"issue_link", `{"ref":"abcd","to_ref":"bcde","type":"parent","replace":true}`},
		{"issue_link", `{"ref":"abcd","to_ref":"bcde","type":"delete"}`},
		{"issue_claim", `{"ref":"abcd","attempt_id":"75ad1c34-a802-4077-8c94-5437ea394fe2","claim_kind":"hard"}`},
		{"issue_claim", `{"ref":"abcd","attempt_id":"not-a-uuid"}`},
		{"issue_renew", `{"ref":"abcd","client_kind":"admin"}`}, {"issue_release", `{"ref":"abcd","force":true}`},
		{"issue_close", `{"ref":"abcd","retry_protocol":"close-v1"}`}, {"issue_close", `{"ref":"abcd","protocol":"close-v2"}`},
		{"issue_close", `{"ref":"abcd","evidence":[{"type":"test","command":"go test","actor":"Human"}]}`},
		{"issue_list", `{} {}`}, {"issue_list", `{} true`}, {"issue_list", `null`},
		{"issue_list", `{"limit":0}`}, {"issue_list", `{"limit":1001}`}, {"issue_list", `{"status":"deleted"}`},
		{"issue_graph", `{"ref":"abcd","depth":0}`}, {"issue_graph", `{"ref":"abcd","depth":11}`}, {"issue_graph", `{"ref":"abcd","depth":"full"}`},
		{"issue_claim", `{"ref":"abcd","attempt_id":"75ad1c34-a802-4077-8c94-5437ea394fe2","ttl_seconds":59}`},
		{"issue_renew", `{"ref":"abcd","ttl_seconds":3601}`},
	} {
		t.Run(tc.op+"/"+tc.body, func(t *testing.T) {
			w := toolsRequest(f.handler, tc.op, toolsRuntimeA, "", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("wanted DTO rejection, HTTP %d", w.Code)
			}
			if toolsJSON(t, w)["error"] == nil {
				t.Fatal("missing Kata-style error envelope")
			}
		})
	}
	for _, ref := range []string{"../issues", "forge#abcd", "abcd/comments", "%2f", "abcd?owner=Human", " abcd", "ABCD", "abc", "abcl"} {
		w := toolsRequest(f.handler, "issue_get", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q}`, ref))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe ref %q: HTTP %d", ref, w.Code)
		}
	}
	for _, runtime := range []string{"", "not-uuid", "b0d4ec6b-c5c7-1cb4-9ec1-a406ad42719a", "b0d4ec6b-c5c7-4cb4-0ec1-a406ad42719a"} {
		w := toolsRequest(f.handler, "issue_list", runtime, "", `{}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad runtime: HTTP %d", w.Code)
		}
	}
	large := toolsRequest(f.handler, "issue_create", toolsRuntimeA, "", `{"title":"oversized","body":"`+strings.Repeat("x", 1<<20)+`"}`)
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body cap: HTTP %d", large.Code)
	}
}

func TestToolsProjectScopeAndInternalOperationCapability(t *testing.T) {
	f := newToolsFixture(t)
	p, err := f.service.EnsureProject(context.Background(), kata.ProjectSpec{UID: "01K00000000000000000000002", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	otherTools := newToolHandler(f.native, p.Project, f.signer)
	foreign, _ := toolsCreate(t, otherTools, "Foreign-project authority must stay isolated")
	local, _ := toolsCreate(t, f.handler, "Local graph source")
	for _, tc := range []struct{ op, body string }{
		{"issue_get", fmt.Sprintf(`{"ref":%q}`, foreign)},
		{"issue_graph", fmt.Sprintf(`{"ref":%q,"depth":2}`, foreign)},
		{"issue_comment", fmt.Sprintf(`{"ref":%q,"body":"forbidden contribution"}`, foreign)},
		{"issue_link", fmt.Sprintf(`{"ref":%q,"type":"related","to_ref":%q}`, local, foreign)},
		{"issue_claim", fmt.Sprintf(`{"ref":%q,"attempt_id":%q}`, foreign, toolsAttemptA)},
	} {
		w := toolsRequest(f.handler, tc.op, toolsRuntimeA, "", tc.body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s crossed project scope: HTTP %d", tc.op, w.Code)
		}
	}
	// Seed an existing cross-project edge as the host, then require cumulative
	// scope to hide it from the bounded worker graph/show surface.
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/issues/%s/links", f.project.ID, local), strings.NewReader(fmt.Sprintf(`{"type":"related","to_ref":%q}`, foreign)))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), toolsSeedKey{}, true)
	ctx = kata.WithPrincipal(ctx, kata.Principal{Subject: "seed", Actor: "Human"})
	seed := httptest.NewRecorder()
	f.native.ServeHTTP(seed, r.WithContext(ctx))
	toolsSuccess(t, seed)
	graph := toolsRequest(f.handler, "issue_graph", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q,"depth":2}`, local))
	if graph.Code != http.StatusNotFound {
		t.Fatalf("graph leaked cross-project dependency: HTTP %d", graph.Code)
	}

	// Even a real principal cannot call a raw worker endpoint without the
	// private dispatch capability.
	raw := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/projects/%d/issues", f.project.ID), nil)
	raw = raw.WithContext(kata.WithPrincipal(raw.Context(), kata.Principal{Subject: "session:" + f.signer.session(toolsRuntimeA), Actor: "Agent/test"}))
	denied := httptest.NewRecorder()
	f.native.ServeHTTP(denied, raw)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("native principal acted as capability: HTTP %d", denied.Code)
	}

	// Capture the genuine per-dispatch context instead of exposing a cap
	// constructor. It authorizes only the matched operation and exact identity.
	spy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := kata.Principal{Subject: "session:" + f.signer.session(toolsRuntimeA), Actor: "Agent/" + f.signer.session(toolsRuntimeA)[:12]}
		req := kata.AccessRequest{Principal: principal, Operation: kata.Operation{ID: "listIssues", ProjectIDs: []int64{f.project.ID}, ProjectUIDs: []string{f.project.UID}}}
		d, err := authorizeTool(r.Context(), req)
		if err != nil || d.TransactionFence == nil {
			t.Fatalf("valid internal capability denied: %v", err)
		}
		canceled, cancel := context.WithCancel(r.Context())
		cancel()
		if !errors.Is(d.TransactionFence(canceled, nil), context.Canceled) {
			t.Error("fence ignored cancellation")
		}
		for _, change := range []func(*kata.AccessRequest){
			func(q *kata.AccessRequest) { q.Operation.ID = "deleteIssue" },
			func(q *kata.AccessRequest) { q.Principal.Subject += "x" },
			func(q *kata.AccessRequest) { q.Operation.ProjectIDs = []int64{f.project.ID, p.Project.ID} },
			func(q *kata.AccessRequest) { q.Operation.ProjectUIDs = []string{f.project.UID, p.Project.UID} },
		} {
			bad := req
			change(&bad)
			if _, err := authorizeTool(r.Context(), bad); !errors.Is(err, kata.ErrAccessDenied) {
				t.Error("mismatched private capability allowed")
			}
		}
		if _, err := authorizeTool(context.Background(), req); !errors.Is(err, kata.ErrAccessDenied) {
			t.Error("missing capability allowed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	toolsSuccess(t, toolsRequest(newToolHandler(spy, f.project, f.signer), "issue_list", toolsRuntimeA, "", `{}`))
}

func TestToolsCaptureAllocationBound(t *testing.T) {
	capture := &toolCapture{header: make(http.Header)}
	for _, size := range []int{6 << 20, 1 << 20, 1 << 20} {
		if _, err := capture.Write(bytes.Repeat([]byte{'x'}, size)); err != nil {
			t.Fatal(err)
		}
		if capture.body.Cap() > toolResponseLimit {
			t.Fatalf("capture allocation exceeded cap: %d", capture.body.Cap())
		}
	}
	if _, err := capture.Write([]byte{'x'}); err == nil || !capture.overflow {
		t.Fatal("capture did not reject overflow")
	}
	if capture.body.Len() != toolResponseLimit {
		t.Fatal("overflow grew captured body")
	}
}

func TestToolsBoundedCaptureAndControlledDispatch(t *testing.T) {
	project := kata.Project{ID: 1, UID: "01K00000000000000000000001"}
	signer := executionSigner{key: bytes.Repeat([]byte{2}, 32)}
	oversized := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, io.LimitReader(strings.NewReader(strings.Repeat("x", (8<<20)+1)), (8<<20)+1))
	})
	w := toolsRequest(newToolHandler(oversized, project, signer), "issue_list", toolsRuntimeA, "", `{}`)
	if w.Code != http.StatusBadGateway || w.Body.Len() > 1024 {
		t.Fatalf("unbounded upstream response: HTTP %d, bytes %d", w.Code, w.Body.Len())
	}
	proof := executionProof{Subject: signer.identity(toolsRuntimeA, toolsAttemptA, "01K00000000000000000000004"), Session: signer.session(toolsRuntimeA), IssueUID: "01K00000000000000000000004", ClaimUID: "01K00000000000000000000005"}
	calls := 0
	spy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/actions/close") {
			t.Fatal("close performed a live-lease preflight")
		}
		if r.Header.Get("If-Lease-Match") != proof.ClaimUID || r.Header.Get("Idempotency-Key") != "logical-close" || r.Header.Get("If-Match") != `"7"` {
			t.Error("strict close headers lost")
		}
		for _, key := range []string{"Authorization", "X-Forge-Execution", "X-Forge-Session", "X-Forwarded-For"} {
			if r.Header.Get(key) != "" {
				t.Errorf("forwarded untrusted %s", key)
			}
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if toolsString(t, body, "retry_protocol") != "close-v2" || body["ref"] != nil || body["if_match"] != nil {
			t.Error("wrong controlled close wire body")
		}
		w.Header().Set("ETag", `"8"`)
		_, _ = io.WriteString(w, `{"changed":true}`)
	})
	w = toolsRequest(newToolHandler(spy, project, signer), "issue_close", toolsRuntimeA, signer.sign(proof), fmt.Sprintf(`{"ref":%q,"reason":"done","if_match":"\"7\""}`, proof.IssueUID), "Idempotency-Key", "logical-close", "Authorization", "Bearer never-forward", "If-Lease-Match", "client-forgery", "X-Forwarded-For", "127.0.0.1")
	toolsSuccess(t, w)
	if calls != 1 || w.Header().Get("ETag") != `"8"` {
		t.Fatal("close dispatch/ETag mismatch: " + strconv.Itoa(calls))
	}
}
