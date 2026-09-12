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
type supervisedRuntime struct {
	workspaceGates [64]sync.Mutex
	workspaces     *workspaceStore
	mu             sync.Mutex
	instances      map[string]*supervisedInstance
	threads        map[actorIdentity]*supervisedActor
	workers        map[actorIdentity]*supervisedActor
	ended          map[actorSessionKey]bool
	tools          http.Handler
	alive          func(int, string) bool
}
type supervisedInstance struct {
	token   [32]byte
	pid     int
	birth   string
	retired bool
	normal  bool
	ttl     int
	// harness labels the adapter that registered this instance, so a normal-end
	// release records which harness shut down.
	harness string
}
type supervisedActor struct {
	// Non-supervised adapter (the shared `forge` CLI): the caller owns renewal,
	// only the verified proof is kept during filesystem work. Never inserted
	// into the daemon renewal registry.
	workerSession                          string
	workspaceRequest, workspaceFingerprint string
	mu                                     sync.Mutex
	identity                               actorIdentity
	instance                               *supervisedInstance
	harness                                harnessProfile
	retired                                bool
	release                                bool
	paused                                 bool
	unknown                                bool
	lastSeen                               time.Time
	nextRenew                              time.Time
	active                                 *supervisedTenure
	pending                                *supervisedPending
	receipts                               map[string]*supervisedReceipt
	receiptOrder                           []string
	receiptBytes                           int
	lastExecution                          []byte
	lastOperation                          string
}
type supervisedTenure struct {
	ref, alias, token, claim string
	deadline                 time.Time
}
type supervisedPending struct {
	op        string
	params    json.RawMessage
	signature string
	key       string
	claim     string
	token     string
}
type supervisedCommand struct {
	RequestID string          `json:"request_id"`
	Identity  actorIdentity   `json:"identity"`
	Operation string          `json:"operation"`
	Params    json.RawMessage `json:"params,omitempty"`
}
type actorAttributionKey struct{}

