package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type codexEvent struct {
	Identity codexIdentity `json:"identity"`
	Event    string        `json:"event"`
}
type codexSessionKey struct{ instance, session string }

func (m *codexRuntime) event(w http.ResponseWriter, r *http.Request, digest [32]byte) {
	in, ok := decodeToolInput[codexEvent](w, r)
	if !ok {
		return
	}
	if !in.Identity.valid() {
		codexFail(w, 400, "validation")
		return
	}
	switch in.Event {
	case "start", "context", "touch", "prompt", "interrupt", "stop_check", "session_end", "pause":
	default:
		codexFail(w, 400, "unknown_event")
		return
	}
	m.mu.Lock()
	instance := m.instances[in.Identity.Instance]
	if instance == nil || instance.token != digest {
		m.mu.Unlock()
		codexFail(w, 403, "invalid_instance")
		return
	}
	if in.Event == "session_end" {
		m.ended[codexSessionKey{in.Identity.Instance, in.Identity.Session}] = true
		m.mu.Unlock()
		codexReply(w, map[string]bool{"stopped": true})
		return
	}
	if instance.retired || !m.alive(instance.pid, instance.birth) || m.ended[codexSessionKey{in.Identity.Instance, in.Identity.Session}] {
		m.mu.Unlock()
		codexFail(w, 409, "runtime_stopped")
		return
	}
	t := m.threads[in.Identity]
	if t == nil {
		if len(m.threads) >= 4096 {
			m.mu.Unlock()
			codexFail(w, 429, "runtime_limit")
			return
		}
		t = &codexThread{identity: in.Identity, instance: instance, lastSeen: time.Now()}
		m.threads[in.Identity] = t
	}
	m.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retired {
		codexFail(w, 409, "runtime_stopped")
		return
	}
	m.mu.Lock()
	stopped := instance.retired || m.ended[codexSessionKey{in.Identity.Instance, in.Identity.Session}]
	m.mu.Unlock()
	if stopped {
		codexFail(w, 409, "runtime_stopped")
		return
	}
	t.lastSeen = time.Now()
	if in.Event == "interrupt" || in.Event == "pause" {
		t.paused = true
		m.markWorkspaces(t, "paused")
	}
	if in.Event == "prompt" {
		t.paused = false
		m.refreshCodex(t)
		if t.active != nil && !t.unknown {
			m.markWorkspaces(t, "ready")
		} else {
			m.markWorkspaces(t, "orphaned")
		}
	}
	if in.Event == "stop_check" || in.Event == "context" {
		m.refreshCodex(t)
	}
	m.execute(w, t, "status", nil)
}

// Stop/context observations never perform acquire/renew and never erase a
// pending close receipt. Failure is unknown, not unclaimed.
func (m *codexRuntime) refreshCodex(t *codexThread) {
	if t.active == nil || t.pending != nil && t.pending.op == "issue_close" {
		return
	}
	started := time.Now()
	res := m.dispatch(t, "issue_get", marshalCodex(map[string]string{"ref": t.active.ref}), "", "")
	var body map[string]any
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &body) != nil {
		t.unknown = true
		return
	}
	issue, ok := body["issue"].(map[string]any)
	if !ok || issue["uid"] != t.active.ref {
		t.unknown = true
		return
	}
	t.unknown = false
	lease, _ := body["lease"].(map[string]any)
	if lease["claim_uid"] != t.active.claim || lease["issue_uid"] != t.active.ref || issue["status"] != "open" {
		t.active = nil
		return
	}
	expires, e1 := time.Parse(time.RFC3339Nano, fmt.Sprint(lease["expires_at"]))
	now, e2 := time.Parse(time.RFC3339Nano, fmt.Sprint(body["lease_hub_now"]))
	if e1 != nil || e2 != nil {
		t.unknown = true
		return
	}
	deadline := started.Add(expires.Sub(now) - time.Second)
	if deadline.Before(t.active.deadline) {
		t.active.deadline = deadline
	}
	if !time.Now().Before(t.active.deadline) {
		t.active = nil
	}
}
