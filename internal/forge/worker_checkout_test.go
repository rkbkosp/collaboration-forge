package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rkbkosp/collaboration-forge/internal/checkout"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerCheckoutSharesLeaseAndReplaysArchive(t *testing.T) {
	f := newToolsFixture(t)
	m := newSupervisedRuntime(f.handler)
	var err error
	m.workspaces, err = newWorkspaceStore(t.TempDir(), f.project.UID)
	if err != nil {
		t.Fatal(err)
	}
	m.workspaces.worktreeRoot = filepath.Join(m.workspaces.root, "trees")
	t.Cleanup(m.workspaces.close)
	f.handler.(*toolHandler).checkouts = m
	uid, _ := toolsCreate(t, f.handler, "worker checkout")
	token, _, claim := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	body := string(runtimeJSON(map[string]any{"ref": uid, "source": checkoutRepo(t), "dirty": true}))
	if w := toolsRequest(f.handler, "checkout", toolsRuntimeA, "", body, "Idempotency-Key", toolsAttemptB); w.Code != 403 {
		t.Fatal("missing proof accepted", w.Code)
	}
	if w := toolsRequest(f.handler, "checkout", toolsRuntimeB, token, body, "Idempotency-Key", toolsAttemptB); w.Code != 403 {
		t.Fatal("other runtime accepted", w.Code)
	}
	w := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB)
	toolsSuccess(t, w)
	var r workspaceRecord
	json.Unmarshal(w.Body.Bytes(), &r)
	id := r.ID
	for n := 0; n < 500 && r.State == "preparing"; n++ {
		time.Sleep(10 * time.Millisecond)
		w = toolsRequest(f.handler, "checkout_status", toolsRuntimeA, "", fmt.Sprintf(`{"workspace_id":%q}`, id))
		toolsSuccess(t, w)
		json.Unmarshal(w.Body.Bytes(), &r)
	}
	if r.State != "ready" {
		t.Fatal(r.State, r.Error)
	}
	if r.Tenure != workspaceTenure(claim) {
		t.Fatal("lease changed")
	}
	replay := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB)
	toolsSuccess(t, replay)
	var again workspaceRecord
	json.Unmarshal(replay.Body.Bytes(), &again)
	if again.ID != id {
		t.Fatal("duplicate checkout")
	}
	closeBody := fmt.Sprintf(`{"ref":%q,"reason":"audit-no-change","message":"Verified the generated worker checkout fixture and retained archive. No product edits in fixture.","evidence":[{"type":"no-change-audit","rationale":"Generated fixture exercises retained checkout lifecycle."}]}`, uid)
	closed := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, closeBody, "Idempotency-Key", "worker-close")
	toolsSuccess(t, closed)
	var result map[string]any
	json.Unmarshal(closed.Body.Bytes(), &result)
	if result["workspace_archive"].(map[string]any)["state"] != "archived" {
		t.Fatal("not archived")
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, closeBody, "Idempotency-Key", "worker-close"))
	if _, err = os.Stat(r.Path); err != nil {
		t.Fatal("files removed", err)
	}
	// A repeated create after close retrieves its existing artifact; it must not
	// need a live lease or acquire again to resolve an uncertain old response.
	replay = toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB)
	toolsSuccess(t, replay)
	json.Unmarshal(replay.Body.Bytes(), &again)
	if again.ID != id || again.State != "archived" {
		t.Fatal("lost retained receipt")
	}
}

