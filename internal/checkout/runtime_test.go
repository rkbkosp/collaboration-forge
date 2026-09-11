package checkout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeAcquireRetryIdentityAndCloseReceipt(t *testing.T) {
	var mu sync.Mutex
	var attempts []string
	var keys []string
	closeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if strings.HasSuffix(r.URL.Path, "issue_claim") {
			attempts = append(attempts, b["attempt_id"].(string))
			if len(attempts) == 1 {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"granted": true, "execution_token": "private-proof", "execution_id": "public-execution", "server_now": time.Now(), "lease": map[string]any{"claim_uid": "claim", "issue_uid": "issue", "expires_at": time.Now().Add(time.Minute)}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "issue_renew") {
			json.NewEncoder(w).Encode(map[string]any{"server_now": time.Now(), "lease": map[string]any{"claim_uid": "claim", "issue_uid": "issue", "expires_at": time.Now().Add(time.Minute)}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "issue_close") {
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			closeCalls++
			if closeCalls == 1 {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(`{"reused":true}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	rt, e := NewRuntime(server.URL, strings.Repeat("w", 32), 60)
	if e != nil {
		t.Fatal(e)
	}
	defer rt.Shutdown()
	if e = rt.Claim(context.Background(), "abcd"); e != nil {
		t.Fatal(e)
	}
	if len(attempts) != 2 || attempts[0] != attempts[1] {
		t.Fatal("retry changed attempt")
	}
	if _, e = rt.Tool(context.Background(), "issue_close", json.RawMessage(`{"reason":"done","message":"Complete"}`)); e == nil {
		t.Fatal("ambiguous close accepted")
	}
	if _, e = rt.Tool(context.Background(), "guard", nil); e == nil {
		t.Fatal("pending close allowed work")
	}
	if _, e = rt.Tool(context.Background(), "retry", nil); e != nil {
		t.Fatal(e)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal("close retry key changed")
	}
}

func TestRuntimeLossBlocksExecutionAndSecretsStayPrivate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "issue_claim") {
			json.NewEncoder(w).Encode(map[string]any{"granted": true, "execution_token": "private-proof", "execution_id": "execution", "server_now": time.Now(), "lease": map[string]any{"claim_uid": "claim", "issue_uid": "issue", "expires_at": time.Now().Add(time.Minute)}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "issue_comment") {
			w.Write([]byte(`{"body":"private-proof"}`))
			return
		}
		w.WriteHeader(409)
	}))
	defer server.Close()
	r, e := NewRuntime(server.URL, strings.Repeat("w", 32), 60)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Shutdown()
	if e = r.Claim(context.Background(), "abcd"); e != nil {
		t.Fatal(e)
	}
	raw, e := r.Tool(context.Background(), "issue_comment", json.RawMessage(`{"body":"hello"}`))
	if e != nil || strings.Contains(string(raw), "private-proof") {
		t.Fatal("redaction failed")
	}
	if e = r.Guard(context.Background()); e == nil {
		t.Fatal("lost guard allowed")
	}
	select {
	case <-r.Lost():
	default:
		t.Fatal("loss not signaled")
	}
	if _, e = r.Tool(context.Background(), "issue_workspace", nil); e == nil {
		t.Fatal("Agent can fabricate workspace observation")
	}
}
func TestNewRuntimeRejectsRemoteAndRedirects(t *testing.T) {
	if _, e := NewRuntime("http://0.0.0.0:1", strings.Repeat("w", 32), 60); e == nil {
		t.Fatal("remote listener accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.invalid", 307) }))
	defer server.Close()
	r, e := NewRuntime(server.URL, strings.Repeat("w", 32), 60)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Shutdown()
	if e = r.Claim(context.Background(), "abcd"); e == nil {
		t.Fatal("redirect followed")
	}
}
