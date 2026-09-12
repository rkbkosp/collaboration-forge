package forge

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"time"
)

// Bound memory independently of the number of issues ever closed. Serialize
// create/close/release for a tenure even before its first artifact exists.
func (m *supervisedRuntime) lockWorkerWorkspace(proof executionProof) func() {
	hash := sha256.Sum256([]byte(proof.ClaimUID))
	gate := &m.workspaceGates[int(hash[0])%len(m.workspaceGates)]
	gate.Lock()
	return gate.Unlock
}

// The adapter uses an existing signed tenure. It never acquires, renews or
// restores authority from a workspace record. CLI and Pi still own lifecycle.
func (m *supervisedRuntime) workerThread(session string, proof executionProof) *supervisedActor {
	id := actorIdentity{Instance: "worker", Session: session, Thread: workspaceTenure(proof.ClaimUID)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[actorIdentity]*supervisedActor{}
	}
	if t := m.workers[id]; t != nil {
		return t
	}
	if len(m.workers) >= 4096 {
		return nil
	}
	t := &supervisedActor{identity: id, workerSession: session}
	m.workers[id] = t
	return t
}

func (m *supervisedRuntime) hasWorkerWorkspaces(session string, proof executionProof) bool {
	if m.workspaces == nil {
		return false
	}
	owner := actorIdentity{Instance: "worker", Session: session, Thread: workspaceTenure(proof.ClaimUID)}
	for _, r := range m.workspaces.list(proof.IssueUID) {
		if r.Owner == owner {
			return true
		}
	}
	return false
}

func (h *toolHandler) workerCheckout(w http.ResponseWriter, r *http.Request, session, op string) {
	m := h.checkouts
	if m == nil || m.workspaces == nil {
		toolError(503, "checkout_unavailable", "Deploy matching Forge CLI and daemon before checkout").serve(w)
		return
	}
	in, ok := decodeToolInput[json.RawMessage](w, r)
	if !ok {
		return
	}
	// Read/archive operations carry no authority; archive independently verifies
	// that the authoritative issue is closed.
	if op != "checkout" {
		m.checkoutCommand(w, &supervisedActor{workerSession: session}, op, in)
		return
	}
	var params struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(in, &params) != nil {
		toolError(400, "validation", "invalid checkout input").serve(w)
		return
	}
	proof, ok := h.execution(w, r, session, params.Ref)
	if !ok {
		return
	}
	unlock := m.lockWorkerWorkspace(proof)
	defer unlock()
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !toolUUID(key, "47") {
		toolError(400, "request_id_required", "checkout requires a stable request UUID").serve(w)
		return
	}
	t := m.workerThread(session, proof)
	if t == nil {
		toolError(429, "workspace_limit", "workspace runtime limit").serve(w)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	request := h.signer.digest("checkout-request-v1", session, proof.ClaimUID, key)
	var canonical any
	json.Unmarshal(in, &canonical)
	fingerprint := workspaceTenure(string(runtimeJSON(canonical)))
	for _, record := range m.workspaces.list(proof.IssueUID) {
		if record.Request != request {
			continue
		}
		if record.Fingerprint != fingerprint {
			toolError(409, "request_id_conflict", "checkout request ID was reused with different parameters").serve(w)
			return
		}
		runtimeReply(w, record)
		return
	}
	t.active = &supervisedTenure{ref: proof.IssueUID, alias: proof.IssueUID, claim: proof.ClaimUID, token: r.Header.Get("X-Forge-Execution"), deadline: time.Now()}
	t.workspaceRequest, t.workspaceFingerprint = request, fingerprint
	m.checkoutCommand(w, t, op, in)
}

func (m *supervisedRuntime) workerCheckoutBusy(proof executionProof) bool {
	if m.workspaces == nil {
		return false
	}
	for _, r := range m.workspaces.list(proof.IssueUID) {
		if r.Tenure == workspaceTenure(proof.ClaimUID) && r.State == "preparing" {
			return true
		}
	}
	return false
}
func (m *supervisedRuntime) retireWorkerWorkspaces(session string, proof executionProof) {
	t := m.workerThread(session, proof)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = nil
	m.markWorkspaces(t, "orphaned")
}
