package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"go.kenn.io/kata"
)

const (
	toolBodyLimit     = 1 << 20
	toolResponseLimit = 8 << 20
)

type toolHandler struct {
	handler http.Handler
	project kata.Project
	signer  executionSigner
}

// newToolHandler is an in-process typed façade, not an authentication boundary.
// Its host must authenticate worker/admin Bearers before entering this handler.
func newToolHandler(handler http.Handler, project kata.Project, signer executionSigner) http.Handler {
	return &toolHandler{handler: handler, project: project, signer: signer}
}

func (h *toolHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/forge/v1/tools/"
	if !strings.HasPrefix(r.URL.Path, prefix) || strings.Contains(strings.TrimPrefix(r.URL.Path, prefix), "/") {
		toolError(http.StatusNotFound, "not_found", "unknown tool operation").serve(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		toolError(http.StatusMethodNotAllowed, "method_not_allowed", "tools require POST").serve(w)
		return
	}
	runtime := r.Header.Get("X-Forge-Session")
	if len(r.Header.Values("X-Forge-Session")) != 1 || !toolUUID(runtime, "47") {
		toolError(http.StatusBadRequest, "validation", "X-Forge-Session must be a Pi session UUIDv4 or UUIDv7").serve(w)
		return
	}
	// A distinct execution Subject controls the lease. The display actor stays
	// session-stable so comments and execution events correlate. Pi must never
	// restore an old attempt into a new runtime, even for the same session.
	session := h.signer.session(runtime)
	principal := kata.Principal{Subject: "session:" + session, Actor: "Agent/" + session[:12]}
	if _, ok := r.Context().Value(codexAttributionKey{}).(codexIdentity); ok {
		principal.Actor = "Codex/" + session[:12]
	}
	h.invoke(w, r, strings.TrimPrefix(r.URL.Path, prefix), runtime, principal)
}

func (h *toolHandler) invoke(w http.ResponseWriter, r *http.Request, operation, runtime string, principal kata.Principal) {
	base := "/api/v1/projects/" + strconv.FormatInt(h.project.ID, 10) + "/issues"
	badInput := func() { toolError(http.StatusBadRequest, "validation", "invalid tool input").serve(w) }
	forward := func(op, method, path string, body any, headers http.Header) {
		h.dispatch(r.Context(), principal, op, method, path, body, headers).serve(w)
	}
	switch operation {
	case "issue_workspace":
		h.workspace(w, r, runtime, principal)
	case "issue_list":
		in, ok := decodeToolInput[issueListInput](w, r)
		if !ok {
			return
		}
		limit, ok := toolBoundedInt(in.Limit, 100, 1, 1000)
		if !ok || (in.Status != "" && in.Status != "open" && in.Status != "closed") {
			badInput()
			return
		}
		query := url.Values{"limit": {strconv.Itoa(limit)}}
		if in.Status != "" {
			query.Set("status", in.Status)
		}
		forward("listIssues", http.MethodGet, base+"?"+query.Encode(), nil, nil)
	case "issue_timeline":
		in, ok := decodeToolInput[issueTimelineInput](w, r)
		if !ok {
			return
		}
		h.timeline(r.Context(), principal, in).serve(w)
	case "issue_get":
		in, ok := decodeToolInput[issueGetInput](w, r)
		if !ok {
			return
		}
		if !toolRef(in.Ref) {
			badInput()
			return
		}
		forward("showIssue", http.MethodGet, base+"/"+in.Ref, nil, nil)
	case "issue_graph":
		in, ok := decodeToolInput[issueGraphInput](w, r)
		if !ok {
			return
		}
		depth, ok := toolBoundedInt(in.Depth, 2, 1, 10)
		if !ok || !toolRef(in.Ref) {
			badInput()
			return
		}
		forward("reachableIssueGraph", http.MethodGet, base+"/"+in.Ref+"/graph?depth="+strconv.Itoa(depth), nil, nil)
	case "issue_create":
		in, ok := decodeToolInput[issueCreateInput](w, r)
		if !ok {
			return
		}
		forward("createIssue", http.MethodPost, base, in, toolOptionalIdempotency(r))
	case "issue_comment":
		in, ok := decodeToolInput[issueCommentInput](w, r)
		if !ok {
			return
		}
		if !toolRef(in.Ref) {
			badInput()
			return
		}
		forward("createComment", http.MethodPost, base+"/"+in.Ref+"/comments", toolCommentBody{Body: in.Body}, toolOptionalIdempotency(r))
	case "issue_link":
		in, ok := decodeToolInput[issueLinkInput](w, r)
		if !ok {
			return
		}
		if !toolRef(in.Ref) || !toolRef(in.ToRef) || (in.Type != "related" && in.Type != "blocks" && in.Type != "parent") {
			badInput()
			return
		}
		forward("createLink", http.MethodPost, base+"/"+in.Ref+"/links", toolLinkBody{Type: in.Type, ToRef: in.ToRef}, nil)
	case "issue_claim":
		in, ok := decodeToolInput[issueClaimInput](w, r)
		if !ok {
			return
		}
		ttl, ok := toolBoundedInt(in.TTLSeconds, 300, 60, 3600)
		if !ok || !toolRef(in.Ref) || !toolUUID(in.AttemptID, "47") {
			badInput()
			return
		}
		// Resolve short IDs before deriving the tenure identity, so a lost
		// acquire response can be retried by either ref with the same attempt.
		shown := h.dispatch(r.Context(), principal, "showIssue", http.MethodGet, base+"/"+in.Ref, nil, nil)
		if !shown.success() {
			shown.serve(w)
			return
		}
		var projection struct {
			Issue struct {
				UID    string `json:"uid"`
				Status string `json:"status"`
			} `json:"issue"`
		}
		if json.Unmarshal(shown.body, &projection) != nil || !toolCanonicalUID(projection.Issue.UID) {
			toolError(http.StatusBadGateway, "invalid_response", "Kata returned an invalid issue identity").serve(w)
			return
		}
		if projection.Issue.Status != "open" {
			toolError(http.StatusConflict, "issue_not_open", "execution requires an open issue").serve(w)
			return
		}
		uid := projection.Issue.UID
		principal.Subject = h.signer.identity(runtime, in.AttemptID, uid)
		// Kata's claim event actor is its opaque holder identity. Preserve a
		// server-derived link to the same actor shown on comments and close,
		// without exposing the private logical-acquire nonce or token.
		purpose := principal.Actor + " [Pi session " + runtime + "]"
		if id, ok := r.Context().Value(codexAttributionKey{}).(codexIdentity); ok {
			purpose = principal.Actor + " [Codex session " + id.Session + ", thread " + id.Thread + ", instance " + id.Instance + "]"
		}
		if in.Purpose != "" {
			purpose += ": " + in.Purpose
		}
		result := h.dispatch(r.Context(), principal, "acquireIssueLease", http.MethodPost, base+"/"+uid+"/lease/actions/acquire", toolTimedLeaseBody{ClaimKind: "timed", TTLSeconds: ttl, Purpose: purpose}, nil)
		if !result.success() {
			result.serve(w)
			return
		}
		var claim struct {
			Granted bool `json:"granted"`
			Lease   *struct {
				ClaimUID string `json:"claim_uid"`
				IssueUID string `json:"issue_uid"`
			} `json:"lease"`
		}
		if json.Unmarshal(result.body, &claim) != nil {
			toolError(http.StatusBadGateway, "invalid_response", "Kata returned an invalid lease result").serve(w)
			return
		}
		if !claim.Granted {
			result.status = http.StatusConflict
			result.withFields(map[string]any{"status": http.StatusConflict, "error": map[string]string{"code": "claim_denied", "message": "another execution holds this issue"}}).serve(w)
			return
		}
		if claim.Lease == nil || !toolCanonicalUID(claim.Lease.ClaimUID) || claim.Lease.IssueUID != uid {
			toolError(http.StatusBadGateway, "invalid_response", "Kata returned an invalid lease identity").serve(w)
			return
		}
		// Legacy Kata can acquire a lease on an already closed issue. A host
		// close may race the initial show; reconcile after acquire and release
		// this principal's lease rather than grant execution on closed work.
		confirmed := h.dispatch(r.Context(), principal, "showIssue", http.MethodGet, base+"/"+uid, nil, nil)
		if !confirmed.success() {
			confirmed.serve(w)
			return
		}
		if json.Unmarshal(confirmed.body, &projection) != nil {
			toolError(http.StatusBadGateway, "invalid_response", "Kata returned an invalid issue projection").serve(w)
			return
		}
		if projection.Issue.Status != "open" {
			released := h.dispatch(r.Context(), principal, "releaseIssueLease", http.MethodPost, base+"/"+uid+"/lease/actions/release", toolReleaseBody{Reason: "issue_not_open"}, nil)
			toolError(http.StatusConflict, "issue_not_open", "issue closed while execution was being acquired").withFields(map[string]any{"cleanup_pending": !released.success()}).serve(w)
			return
		}
		// Kata hashes Subject into a host lease holder; never compare that
		// holder to the original Subject or rebuild Kata's private hash here.
		proof := executionProof{Subject: principal.Subject, Session: h.signer.session(runtime), IssueUID: uid, ClaimUID: claim.Lease.ClaimUID}
		result.withFields(map[string]any{"execution_token": h.signer.sign(proof), "execution_id": principal.Subject, "server_now": time.Now().UTC()}).serve(w)
	case "issue_renew":
		in, ok := decodeToolInput[issueRenewInput](w, r)
		if !ok {
			return
		}
		ttl, ok := toolBoundedInt(in.TTLSeconds, 300, 60, 3600)
		if !ok || !toolRef(in.Ref) {
			badInput()
			return
		}
		proof, ok := h.execution(w, r, runtime, in.Ref)
		if !ok {
			return
		}
		principal.Subject = proof.Subject
		result := h.dispatch(r.Context(), principal, "renewIssueLease", http.MethodPost, base+"/"+proof.IssueUID+"/lease/actions/renew", toolTimedLeaseBody{ClaimKind: "timed", TTLSeconds: ttl}, nil)
		if result.success() {
			result = result.withFields(map[string]any{"server_now": time.Now().UTC()})
		}
		result.serve(w)
	case "issue_release":
		in, ok := decodeToolInput[issueReleaseInput](w, r)
		if !ok {
			return
		}
		if !toolRef(in.Ref) {
			badInput()
			return
		}
		proof, ok := h.execution(w, r, runtime, in.Ref)
		if !ok {
			return
		}
		principal.Subject = proof.Subject
		forward("releaseIssueLease", http.MethodPost, base+"/"+proof.IssueUID+"/lease/actions/release", toolReleaseBody{Reason: in.Reason}, nil)
	case "issue_close":
		in, ok := decodeToolInput[issueCloseInput](w, r)
		if !ok {
			return
		}
		if !toolRef(in.Ref) || len(in.IfMatch) > 128 || strings.ContainsAny(in.IfMatch, "\r\n") {
			badInput()
			return
		}
		for _, evidence := range in.Evidence {
			if evidence.IssueRef != "" && !toolRef(evidence.IssueRef) {
				badInput()
				return
			}
		}
		key := r.Header.Get("Idempotency-Key")
		if len(r.Header.Values("Idempotency-Key")) != 1 || strings.TrimSpace(key) == "" || len(key) > 1024 || strings.ContainsAny(key, "\r\n") {
			toolError(http.StatusBadRequest, "idempotency_key_required", "close requires a stable Idempotency-Key header").serve(w)
			return
		}
		proof, ok := h.execution(w, r, runtime, in.Ref)
		if !ok {
			return
		}
		principal.Subject = proof.Subject
		headers := http.Header{"Idempotency-Key": {key}, "If-Lease-Match": {proof.ClaimUID}}
		if in.IfMatch != "" {
			headers.Set("If-Match", in.IfMatch)
		}
		// STRICT CLOSE: no show/status/live-lease preflight. Kata's atomic
		// close-v2 guard checks exact ClaimUID + complete host principal and
		// releases the lease. K7 must replay receipts BEFORE live validation.
		forward("closeIssue", http.MethodPost, base+"/"+proof.IssueUID+"/actions/close", toolCloseBody{Reason: in.Reason, Message: in.Message, Evidence: in.Evidence, RetryProtocol: "close-v2"}, headers)
	default:
		toolError(http.StatusNotFound, "not_found", "unknown tool operation").serve(w)
	}
}

func (h *toolHandler) execution(w http.ResponseWriter, r *http.Request, runtime, ref string) (executionProof, bool) {
	proof, err := h.signer.verify(r.Header.Get("X-Forge-Execution"), runtime)
	if len(r.Header.Values("X-Forge-Execution")) != 1 || err != nil || !toolCanonicalUID(proof.IssueUID) || !toolCanonicalUID(proof.ClaimUID) || ref != proof.IssueUID {
		toolError(http.StatusForbidden, "invalid_execution", "execution credential does not match this session and canonical issue").serve(w)
		return executionProof{}, false
	}
	return proof, true
}

func decodeToolInput[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var zero T
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, toolBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			toolError(http.StatusRequestEntityTooLarge, "body_too_large", "tool body exceeds 1 MiB").serve(w)
		} else {
			toolError(http.StatusBadRequest, "validation", "cannot read tool body").serve(w)
		}
		return zero, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input *T
	err = decoder.Decode(&input)
	if err == nil && input != nil {
		var trailing any
		if decoder.Decode(&trailing) == io.EOF {
			return *input, true
		}
	}
	// Do not reflect request data (which can contain credentials) into errors.
	toolError(http.StatusBadRequest, "validation", "expected one typed JSON object with no unknown fields").serve(w)
	return zero, false
}

