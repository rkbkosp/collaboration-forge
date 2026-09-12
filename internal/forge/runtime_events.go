package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type supervisedEvent struct {
	Identity actorIdentity `json:"identity"`
	Event    string        `json:"event"`
}
type actorSessionKey struct{ instance, session string }

func (m *supervisedRuntime) event(h harnessProfile, w http.ResponseWriter, r *http.Request, digest [32]byte) {
	in, ok := decodeToolInput[supervisedEvent](w, r)
	if !ok {
		return
	}
	if !in.Identity.valid() {
		h.fail(w, 400, "validation")
		return
	}
	switch in.Event {
	case "start", "context", "touch", "prompt", "interrupt", "stop_check", "session_end", "pause":
	default:
		h.fail(w, 400, "unknown_event")
		return
	}
	m.mu.Lock()
	instance := m.instances[in.Identity.Instance]
	if instance == nil || instance.token != digest || instance.harness != h.Name {
		m.mu.Unlock()
		h.fail(w, 403, "invalid_instance")
		return
	}
	if in.Event == "session_end" {
		m.ended[actorSessionKey{in.Identity.Instance, in.Identity.Session}] = true
		m.mu.Unlock()
		runtimeReply(w, map[string]bool{"stopped": true})
		return
	}
	if instance.retired || !m.alive(instance.pid, instance.birth) || m.ended[actorSessionKey{in.Identity.Instance, in.Identity.Session}] {
		m.mu.Unlock()
		h.fail(w, 409, "runtime_stopped")
		return
	}
	t := m.threads[in.Identity]
	if t == nil {
		if len(m.threads) >= 4096 {
			m.mu.Unlock()
			h.fail(w, 429, "runtime_limit")
			return
		}
		t = &supervisedActor{identity: in.Identity, instance: instance, harness: h, lastSeen: time.Now()}
		m.threads[in.Identity] = t
	}
	m.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retired {
		h.fail(w, 409, "runtime_stopped")
		return
	}
	m.mu.Lock()
	stopped := instance.retired || m.ended[actorSessionKey{in.Identity.Instance, in.Identity.Session}]
	m.mu.Unlock()
	if stopped {
		h.fail(w, 409, "runtime_stopped")
		return
	}
	t.lastSeen = time.Now()
	if in.Event == "interrupt" || in.Event == "pause" {
		t.paused = true
		m.markWorkspaces(t, "paused")
	}
	if in.Event == "prompt" {
		t.paused = false
		m.refreshActor(t)
		if t.active != nil && !t.unknown {
			m.markWorkspaces(t, "ready")
		} else {
			m.markWorkspaces(t, "orphaned")
		}
	}
	if in.Event == "stop_check" || in.Event == "context" {
		m.refreshActor(t)
	}
	m.execute(w, t, "status", nil)
}

// Stop/context observations never perform acquire/renew and never erase a
// pending close receipt. Failure is unknown, not unclaimed.
func (m *supervisedRuntime) refreshActor(t *supervisedActor) {
	if t.active == nil || t.pending != nil && t.pending.op == "issue_close" {
		return
	}
	started := time.Now()
	res := m.dispatch(t, "issue_get", runtimeJSON(map[string]string{"ref": t.active.ref}), "", "")
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
	if t.workerSession != "" || deadline.Before(t.active.deadline) {
		t.active.deadline = deadline
	}
	if !time.Now().Before(t.active.deadline) {
		t.active = nil
	}
}