func TestWorkerCheckoutPersistenceFailureDoesNotDuplicateRequest(t *testing.T) {
	f := newToolsFixture(t)
	m := newSupervisedRuntime(f.handler)
	var err error
	m.workspaces, err = newWorkspaceStore(t.TempDir(), f.project.UID)
	if err != nil {
		t.Fatal(err)
	}
	m.workspaces.worktreeRoot = filepath.Join(m.workspaces.root, "trees")
	t.Cleanup(m.workspaces.close)
	f.handler.(*toolHandler).checkouts = m
	uid, _ := toolsCreate(t, f.handler, "uncertain filesystem persistence")
	token, _, _ := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	body := string(runtimeJSON(map[string]any{"ref": uid, "source": checkoutRepo(t), "dirty": true}))
	write := m.workspaces.write
	m.workspaces.write = func(r workspaceRecord) error {
		if err := write(r); err != nil {
			return err
		}
		return errors.New("directory fsync failed after rename")
	}
	if w := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB); w.Code != 503 {
		t.Fatal(w.Code)
	}
	m.workspaces.write = write
	w := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB)
	toolsSuccess(t, w)
	var r workspaceRecord
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.State != "failed" || len(m.workspaces.list(uid)) != 1 {
		t.Fatal("uncertain store write created another job", r.State)
	}
}

func TestWorkerCheckoutPreparationReleaseAndRestartFence(t *testing.T) {
	f := newToolsFixture(t)
	m := newSupervisedRuntime(f.handler)
	var err error
	m.workspaces, err = newWorkspaceStore(t.TempDir(), f.project.UID)
	if err != nil {
		t.Fatal(err)
	}
	m.workspaces.worktreeRoot = filepath.Join(m.workspaces.root, "trees")
	t.Cleanup(m.workspaces.close)
	f.handler.(*toolHandler).checkouts = m
	uid, _ := toolsCreate(t, f.handler, "worker preparation lifecycle")
	token, _, _ := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	started, unblock := make(chan struct{}), make(chan struct{})
	capture := m.workspaces.capture
	m.workspaces.capture = func(ctx context.Context, o checkout.Options) (checkout.Snapshot, error) {
		close(started)
		select {
		case <-unblock:
		case <-ctx.Done():
			return checkout.Snapshot{}, ctx.Err()
		}
		return capture(ctx, o)
	}
	body := string(runtimeJSON(map[string]any{"ref": uid, "source": checkoutRepo(t), "dirty": true}))
	w := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB)
	toolsSuccess(t, w)
	var r workspaceRecord
	json.Unmarshal(w.Body.Bytes(), &r)
	<-started
	changed := string(runtimeJSON(map[string]any{"ref": uid, "source": "/different", "dirty": true}))
	if w := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, changed, "Idempotency-Key", toolsAttemptB); w.Code != 409 {
		t.Fatal("request fingerprint not fenced", w.Code)
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_renew", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q}`, uid)))
	if w := toolsRequest(f.handler, "issue_close", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q,"reason":"done","message":"Preparing work cannot be closed before verification."}`, uid), "Idempotency-Key", "busy-close"); w.Code != 409 {
		t.Fatal("closed preparing checkout", w.Code)
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_release", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q}`, uid)))
	close(unblock)
	m.workspaces.wg.Wait()
	got := m.workspaces.list(uid)[0]
	if got.State != "orphaned" {
		t.Fatal("not orphaned after lease loss", got.State)
	}
	if _, err := os.Stat(got.Path); !os.IsNotExist(err) {
		t.Fatal("restored after lease loss", err)
	}
	m.workspaces.close()
	n := newSupervisedRuntime(f.handler)
	n.workspaces, err = newWorkspaceStore(m.workspaces.root, f.project.UID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.workspaces.close)
	f.handler.(*toolHandler).checkouts = n
	w = toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptB)
	toolsSuccess(t, w)
	var replay workspaceRecord
	json.Unmarshal(w.Body.Bytes(), &replay)
	if replay.ID != r.ID || replay.State != "orphaned" {
		t.Fatal("restart recreated old execution")
	}
	if w := toolsRequest(f.handler, "checkout", toolsRuntimeA, token, body, "Idempotency-Key", toolsAttemptV7); w.Code != 409 {
		t.Fatal("stale proof created new work", w.Code)
	}
}
