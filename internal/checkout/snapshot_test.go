package checkout

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s: %v", args, b, e)
	}
	return b
}
func seed(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	gitTest(t, d, "config", "user.name", "Test")
	gitTest(t, d, "config", "user.email", "test@example.invalid")
	os.WriteFile(filepath.Join(d, "a"), []byte("base\n"), 0644)
	os.WriteFile(filepath.Join(d, "gone"), []byte("delete\n"), 0644)
	gitTest(t, d, "add", ".")
	gitTest(t, d, "commit", "-qm", "base")
	return d
}
func TestSnapshotRestoresThreeLayersWithoutTouchingSource(t *testing.T) {
	d := seed(t)
	os.WriteFile(filepath.Join(d, "a"), []byte("staged\n"), 0644)
	gitTest(t, d, "add", "a")
	os.WriteFile(filepath.Join(d, "a"), []byte("working\n"), 0644)
	os.Remove(filepath.Join(d, "gone"))
	os.WriteFile(filepath.Join(d, "binary"), []byte{0, 255, 1}, 0755)
	os.Symlink("a", filepath.Join(d, "link"))
	os.WriteFile(filepath.Join(d, ".gitignore"), []byte("ignored\n"), 0644)
	os.WriteFile(filepath.Join(d, "ignored"), []byte("private"), 0644)
	before := gitTest(t, d, "status", "--porcelain=v1", "-z")
	idx, _ := os.ReadFile(filepath.Join(d, ".git", "index"))
	s, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Dirty: true})
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, gitTest(t, d, "status", "--porcelain=v1", "-z")) {
		t.Fatal("source changed")
	}
	after, _ := os.ReadFile(filepath.Join(d, ".git", "index"))
	if !bytes.Equal(idx, after) {
		t.Fatal("index changed")
	}
	dest := filepath.Join(t.TempDir(), "execution")
	w, e := Restore(context.Background(), s.Path, dest, "forge/test")
	if e != nil {
		t.Fatal(e)
	}
	if string(gitTest(t, w, "show", ":a")) != "staged\n" {
		t.Fatal("staging lost")
	}
	b, _ := os.ReadFile(filepath.Join(w, "a"))
	if string(b) != "working\n" {
		t.Fatal("work content lost")
	}
	if !bytes.Equal(before, gitTest(t, w, "status", "--porcelain=v1", "-z")) {
		t.Fatalf("status mismatch %q %q", before, gitTest(t, w, "status", "--porcelain=v1", "-z"))
	}
	if _, e = os.Stat(filepath.Join(w, "ignored")); !os.IsNotExist(e) {
		t.Fatal("ignored copied")
	}
	if _, e = Restore(context.Background(), s.Path, dest, "forge/again"); e == nil {
		t.Fatal("overwrote destination")
	}
	// Artifact can restore without access to the original source.
	os.RemoveAll(d)
	if _, e = Restore(context.Background(), s.Path, filepath.Join(t.TempDir(), "restored"), "forge/fresh"); e != nil {
		t.Fatal(e)
	}
}
func TestSnapshotRejectsCorruptionAndUnsupportedState(t *testing.T) {
	d := seed(t)
	gitTest(t, d, "update-index", "--assume-unchanged", "a")
	if _, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Dirty: true}); e == nil {
		t.Fatal("special index accepted")
	}
	gitTest(t, d, "update-index", "--no-assume-unchanged", "a")
	s, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Ref: "HEAD"})
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(s.Path, "base.bundle"), []byte("corrupt"), 0600)
	if _, e = Restore(context.Background(), s.Path, filepath.Join(t.TempDir(), "bad"), "forge/bad"); e == nil {
		t.Fatal("corruption accepted")
	}
}
func TestSnapshotRefExcludesDirtyAndRejectsSensitiveInput(t *testing.T) {
	d := seed(t)
	os.WriteFile(filepath.Join(d, "a"), []byte("dirty"), 0644)
	s, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Ref: "HEAD"})
	if e != nil {
		t.Fatal(e)
	}
	w, e := Restore(context.Background(), s.Path, filepath.Join(t.TempDir(), "x"), "forge/ref")
	if e != nil {
		t.Fatal(e)
	}
	if len(gitTest(t, w, "status", "--porcelain")) != 0 {
		t.Fatal("ref captured dirty")
	}
	os.WriteFile(filepath.Join(d, ".env"), []byte("TOKEN=private"), 0600)
	if _, e = Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Dirty: true}); e == nil {
		t.Fatal("sensitive file accepted")
	}
}

func TestSnapshotRejectsNestedRepoAndIntentToAdd(t *testing.T) {
	d := seed(t)
	nested := filepath.Join(d, "nested")
	os.Mkdir(nested, 0700)
	gitTest(t, nested, "init", "-q")
	if _, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Dirty: true}); e == nil {
		t.Fatal("nested repo accepted")
	}
	os.RemoveAll(nested)
	os.WriteFile(filepath.Join(d, "ita"), []byte("x"), 0644)
	gitTest(t, d, "add", "-N", "ita")
	if _, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Dirty: true}); e == nil {
		t.Fatal("intent to add accepted")
	}
}
func TestSnapshotPreservesUnusualPathsAndShapeChanges(t *testing.T) {
	d := seed(t)
	os.Remove(filepath.Join(d, "a"))
	os.Mkdir(filepath.Join(d, "a"), 0700)
	os.WriteFile(filepath.Join(d, "a", "child"), []byte("child"), 0644)
	gitTest(t, d, "add", "a")
	os.WriteFile(filepath.Join(d, "tab\tand\nnewline"), []byte("weird"), 0644)
	s, e := Capture(context.Background(), Options{Source: d, Store: t.TempDir(), Dirty: true})
	if e != nil {
		t.Fatal(e)
	}
	w, e := Restore(context.Background(), s.Path, filepath.Join(t.TempDir(), "x"), "forge/shape")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(gitTest(t, d, "status", "--porcelain", "-z"), gitTest(t, w, "status", "--porcelain", "-z")) {
		t.Fatal("shape mismatch")
	}
}
func TestConcurrentSourceChangeRejectsSnapshot(t *testing.T) {
	d := seed(t)
	store := t.TempDir()
	_, e := capture(context.Background(), Options{Source: d, Store: store, Dirty: true}, func() { os.WriteFile(filepath.Join(d, "a"), []byte("racing write"), 0644) })
	if e == nil {
		t.Fatal("mixed snapshot accepted")
	}
	files, _ := os.ReadDir(store)
	if len(files) != 0 {
		t.Fatal("published failed snapshot")
	}
}