func newSupervisedRuntime(tools http.Handler) *supervisedRuntime {
	return &supervisedRuntime{ended: map[actorSessionKey]bool{}, instances: map[string]*supervisedInstance{}, threads: map[actorIdentity]*supervisedActor{}, tools: tools, alive: processAlive}
}
func runtimeNonce() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic("system randomness unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func runtimeReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ServeHTTP serves every configured harness control namespace
// (/forge/v1/codex/*, /forge/v1/claude/*) from the one ephemeral runtime.
func (m *supervisedRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h, op := harnessForPath(r.URL.Path)
	m.serveProfile(h, op, w, r)
}

func (m *supervisedRuntime) serveProfile(h harnessProfile, op string, w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		h.fail(w, 405, "method_not_allowed")
		return
	}
	values := r.Header.Values(h.CapabilityHeader)
	token := r.Header.Get(h.CapabilityHeader)
	if len(values) != 1 || len(token) != 64 || strings.Trim(token, "0123456789abcdef") != "" {
		h.fail(w, 403, "invalid_instance")
		return
	}
	digest := sha256.Sum256([]byte(token))
	if op == "register" {
		in, ok := decodeToolInput[struct {
			Instance string `json:"instance_id"`
			PID      int    `json:"pid"`
			TTL      *int   `json:"ttl_seconds,omitempty"`
		}](w, r)
		if !ok {
			return
		}
		if !attributionID(in.Instance) {
			h.fail(w, 400, "validation")
			return
		}
		ttl, validTTL := toolBoundedInt(in.TTL, 300, 60, 3600)
		if !validTTL {
			h.fail(w, 400, "validation")
			return
		}
		birth, err := processBirth(in.PID)
		if err != nil {
			h.fail(w, 409, "process_unavailable")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		old := m.instances[in.Instance]
		if old != nil {
			if old.token != digest || old.pid != in.PID || old.birth != birth || old.ttl != ttl || old.retired || old.harness != h.Name {
				h.fail(w, 409, "instance_conflict")
				return
			}
		} else {
			if len(m.instances) >= 256 {
				h.fail(w, 429, "runtime_limit")
				return
			}
			m.instances[in.Instance] = &supervisedInstance{token: digest, pid: in.PID, birth: birth, ttl: ttl, harness: h.Name}
		}
		reply := map[string]any{"registered": true, "protocol": 1}
		if m.workspaces != nil {
			reply["workspace_root"] = m.workspaces.root
			reply["project_uid"] = m.workspaces.project
		}
		runtimeReply(w, reply)
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
			h.fail(w, 403, "invalid_instance")
			return
		}
		if !instance.retired {
			instance.normal = in.Normal
		}
		instance.retired = true
		m.mu.Unlock()
		runtimeReply(w, map[string]bool{"stopped": true})
		return
	}
	if op == "event" {
		m.event(h, w, r, digest)
		return
	}
	if op != "tool" {
		h.fail(w, 404, "unknown_operation")
		return
	}
	in, ok := decodeToolInput[supervisedCommand](w, r)
	if !ok {
		return
	}
	if !in.Identity.valid() {
		h.fail(w, 400, "validation")
		return
	}
	// An instance capability authorizes only the namespace that registered it, so
	// a caller cannot relabel its own attribution by switching control paths.
	m.mu.Lock()
	instance := m.instances[in.Identity.Instance]
	if instance == nil || instance.token != digest || instance.harness != h.Name {
		m.mu.Unlock()
		h.fail(w, 403, "invalid_instance")
		return
	}
	if instance.retired || m.ended[actorSessionKey{in.Identity.Instance, in.Identity.Session}] || !m.alive(instance.pid, instance.birth) {
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
	m.mu.Lock()
	stopped := instance.retired || m.ended[actorSessionKey{in.Identity.Instance, in.Identity.Session}]
	m.mu.Unlock()
	if t.retired || stopped {
		h.fail(w, 409, "runtime_stopped")
		return
	}
	t.lastSeen = time.Now()
	m.command(h, w, t, in)
}
func (m *supervisedRuntime) dispatch(t *supervisedActor, op string, params json.RawMessage, token, key string) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if t.workerSession == "" {
		ctx = context.WithValue(ctx, actorAttributionKey{}, actorAttribution{harness: t.harness, identity: t.identity})
	}
	r, _ := http.NewRequestWithContext(ctx, "POST", "/forge/v1/tools/"+op, bytes.NewReader(params))
	r.Header.Set("Content-Type", "application/json")
	session := t.workerSession
	if session == "" {
		session = t.identity.runtime()
	}
	r.Header.Set("X-Forge-Session", session)
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
func runtimeJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func runtimeSafe(v any, secrets ...string) any {
	switch x := v.(type) {
	case map[string]any:
		r := map[string]any{}
		for k, v := range x {
			if k == "execution_token" || k == "execution_id" || k == "attempt_id" {
				continue
			}
			r[k] = runtimeSafe(v, secrets...)
		}
		return r
	case []any:
		for i, v := range x {
			x[i] = runtimeSafe(v, secrets...)
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
func (m *supervisedRuntime) execute(w http.ResponseWriter, t *supervisedActor, op string, params json.RawMessage) {
	h := t.harness
	if strings.HasPrefix(op, "checkout") {
		m.checkoutCommand(w, t, op, params)
		return
	}
	if op == "issue_close" && m.checkoutBusy(t) {
		h.fail(w, 409, "checkout_busy")
		return
	}
	if op == "status" || op == "touch" || op == "start" {
		if t.active != nil && !time.Now().Before(t.active.deadline) {
			t.active = nil
		}
		runtimeReply(w, map[string]any{"workspaces": m.threadWorkspaces(t), "active": t.active != nil, "pending": t.pending != nil, "paused": t.paused, "unknown": t.unknown, "issue": func() string {
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
		h.fail(w, 400, "validation")
		return
	}
	if _, ok := p["attempt_id"]; ok {
		h.fail(w, 400, "validation")
		return
	}
	if _, ok := p["ttl_seconds"]; ok {
		h.fail(w, 400, "validation")
		return
	}
	ref, _ := p["ref"].(string)
	signature := string(runtimeJSON(p))
	token, key := "", ""
	execution := op == "issue_claim" || op == "issue_close" || op == "issue_release" || op == "issue_renew"
	if op == "retry" {
		if t.pending == nil {
			h.fail(w, 409, "no_pending")
			return
		}
		op = t.pending.op
		execution = true
		params = t.pending.params
		var original map[string]any
		_ = json.Unmarshal(params, &original)
		ref, _ = original["ref"].(string)
		key = t.pending.key
		token = t.pending.token
	} else if execution {
		if t.pending != nil {
			if t.pending.op != op || t.pending.signature != signature {
				h.fail(w, 409, "pending_operation")
				return
			}
			params = t.pending.params
			key = t.pending.key
			token = t.pending.token
		} else {
			if t.active != nil && !time.Now().Before(t.active.deadline) {
				t.active = nil
			}
			if op == "issue_claim" {
				if t.active != nil {
					h.fail(w, 409, "lease_active")
					return
				}
				p["attempt_id"] = runtimeNonce()
				p["ttl_seconds"] = t.instance.ttl
			} else {
				if t.active == nil || ref != t.active.ref && ref != t.active.alias {
					h.fail(w, 409, "lease_required")
					return
				}
				token = t.active.token
				p["ref"] = t.active.ref
				if op == "issue_close" {
					key = runtimeNonce()
				}
				if op == "issue_renew" {
					p["ttl_seconds"] = t.instance.ttl
				}
			}
			params = runtimeJSON(p)
			t.pending = &supervisedPending{op: op, params: params, signature: signature, key: key, token: token}
			if t.active != nil {
				t.pending.claim = t.active.claim
			}
		}
	} else {
		switch op {
		case "issue_list", "issue_get", "issue_graph", "issue_create", "issue_comment", "issue_link", "issue_timeline":
		default:
			h.fail(w, 404, "unknown_operation")
			return
		}
	}
	started := time.Now()
	res := m.dispatch(t, op, params, token, key)
	var body map[string]any
	err := json.Unmarshal(res.Body.Bytes(), &body)
	if err != nil {
		h.fail(w, 502, "codex_operation_unknown")
		return
	}
	if res.Code >= 500 {
		forwardForgeError(w, res.Code, res.Body.Bytes(), "codex_operation_unknown", "Codex operation outcome is unknown")
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
		forwardForgeError(w, res.Code, res.Body.Bytes(), "codex_operation_rejected", "Codex operation was rejected")
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
			runtimeReply(w, runtimeSafe(body, token))
			return
		}
		if body["granted"] != true || uid == "" || claim == "" || token == "" || serverNow.IsZero() || expiry.IsZero() {
			h.fail(w, 502, "codex_operation_unknown")
			return
		}
		if op == "issue_renew" && (t.active == nil || t.active.claim != claim || !started.Before(t.active.deadline)) {
			t.active = nil
			t.pending = nil
			h.fail(w, 409, "lease_lost")
			return
		}
		deadline := started.Add(min(expiry.Sub(serverNow), time.Duration(t.instance.ttl)*time.Second) - time.Second)
		if !time.Now().Before(deadline) {
			t.active = nil
			t.pending = nil
			h.fail(w, 409, "lease_lost")
			return
		}
		alias := ref
		if t.active != nil {
			alias = t.active.alias
		}
		t.unknown = false
		if op == "issue_claim" {
			m.markWorkspaces(t, "orphaned")
			t.paused = false
		}
		t.active = &supervisedTenure{ref: uid, alias: alias, claim: claim, token: token, deadline: deadline}
		t.nextRenew = time.Now().Add(min(30*time.Second, time.Duration(t.instance.ttl)*time.Second/3))
	}
	if op == "issue_close" {
		if _, ok := body["changed"].(bool); !ok || body["issue"] == nil {
			h.fail(w, 502, "codex_operation_unknown")
			return
		}
		if t.pending != nil {
			body["workspace_archive"] = m.archiveWorkspaces(t, t.pending.claim)
		}
		t.active = nil
		t.unknown = false
	}
	if op == "issue_release" {
		m.markWorkspaces(t, "orphaned")
		t.active = nil
		t.unknown = false
	}
	if execution || op == "issue_claim" || op == "issue_close" {
		t.pending = nil
	}
	runtimeReply(w, runtimeSafe(body, token))
}
func (m *supervisedRuntime) tick() {
	m.mu.Lock()
	var ts []*supervisedActor
	for _, i := range m.instances {
		if !i.retired && !m.alive(i.pid, i.birth) {
			i.retired = true
		}
	}
	dead := map[*supervisedActor]bool{}
	normal := map[*supervisedActor]bool{}
	for _, t := range m.threads {
		ts = append(ts, t)
		dead[t] = t.instance.retired || m.ended[actorSessionKey{t.identity.Instance, t.identity.Session}]
		normal[t] = t.instance.normal || m.ended[actorSessionKey{t.identity.Instance, t.identity.Session}]
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func(t *supervisedActor) {
			defer wg.Done()
			t.mu.Lock()
			defer t.mu.Unlock()
			m.mu.Lock()
			ended := m.ended[actorSessionKey{t.identity.Instance, t.identity.Session}]
			deadNow, normalNow := t.instance.retired || ended, t.instance.normal || ended
			m.mu.Unlock()
			isDead, isNormal := dead[t] || deadNow, normal[t] || normalNow
			if isDead && !t.retired {
				t.release = isNormal
			}
			if isDead || time.Since(t.lastSeen) > 30*time.Minute {
				t.retired = true
			}
			if t.retired {
				m.markWorkspaces(t, "orphaned")
				if t.release && t.active != nil {
					m.dispatch(t, "issue_release", runtimeJSON(map[string]string{"ref": t.active.ref, "reason": t.harness.releaseReason()}), t.active.token, "")
				}
				t.release = false
				t.active = nil
				t.pending = nil
				return
			}
			if t.active == nil || t.paused {
				if t.active == nil && (t.pending == nil || t.pending.op != "issue_close") {
					m.markWorkspaces(t, "orphaned")
				}
				return
			}
			if !time.Now().Before(t.active.deadline) {
				m.markWorkspaces(t, "orphaned")
				t.active = nil
				return
			}
			if t.pending != nil && t.pending.op == "issue_close" {
				return
			}
			if !time.Now().Before(t.nextRenew) {
				t.nextRenew = time.Now().Add(5 * time.Second)
				m.execute(httptest.NewRecorder(), t, "issue_renew", runtimeJSON(map[string]string{"ref": t.active.ref}))
			}
		}(t)
	}
	wg.Wait()
}
func (m *supervisedRuntime) run(ctx context.Context) {
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
