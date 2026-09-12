package forge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Every served namespace must dispatch into the identical ephemeral runtime with
// the same execution semantics. This is the extraction's contract: adding a
// harness adds a control namespace and a diagnostic label, not a second protocol.
func TestHarnessNamespacesShareOneRuntimeAndProtocol(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Harness namespace parity")
	m := newSupervisedRuntime(f.handler)

	for _, profile := range harnessProfiles {
		register := map[string]any{"instance_id": "instance-" + profile.Name, "pid": os.Getpid()}
		if w := harnessRequest(m, profile, "register", register, codexTestToken); w.Code != 200 {
			t.Fatalf("%s register: %d %s", profile.Name, w.Code, w.Body.String())
		}
		identity := actorIdentity{Instance: "instance-" + profile.Name, Session: "session", Thread: "actor"}
		call := func(op string, params any) *httptest.ResponseRecorder {
			return harnessRequest(m, profile, "tool", supervisedCommand{Identity: identity, RequestID: runtimeNonce(), Operation: op, Params: runtimeJSON(params)}, codexTestToken)
		}
		claimed := call("issue_claim", map[string]string{"ref": uid})
		toolsSuccess(t, claimed)
		if strings.Contains(claimed.Body.String(), "private-") {
			t.Fatalf("%s leaked a private execution value", profile.Name)
		}
		// The harness name is the only difference in the derived attribution.
		if !strings.Contains(claimed.Body.String(), "["+profile.Name+" session session") {
			t.Fatalf("%s attribution missing: %s", profile.Name, claimed.Body.String())
		}
		if w := call("issue_release", map[string]string{"ref": uid, "reason": "parity_fixture"}); w.Code != 200 {
			t.Fatalf("%s release: %d %s", profile.Name, w.Code, w.Body.String())
		}
	}

	// A capability minted for one namespace must not authorize another.
	codexToken := codexTestToken
	if w := harnessRequest(m, claudeHarness, "tool", supervisedCommand{Identity: actorIdentity{Instance: "instance-Codex", Session: "session", Thread: "actor"}, RequestID: runtimeNonce(), Operation: "status"}, codexToken); w.Code != 403 {
		t.Fatalf("cross-namespace capability accepted: %d", w.Code)
	}
}

// The Claude actor slot is `agent_id`. Both harness spellings must satisfy the
// same validation, keep their own namespaces, and stay distinct from each other.
func TestActorIdentitySupportsBothHarnessSpellings(t *testing.T) {
	codex := actorIdentity{Instance: "i", Session: "s", Thread: "actor"}
	claude := actorIdentity{Instance: "i", Session: "s", Actor: "actor"}
	if codex.slot() != claude.slot() || codex.slot() != "actor" {
		t.Fatal("actor slot is not harness-neutral")
	}
	if !codex.valid() || !claude.valid() {
		t.Fatal("valid harness identity refused")
	}
	if codex.runtime() == claude.runtime() {
		t.Fatal("wire spellings share one execution session")
	}
	if (actorIdentity{Instance: "i", Session: "s"}).valid() || (actorIdentity{Instance: "i", Session: "s", Actor: " "}).valid() {
		t.Fatal("identity without a usable actor accepted")
	}
}

// The derived runtime session is embedded in the signed execution proof, so a
// Codex identity must keep hashing to exactly the bytes it always did. Changing
// this silently invalidates in-flight close-receipt recovery across an upgrade.
func TestCodexRuntimeSessionBytesAreStable(t *testing.T) {
	for _, tc := range []struct {
		identity actorIdentity
		want     string
	}{
		{actorIdentity{Instance: "instance-a", Session: "session-a", Thread: "thread-a"}, "6f01cbd9-3e39-495c-a434-3bf9bbe6d27a"},
		{actorIdentity{Instance: "i", Session: "s", Thread: "t"}, "bcdb667c-7daa-4d02-a45c-84527c12f48b"},
	} {
		if got := tc.identity.runtime(); got != tc.want {
			t.Fatalf("derived runtime session changed: got %s want %s", got, tc.want)
		}
	}
}

func TestHarnessReleaseReasonAndWording(t *testing.T) {
	if codexHarness.releaseReason() != "codex_normal_end" {
		t.Fatal("Codex release reason changed")
	}
	if claudeHarness.releaseReason() != "claude_normal_end" {
		t.Fatal("Claude release reason")
	}
	// Existing Codex wording is byte-for-byte preserved.
	for code, want := range map[string]string{
		"method_not_allowed": "Codex endpoint requires POST",
		"invalid_instance":   "Codex instance credential is invalid",
		"validation":         "Invalid Codex request",
		"unknown_operation":  "Unknown Codex operation",
		"other":              "Codex operation failed",
	} {
		if got := codexHarness.errorMessage(code); got != want {
			t.Fatalf("Codex wording for %s: %q", code, got)
		}
	}
	if h, op := harnessForPath("/forge/v1/claude/tool"); h.Name != "Claude" || op != "tool" {
		t.Fatal("Claude namespace not routed")
	}
	if h, op := harnessForPath("/forge/v1/codex/register"); h.Name != "Codex" || op != "register" {
		t.Fatal("Codex namespace not routed")
	}
}

func harnessRequest(m http.Handler, profile harnessProfile, op string, body any, token string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", profile.Prefix+op, bytes.NewReader(b))
	r.Header.Set(profile.CapabilityHeader, token)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
