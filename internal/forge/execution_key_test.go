package forge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionKeyPersistsAndRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	first, err := loadExecutionSigner(dir)
	if err != nil {
		t.Fatal(err)
	}
	proof := executionProof{Subject: "execution:a", Session: first.session("pi-session"), IssueUID: "issue", ClaimUID: "claim"}
	token := first.sign(proof)
	second, err := loadExecutionSigner(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.verify(token, "pi-session"); err != nil {
		t.Fatal("restart lost receipt credential", err)
	}
	path := filepath.Join(dir, "execution-key")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key mode: %v %v", info, err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExecutionSigner(dir); err == nil {
		t.Fatal("corrupt key was silently replaced")
	}
}
