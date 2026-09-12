package forge

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeRootPriority(t *testing.T) {
	for _, mode := range []string{"explicit", "volume", "fallback", "explicit-failure", "all-fail"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			volume, fallback := filepath.Join(base, "volume"), filepath.Join(base, "support")
			explicit := ""
			want := volume
			if mode == "explicit" || mode == "explicit-failure" {
				explicit = filepath.Join(base, "chosen")
				want = explicit
			}
			if mode == "fallback" || mode == "all-fail" {
				os.WriteFile(volume, []byte("blocked"), 0600)
				want = fallback
			}
			if mode == "explicit-failure" {
				os.WriteFile(explicit, []byte("blocked"), 0600)
			}
			if mode == "all-fail" {
				os.WriteFile(fallback, []byte("blocked"), 0600)
			}
			got, err := chooseWorktreeRoot(explicit, func() (string, error) { return volume, nil }, func() (string, error) { return fallback, nil })
			if mode == "explicit-failure" || mode == "all-fail" {
				if err == nil {
					t.Fatal("silently ignored unusable root")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, _ = filepath.EvalSymlinks(want)
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
			if mode == "explicit" {
				if _, err := os.Stat(volume); !os.IsNotExist(err) {
					t.Fatal("explicit root did not bypass default")
				}
			}
		})
	}
}

func TestCheckoutPathDoesNotEncodeRelationships(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	r = waitCheckout(t, m, i, r.ID)
	if r.State != "ready" {
		t.Fatal(r.State, r.Error)
	}
	// A single opaque workspace identifier is the only namespace component.
	if filepath.Base(filepath.Dir(r.Path)) != r.ID || filepath.Base(r.Path) != "worktree" {
		t.Fatal(r.Path)
	}
	root := filepath.Dir(filepath.Dir(r.Path))
	if filepath.Base(root) != "trees" {
		t.Fatal("fixture root not respected", r.Path)
	}
	if root != filepath.Join(m.workspaces.root, "trees") {
		t.Fatalf("path encodes ledger relationships: %s", r.Path)
	}
}

func TestVolumeRootMatchesSourceFilesystem(t *testing.T) {
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := sameVolumeWorktreeRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect only; the test must never create a directory at a real mount root.
	var src, dst unix.Stat_t
	if err = unix.Stat(source, &src); err != nil {
		t.Fatal(err)
	}
	if err = unix.Stat(filepath.Dir(root), &dst); err != nil {
		t.Fatal(err)
	}
	if src.Dev != dst.Dev {
		t.Fatal("default root crossed volumes")
	}
}

func TestWorktreeRootDiscoveryFailureFallsBack(t *testing.T) {
	fallback := filepath.Join(t.TempDir(), "support")
	got, err := chooseWorktreeRoot("", func() (string, error) { return "", os.ErrPermission }, func() (string, error) { return fallback, nil })
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(fallback)
	if got != want {
		t.Fatal(got)
	}
	if _, err := usableWorktreeRoot("relative"); err == nil {
		t.Fatal("accepted relative root")
	}
}

func TestCheckoutOutsideRecordRootRequiresExplicitRecovery(t *testing.T) {
	m, i, uid, source := checkoutFixture(t)
	m.workspaces.worktreeRoot = t.TempDir()
	r := beginCheckout(t, m, i, map[string]any{"ref": uid, "source": source, "dirty": true})
	r = waitCheckout(t, m, i, r.ID)
	if r.State != "ready" {
		t.Fatal(r.State, r.Error)
	}
	w := codexRequest(m, "tool", codexCommand{Identity: i, Operation: "checkout", Params: marshalCodex(map[string]any{"ref": uid, "source": r.Path, "dirty": true})}, codexTestToken)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "explicit_recovery_required") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLegacyWorkspaceRecordsRemainReadable(t *testing.T) {
	root := t.TempDir()
	s, err := newWorkspaceStore(root, "01K00000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	r := workspaceRecord{ID: codexNonce(), Project: s.project, Issue: "01K00000000000000000000002", Tenure: strings.Repeat("a", 64), Repository: strings.Repeat("b", 64), State: "ready"}
	r.Path = filepath.Join(s.root, "trees", r.Issue, r.Tenure, r.Repository, r.ID, "worktree")
	if err = s.put(r); err != nil {
		t.Fatal(err)
	}
	s.close()
	s, err = newWorkspaceStore(root, r.Project)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	got := s.list(r.Issue)
	if len(got) != 1 || got[0].Path != r.Path || got[0].State != "orphaned" {
		t.Fatal(got)
	}
}
