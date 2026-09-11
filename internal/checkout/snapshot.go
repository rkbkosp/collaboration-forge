// Package checkout stores immutable source snapshots, never execution credentials.
package checkout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxBytes = 256 << 20
const maxFile = 32 << 20

type Options struct {
	Source, Store, Ref string
	Dirty              bool
}
type Entry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Data []byte `json:"data"`
}
type Manifest struct {
	Version    int     `json:"version"`
	Base       string  `json:"base_commit"`
	Repository string  `json:"repository_id"`
	Kind       string  `json:"kind"`
	BundleHash string  `json:"bundle_sha256"`
	Index      []Entry `json:"index"`
	Work       []Entry `json:"work"`
	Excluded   string  `json:"excluded"`
}
type Snapshot struct {
	ID, Path string
	Manifest Manifest
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func git(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	c.Dir = dir
	// Caller Git overrides must not redirect reads/writes into another repository.
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			c.Env = append(c.Env, v)
		}
	}
	c.Env = append(c.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	c.Stdin = bytes.NewReader(input)
	var out limitedBuffer
	c.Stdout = &out
	c.Stderr = &limitedBuffer{}
	if e := c.Run(); e != nil {
		return nil, fmt.Errorf("git %s failed (diagnostics suppressed)", args[0])
	}
	return out.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxBytes {
		return 0, errors.New("snapshot exceeds size limit")
	}
	return b.Buffer.Write(p)
}
func textGit(ctx context.Context, dir string, args ...string) (string, error) {
	b, e := git(ctx, dir, nil, args...)
	return strings.TrimSpace(string(b)), e
}
func validPath(p string) bool {
	if p == "" || strings.Contains(p, "\x00") || strings.Contains(p, "\\") || filepath.IsAbs(p) || filepath.ToSlash(filepath.Clean(p)) != p {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." || strings.EqualFold(c, ".git") {
			return false
		}
	}
	return true
}
func safeEntry(e Entry) error {
	if !validPath(e.Path) || (e.Mode != "100644" && e.Mode != "100755" && e.Mode != "120000") || len(e.Data) > maxFile {
		return errors.New("unsupported snapshot entry")
	}
	return nil
}
func sensitive(e Entry) bool {
	name := strings.ToLower(filepath.Base(e.Path))
	if name == ".env" || strings.HasPrefix(name, ".env.") || name == "id_rsa" || name == "id_ed25519" || name == "credentials" || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") {
		return true
	}
	return bytes.Contains(e.Data, []byte("-----BEGIN PRIVATE KEY-----")) || bytes.Contains(e.Data, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")) || bytes.Contains(e.Data, []byte("-----BEGIN RSA PRIVATE KEY-----"))
}
func split0(b []byte) []string {
	s := strings.Split(string(b), "\x00")
	if len(s) > 0 && s[len(s)-1] == "" {
		s = s[:len(s)-1]
	}
	return s
}
func entries(ctx context.Context, root string, tree bool, ref string) ([]Entry, error) {
	args := []string{"ls-files", "--stage", "-z"}
	if tree {
		args = []string{"ls-tree", "-r", "-z", ref}
	}
	raw, e := git(ctx, root, nil, args...)
	if e != nil {
		return nil, e
	}
	result := []Entry{}
	for _, r := range split0(raw) {
		a, p, ok := strings.Cut(r, "\t")
		f := strings.Fields(a)
		if !ok || len(f) != 3 {
			return nil, errors.New("invalid Git entry")
		}
		mode, oid := f[0], f[1]
		if tree {
			oid = f[2]
			if f[1] != "blob" {
				return nil, errors.New("submodules unsupported")
			}
		} else if f[2] != "0" {
			return nil, errors.New("unresolved index stages unsupported")
		}
		b, e := git(ctx, root, nil, "cat-file", "blob", oid)
		if e != nil {
			return nil, e
		}
		v := Entry{p, mode, b}
		if e = safeEntry(v); e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}
func readWork(root, p string) (Entry, bool, error) {
	if !validPath(p) {
		return Entry{}, false, errors.New("unsafe source path")
	}
	components := strings.Split(p, "/")
	for i := 1; i < len(components); i++ {
		info, e := os.Lstat(filepath.Join(root, filepath.Join(components[:i]...)))
		if os.IsNotExist(e) {
			return Entry{}, false, nil
		}
		if e != nil {
			return Entry{}, false, e
		}
		if !info.IsDir() {
			return Entry{}, false, errors.New("non-directory source ancestor")
		}
	}
	full := filepath.Join(root, p)
	i, e := os.Lstat(full)
	if os.IsNotExist(e) {
		return Entry{}, false, nil
	}
	if e != nil {
		return Entry{}, false, e
	}
	v := Entry{Path: p, Mode: "100644"}
	if i.Mode()&os.ModeSymlink != 0 {
		s, e := os.Readlink(full)
		v.Data = []byte(s)
		v.Mode = "120000"
		return v, true, e
	}
	if i.IsDir() {
		return v, false, nil
	}
	if !i.Mode().IsRegular() || i.Size() > maxFile {
		return v, false, errors.New("unsupported file type or size")
	}
	if i.Mode()&0111 != 0 {
		v.Mode = "100755"
	}
	v.Data, e = os.ReadFile(full)
	if len(v.Data) > maxFile {
		return v, false, errors.New("file grew beyond limit")
	}
	return v, true, e
}
func scan(ctx context.Context, root, ref string, dirty bool) (Manifest, error) {
	m := Manifest{Version: 1, Kind: "commit", Excluded: "ignored files; filesystem timestamps, ownership and extended attributes"}
	var e error
	m.Base, e = textGit(ctx, root, "rev-parse", "--verify", ref+"^{commit}")
	if e != nil {
		return m, e
	}
	common, e := textGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return m, e
	}
	m.Repository = digest([]byte(common))
	m.Index, e = entries(ctx, root, !dirty, m.Base)
	if e != nil {
		return m, e
	}
	m.Work = append([]Entry{}, m.Index...)
	if dirty {
		m.Kind = "dirty"
		flags, e := git(ctx, root, nil, "ls-files", "-v", "-z")
		if e != nil {
			return m, e
		}
		for _, r := range split0(flags) {
			if !strings.HasPrefix(r, "H ") {
				return m, errors.New("special index flags unsupported")
			}
		}
		// Intent-to-add and extended index states are not represented by this format.
		debug, e := git(ctx, root, nil, "ls-files", "--debug")
		if e != nil {
			return m, e
		}
		for _, line := range strings.Split(string(debug), "\n") {
			if strings.Contains(line, "flags:") && !strings.HasSuffix(line, "flags: 0") {
				return m, errors.New("extended index flags unsupported")
			}
		}
		all, e := git(ctx, root, nil, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		if e != nil {
			return m, e
		}
		seen := map[string]bool{}
		m.Work = []Entry{}
		for _, p := range split0(all) {
			if seen[p] {
				continue
			}
			seen[p] = true
			v, exists, e := readWork(root, p)
			if e != nil {
				return m, e
			}
			if exists {
				m.Work = append(m.Work, v)
			}
		}
		sort.Slice(m.Work, func(i, j int) bool { return m.Work[i].Path < m.Work[j].Path })
	}
	for _, list := range [][]Entry{m.Index, m.Work} {
		for _, v := range list {
			if sensitive(v) {
				return m, errors.New("snapshot contains a suspected credential file or private key; exclude or remove it before retrying")
			}
		}
	}
	b, _ := json.Marshal(m)
	if len(b) > maxBytes {
		return m, errors.New("snapshot exceeds limit")
	}
	return m, nil
}

// Capture reads the entire repository; dirty capture requires a quiescent source.
// Two complete scans detect changes but are not a filesystem atomic snapshot.
func Capture(ctx context.Context, o Options) (Snapshot, error) { return capture(ctx, o, nil) }
func capture(ctx context.Context, o Options, beforeRescan func()) (Snapshot, error) {
	if o.Dirty == (o.Ref != "") {
		return Snapshot{}, errors.New("choose exactly one of dirty or ref")
	}
	root, e := textGit(ctx, o.Source, "rev-parse", "--show-toplevel")
	if e != nil {
		return Snapshot{}, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return Snapshot{}, e
	}
	store, e := filepath.Abs(o.Store)
	if e != nil {
		return Snapshot{}, e
	}
	if store == root || strings.HasPrefix(store, root+string(os.PathSeparator)) {
		return Snapshot{}, errors.New("snapshot store must be outside source worktree")
	}
	ancestor := store
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return Snapshot{}, errors.New("invalid store ancestor")
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return Snapshot{}, err
	}
	if realAncestor == root || strings.HasPrefix(realAncestor, root+string(os.PathSeparator)) {
		return Snapshot{}, errors.New("snapshot store resolves inside source")
	}
	if e = os.MkdirAll(store, 0700); e != nil {
		return Snapshot{}, e
	}
	store, e = filepath.EvalSymlinks(store)
	if e != nil {
		return Snapshot{}, e
	}
	if store == root || strings.HasPrefix(store, root+string(os.PathSeparator)) {
		return Snapshot{}, errors.New("snapshot store must be outside source worktree")
	}
	ref := o.Ref
	if o.Dirty {
		ref = "HEAD"
	}
	m, e := scan(ctx, root, ref, o.Dirty)
	if e != nil {
		return Snapshot{}, e
	}
	tmp, e := os.MkdirTemp(store, ".preparing-")
	if e != nil {
		return Snapshot{}, e
	}
	defer os.RemoveAll(tmp)
	bare := filepath.Join(tmp, "objects.git")
	if _, e = git(ctx, tmp, nil, "init", "--bare", "-q", bare); e != nil {
		return Snapshot{}, e
	}
	if _, e = git(ctx, bare, nil, "fetch", "--no-tags", root, m.Base+":refs/heads/base"); e != nil {
		return Snapshot{}, e
	}
	bundle := filepath.Join(tmp, "base.bundle")
	if _, e = git(ctx, bare, nil, "bundle", "create", bundle, "refs/heads/base"); e != nil {
		return Snapshot{}, e
	}
	b, e := readBounded(bundle)
	if e != nil {
		return Snapshot{}, e
	}
	m.BundleHash = digest(b)
	if beforeRescan != nil {
		beforeRescan()
	}
	check, e := scan(ctx, root, ref, o.Dirty)
	if e != nil {
		return Snapshot{}, e
	}
	check.BundleHash = m.BundleHash
	a, _ := json.Marshal(m)
	z, _ := json.Marshal(check)
	if !bytes.Equal(a, z) {
		return Snapshot{}, errors.New("source changed during snapshot; stop writers and retry")
	}
	if e = os.RemoveAll(bare); e != nil {
		return Snapshot{}, e
	}
	if e = writeSync(filepath.Join(tmp, "manifest.json"), a); e != nil {
		return Snapshot{}, e
	}
	if e = writeSync(bundle, b); e != nil {
		return Snapshot{}, e
	}
	id := digest(a)
	dest := filepath.Join(store, id)
	if _, e = os.Stat(dest); e == nil {
		if _, e = Load(dest); e != nil {
			return Snapshot{}, e
		}
	} else {
		if e = syncDir(tmp); e != nil {
			return Snapshot{}, e
		}
		if e = os.Rename(tmp, dest); e != nil {
			return Snapshot{}, e
		}
		if e = syncDir(store); e != nil {
			return Snapshot{}, e
		}
	}
	return Snapshot{id, dest, m}, nil
}
func readBounded(p string) ([]byte, error) {
	i, e := os.Lstat(p)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() || i.Size() > maxBytes {
		return nil, errors.New("invalid artifact file")
	}
	b, e := os.ReadFile(p)
	if len(b) > maxBytes {
		return nil, errors.New("artifact too large")
	}
	return b, e
}
func writeSync(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	return errors.Join(e, f.Sync(), f.Close())
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}
func Load(path string) (Snapshot, error) {
	a, e := readBounded(filepath.Join(path, "manifest.json"))
	if e != nil {
		return Snapshot{}, e
	}
	var m Manifest
	if e = json.Unmarshal(a, &m); e != nil {
		return Snapshot{}, e
	}
	if m.Version != 1 || digest(a) != filepath.Base(filepath.Clean(path)) {
		return Snapshot{}, errors.New("snapshot identity mismatch")
	}
	b, e := readBounded(filepath.Join(path, "base.bundle"))
	if e != nil || digest(b) != m.BundleHash {
		return Snapshot{}, errors.New("snapshot bundle integrity failure")
	}
	for _, list := range [][]Entry{m.Index, m.Work} {
		seen := map[string]bool{}
		for _, v := range list {
			if e = safeEntry(v); e != nil {
				return Snapshot{}, e
			}
			if seen[v.Path] {
				return Snapshot{}, errors.New("duplicate snapshot path")
			}
			seen[v.Path] = true
		}
		for p := range seen {
			for parent := filepath.Dir(p); parent != "."; parent = filepath.Dir(parent) {
				if seen[parent] {
					return Snapshot{}, errors.New("conflicting snapshot paths")
				}
			}
		}
	}
	return Snapshot{digest(a), path, m}, nil
}

