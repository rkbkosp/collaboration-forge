package forge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/rkbkosp/collaboration-forge/internal/checkout"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func checkoutRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Fixture"}} {
		c := exec.Command("git", args...)
		c.Dir = d
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	os.WriteFile(filepath.Join(d, "file.txt"), []byte("base\n"), 0600)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = d
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	return d
}
func waitCheckout(t *testing.T, m *codexRuntime, i codexIdentity, id string) workspaceRecord {
	t.Helper()
	for n := 0; n < 300; n++ {
		w := codexRequest(m, "tool", codexCommand{Identity: i, Operation: "checkout_status", Params: marshalCodex(map[string]string{"workspace_id": id})}, codexTestToken)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var r workspaceRecord
		json.Unmarshal(w.Body.Bytes(), &r)
		if r.State != "preparing" {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("checkout timeout")
	return workspaceRecord{}
}
func TestCheckoutRequiresExistingTenureAndArchivesWithoutRemovingFiles(t *testing.T) {
	f := newToolsFixture(t)
	m := newCodexRuntime(f.handler)
	var e error
	m.workspaces, e = newWorkspaceStore(t.TempDir(), f.project.UID)
	if e != nil {
		t.Fatal(e)
	}
	m.workspaces.worktreeRoot = filepath.Join(m.workspaces.root, "trees")
	t.Cleanup(m.workspaces.close)
	codexRequest(m, "register", map[string]any{"instance_id": "checkout", "pid": os.Getpid()}, codexTestToken)
	i := codexIdentity{"checkout", "session", "thread"}
	uid, _ := toolsCreate(t, f.handler, "checkout fixture")
	source := checkoutRepo(t)
	params := marshalCodex(map[string]any{"ref": uid, "source": source, "dirty": true})
	cmd := codexCommand{Identity: i, Operation: "checkout", Params: params, RequestID: "checkout-request"}
	if w := codexRequest(m, "tool", cmd, codexTestToken); w.Code != 409 {
		t.Fatal("unclaimed checkout allowed", w.Code)
	}
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_claim", Params: marshalCodex(map[string]string{"ref": uid})}, codexTestToken))
	claim := m.threads[i].active.claim
	cmd.RequestID = "claimed-checkout"
	w := codexRequest(m, "tool", cmd, codexTestToken)
	toolsSuccess(t, w)
	var start workspaceRecord
	json.Unmarshal(w.Body.Bytes(), &start)
	ready := waitCheckout(t, m, i, start.ID)
	if ready.State != "ready" {
		t.Fatal(ready.State, ready.Error)
	}
	if m.threads[i].active.claim != claim {
		t.Fatal("checkout acquired a new lease")
	}
	again := codexRequest(m, "tool", cmd, codexTestToken)
	var replay workspaceRecord
	json.Unmarshal(again.Body.Bytes(), &replay)
	if replay.ID != ready.ID {
		t.Fatal("request replay duplicated checkout")
	}
	close := map[string]any{"ref": uid, "reason": "audit-no-change", "message": "Generated checkout fixture verified; no product change is needed for this audit.", "evidence": []any{map[string]string{"type": "no-change-audit", "rationale": "Generated test fixture validates checkout and archive behavior."}}}
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_close", Params: marshalCodex(close)}, codexTestToken))
	archived := waitCheckout(t, m, i, start.ID)
	if archived.State != "archived" {
		t.Fatal("not archived", archived)
	}
	if _, e := os.Stat(filepath.Join(ready.Path, "file.txt")); e != nil {
		t.Fatal("archive removed files", e)
	}
}

func checkoutFixture(t *testing.T) (*codexRuntime, codexIdentity, string, string) {
	t.Helper()
	f := newToolsFixture(t)
	m := newCodexRuntime(f.handler)
	var e error
	m.workspaces, e = newWorkspaceStore(t.TempDir(), f.project.UID)
	if e != nil {
		t.Fatal(e)
	}
	m.workspaces.worktreeRoot = filepath.Join(m.workspaces.root, "trees")
	t.Cleanup(m.workspaces.close)
	i := codexIdentity{"workspace-instance", "session", "thread"}
	toolsSuccess(t, codexRequest(m, "register", map[string]any{"instance_id": i.Instance, "pid": os.Getpid()}, codexTestToken))
	uid, _ := toolsCreate(t, f.handler, "isolated checkout tests")
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_claim", Params: marshalCodex(map[string]string{"ref": uid})}, codexTestToken))
	return m, i, uid, checkoutRepo(t)
}
func beginCheckout(t *testing.T, m *codexRuntime, i codexIdentity, p map[string]any) workspaceRecord {
	t.Helper()
	w := codexRequest(m, "tool", codexCommand{Identity: i, Operation: "checkout", Params: marshalCodex(p)}, codexTestToken)
	toolsSuccess(t, w)
	var r workspaceRecord
	json.Unmarshal(w.Body.Bytes(), &r)
	return r
}
func closeCheckout(t *testing.T, m *codexRuntime, i codexIdentity, uid string) *httptest.ResponseRecorder {
	t.Helper()
	return codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_close", Params: marshalCodex(map[string]any{"ref": uid, "reason": "audit-no-change", "message": "Generated checkout fixture was verified and requires no product code change.", "evidence": []any{map[string]string{"type": "no-change-audit", "rationale": "This generated fixture validates workspace lifecycle behavior only."}}})}, codexTestToken)
}
func TestCheckoutMultipleRepositoriesSourceModesAndThreadFence(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	second := checkoutRepo(t)
	os.WriteFile(filepath.Join(source, "file.txt"), []byte("working\n"), 0600)
	os.WriteFile(filepath.Join(source, "new.txt"), []byte("untracked\n"), 0600)
	other := i
	other.Thread = "other"
	w := codexRequest(m, "tool", codexCommand{Identity: other, Operation: "checkout", Params: marshalCodex(map[string]any{"ref": uid, "source": source, "dirty": true})}, codexTestToken)
	if w.Code != 409 {
		t.Fatal("other thread reused authority", w.Code)
	}
	a := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	b := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": second, "commit": "HEAD"})
	a = waitCheckout(t, m, i, a.ID)
	b = waitCheckout(t, m, i, b.ID)
	if a.State != "ready" || b.State != "ready" || a.Repository == b.Repository || a.Tenure != b.Tenure || a.Path == b.Path {
		t.Fatal(a, b)
	}
	contents, e := os.ReadFile(filepath.Join(a.Path, "file.txt"))
	if e != nil || string(contents) != "working\n" {
		t.Fatal("dirty content not restored", e)
	}
	if _, e = os.Stat(filepath.Join(a.Path, "new.txt")); e != nil {
		t.Fatal(e)
	}
	c := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "commit": "HEAD"})
	c = waitCheckout(t, m, i, c.ID)
	contents, e = os.ReadFile(filepath.Join(c.Path, "file.txt"))
	if e != nil || string(contents) != "base\n" {
		t.Fatal("commit snapshot included dirt", e)
	}
	if _, e = os.Stat(filepath.Join(c.Path, "new.txt")); !os.IsNotExist(e) {
		t.Fatal("commit snapshot included untracked file")
	}
	toolsSuccess(t, closeCheckout(t, m, i, uid))
	for _, r := range []workspaceRecord{a, b, c} {
		if got := waitCheckout(t, m, i, r.ID); got.State != "archived" {
			t.Fatal(got)
		}
	}
}
func TestCheckoutReleaseRecoveryRequiresFreshClaimAndPreservesOldTree(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	r = waitCheckout(t, m, i, r.ID)
	os.WriteFile(filepath.Join(r.Path, "progress.txt"), []byte("retained progress"), 0600)
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_release", Params: marshalCodex(map[string]string{"ref": uid})}, codexTestToken))
	if waitCheckout(t, m, i, r.ID).State != "orphaned" {
		t.Fatal("release should retain an orphan")
	}
	j := codexIdentity{"fresh-instance", "session", "thread"}
	codexRequest(m, "register", map[string]any{"instance_id": j.Instance, "pid": os.Getpid()}, codexTestToken)
	params := marshalCodex(map[string]any{"ref": uid, "recover": r.ID, "dirty": true})
	w := codexRequest(m, "tool", codexCommand{Identity: j, Operation: "checkout", Params: params}, codexTestToken)
	if w.Code != 409 {
		t.Fatal("recovery restored old authority")
	}
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: j, Operation: "issue_claim", Params: marshalCodex(map[string]string{"ref": uid})}, codexTestToken))
	fresh := beginCheckout(t, m, j, map[string]any{"ref": uid, "recover": r.ID, "dirty": true})
	fresh = waitCheckout(t, m, j, fresh.ID)
	if fresh.State != "ready" || fresh.Path == r.Path || fresh.Tenure == r.Tenure || fresh.RecoveredFrom != r.ID {
		t.Fatal(fresh)
	}
	for _, p := range []string{r.Path, fresh.Path} {
		b, e := os.ReadFile(filepath.Join(p, "progress.txt"))
		if e != nil || string(b) != "retained progress" {
			t.Fatal("progress lost", e)
		}
	}
}
func TestCheckoutArchiveFailureCannotInvalidateCommittedClose(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	r = waitCheckout(t, m, i, r.ID)
	s := m.workspaces
	s.mu.Lock()
	write := s.write
	s.write = func(workspaceRecord) error { return errors.New("fixture write failure") }
	s.mu.Unlock()
	w := closeCheckout(t, m, i, uid)
	toolsSuccess(t, w)
	if !strings.Contains(w.Body.String(), `"state":"pending"`) {
		t.Fatal("close hid archive failure", w.Body.String())
	}
	if m.threads[i].active != nil {
		t.Fatal("close retained authority")
	}
	s.mu.Lock()
	s.write = write
	s.mu.Unlock()
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "checkout_archive", Params: marshalCodex(map[string]string{"workspace_id": r.ID})}, codexTestToken))
	if waitCheckout(t, m, i, r.ID).State != "archived" {
		t.Fatal("archive retry failed")
	}
	if _, e := os.Stat(r.Path); e != nil {
		t.Fatal("files removed")
	}
}
func TestCheckoutDoesNotBlockRenewalAndRejectsLostLeaseBeforeRestore(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	s := m.workspaces
	started := make(chan struct{})
	unblock := make(chan struct{})
	capture := s.capture
	s.capture = func(ctx context.Context, o checkout.Options) (checkout.Snapshot, error) {
		close(started)
		select {
		case <-unblock:
		case <-ctx.Done():
			return checkout.Snapshot{}, ctx.Err()
		}
		return capture(ctx, o)
	}
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	<-started
	thread := m.threads[i]
	thread.mu.Lock()
	thread.nextRenew = time.Now().Add(-time.Second)
	thread.mu.Unlock()
	done := make(chan struct{})
	go func() { m.tick(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("copy blocked renewer")
	}
	thread.mu.Lock()
	if time.Until(thread.nextRenew) < 20*time.Second {
		t.Fatal("renewal did not run")
	}
	thread.mu.Unlock()
	if w := closeCheckout(t, m, i, uid); w.Code != 409 {
		t.Fatal("closed while checkout was preparing", w.Code)
	}
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "issue_release", Params: marshalCodex(map[string]string{"ref": uid})}, codexTestToken))
	close(unblock)
	r = waitCheckout(t, m, i, r.ID)
	if r.State != "orphaned" {
		t.Fatal(r)
	}
	if _, e := os.Stat(r.Path); !os.IsNotExist(e) {
		t.Fatal("restored after lease loss", e)
	}
}
func TestCheckoutRestartRetainsArtifactsButOrphansRuntimeState(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	r = waitCheckout(t, m, i, r.ID)
	m.workspaces.close()
	fresh, e := newWorkspaceStore(m.workspaces.root, m.workspaces.project)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.close()
	rows := fresh.list(uid)
	if len(rows) != 1 || rows[0].State != "orphaned" || rows[0].Path != r.Path {
		t.Fatal(rows)
	}
	n := newCodexRuntime(m.tools)
	n.workspaces = fresh
	if w := codexRequest(n, "tool", codexCommand{Identity: i, Operation: "checkout_status", Params: marshalCodex(map[string]string{"workspace_id": r.ID})}, codexTestToken); w.Code != 403 {
		t.Fatal("restart restored runtime", w.Code)
	}
}

