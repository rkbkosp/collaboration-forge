package checkout

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func validOID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && (len(b) == 20 || len(b) == 32) && s == strings.ToLower(s)
}
func subBundle(s Submodule) string { return "submodule-" + digest([]byte(s.Path)) + ".bundle" }

// Only initialized, directly pinned submodules are read. Never follow a URL or
// run submodule update: all objects must already exist in this local checkout.
func scanSubmodule(ctx context.Context, root string, entry Entry, dirty bool) (Submodule, error) {
	s := Submodule{Path: entry.Path, Commit: string(entry.Data)}
	dir := filepath.Join(root, s.Path)
	real, e := filepath.EvalSymlinks(dir)
	if e != nil || real != dir {
		return s, errors.New("submodule must be an initialized local directory without symlinks")
	}
	top, e := textGit(ctx, dir, "rev-parse", "--show-toplevel")
	if e != nil || top != dir {
		return s, errors.New("submodule not initialized")
	}
	if dirty {
		head, e := textGit(ctx, dir, "rev-parse", "HEAD")
		if e != nil || head != s.Commit {
			return s, errors.New("submodule must be at its pinned index commit")
		}
		status, e := textGit(ctx, dir, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
		if e != nil || status != "" {
			return s, errors.New("dirty submodule unsupported; commit or preserve its work separately")
		}
	}
	s.Entries, e = entries(ctx, dir, true, s.Commit)
	if e != nil {
		return s, e
	}
	for _, v := range s.Entries {
		if v.Mode == "160000" {
			return s, errors.New("nested submodule snapshots unsupported")
		}
		if sensitive(v) {
			return s, errors.New("submodule contains suspected credential material")
		}
	}
	if dirty {
		actual, err := scan(ctx, dir, s.Commit, true)
		if err != nil {
			return s, err
		}
		expected, _ := json.Marshal(s.Entries)
		index, _ := json.Marshal(actual.Index)
		work, _ := json.Marshal(actual.Work)
		if string(expected) != string(index) || string(expected) != string(work) {
			return s, errors.New("submodule has uncommitted bytes or index state")
		}
	}
	return s, nil
}

func captureSubmodule(ctx context.Context, root, tmp string, s Submodule) (string, error) {
	bare := filepath.Join(tmp, "submodule-objects.git")
	if _, e := git(ctx, tmp, nil, "init", "--bare", "-q", bare); e != nil {
		return "", e
	}
	defer os.RemoveAll(bare)
	if _, e := git(ctx, bare, nil, "fetch", "--no-tags", filepath.Join(root, s.Path), s.Commit+":refs/heads/base"); e != nil {
		return "", e
	}
	bundle := filepath.Join(tmp, subBundle(s))
	if _, e := git(ctx, bare, nil, "bundle", "create", bundle, "refs/heads/base"); e != nil {
		return "", e
	}
	b, e := readBounded(bundle)
	if e != nil {
		return "", e
	}
	if e = writeSync(bundle, b); e != nil {
		return "", e
	}
	return digest(b), nil
}

func validateSubmodules(dir string, m Manifest) error {
	links := map[string]string{}
	work := map[string]Entry{}
	for _, v := range m.Work {
		work[v.Path] = v
	}
	for _, v := range m.Index {
		if v.Mode == "160000" {
			links[v.Path] = string(v.Data)
		}
	}
	if len(links) != len(m.Submodules) {
		return errors.New("submodule manifest mismatch")
	}
	seen := map[string]bool{}
	for _, s := range m.Submodules {
		if !validPath(s.Path) || !validOID(s.Commit) || links[s.Path] != s.Commit || seen[s.Path] {
			return errors.New("invalid submodule manifest")
		}
		seen[s.Path] = true
		v, ok := work[s.Path]
		if !ok || v.Mode != "160000" || string(v.Data) != s.Commit {
			return errors.New("submodule work entry missing or replaced")
		}
		for p := filepath.Dir(s.Path); p != "."; p = filepath.Dir(p) {
			if _, ok := work[p]; ok {
				return errors.New("submodule path conflicts with working file or symlink")
			}
		}
		paths := map[string]bool{}
		for _, v := range s.Entries {
			if safeEntry(v) != nil || v.Mode == "160000" || sensitive(v) || paths[v.Path] {
				return errors.New("invalid submodule entry")
			}
			paths[v.Path] = true
		}
		for p := range paths {
			for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
				if paths[d] {
					return errors.New("conflicting submodule paths")
				}
			}
		}
		b, e := readBounded(filepath.Join(dir, subBundle(s)))
		if e != nil || digest(b) != s.BundleHash {
			return errors.New("submodule bundle integrity failure")
		}
	}
	for _, v := range m.Work {
		if v.Mode == "160000" && links[v.Path] != string(v.Data) {
			return errors.New("submodule work mismatch")
		}
	}
	return nil
}

func restoreSubmodule(ctx context.Context, snapshot, root string, s Submodule) error {
	dir := filepath.Join(root, s.Path)
	if e := os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return e
	}
	if e := os.Mkdir(dir, 0700); e != nil {
		return e
	}
	if _, e := git(ctx, dir, nil, "init", "-q"); e != nil {
		return e
	}
	if _, e := git(ctx, dir, nil, "fetch", "--no-tags", filepath.Join(snapshot, subBundle(s)), "refs/heads/base"); e != nil {
		return e
	}
	got, e := textGit(ctx, dir, "rev-parse", "FETCH_HEAD")
	if e != nil || got != s.Commit {
		return errors.New("submodule bundle commit mismatch")
	}
	if _, e = git(ctx, dir, nil, "update-ref", "--no-deref", "HEAD", s.Commit); e != nil {
		return e
	}
	if _, e = git(ctx, dir, nil, "read-tree", s.Commit); e != nil {
		return e
	}
	// Materialize validated bytes without invoking Git checkout filters or hooks.
	actual, e := entries(ctx, dir, true, s.Commit)
	if e != nil {
		return e
	}
	if len(actual) != len(s.Entries) {
		return errors.New("submodule tree mismatch")
	}
	for i, v := range s.Entries {
		a := actual[i]
		if a.Path != v.Path || a.Mode != v.Mode || string(a.Data) != string(v.Data) {
			return errors.New("submodule tree mismatch")
		}
		p := filepath.Join(dir, v.Path)
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		if v.Mode == "120000" {
			e = os.Symlink(string(v.Data), p)
		} else {
			mode := os.FileMode(0644)
			if v.Mode == "100755" {
				mode = 0755
			}
			e = os.WriteFile(p, v.Data, mode)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
