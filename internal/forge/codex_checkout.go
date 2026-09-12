package forge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/rkbkosp/collaboration-forge/internal/checkout"
)

// Filesystem coordination metadata only: no Issue database, credentials, or
// restored execution authority. Kata remains the owner of issues and leases.
type workspaceRecord struct {
	ID            string        `json:"workspace_id"`
	Project       string        `json:"project_uid"`
	Issue         string        `json:"issue_uid"`
	Tenure        string        `json:"tenure"`
	Owner         codexIdentity `json:"owner"`
	Repository    string        `json:"repository_id,omitempty"`
	Source        string        `json:"source"`
	Kind          string        `json:"source_kind"`
	Base          string        `json:"base_commit,omitempty"`
	Snapshot      string        `json:"snapshot_id,omitempty"`
	SnapshotPath  string        `json:"snapshot_path,omitempty"`
	Path          string        `json:"worktree,omitempty"`
	WorktreeRoot  string        `json:"worktree_root,omitempty"`
	State         string        `json:"state"`
	Error         string        `json:"error_code,omitempty"`
	Updated       time.Time     `json:"updated_at"`
	RecoveredFrom string        `json:"recovered_from,omitempty"`
	Request       string        `json:"request_hash,omitempty"`
	Fingerprint   string        `json:"request_fingerprint,omitempty"`
}
type workspaceStore struct {
	lock          *os.File
	closeOnce     sync.Once
	capture       func(context.Context, checkout.Options) (checkout.Snapshot, error)
	mu            sync.Mutex
	root, project string
	worktreeRoot  string
	records       map[string]workspaceRecord
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	slots         chan struct{}
	stopped       bool
	closed        bool
	write         func(workspaceRecord) error
}

