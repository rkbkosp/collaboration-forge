package checkout

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

func UUID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type pendingClose struct {
	body json.RawMessage
	key  string
}
type Runtime struct {
	mu                                                            sync.Mutex
	url, worker, session, attempt, proof, issue, claim, execution string
	ttl                                                           int
	client                                                        *http.Client
	deadline                                                      time.Time
	pending                                                       *pendingClose
	active, stopped                                               bool
	done, lost                                                    chan struct{}
	stopOnce, lostOnce                                            sync.Once
}

func NewRuntime(origin, token string, ttl int) (*Runtime, error) {
	u, e := url.Parse(origin)
	if e != nil {
		return nil, errors.New("invalid Forge origin")
	}
	ip, e := netip.ParseAddr(u.Hostname())
	if e != nil || !ip.IsLoopback() || ip.Zone() != "" || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Forge URL must be an HTTP loopback IP literal")
	}
	if ttl < 60 || ttl > 3600 {
		return nil, errors.New("TTL must be 60..3600 seconds")
	}
	if len(token) < 32 || strings.ContainsAny(token, " \r\n\t") {
		return nil, errors.New("invalid worker credential")
	}
	return &Runtime{url: strings.TrimSuffix(origin, "/"), worker: token, session: UUID(), attempt: UUID(), ttl: ttl, done: make(chan struct{}), lost: make(chan struct{}), client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Error is a sanitized Forge/checkout transport error. It intentionally carries
// no response body, request, credential, or execution proof.
type Error struct {
	Status    int
	Code      string
	Message   string
	Hint      string
	Data      map[string]any
	Ambiguous bool
}

func (e *Error) Error() string {
	if e == nil || e.Message == "" {
		return "Forge response uncertain; retry original operation"
	}
	return e.Message
}

func errorCode(value string) string {
	if value == "" || len(value) > 100 || value[0] < 'a' || value[0] > 'z' {
		return "http_error"
	}
	for _, c := range value[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return "http_error"
		}
	}
	return value
}

func errorText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 {
		return fallback
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "worker_token", "admin_token", "execution_token", "attempt_id", "execution_id", "claim_uid", "credential", "secret", "token"} {
		if strings.Contains(lower, marker) {
			return fallback
		}
	}
	return strings.ReplaceAll(value, "Bearer ", "Bearer [REDACTED]")
}