// Restore creates a new execution container with its own Git object store and
// linked worktree. It never touches a source repository or existing destination.
func Restore(ctx context.Context, snapshot, dest, branch string) (string, error) {
	s, e := Load(snapshot)
	if e != nil {
		return "", e
	}
	dest, e = filepath.Abs(dest)
	if e != nil {
		return "", e
	}
	if e = os.Mkdir(dest, 0700); e != nil {
		return "", e
	}
	// Partial destinations are intentionally retained for diagnosis/recovery.
	bare := filepath.Join(dest, "repository.git")
	if _, e = git(ctx, dest, nil, "init", "--bare", "-q", bare); e != nil {
		return "", e
	}
	bundle, e := filepath.Abs(filepath.Join(snapshot, "base.bundle"))
	if e != nil {
		return "", e
	}
	if _, e = git(ctx, bare, nil, "fetch", "--no-tags", bundle, "refs/heads/base:refs/heads/base"); e != nil {
		return "", e
	}
	if got, _ := textGit(ctx, bare, "rev-parse", "refs/heads/base"); got != s.Manifest.Base {
		return "", errors.New("bundle base mismatch")
	}
	w := filepath.Join(dest, "worktree")
	if _, e = git(ctx, bare, nil, "-c", "core.autocrlf=false", "worktree", "add", "--no-checkout", "-b", branch, w, s.Manifest.Base); e != nil {
		return "", e
	}
	for _, v := range s.Manifest.Work {
		p := filepath.Join(w, v.Path)
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return "", e
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
			return "", e
		}
	}
	if _, e = git(ctx, w, nil, "read-tree", "--empty"); e != nil {
		return "", e
	}
	for _, v := range s.Manifest.Index {
		oid, e := git(ctx, w, v.Data, "hash-object", "-w", "--stdin")
		if e != nil {
			return "", e
		}
		if _, e = git(ctx, w, nil, "update-index", "--add", "--cacheinfo", v.Mode, strings.TrimSpace(string(oid)), v.Path); e != nil {
			return "", e
		}
	}
	// Verify actual bytes, modes, staging and absence of extra nonignored files.
	actual, e := scan(ctx, w, "HEAD", true)
	if e != nil {
		return "", e
	}
	a, _ := json.Marshal(actual.Index)
	b, _ := json.Marshal(s.Manifest.Index)
	x, _ := json.Marshal(actual.Work)
	y, _ := json.Marshal(s.Manifest.Work)
	if actual.Base != s.Manifest.Base || !bytes.Equal(a, b) || !bytes.Equal(x, y) {
		return "", errors.New("restored workspace verification failed")
	}
	if e = writeSync(filepath.Join(dest, "snapshot-id"), []byte(s.ID+"\n")); e != nil {
		return "", e
	}
	return w, nil
}