func newWorkspaceStore(root, project string) (_ *workspaceStore, err error) {
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Join(root, "records"), 0700); e != nil {
		return nil, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	lock, e := openPrivateFile(filepath.Join(root, "workspace.lock"), os.O_CREATE|os.O_RDWR)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("workspace root already in use")
	}
	defer func() {
		if err != nil {
			unlock(lock)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	s := &workspaceStore{lock: lock, root: root, project: project, records: map[string]workspaceRecord{}, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 4), capture: checkout.Capture}
	s.write = func(r workspaceRecord) error {
		b, e := json.MarshalIndent(r, "", "  ")
		if e != nil {
			return e
		}
		f, e := os.CreateTemp(filepath.Join(s.root, "records"), ".record-")
		if e != nil {
			return e
		}
		name := f.Name()
		defer os.Remove(name)
		if _, e = f.Write(b); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if e = os.Rename(name, filepath.Join(s.root, "records", r.ID+".json")); e != nil {
			return e
		}
		d, e := os.Open(filepath.Join(s.root, "records"))
		if e != nil {
			return e
		}
		defer d.Close()
		return d.Sync()
	}
	entries, e := os.ReadDir(filepath.Join(root, "records"))
	if e != nil {
		cancel()
		return nil, e
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(s.records) >= 4096 {
			cancel()
			return nil, errors.New("workspace record limit")
		}
		p := filepath.Join(root, "records", entry.Name())
		info, e := os.Lstat(p)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			cancel()
			return nil, errors.New("invalid workspace record")
		}
		b, e := os.ReadFile(p)
		var r workspaceRecord
		if e != nil || json.Unmarshal(b, &r) != nil || !toolUUID(r.ID, "47") || entry.Name() != r.ID+".json" || r.Project != project || !toolCanonicalUID(r.Issue) || !isHex(r.Tenure, 32) {
			cancel()
			return nil, errors.New("invalid workspace record")
		}
		expected := filepath.Join(root, "trees", r.Issue, r.Tenure, r.Repository, r.ID, "worktree")
		if r.WorktreeRoot != "" {
			if !filepath.IsAbs(r.WorktreeRoot) || filepath.Clean(r.WorktreeRoot) != r.WorktreeRoot {
				cancel()
				return nil, errors.New("invalid worktree root")
			}
			expected = filepath.Join(r.WorktreeRoot, r.ID, "worktree")
		}
		if r.Path != "" && (!isHex(r.Repository, 32) || r.Path != expected) {
			cancel()
			return nil, errors.New("invalid workspace path")
		}
		if r.State == "preparing" || r.State == "ready" || r.State == "paused" {
			r.State = "orphaned"
			r.Error = "daemon_restarted"
			r.Updated = time.Now()
			if e = s.write(r); e != nil {
				cancel()
				return nil, e
			}
		}
		s.records[r.ID] = r
	}
	return s, nil
}
func (s *workspaceStore) close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		s.mu.Lock()
		s.closed = true
		unlock(s.lock)
		s.mu.Unlock()
	})
}
func workspaceTenure(claim string) string {
	b := sha256.Sum256([]byte(claim))
	return hex.EncodeToString(b[:])
}
func (s *workspaceStore) put(r workspaceRecord) error {
	if s.closed {
		return errors.New("workspace store closed")
	}
	r.Updated = time.Now().UTC()
	e := s.write(r)
	s.records[r.ID] = r
	return e
}
func (s *workspaceStore) list(issue string) []workspaceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []workspaceRecord{}
	for _, r := range s.records {
		if issue == "" || issue == r.Issue {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *codexRuntime) checkoutCommand(w http.ResponseWriter, t *codexThread, op string, raw json.RawMessage) {
	s := m.workspaces
	if s == nil {
		codexFail(w, 503, "checkout_unavailable")
		return
	}
	var in struct {
		Ref     string `json:"ref"`
		Source  string `json:"source"`
		Dirty   bool   `json:"dirty"`
		Commit  string `json:"commit"`
		Recover string `json:"recover"`
		ID      string `json:"workspace_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		codexFail(w, 400, "validation")
		return
	}
	if op == "checkout_list" {
		if in.Ref != "" {
			res := m.dispatch(t, "issue_get", marshalCodex(map[string]string{"ref": in.Ref}), "", "")
			var b struct {
				Issue struct {
					UID string `json:"uid"`
				} `json:"issue"`
			}
			if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &b) != nil {
				codexFail(w, 404, "issue_not_found")
				return
			}
			in.Ref = b.Issue.UID
		}
		codexReply(w, map[string]any{"workspaces": s.list(in.Ref)})
		return
	}
	if op == "checkout_status" || op == "checkout_archive" {
		s.mu.Lock()
		r, ok := s.records[in.ID]
		s.mu.Unlock()
		if !ok {
			codexFail(w, 404, "workspace_not_found")
			return
		}
		if op == "checkout_archive" {
			if r.State == "preparing" {
				codexFail(w, 409, "checkout_busy")
				return
			}
			res := m.dispatch(t, "issue_get", marshalCodex(map[string]string{"ref": r.Issue}), "", "")
			var b struct {
				Issue struct{ UID, Status string } `json:"issue"`
			}
			if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &b) != nil || b.Issue.UID != r.Issue || b.Issue.Status != "closed" {
				codexFail(w, 409, "archive_requires_closed_issue")
				return
			}
			s.mu.Lock()
			r = s.records[in.ID]
			r.State = "archived"
			r.Error = ""
			e := s.put(r)
			if e != nil {
				r.State = "archive_pending"
				r.Error = "workspace_archive_failed"
				s.records[r.ID] = r
			}
			s.mu.Unlock()
			if e != nil {
				codexFail(w, 503, "workspace_archive_failed")
				return
			}
		}
		codexReply(w, r)
		return
	}
	if op != "checkout" {
		codexFail(w, 404, "unknown_operation")
		return
	}
	if !toolRef(in.Ref) || in.Dirty == (in.Commit != "") || (in.Recover != "" && in.Source != "") {
		codexFail(w, 400, "choose_checkout_source")
		return
	}
	if t.pending != nil {
		codexFail(w, 409, "execution_pending")
		return
	}
	m.refreshCodex(t)
	if t.active == nil || t.unknown || t.paused || (in.Ref != t.active.ref && in.Ref != t.active.alias) {
		codexFail(w, 409, "checkout_requires_current_lease")
		return
	}
	source := in.Source
	if in.Recover != "" {
		s.mu.Lock()
		old, ok := s.records[in.Recover]
		s.mu.Unlock()
		if !ok || old.Issue != t.active.ref || old.Path == "" || old.State == "preparing" {
			codexFail(w, 409, "invalid_recovery_workspace")
			return
		}
		source = old.Path
	}
	if !filepath.IsAbs(source) {
		codexFail(w, 400, "source_must_be_absolute")
		return
	}
	source, e := filepath.EvalSymlinks(source)
	if e != nil {
		codexFail(w, 400, "source_unavailable")
		return
	}
	managed := strings.HasPrefix(source, s.root+string(os.PathSeparator))
	for _, record := range s.list("") {
		if record.Path != "" && (source == record.Path || strings.HasPrefix(source, record.Path+string(os.PathSeparator))) {
			managed = true
		}
	}
	if in.Recover == "" && managed {
		codexFail(w, 409, "explicit_recovery_required")
		return
	}
	s.mu.Lock()
	if s.stopped || len(s.records) >= 4096 {
		s.mu.Unlock()
		codexFail(w, 429, "workspace_limit")
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.Unlock()
		codexFail(w, 429, "checkout_busy")
		return
	}
	kind := "commit"
	if in.Dirty {
		kind = "dirty"
	}
	r := workspaceRecord{ID: codexNonce(), Project: s.project, Issue: t.active.ref, Tenure: workspaceTenure(t.active.claim), Owner: t.identity, Source: source, Kind: kind, State: "preparing", RecoveredFrom: in.Recover}
	r.Request, r.Fingerprint = t.workspaceRequest, t.workspaceFingerprint
	if e = s.put(r); e != nil {
		// A rename may have committed before directory fsync failed. Preserve
		// the request identity in memory so a retry cannot create a second job.
		r.State, r.Error = "failed", "workspace_store_failed"
		s.records[r.ID] = r
		<-s.slots
		s.mu.Unlock()
		codexFail(w, 503, "workspace_store_failed")
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	claim := t.active.claim
	go m.prepareWorkspace(t, claim, r, in.Commit, in.Dirty)
	codexReply(w, r)
}
func (m *codexRuntime) prepareWorkspace(t *codexThread, claim string, r workspaceRecord, commit string, dirty bool) {
	s := m.workspaces
	defer s.wg.Done()
	defer func() { <-s.slots }()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	defer cancel()
	root, e := s.selectWorktreeRoot(r.Source)
	if e != nil {
		m.workspaceFailed(r, "worktree_root_unavailable")
		return
	}
	r.WorktreeRoot = root
	snap, e := s.capture(ctx, checkout.Options{Source: r.Source, Store: filepath.Join(root, ".snapshots"), Ref: commit, Dirty: dirty})
	if e != nil {
		m.workspaceFailed(r, "snapshot_failed")
		return
	}
	r.Snapshot = snap.ID
	r.SnapshotPath = snap.Path
	r.Repository = snap.Manifest.Repository
	r.Base = snap.Manifest.Base
	dest := filepath.Join(root, r.ID)
	r.Path = filepath.Join(dest, "worktree")
	// Revalidate the exact original tenure around every side-effectful phase.
	t.mu.Lock()
	valid := m.workspaceLive(t, claim)
	if valid {
		e = m.observeWorkspace(t, r, "preparing")
	}
	t.mu.Unlock()
	if !valid {
		m.workspaceFailed(r, "lease_lost")
		return
	}
	if e != nil {
		m.workspaceFailed(r, "workspace_observation_failed")
		return
	}
	s.mu.Lock()
	e = s.put(r)
	s.mu.Unlock()
	if e != nil {
		m.workspaceFailed(r, "workspace_store_failed")
		return
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e == nil {
		_, e = checkout.Restore(ctx, snap.Path, dest, "forge/"+r.ID)
	}
	if e != nil {
		m.workspaceFailed(r, "restore_failed")
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !m.workspaceLive(t, claim) {
		m.workspaceFailed(r, "lease_lost")
		return
	}
	if e = m.observeWorkspace(t, r, "ready"); e != nil {
		m.workspaceFailed(r, "workspace_observation_failed")
		return
	}
	r.State = "ready"
	s.mu.Lock()
	e = s.put(r)
	s.mu.Unlock()
	if e != nil {
		m.workspaceFailed(r, "workspace_store_failed")
	}
}
func (m *codexRuntime) workspaceLive(t *codexThread, claim string) bool {
	if t.workerSession != "" {
		m.refreshCodex(t)
		return t.active != nil && !t.unknown && t.active.claim == claim
	}
	m.mu.Lock()
	stopped := t.retired || t.instance.retired || m.ended[codexSessionKey{t.identity.Instance, t.identity.Session}] || !m.alive(t.instance.pid, t.instance.birth)
	m.mu.Unlock()
	if stopped || t.paused || t.pending != nil {
		return false
	}
	m.refreshCodex(t)
	return t.active != nil && !t.unknown && t.active.claim == claim
}
func (m *codexRuntime) observeWorkspace(t *codexThread, r workspaceRecord, state string) error {
	in := workspaceInput{Ref: r.Issue, SnapshotID: r.Snapshot, BaseCommit: r.Base, RepositoryID: r.Repository, WorkspaceID: r.ID, State: state, SourceKind: r.Kind, Device: "local", Location: r.Path, SnapshotLocation: r.SnapshotPath}
	res := m.dispatch(t, "issue_workspace", marshalCodex(in), t.active.token, "")
	if res.Code != 200 && res.Code != 201 {
		return errors.New("workspace observation failed")
	}
	return nil
}
func (m *codexRuntime) workspaceFailed(r workspaceRecord, code string) {
	r.State = "failed"
	if code == "lease_lost" || m.workspaces.ctx.Err() != nil {
		r.State = "orphaned"
	}
	r.Error = code
	s := m.workspaces
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(r)
}
func (m *codexRuntime) checkoutBusy(t *codexThread) bool {
	if m.workspaces == nil || t.active == nil {
		return false
	}
	for _, r := range m.workspaces.list(t.active.ref) {
		if r.Tenure == workspaceTenure(t.active.claim) && r.State == "preparing" {
			return true
		}
	}
	return false
}
func (m *codexRuntime) archiveWorkspaces(t *codexThread, claim string) map[string]any {
	result := map[string]any{"state": "archived", "workspaces": []string{}}
	if m.workspaces == nil || claim == "" {
		return result
	}
	s := m.workspaces
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	for _, r := range s.records {
		if r.Owner != t.identity || r.Tenure != workspaceTenure(claim) {
			continue
		}
		ids = append(ids, r.ID)
		r.State = "archived"
		r.Error = ""
		if e := s.put(r); e != nil {
			r.State = "archive_pending"
			r.Error = "workspace_archive_failed"
			s.records[r.ID] = r
			result["state"] = "pending"
		}
	}
	result["workspaces"] = ids
	return result
}
func (m *codexRuntime) markWorkspaces(t *codexThread, state string) {
	if m.workspaces == nil {
		return
	}
	s := m.workspaces
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r.Owner != t.identity {
			continue
		}
		if (state == "ready" || state == "paused") && (t.active == nil || r.Tenure != workspaceTenure(t.active.claim)) {
			continue
		}
		if r.State == "ready" || r.State == "paused" {
			r.State = state
			s.put(r)
		}
	}
}

func (m *codexRuntime) threadWorkspaces(t *codexThread) []workspaceRecord {
	out := []workspaceRecord{}
	if m.workspaces == nil {
		return out
	}
	for _, r := range m.workspaces.list("") {
		if r.Owner == t.identity && r.State != "archived" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}
