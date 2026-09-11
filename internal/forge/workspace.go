package forge

import (
	"encoding/hex"
	"encoding/json"
	"go.kenn.io/kata"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Workspace observations live in Kata comments/events. They are historical
// observations, not another execution authority or a substitute for close-v2.
type workspaceInput struct {
	Ref              string `json:"ref"`
	SnapshotID       string `json:"snapshot_id"`
	BaseCommit       string `json:"base_commit"`
	RepositoryID     string `json:"repository_id"`
	WorkspaceID      string `json:"workspace_id"`
	State            string `json:"state"`
	SourceKind       string `json:"source_kind"`
	Device           string `json:"device"`
	Location         string `json:"location"`
	SnapshotLocation string `json:"snapshot_location"`
}

func isHex(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && s == strings.ToLower(s)
}
func (h *toolHandler) workspace(w http.ResponseWriter, r *http.Request, runtime string, principal kata.Principal) {
	in, ok := decodeToolInput[workspaceInput](w, r)
	if !ok {
		return
	}
	if !isHex(in.SnapshotID, 32) || !(isHex(in.BaseCommit, 20) || isHex(in.BaseCommit, 32)) || !isHex(in.RepositoryID, 32) || !toolUUID(in.WorkspaceID, "47") || (in.State != "ready" && in.State != "failed") || (in.SourceKind != "dirty" && in.SourceKind != "commit") || len(in.Device) > 256 || len(in.Location) > 4096 || len(in.SnapshotLocation) > 4096 {
		toolError(400, "validation", "invalid workspace observation").serve(w)
		return
	}
	proof, ok := h.execution(w, r, runtime, in.Ref)
	if !ok {
		return
	}
	principal.Subject = proof.Subject
	// Read-only exact lease confirmation. The comment records an observation at
	// this point; a lease transition before comment commit remains legitimate
	// history. Current authority must always be read from Kata's live lease.
	base := "/api/v1/projects/" + strconv.FormatInt(h.project.ID, 10) + "/issues/" + proof.IssueUID
	status := h.dispatch(r.Context(), principal, "showIssue", http.MethodGet, base, nil, nil)
	if !status.success() {
		status.serve(w)
		return
	}
	var shown struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
		Lease *struct {
			ClaimUID   string     `json:"claim_uid"`
			ExpiresAt  *time.Time `json:"expires_at"`
			ReleasedAt *time.Time `json:"released_at"`
		} `json:"lease"`
	}
	if json.Unmarshal(status.body, &shown) != nil {
		toolError(502, "invalid_response", "invalid lease projection").serve(w)
		return
	}
	if shown.Issue.Status != "open" || shown.Lease == nil || shown.Lease.ClaimUID != proof.ClaimUID || shown.Lease.ReleasedAt != nil || shown.Lease.ExpiresAt == nil || !shown.Lease.ExpiresAt.After(time.Now()) {
		toolError(409, "claim_lost", "workspace requires current execution lease").serve(w)
		return
	}
	// Deterministic body/key make ambiguous response replay safe. No client can
	// choose the actor, claim binding, record kind or idempotency namespace.
	body, _ := json.Marshal(struct {
		Kind        string         `json:"kind"`
		Execution   string         `json:"execution_id"`
		Claim       string         `json:"claim_uid"`
		Observation workspaceInput `json:"workspace"`
	}{"forge.workspace.v1", proof.Subject, proof.ClaimUID, in})
	key := h.signer.digest("workspace-registration-v1", proof.Subject, in.WorkspaceID, in.State)
	h.dispatch(r.Context(), principal, "createComment", http.MethodPost, base+"/comments", toolCommentBody{Body: string(body)}, http.Header{"Idempotency-Key": {key}}).serve(w)
}
