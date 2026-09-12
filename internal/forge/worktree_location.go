package forge

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// ProjectConfig is host-owned configuration, never worker Issue metadata.
type ProjectConfig struct {
	WorktreeRoot string `toml:"worktree_root"`
}

func chooseWorktreeRoot(explicit string, volume, support func() (string, error)) (string, error) {
	if explicit != "" {
		return usableWorktreeRoot(explicit)
	}
	candidate, err := volume()
	if err == nil {
		if root, e := usableWorktreeRoot(candidate); e == nil {
			return root, nil
		}
	}
	candidate, err = support()
	if err != nil {
		return "", err
	}
	return usableWorktreeRoot(candidate)
}

func usableWorktreeRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("worktree root must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	// Probe actual creation, not just mode bits (ACLs/read-only mounts can differ).
	probe, err := os.MkdirTemp(root, ".probe-")
	if err != nil {
		return "", err
	}
	if err = os.Remove(probe); err != nil {
		return "", err
	}
	return root, nil
}

func sameVolumeWorktreeRoot(source string) (string, error) {
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	var sourceStat unix.Stat_t
	if err = unix.Stat(root, &sourceStat); err != nil {
		return "", err
	}
	for {
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		var st unix.Stat_t
		if err = unix.Stat(parent, &st); err != nil {
			return "", err
		}
		if st.Dev != sourceStat.Dev {
			break
		}
		root = parent
	}
	candidate := filepath.Join(root, ".forge-worktrees-"+strconv.Itoa(os.Getuid()))
	// An existing symlink/mount must not turn the volume preference into a
	// silent cross-volume selection.
	if resolved, e := filepath.EvalSymlinks(candidate); e == nil {
		var st unix.Stat_t
		if e = unix.Stat(resolved, &st); e != nil {
			return "", e
		}
		if st.Dev != sourceStat.Dev {
			return "", errors.New("worktree root is on another volume")
		}
	}
	return candidate, nil
}

func applicationWorktreeRoot() (string, error) {
	root, err := os.UserConfigDir() // Application Support on macOS.
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Forge", "worktrees"), nil
}

func (s *workspaceStore) selectWorktreeRoot(source string) (string, error) {
	return chooseWorktreeRoot(s.worktreeRoot, func() (string, error) { return sameVolumeWorktreeRoot(source) }, applicationWorktreeRoot)
}