func errorData(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	clean := make(map[string]any, len(value))
	for key, child := range value {
		lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		compact := strings.ReplaceAll(lower, "_", "")
		if strings.Contains(compact, "token") || strings.Contains(compact, "attempt") || strings.Contains(compact, "executionid") || strings.Contains(compact, "claimuid") || strings.Contains(compact, "credential") || strings.Contains(compact, "secret") {
			continue
		}
		switch nested := child.(type) {
		case map[string]any:
			if safe := errorData(nested); safe != nil {
				clean[key] = safe
			}
		case []any:
			items := make([]any, 0, len(nested))
			for _, item := range nested {
				if object, ok := item.(map[string]any); ok {
					items = append(items, errorData(object))
				} else if text, ok := item.(string); ok {
					if safe := errorText(text, ""); safe != "" {
						items = append(items, safe)
					}
				} else {
					items = append(items, item)
				}
			}
			clean[key] = items
		case string:
			if safe := errorText(nested, ""); safe != "" {
				clean[key] = safe
			}
		default:
			clean[key] = child
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func checkoutMutation(op string) bool {
	switch op {
	case "issue_get", "issue_timeline", "issue_graph", "issue_list":
		return false
	default:
		return true
	}
}

func requestFailure(status int, raw []byte, fallbackCode, fallbackMessage string, mutation bool) *Error {
	failure := &Error{Status: status, Code: fallbackCode, Message: fallbackMessage, Ambiguous: mutation && (status == 0 || status == 408 || status == 429 || status >= 500)}
	var envelope struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Hint      string         `json:"hint"`
			Data      map[string]any `json:"data"`
			Ambiguous bool           `json:"ambiguous"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
		failure.Code = errorCode(envelope.Error.Code)
		failure.Message = errorText(envelope.Error.Message, fallbackMessage)
		failure.Hint = errorText(envelope.Error.Hint, "")
		failure.Data = errorData(envelope.Error.Data)
		failure.Ambiguous = failure.Ambiguous || (mutation && envelope.Error.Ambiguous)
	}
	return failure
}

func ambiguous(e error) bool {
	var r *Error
	return errors.As(e, &r) && r.Ambiguous
}
func (r *Runtime) request(ctx context.Context, op string, body any, key string) (json.RawMessage, error) {
	b, e := json.Marshal(body)
	if e != nil {
		return nil, e
	}
	if len(b) > 1<<20 {
		return nil, errors.New("tool request exceeds 1 MiB")
	}
	req, e := http.NewRequestWithContext(ctx, "POST", r.url+"/forge/v1/tools/"+op, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+r.worker)
	req.Header.Set("X-Forge-Session", r.session)
	req.Header.Set("Content-Type", "application/json")
	if r.proof != "" {
		req.Header.Set("X-Forge-Execution", r.proof)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	mutation := checkoutMutation(op)
	resp, e := r.client.Do(req)
	if e != nil {
		return nil, requestFailure(0, nil, "transport_unknown", "Forge response uncertain; retry original operation", mutation)
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if e != nil || len(raw) > 8<<20 {
		return nil, requestFailure(0, nil, "invalid_response", "Forge response was incomplete; inspect state before retrying", mutation)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, requestFailure(resp.StatusCode, raw, "http_error", "Forge request was rejected", mutation)
	}
	if !json.Valid(raw) {
		return nil, requestFailure(0, nil, "invalid_response", "Forge returned an unreadable response; inspect state before retrying", mutation)
	}
	return raw, nil
}
func (r *Runtime) setLease(raw []byte, started time.Time, acquire bool) error {
	var v struct {
		Granted   bool      `json:"granted"`
		Token     string    `json:"execution_token"`
		Execution string    `json:"execution_id"`
		Now       time.Time `json:"server_now"`
		Lease     struct {
			Claim   string    `json:"claim_uid"`
			Issue   string    `json:"issue_uid"`
			Expires time.Time `json:"expires_at"`
		} `json:"lease"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Lease.Claim == "" || v.Lease.Issue == "" || v.Now.IsZero() || v.Lease.Expires.IsZero() {
		return requestFailure(0, nil, "invalid_response", "Forge lease response was incomplete; inspect state before retrying", true)
	}
	if acquire {
		if !v.Granted || v.Token == "" || v.Execution == "" {
			return requestFailure(0, nil, "invalid_response", "Forge lease response was incomplete; inspect state before retrying", true)
		}
		r.proof = v.Token
		r.claim = v.Lease.Claim
		r.issue = v.Lease.Issue
		r.execution = v.Execution
	} else if v.Lease.Claim != r.claim || v.Lease.Issue != r.issue {
		return errors.New("execution lease replaced")
	}
	ttl := v.Lease.Expires.Sub(v.Now)
	if max := time.Duration(r.ttl) * time.Second; ttl > max {
		ttl = max
	}
	r.deadline = started.Add(ttl - time.Second)
	if !time.Now().Before(r.deadline) {
		return errors.New("lease confirmation expired")
	}
	r.active = true
	return nil
}
func (r *Runtime) Claim(ctx context.Context, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.active {
		return errors.New("runtime cannot acquire")
	}
	body := map[string]any{"ref": ref, "attempt_id": r.attempt, "ttl_seconds": r.ttl, "purpose": "code checkout"}
	var err error
	for range 3 {
		started := time.Now()
		raw, e := r.request(ctx, "issue_claim", body, "")
		err = e
		if e == nil {
			err = r.setLease(raw, started, true)
		}
		if err == nil {
			go r.heartbeat()
			return nil
		}
		if !ambiguous(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}
func (r *Runtime) fail() { r.active = false; r.lostOnce.Do(func() { close(r.lost) }) }
func (r *Runtime) heartbeat() {
	interval := min(time.Duration(r.ttl)*time.Second/3, 30*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			if r.active && r.pending == nil {
				if e := r.renew(context.Background()); e != nil {
					r.fail()
				}
			} else if r.pending != nil && !time.Now().Before(r.deadline) {
				r.fail()
			}
			r.mu.Unlock()
		}
	}
}
func (r *Runtime) renew(ctx context.Context) error {
	if r.stopped || !r.active || !time.Now().Before(r.deadline) {
		return errors.New("execution has no confirmed live lease")
	}
	started := time.Now()
	raw, e := r.request(ctx, "issue_renew", map[string]any{"ref": r.issue, "ttl_seconds": r.ttl}, "")
	if e != nil {
		return e
	}
	return r.setLease(raw, started, false)
}
func (r *Runtime) Guard(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil {
		return errors.New("close response pending; retry before working")
	}
	if e := r.renew(ctx); e != nil {
		r.fail()
		return e
	}
	return nil
}
func (r *Runtime) Lost() <-chan struct{} { return r.lost }
func (r *Runtime) Issue() string         { r.mu.Lock(); defer r.mu.Unlock(); return r.issue }
func (r *Runtime) Shutdown() {
	r.stopOnce.Do(func() {
		close(r.done)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.stopped = true
		r.active = false
		if r.proof != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = r.request(ctx, "issue_release", map[string]any{"ref": r.issue, "reason": "checkout_runtime_stopped"}, "")
		}
		r.worker = ""
		r.proof = ""
		r.attempt = ""
		r.client.CloseIdleConnections()
	})
}
func (r *Runtime) Register(ctx context.Context, s Snapshot, workspaceID, state, location string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.renew(ctx); e != nil {
		r.fail()
		return e
	}
	device, err := os.Hostname()
	if err != nil {
		return errors.New("cannot identify workspace device")
	}
	body := map[string]any{"ref": r.issue, "snapshot_id": s.ID, "base_commit": s.Manifest.Base, "repository_id": s.Manifest.Repository, "workspace_id": workspaceID, "state": state, "source_kind": s.Manifest.Kind, "device": device, "location": location, "snapshot_location": s.Path}
	var e error
	for range 3 {
		_, e = r.request(ctx, "issue_workspace", body, "")
		if e == nil || !ambiguous(e) {
			return e
		}
	}
	return e
}
func (r *Runtime) Tool(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	if op == "guard" {
		if e := r.Guard(ctx); e != nil {
			return nil, e
		}
		return json.RawMessage(`{"allowed":true}`), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return nil, errors.New("runtime stopped")
	}
	var body map[string]any
	if len(raw) > 0 {
		if e := json.Unmarshal(raw, &body); e != nil || body == nil {
			return nil, errors.New("expected JSON object")
		}
	} else {
		body = map[string]any{}
	}
	if op == "state" {
		return json.Marshal(map[string]any{"issue": r.issue, "claim_uid": r.claim, "active": r.active && time.Now().Before(r.deadline), "pending_close": r.pending != nil})
	}
	if op == "retry" {
		if r.pending == nil {
			return nil, errors.New("no pending close")
		}
		return r.close(ctx)
	}
	switch op {
	case "issue_close":
		if r.pending != nil {
			return nil, errors.New("use retry for pending original close")
		}
		if e := r.renew(ctx); e != nil {
			r.fail()
			return nil, e
		}
		body["ref"] = r.issue
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		r.pending = &pendingClose{b, UUID()}
		return r.close(ctx)
	case "issue_get", "issue_timeline", "issue_graph", "issue_list", "issue_create", "issue_comment", "issue_link":
	default:
		return nil, errors.New("unsupported runtime tool")
	}
	if op != "issue_list" && op != "issue_create" {
		if _, ok := body["ref"]; !ok {
			body["ref"] = r.issue
		}
	}
	result, e := r.request(ctx, op, body, "")
	return r.sanitize(result), e
}
func (r *Runtime) close(ctx context.Context) (json.RawMessage, error) {
	v, e := r.request(ctx, "issue_close", r.pending.body, r.pending.key)
	if e == nil {
		r.pending = nil
		r.active = false
	} else if !ambiguous(e) {
		r.pending = nil
	}
	return r.sanitize(v), e
}
func (r *Runtime) sanitize(raw []byte) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var clean func(any) any
	clean = func(x any) any {
		switch a := x.(type) {
		case map[string]any:
			for k, b := range a {
				if k == "execution_token" || k == "attempt_id" {
					delete(a, k)
				} else {
					a[k] = clean(b)
				}
			}
		case []any:
			for i := range a {
				a[i] = clean(a[i])
			}
		case string:
			for _, secret := range []string{r.worker, r.proof, r.attempt} {
				if secret != "" {
					a = strings.ReplaceAll(a, secret, "[REDACTED]")
				}
			}
			return a
		}
		return x
	}
	out, _ := json.Marshal(clean(v))
	return out
}
