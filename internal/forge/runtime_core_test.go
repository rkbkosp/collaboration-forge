package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const codexTestToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func codexRequest(m http.Handler, op string, body any, token string) *httptest.ResponseRecorder {
	if c, ok := body.(supervisedCommand); ok && c.RequestID == "" {
		c.RequestID = runtimeNonce()
		body = c
	}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/forge/v1/codex/"+op, bytes.NewReader(b))
	r.Header.Set("X-Forge-Codex-Token", token)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
func TestSupervisedRuntimeRegistrationAndActorFence(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"granted":true,"execution_token":"private-proof","execution_id":"private-execution","server_now":"2026-09-11T00:00:00Z","lease":{"issue_uid":"01K00000000000000000000002","claim_uid":"01K00000000000000000000003","expires_at":"2026-09-11T00:05:00Z"}}`))
	})
	m := newSupervisedRuntime(h)
	reg := map[string]any{"instance_id": "i1", "pid": os.Getpid()}
	if w := codexRequest(m, "register", reg, codexTestToken); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a := actorIdentity{Instance: "i1", Session: "s1", Thread: "t1"}
	b := actorIdentity{Instance: "i1", Session: "s1", Thread: "t2"}
	invoke := func(i actorIdentity, op string) *httptest.ResponseRecorder {
		return codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: op, Params: json.RawMessage(`{"ref":"abcd"}`)}, codexTestToken)
	}
	if w := invoke(a, "issue_claim"); w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := invoke(b, "issue_close"); w.Code != 409 {
		t.Fatal("thread inherited claim", w.Code)
	}
	if w := codexRequest(m, "tool", supervisedCommand{Identity: a, Operation: "issue_release", Params: json.RawMessage(`{"ref":"abcd"}`)}, strings.Repeat("b", 64)); w.Code != 403 {
		t.Fatal("wrong capability accepted")
	}
	before := calls
	m.alive = func(int, string) bool { return false }
	m.tick()
	if w := invoke(a, "issue_release"); w.Code != 409 {
		t.Fatal("dead process accepted", w.Code)
	}
	if calls != before {
		t.Fatal("crash renewed or released")
	}
}
func TestSupervisedRuntimeRenewsWithoutClientTouchAndEndsNormally(t *testing.T) {
	renew, release := 0, 0
	now := time.Now().UTC()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "issue_renew") {
			renew++
		}
		if strings.HasSuffix(r.URL.Path, "issue_release") {
			release++
		}
		json.NewEncoder(w).Encode(map[string]any{"granted": true, "execution_token": "proof", "execution_id": "exec", "server_now": now, "lease": map[string]any{"issue_uid": "uid", "claim_uid": "claim", "expires_at": now.Add(300 * time.Second)}})
	})
	m := newSupervisedRuntime(h)
	codexRequest(m, "register", map[string]any{"instance_id": "i", "pid": os.Getpid()}, codexTestToken)
	i := actorIdentity{Instance: "i", Session: "s", Thread: "t"}
	w := codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "issue_claim", Params: json.RawMessage(`{"ref":"abcd"}`)}, codexTestToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	m.threads[i].nextRenew = time.Now().Add(-time.Second)
	m.tick()
	if renew != 1 {
		t.Fatal("no daemon renewal")
	}
	if time.Until(m.threads[i].nextRenew) < 25*time.Second {
		t.Fatal("successful renewal must keep normal cadence, not retry cadence")
	}
	codexRequest(m, "end", map[string]any{"instance_id": "i", "normal": true}, codexTestToken)
	m.tick()
	if release != 1 {
		t.Fatal("normal end did not release")
	}
	m.tick()
	if release != 1 {
		t.Fatal("duplicate release")
	}
}

func TestCodexRuntimeRealKataCloseAndAttribution(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Codex lifecycle")
	m := newSupervisedRuntime(f.handler)
	codexRequest(m, "register", map[string]any{"instance_id": "real", "pid": os.Getpid()}, codexTestToken)
	i := actorIdentity{Instance: "real", Session: "session", Thread: "thread"}
	call := func(op string, p any) *httptest.ResponseRecorder {
		return codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: op, Params: runtimeJSON(p)}, codexTestToken)
	}
	w := call("issue_claim", map[string]any{"ref": uid})
	toolsSuccess(t, w)
	if !strings.Contains(w.Body.String(), "Codex session session") || strings.Contains(w.Body.String(), "Pi session") {
		t.Fatal("wrong attribution")
	}
	close := map[string]any{"ref": uid, "reason": "audit-no-change", "message": "Verified Codex runtime with the real embedded service; this generated fixture needs no product modification.", "evidence": []any{map[string]string{"type": "no-change-audit", "rationale": "Generated runtime acceptance fixture; no product changes required."}}}
	toolsSuccess(t, call("issue_close", close))
	if m.threads[i].active != nil {
		t.Fatal("close retained tenure")
	}
	toolsSuccess(t, call("issue_comment", map[string]string{"ref": uid, "body": "Knowledge contribution after close"}))
	if w := call("issue_force_release", map[string]string{"ref": uid}); w.Code != 404 {
		t.Fatal("authority escape")
	}
}
func TestSupervisedEndDoesNotWaitForBusyActor(t *testing.T) {
	m := newSupervisedRuntime(http.NotFoundHandler())
	codexRequest(m, "register", map[string]any{"instance_id": "busy", "pid": os.Getpid()}, codexTestToken)
	i := actorIdentity{Instance: "busy", Session: "s", Thread: "t"}
	codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "start"}, codexTestToken)
	m.threads[i].mu.Lock()
	defer m.threads[i].mu.Unlock()
	done := make(chan int, 1)
	go func() {
		done <- codexRequest(m, "end", map[string]any{"instance_id": "busy", "normal": true}, codexTestToken).Code
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("end blocked on execution")
	}
}

func TestSupervisedResultReplayCannotRepeatMutationOrCrossTenure(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Codex receipt")
	m := newSupervisedRuntime(f.handler)
	codexRequest(m, "register", map[string]any{"instance_id": "replay", "pid": os.Getpid()}, codexTestToken)
	i := actorIdentity{Instance: "replay", Session: "s", Thread: "t"}
	cmd := supervisedCommand{Identity: i, Operation: "issue_claim", Params: runtimeJSON(map[string]string{"ref": uid}), RequestID: "request-1"}
	first := codexRequest(m, "tool", cmd, codexTestToken)
	toolsSuccess(t, first)
	again := codexRequest(m, "tool", cmd, codexTestToken)
	toolsSuccess(t, again)
	var initial, replayed map[string]any
	json.Unmarshal(first.Body.Bytes(), &initial)
	json.Unmarshal(again.Body.Bytes(), &replayed)
	if replayed["client_replayed"] != true || replayed["current_state_not_refreshed"] != true {
		t.Fatal("historical response not marked")
	}
	if fmt.Sprint(initial["lease"]) != fmt.Sprint(replayed["lease"]) {
		t.Fatal("original response not recovered")
	}
	cmd.Params = runtimeJSON(map[string]string{"ref": "different"})
	if w := codexRequest(m, "tool", cmd, codexTestToken); w.Code != 409 {
		t.Fatal("request ID rebound")
	}
	codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "issue_release", Params: runtimeJSON(map[string]string{"ref": uid})}, codexTestToken)
	cmd.Params = runtimeJSON(map[string]string{"ref": uid})
	toolsSuccess(t, codexRequest(m, "tool", cmd, codexTestToken))
	if m.threads[i].active != nil {
		t.Fatal("replayed acquire restored old tenure")
	}
}

func TestSupervisedAmbiguousAcquireReusesAttemptAndContributionDoesNotClearPending(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Ambiguous acquire")
	var attempts []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "issue_claim") {
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			attempts = append(attempts, p["attempt_id"].(string))
			r.Body = io.NopCloser(bytes.NewReader(runtimeJSON(p)))
			if len(attempts) == 1 {
				original := httptest.NewRecorder()
				f.handler.ServeHTTP(original, r)
				toolsSuccess(t, original)
				w.WriteHeader(502)
				w.Write([]byte(`{}`))
				return
			}
		}
		f.handler.ServeHTTP(w, r)
	})
	m := newSupervisedRuntime(h)
	codexRequest(m, "register", map[string]any{"instance_id": "retry", "pid": os.Getpid()}, codexTestToken)
	i := actorIdentity{Instance: "retry", Session: "s", Thread: "t"}
	cmd := supervisedCommand{Identity: i, Operation: "issue_claim", Params: runtimeJSON(map[string]string{"ref": uid}), RequestID: "stable"}
	if w := codexRequest(m, "tool", cmd, codexTestToken); w.Code != 502 {
		t.Fatal(w.Code)
	}
	codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "issue_comment", Params: runtimeJSON(map[string]string{"ref": "bad", "body": "bad"})}, codexTestToken)
	if m.threads[i].pending == nil {
		t.Fatal("contribution erased execution retry")
	}
	toolsSuccess(t, codexRequest(m, "tool", cmd, codexTestToken))
	if len(attempts) != 2 || attempts[0] != attempts[1] {
		t.Fatal("new acquire identity on network retry")
	}
}
func TestSupervisedPendingCloseAfterExpiryStillReplaysKataReceipt(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Close response loss")
	closeCalls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "issue_close") {
			closeCalls++
			if closeCalls == 1 {
				out := httptest.NewRecorder()
				f.handler.ServeHTTP(out, r)
				toolsSuccess(t, out)
				w.WriteHeader(502)
				w.Write([]byte(`{}`))
				return
			}
		}
		f.handler.ServeHTTP(w, r)
	})
	m := newSupervisedRuntime(h)
	codexRequest(m, "register", map[string]any{"instance_id": "close-retry", "pid": os.Getpid()}, codexTestToken)
	i := actorIdentity{Instance: "close-retry", Session: "s", Thread: "t"}
	toolsSuccess(t, codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "issue_claim", Params: runtimeJSON(map[string]string{"ref": uid})}, codexTestToken))
	p := map[string]any{"ref": uid, "reason": "audit-no-change", "message": "Verified generated fixture; close response loss requires receipt recovery without repeating the mutation.", "evidence": []any{map[string]string{"type": "no-change-audit", "rationale": "Generated fixture only; no product change required."}}}
	if w := codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "issue_close", Params: runtimeJSON(p)}, codexTestToken); w.Code != 502 {
		t.Fatal(w.Code)
	}
	m.threads[i].active.deadline = time.Now().Add(-time.Second)
	toolsSuccess(t, codexRequest(m, "event", supervisedEvent{Identity: i, Event: "stop_check"}, codexTestToken))
	toolsSuccess(t, codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "retry"}, codexTestToken))
	if closeCalls != 2 {
		t.Fatal("receipt retry missing")
	}
	w := codexRequest(m, "tool", supervisedCommand{Identity: i, Operation: "retry"}, codexTestToken)
	if !strings.Contains(w.Body.String(), `"client_operation":"issue_close"`) {
		t.Fatal("historical retry must identify the resolved close, not the previous claim")
	}
}
