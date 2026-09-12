package checkout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedSubmoduleOfflineSnapshot(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "dirty"}[dirty], func(t *testing.T) {
			root, child := seed(t), seed(t)
			gitTest(t, root, "-c", "protocol.file.allow=always", "submodule", "add", child, "deps/kata")
			gitTest(t, root, "commit", "-qam", "pin dependency")
			before := string(gitTest(t, root, "status", "--porcelain"))
			opts := Options{Source: root, Store: t.TempDir(), Dirty: dirty}
			if !dirty {
				opts.Ref = "HEAD"
			}
			snap, err := Capture(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if string(gitTest(t, root, "status", "--porcelain")) != before {
				t.Fatal("source changed")
			}
			os.RemoveAll(root)
			os.RemoveAll(child)
			work, err := Restore(context.Background(), snap.Path, filepath.Join(t.TempDir(), "restored"), "forge/test")
			if err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(filepath.Join(work, "deps/kata/a")); err != nil || string(b) != "base\n" {
				t.Fatal("missing dependency", err)
			}
			if got := strings.TrimSpace(string(gitTest(t, work, "status", "--porcelain"))); got != "" {
				t.Fatal(got)
			}
			if _, err = Capture(context.Background(), Options{Source: work, Store: t.TempDir(), Dirty: true}); err != nil {
				t.Fatal("recovery", err)
			}
		})
	}
}

func TestDirtySubmoduleRejectedWithoutChangingSource(t *testing.T) {
	root, child := seed(t), seed(t)
	gitTest(t, root, "-c", "protocol.file.allow=always", "submodule", "add", child, "deps/kata")
	gitTest(t, root, "commit", "-qam", "pin dependency")
	os.WriteFile(filepath.Join(root, "deps/kata/a"), []byte("local work\n"), 0644)
	if _, err := Capture(context.Background(), Options{Source: root, Store: t.TempDir(), Dirty: true}); err == nil {
		t.Fatal("silently lost dirty dependency")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "deps/kata/a")); string(b) != "local work\n" {
		t.Fatal("source modified")
	}
}

func TestSubmoduleManifestRejectsSymlinkAncestor(t *testing.T) {
	oid := strings.Repeat("a", 40)
	link := Entry{Path: "outside/child", Mode: "160000", Data: []byte(oid)}
	manifest := Manifest{Index: []Entry{link}, Work: []Entry{{Path: "outside", Mode: "120000", Data: []byte("/tmp")}}, Submodules: []Submodule{{Path: link.Path, Commit: oid}}}
	if err := validateSubmodules(t.TempDir(), manifest); err == nil {
		t.Fatal("submodule restore could follow a worktree symlink")
	}
}

// Opt-in acceptance against a quiescent development worktree, not the active
// user checkout. Bundles and the restored build live only in disposable dirs.
func TestRepositoryWithPinnedKataBuildsAfterRestore(t *testing.T) {
	source := os.Getenv("FORGE_CHECKOUT_SOURCE")
	if source == "" {
		t.Skip("set FORGE_CHECKOUT_SOURCE to a quiescent Forge development worktree")
	}
	before := string(gitTest(t, source, "status", "--porcelain=v1", "-z"))
	snap, err := Capture(context.Background(), Options{Source: source, Store: t.TempDir(), Dirty: true})
	if err != nil {
		t.Fatal(err)
	}
	work, err := Restore(context.Background(), snap.Path, filepath.Join(t.TempDir(), "restored"), "forge/acceptance")
	if err != nil {
		t.Fatal(err)
	}
	if string(gitTest(t, source, "status", "--porcelain=v1", "-z")) != before {
		t.Fatal("source state changed")
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "forged"), "./cmd/forged")
	cmd.Dir = work
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restored Forge build: %v\n%s", err, output)
	}
}