func toolBoundedInt(value *int, fallback, min, max int) (int, bool) {
	if value == nil {
		return fallback, true
	}
	return *value, *value >= min && *value <= max
}

func toolUUID(value, versions string) bool {
	if len(value) != 36 || !strings.ContainsRune(versions, rune(value[14])) || !strings.ContainsRune("89aAbB", rune(value[19])) {
		return false
	}
	for i := range len(value) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", rune(value[i])) {
			return false
		}
	}
	return true
}

func toolCanonicalUID(value string) bool {
	uid, err := ulid.ParseStrict(value)
	return err == nil && uid.String() == value
}

func toolRef(value string) bool {
	if len(value) == 26 {
		if _, err := ulid.ParseStrict(value); err == nil {
			return true
		}
	}
	// Public Kata bare short IDs are 4..26 lowercase Crockford characters.
	// No separators, percent escapes, path fragments, or qualified project.
	if len(value) < 4 || len(value) > 26 {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdefghjkmnpqrstvwxyz", c) {
			return false
		}
	}
	return true
}

func toolOptionalIdempotency(r *http.Request) http.Header {
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) == 1 && key != "" && len(key) <= 1024 && !strings.ContainsAny(key, "\r\n") {
		return http.Header{"Idempotency-Key": {key}}
	}
	return nil
}

