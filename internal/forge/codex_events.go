package forge

import (
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
	t.lastSeen = time.Now()
	if in.Event == "interrupt" || in.Event == "pause" {
		t.paused = true
	}
	if in.Event == "prompt" {
		t.paused = false
	}
	m.execute(w, t, "status", nil)
}
