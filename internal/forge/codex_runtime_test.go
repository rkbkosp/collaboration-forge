package forge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const codexTestToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func codexRequest(m http.Handler, op string, body any, token string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/forge/v1/codex/"+op, bytes.NewReader(b))
	r.Header.Set("X-Forge-Codex-Token", token)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
func TestCodexRuntimeRegistrationAndThreadFence(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"granted":true,"execution_token":"private-proof","execution_id":"private-execution","server_now":"2026-09-11T00:00:00Z","lease":{"issue_uid":"01K00000000000000000000002","claim_uid":"01K00000000000000000000003","expires_at":"2026-09-11T00:05:00Z"}}`))
	})
	m := newCodexRuntime(h)
	reg := map[string]any{"instance_id": "i1", "pid": os.Getpid()}
	if w := codexRequest(m, "register", reg, codexTestToken); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a := codexIdentity{"i1", "s1", "t1"}
	b := codexIdentity{"i1", "s1", "t2"}
	invoke := func(i codexIdentity, op string) *httptest.ResponseRecorder {
		return codexRequest(m, "tool", codexCommand{Identity: i, Operation: op, Params: json.RawMessage(`{"ref":"abcd"}`)}, codexTestToken)
	}
	if w := invoke(a, "issue_claim"); w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := invoke(b, "issue_close"); w.Code != 409 {
		t.Fatal("thread inherited claim", w.Code)
	}
	if w := codexRequest(m, "tool", codexCommand{Identity: a, Operation: "issue_release", Params: json.RawMessage(`{"ref":"abcd"}`)}, strings.Repeat("b", 64)); w.Code != 403 {
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
func TestCodexRuntimeRenewsWithoutClientTouchAndEndsNormally(t *testing.T) {
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
	m := newCodexRuntime(h)
	codexRequest(m, "register", map[string]any{"instance_id": "i", "pid": os.Getpid()}, codexTestToken)
	i := codexIdentity{"i", "s", "t"}
	w := codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_claim", Params: json.RawMessage(`{"ref":"abcd"}`)}, codexTestToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	m.threads[i].nextRenew = time.Now().Add(-time.Second)
	m.tick()
	if renew != 1 {
		t.Fatal("no daemon renewal")
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
	m := newCodexRuntime(f.handler)
	codexRequest(m, "register", map[string]any{"instance_id": "real", "pid": os.Getpid()}, codexTestToken)
	i := codexIdentity{"real", "session", "thread"}
	call := func(op string, p any) *httptest.ResponseRecorder {
		return codexRequest(m, "tool", codexCommand{Identity: i, Operation: op, Params: marshalCodex(p)}, codexTestToken)
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
func TestCodexEndDoesNotWaitForBusyThread(t *testing.T) {
	m := newCodexRuntime(http.NotFoundHandler())
	codexRequest(m, "register", map[string]any{"instance_id": "busy", "pid": os.Getpid()}, codexTestToken)
	i := codexIdentity{"busy", "s", "t"}
	codexRequest(m, "tool", codexCommand{Identity: i, Operation: "start"}, codexTestToken)
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
