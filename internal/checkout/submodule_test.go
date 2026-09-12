package checkout

import (
	"context"
	"os"
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
