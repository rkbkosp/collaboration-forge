package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectWorktreeConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "forge.toml")
	os.WriteFile(file, []byte("[project]\nworktree_root = '/explicit/worktrees'\n"), 0600)
	cfg, err := loadProjectConfig(file, "")
	if err != nil || cfg.WorktreeRoot != "/explicit/worktrees" {
		t.Fatal(cfg, err)
	}
	cfg, err = loadProjectConfig(file, "/override")
	if err != nil || cfg.WorktreeRoot != "/override" {
		t.Fatal(cfg, err)
	}
	os.WriteFile(file, []byte("[project]\nworktree_rooot = '/typo'\n"), 0600)
	if _, err := loadProjectConfig(file, ""); err == nil {
		t.Fatal("ignored typo")
	}
	os.WriteFile(file, []byte("[project]\nworktree_root = 'relative'\n"), 0600)
	if _, err := loadProjectConfig(file, ""); err == nil {
		t.Fatal("accepted ambiguous relative path")
	}
	if _, err := loadProjectConfig(file+"missing", ""); err == nil {
		t.Fatal("ignored missing file")
	}
}