func (h *toolHandler) dispatch(ctx context.Context, principal kata.Principal, operation, method, path string, body any, headers http.Header) *toolResponse {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return toolError(http.StatusInternalServerError, "internal", "cannot encode tool request")
		}
	}
	ctx = context.WithValue(ctx, toolCapabilityKey{}, toolCapability{project: h.project, operation: operation, principal: principal})
	ctx = kata.WithPrincipal(ctx, principal)
	request, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(encoded))
	if err != nil {
		return toolError(http.StatusInternalServerError, "internal", "cannot construct tool request")
	}
	// Construct from scratch: no bearer, actor, execution credential, proxy
	// headers, query parameters or arbitrary caller request is forwarded.
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	capture := &toolCapture{header: make(http.Header)}
	h.handler.ServeHTTP(capture, request)
	if capture.overflow {
		return toolError(http.StatusBadGateway, "response_too_large", "Kata response exceeds 8 MiB")
	}
	status := capture.status
	if status == 0 {
		status = http.StatusOK
	}
	return &toolResponse{status: status, header: capture.header, body: capture.body.Bytes()}
}

// Bounded production capture; httptest.ResponseRecorder is deliberately not
// used here because its buffer can grow without limit before we inspect it.
type toolCapture struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (c *toolCapture) Header() http.Header { return c.header }
func (c *toolCapture) WriteHeader(status int) {
	if c.status == 0 && status >= 200 {
		c.status = status
	}
}
func (c *toolCapture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.overflow || len(p) > toolResponseLimit-c.body.Len() {
		c.overflow = true
		return 0, errors.New("forge: Kata response exceeds capture limit")
	}
	// bytes.Buffer normally doubles capacity; a 6 MiB + 1 MiB write would
	// otherwise allocate 12 MiB despite the logical body limit above.
	if c.body.Len()+len(p) > c.body.Cap() && 2*c.body.Cap() > toolResponseLimit {
		bounded := make([]byte, c.body.Len(), toolResponseLimit)
		copy(bounded, c.body.Bytes())
		c.body = *bytes.NewBuffer(bounded)
	}
	return c.body.Write(p)
}

type toolResponse struct {
	status int
	header http.Header
	body   []byte
}

func (r *toolResponse) success() bool { return r.status >= 200 && r.status < 300 }
func (r *toolResponse) serve(w http.ResponseWriter) {
	for _, name := range []string{"Content-Type", "ETag", "Location", "Retry-After", "X-Kata-Project-Name"} {
		if value := r.header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body)
}
func (r *toolResponse) withFields(fields map[string]any) *toolResponse {
	var object map[string]json.RawMessage
	if json.Unmarshal(r.body, &object) != nil || object == nil {
		return toolError(http.StatusBadGateway, "invalid_response", "Kata returned invalid JSON")
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return toolError(http.StatusInternalServerError, "internal", "cannot encode tool result")
		}
		object[key] = encoded
	}
	body, err := json.Marshal(object)
	if err != nil || len(body) > toolResponseLimit {
		return toolError(http.StatusBadGateway, "response_too_large", "tool response exceeds 8 MiB")
	}
	r.body = body
	return r
}
func toolError(status int, code, message string) *toolResponse {
	body, _ := json.Marshal(struct {
		Status int `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Status: status, Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
	return &toolResponse{status: status, header: make(http.Header), body: body}
}
