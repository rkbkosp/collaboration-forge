package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"go.kenn.io/kata"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCodexEventsScopeCompactionAndDelayedShutdown(t *testing.T) {
	m := newCodexRuntime(http.NotFoundHandler())
	codexRequest(m, "register", map[string]any{"instance_id": "i", "pid": os.Getpid()}, codexTestToken)
	a, b := codexIdentity{"i", "s1", "a"}, codexIdentity{"i", "s2", "b"}
	for _, i := range []codexIdentity{a, b} {
		toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "start"}, codexTestToken))
	}
	event := func(i codexIdentity, e string) {
		toolsSuccess(t, codexRequest(m, "event", codexEvent{Identity: i, Event: e}, codexTestToken))
	}
	m.threads[a].pending = &codexPending{op: "issue_claim", params: json.RawMessage(`{}`)}
	event(a, "context")
	if m.threads[a].pending == nil {
		t.Fatal("compaction erased retry")
	}
	event(a, "stop_check")
	if m.threads[a].retired {
		t.Fatal("Stop prematurely retired thread")
	}
	event(a, "session_end")
	if w := codexRequest(m, "tool", codexCommand{Identity: a, Operation: "touch"}, codexTestToken); w.Code != 409 {
		t.Fatal("ended session still accepted")
	}
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: b, Operation: "touch"}, codexTestToken))
	event(a, "session_end")
}
func TestCodexStopChecksExactLeaseAndPreservesPendingClose(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Stop lease freshness")
	m := newCodexRuntime(f.handler)
	codexRequest(m, "register", map[string]any{"instance_id": "stop", "pid": os.Getpid()}, codexTestToken)
	i := codexIdentity{"stop", "s", "t"}
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_claim", Params: marshalCodex(map[string]string{"ref": uid})}, codexTestToken))
	// Force-release via the public service route under the fixture's supervisor grant.
	r := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/projects/%d/issues/%s/lease/actions/force_release", f.project.ID, uid), strings.NewReader(`{"reason":"fixture"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(kata.WithPrincipal(context.WithValue(r.Context(), toolsSeedKey{}, true), kata.Principal{Subject: "host", Actor: "Human"}))
	w := httptest.NewRecorder()
	f.native.ServeHTTP(w, r)
	toolsSuccess(t, w)
	w = codexRequest(m, "event", codexEvent{Identity: i, Event: "stop_check"}, codexTestToken)
	toolsSuccess(t, w)
	if m.threads[i].active != nil {
		t.Fatal("Stop believed stale lease was live")
	}
	m.threads[i].pending = &codexPending{op: "issue_close", key: "original"}
	w = codexRequest(m, "event", codexEvent{Identity: i, Event: "stop_check"}, codexTestToken)
	toolsSuccess(t, w)
	if m.threads[i].pending == nil || m.threads[i].pending.key != "original" {
		t.Fatal("Stop erased receipt retry")
	}
}