func TestCheckoutUncertainCloseArchivesAfterLeaseExpiry(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	r = waitCheckout(t, m, i, r.ID)
	original := m.tools
	lost := false
	m.tools = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "issue_close") && !lost {
			lost = true
			out := httptest.NewRecorder()
			original.ServeHTTP(out, req)
			toolsSuccess(t, out)
			w.WriteHeader(502)
			w.Write([]byte(`{}`))
			return
		}
		original.ServeHTTP(w, req)
	})
	if w := closeCheckout(t, m, i, uid); w.Code != 502 {
		t.Fatal("response loss not injected", w.Code)
	}
	m.threads[i].active.deadline = time.Now().Add(-time.Second)
	toolsSuccess(t, codexRequest(m, "event", codexEvent{Identity: i, Event: "stop_check"}, codexTestToken))
	toolsSuccess(t, codexRequest(m, "tool", codexCommand{Identity: i, Operation: "retry"}, codexTestToken))
	if got := waitCheckout(t, m, i, r.ID); got.State != "archived" {
		t.Fatal("receipt recovery missed archive", got)
	}
}

func TestCheckoutWorkspaceRootCannotBeSharedByConcurrentDaemons(t *testing.T) {
	root := t.TempDir()
	s, e := newWorkspaceStore(root, "01K00000000000000000000001")
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	other, e := newWorkspaceStore(root, "01K00000000000000000000001")
	if e == nil {
		other.close()
		t.Fatal("concurrent workspace store opened")
	}
	s.close()
	s.mu.Lock()
	lateWrite := s.put(workspaceRecord{ID: "late-write"})
	s.mu.Unlock()
	if lateWrite == nil {
		t.Fatal("closed store accepted a write after releasing its lock")
	}
	reopened, e := newWorkspaceStore(root, "01K00000000000000000000001")
	if e != nil {
		t.Fatal(e)
	}
	reopened.close()
}
