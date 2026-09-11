package forge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Ephemeral coordination only. Kata remains the lease/issue/event authority.
// Restart rejects all old instance capabilities; no execution is restored.
type codexRuntime struct {
	mu        sync.Mutex
	instances map[string]*codexInstance
	threads   map[codexIdentity]*codexThread
	tools     http.Handler
	alive     func(int, string) bool
}
type codexInstance struct {
	token   [32]byte
	pid     int
	birth   string
	retired bool
	normal  bool
}
type codexThread struct {
	mu        sync.Mutex
	identity  codexIdentity
	instance  *codexInstance
	retired   bool
	release   bool
	lastSeen  time.Time
	nextRenew time.Time
	active    *codexTenure
	pending   *codexPending
}
type codexTenure struct {
	ref, alias, token, claim string
	deadline                 time.Time
}
type codexPending struct {
	op        string
	params    json.RawMessage
	signature string
	key       string
}
type codexCommand struct {
	Identity  codexIdentity   `json:"identity"`
	Operation string          `json:"operation"`
	Params    json.RawMessage `json:"params,omitempty"`
}
type codexAttributionKey struct{}

func newCodexRuntime(tools http.Handler) *codexRuntime {
	return &codexRuntime{instances: map[string]*codexInstance{}, threads: map[codexIdentity]*codexThread{}, tools: tools, alive: codexProcessAlive}
}
func codexNonce() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic("system randomness unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func codexReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func codexFail(w http.ResponseWriter, status int, code string) {
	toolError(status, code, code).serve(w)
}
func (m *codexRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		codexFail(w, 405, "method_not_allowed")
		return
	}
	token := r.Header.Get("X-Forge-Codex-Token")
	if len(r.Header.Values("X-Forge-Codex-Token")) != 1 || len(token) != 64 || strings.Trim(token, "0123456789abcdef") != "" {
		codexFail(w, 403, "invalid_instance")
		return
	}
	digest := sha256.Sum256([]byte(token))
	op := strings.TrimPrefix(r.URL.Path, "/forge/v1/codex/")
	if op == "register" {
		in, ok := decodeToolInput[struct {
			Instance string `json:"instance_id"`
			PID      int    `json:"pid"`
		}](w, r)
		if !ok {
			return
		}
		if !codexID(in.Instance) {
			codexFail(w, 400, "validation")
			return
		}
		birth, err := codexProcessBirth(in.PID)
		if err != nil {
			codexFail(w, 409, "process_unavailable")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		old := m.instances[in.Instance]
		if old != nil {
			if old.token != digest || old.pid != in.PID || old.birth != birth || old.retired {
				codexFail(w, 409, "instance_conflict")
				return
			}
		} else {
			if len(m.instances) >= 256 {
				codexFail(w, 429, "runtime_limit")
				return
			}
			m.instances[in.Instance] = &codexInstance{token: digest, pid: in.PID, birth: birth}
		}
		codexReply(w, map[string]any{"registered": true, "protocol": 1})
		return
	}
	if op == "end" {
		in, ok := decodeToolInput[struct {
			Instance string `json:"instance_id"`
			Normal   bool   `json:"normal"`
		}](w, r)
		if !ok {
			return
		}
		m.mu.Lock()
		instance := m.instances[in.Instance]
		if instance == nil || instance.token != digest {
			m.mu.Unlock()
			codexFail(w, 403, "invalid_instance")
			return
		}
		if !instance.retired {
			instance.normal = in.Normal
		}
		instance.retired = true
		m.mu.Unlock()
		codexReply(w, map[string]bool{"stopped": true})
		return
	}
	if op != "tool" {
		codexFail(w, 404, "unknown_operation")
		return
	}
	in, ok := decodeToolInput[codexCommand](w, r)
	if !ok {
		return
	}
	if !in.Identity.valid() {
		codexFail(w, 400, "validation")
		return
	}
	m.mu.Lock()
	instance := m.instances[in.Identity.Instance]
	if instance == nil || instance.token != digest {
		m.mu.Unlock()
		codexFail(w, 403, "invalid_instance")
		return
	}
	if instance.retired || !m.alive(instance.pid, instance.birth) {
		instance.retired = true
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
	m.mu.Lock()
	stopped := instance.retired
	m.mu.Unlock()
	if t.retired || stopped {
		codexFail(w, 409, "runtime_stopped")
		return
	}
	t.lastSeen = time.Now()
	m.execute(w, t, in.Operation, in.Params)
}
func (m *codexRuntime) dispatch(t *codexThread, op string, params json.RawMessage, token, key string) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, codexAttributionKey{}, t.identity)
	r, _ := http.NewRequestWithContext(ctx, "POST", "/forge/v1/tools/"+op, bytes.NewReader(params))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forge-Session", t.identity.runtime())
	if token != "" {
		r.Header.Set("X-Forge-Execution", token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	m.tools.ServeHTTP(w, r)
	return w
}
func marshalCodex(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func codexSafe(v any, secrets ...string) any {
	switch x := v.(type) {
	case map[string]any:
		r := map[string]any{}
		for k, v := range x {
			if k == "execution_token" || k == "execution_id" || k == "attempt_id" {
				continue
			}
			r[k] = codexSafe(v, secrets...)
		}
		return r
	case []any:
		for i, v := range x {
			x[i] = codexSafe(v, secrets...)
		}
		return x
	case string:
		for _, s := range secrets {
			if s != "" {
				x = strings.ReplaceAll(x, s, "[REDACTED]")
			}
		}
		return x
	}
	return v
}
func (m *codexRuntime) execute(w http.ResponseWriter, t *codexThread, op string, params json.RawMessage) {
	if op == "status" || op == "touch" || op == "start" {
		if t.active != nil && !time.Now().Before(t.active.deadline) {
			t.active = nil
		}
		codexReply(w, map[string]any{"active": t.active != nil, "pending": t.pending != nil, "issue": func() string {
			if t.active != nil {
				return t.active.ref
			}
			return ""
		}()})
		return
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var p map[string]any
	if json.Unmarshal(params, &p) != nil || p == nil {
		codexFail(w, 400, "validation")
		return
	}
	if _, ok := p["attempt_id"]; ok {
		codexFail(w, 400, "validation")
		return
	}
	if _, ok := p["ttl_seconds"]; ok {
		codexFail(w, 400, "validation")
		return
	}
	ref, _ := p["ref"].(string)
	signature := string(marshalCodex(p))
	token, key := "", ""
	execution := op == "issue_claim" || op == "issue_close" || op == "issue_release" || op == "issue_renew"
	if op == "retry" {
		if t.pending == nil {
			codexFail(w, 409, "no_pending")
			return
		}
		op = t.pending.op
		execution = true
		params = t.pending.params
		var original map[string]any
		_ = json.Unmarshal(params, &original)
		ref, _ = original["ref"].(string)
		key = t.pending.key
		if t.active != nil {
			token = t.active.token
		}
	} else if execution {
		if t.pending != nil {
			if t.pending.op != op || t.pending.signature != signature {
				codexFail(w, 409, "pending_operation")
				return
			}
			params = t.pending.params
			key = t.pending.key
			if t.active != nil {
				token = t.active.token
			}
		} else {
			if t.active != nil && !time.Now().Before(t.active.deadline) {
				t.active = nil
			}
			if op == "issue_claim" {
				if t.active != nil {
					codexFail(w, 409, "lease_active")
					return
				}
				p["attempt_id"] = codexNonce()
				p["ttl_seconds"] = 300
			} else {
				if t.active == nil || ref != t.active.ref && ref != t.active.alias {
					codexFail(w, 409, "lease_required")
					return
				}
				token = t.active.token
				p["ref"] = t.active.ref
				if op == "issue_close" {
					key = codexNonce()
				}
				if op == "issue_renew" {
					p["ttl_seconds"] = 300
				}
			}
			params = marshalCodex(p)
			t.pending = &codexPending{op: op, params: params, signature: signature, key: key}
		}
	} else {
		switch op {
		case "issue_list", "issue_get", "issue_graph", "issue_create", "issue_comment", "issue_link", "issue_timeline":
		default:
			codexFail(w, 404, "unknown_operation")
			return
		}
	}
	started := time.Now()
	res := m.dispatch(t, op, params, token, key)
	var body map[string]any
	err := json.Unmarshal(res.Body.Bytes(), &body)
	if err != nil || res.Code >= 500 {
		codexFail(w, 502, "codex_operation_unknown")
		return
	}
	if res.Code >= 300 {
		if execution {
			t.pending = nil
		}
		if op == "issue_renew" || op == "issue_release" {
			t.active = nil
		}
		// Keep close tenure for correcting evidence, but never retry a rejected key.
		codexFail(w, res.Code, "codex_operation_rejected")
		return
	}
	if op == "issue_claim" || op == "issue_renew" {
		lease, _ := body["lease"].(map[string]any)
		uid, _ := lease["issue_uid"].(string)
		claim, _ := lease["claim_uid"].(string)
		expiry, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lease["expires_at"]))
		serverNow, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(body["server_now"]))
		if op == "issue_claim" {
			token, _ = body["execution_token"].(string)
		}
		if body["granted"] == false {
			t.active = nil
			t.pending = nil
			codexReply(w, codexSafe(body, token))
			return
		}
		if body["granted"] != true || uid == "" || claim == "" || token == "" || serverNow.IsZero() || expiry.IsZero() {
			codexFail(w, 502, "codex_operation_unknown")
			return
		}
		if op == "issue_renew" && (t.active == nil || t.active.claim != claim || !started.Before(t.active.deadline)) {
			t.active = nil
			t.pending = nil
			codexFail(w, 409, "lease_lost")
			return
		}
		deadline := started.Add(min(expiry.Sub(serverNow), 300*time.Second) - time.Second)
		if !time.Now().Before(deadline) {
			t.active = nil
			t.pending = nil
			codexFail(w, 409, "lease_lost")
			return
		}
		alias := ref
		if t.active != nil {
			alias = t.active.alias
		}
		t.active = &codexTenure{ref: uid, alias: alias, claim: claim, token: token, deadline: deadline}
		t.nextRenew = time.Now().Add(30 * time.Second)
	}
	if op == "issue_close" {
		if _, ok := body["changed"].(bool); !ok || body["issue"] == nil {
			codexFail(w, 502, "codex_operation_unknown")
			return
		}
		t.active = nil
	}
	if op == "issue_release" {
		t.active = nil
	}
	if execution || op == "issue_claim" || op == "issue_close" {
		t.pending = nil
	}
	codexReply(w, codexSafe(body, token))
}
func (m *codexRuntime) tick() {
	m.mu.Lock()
	var ts []*codexThread
	for _, i := range m.instances {
		if !i.retired && !m.alive(i.pid, i.birth) {
			i.retired = true
		}
	}
	dead := map[*codexThread]bool{}
	normal := map[*codexThread]bool{}
	for _, t := range m.threads {
		ts = append(ts, t)
		dead[t] = t.instance.retired
		normal[t] = t.instance.normal
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func(t *codexThread) {
			defer wg.Done()
			t.mu.Lock()
			defer t.mu.Unlock()
			m.mu.Lock()
			deadNow, normalNow := t.instance.retired, t.instance.normal
			m.mu.Unlock()
			isDead, isNormal := dead[t] || deadNow, normal[t] || normalNow
			if isDead && !t.retired {
				t.release = isNormal
			}
			if isDead || time.Since(t.lastSeen) > 30*time.Minute {
				t.retired = true
			}
			if t.retired {
				if t.release && t.active != nil {
					m.dispatch(t, "issue_release", marshalCodex(map[string]string{"ref": t.active.ref, "reason": "codex_normal_end"}), t.active.token, "")
				}
				t.release = false
				t.active = nil
				t.pending = nil
				return
			}
			if t.active == nil {
				return
			}
			if !time.Now().Before(t.active.deadline) {
				t.active = nil
				return
			}
			if t.pending != nil && t.pending.op == "issue_close" {
				return
			}
			if !time.Now().Before(t.nextRenew) {
				m.execute(httptest.NewRecorder(), t, "issue_renew", marshalCodex(map[string]string{"ref": t.active.ref}))
				t.nextRenew = time.Now().Add(5 * time.Second)
			}
		}(t)
	}
	wg.Wait()
}
func (m *codexRuntime) run(ctx context.Context) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.tick()
		}
	}
}
