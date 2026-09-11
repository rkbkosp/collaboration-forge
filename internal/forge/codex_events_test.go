package forge

import (
	"encoding/json"
	"net/http"
	"os"
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
