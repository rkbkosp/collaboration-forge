package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoopbackLiteralOnly(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "127.0.0.2:0", "[::1]:8080"} {
		if err := validateListen(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "localhost:8080", "example.com:8080", "192.168.1.1:8080", "127.0.0.1:http", "127.0.0.1:-1", "127.0.0.1:65536"} {
		if err := validateListen(addr); err == nil {
			t.Errorf("accepted unsafe address %s", addr)
		}
	}
}

func TestDefaultTokenFileIsPrivateAndStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	first, err := loadAdminToken(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatal("token too short")
	}
	second, err := loadAdminToken(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("token changed across startup")
	}
	for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "admin-token"): 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %o", path, info.Mode().Perm())
		}
	}
	if _, err := loadAdminToken(dir, filepath.Join(dir, "missing-explicit-token")); err == nil {
		t.Fatal("missing explicit file silently generated")
	}
}

func TestExplicitTokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provided-token")
	token := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadAdminToken(dir, path)
	if err != nil || got != token {
		t.Fatalf("load: %q %v", got, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAdminToken(dir, path); err == nil {
		t.Fatal("insecure explicit token file accepted")
	}
}

func TestServeRejectsUnsafeListenerBeforeCreatingState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	var output bytes.Buffer
	err := run(context.Background(), []string{"serve", "--data-dir", dir, "--listen", "0.0.0.0:8080"}, &output)
	if err == nil {
		t.Fatal("unsafe listener accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("validation wrote state: %v", err)
	}
}

func TestServeCanceledContextShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, []string{"serve", "--data-dir", t.TempDir(), "--listen", "127.0.0.1:0"}, &output); err != nil {
		t.Fatal(err)
	}
}
