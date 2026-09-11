// Package forge embeds Kata behind Forge's host-owned authentication boundary.
package forge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
	"go.kenn.io/kata"
	"golang.org/x/sys/unix"
)

// ErrDataDirLocked means another Forge instance already owns the data directory.
var ErrDataDirLocked = errors.New("forge: data directory is already locked")

// Config selects a private data directory and its single stable Kata project.
// AdminToken is required, is never persisted here, and must be at least 32
// printable ASCII characters without whitespace. Treat it as a supervisor secret.
type Config struct {
	WorkspaceRoot string // managed filesystem artifacts, not an Issue database
	DataDir       string
	ProjectName   string
	AdminToken    string
	WorkerToken   string // optional; enables only the typed worker façade
}

type persistedConfig struct {
	ProjectUID  string `json:"project_uid"`
	ProjectName string `json:"project_name"`
}

// Server owns one embedded service, its workers, and the data-directory lock.
// The host owns the HTTP listener and should drain requests before Close.
type Server struct {
	service   *kata.Service
	project   kata.Project
	config    Config
	handler   http.Handler
	tools     http.Handler
	codex     *codexRuntime
	signer    executionSigner
	lock      *os.File
	cancel    context.CancelFunc
	runDone   chan struct{}
	runErr    error // published by closing runDone
	closeOnce sync.Once
	closeErr  error
}

// New bootstraps the stable project and starts Kata's background workers,
// including the authoritative timed-lease sweeper. It never opens a listener.
func New(cfg Config) (_ *Server, err error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("forge: data directory is required")
	}
	if err := ValidateAdminToken(cfg.AdminToken); err != nil {
		return nil, err
	}
	if cfg.WorkerToken != "" {
		if err := ValidateAdminToken(cfg.WorkerToken); err != nil || cfg.WorkerToken == cfg.AdminToken {
			return nil, errors.New("forge: worker token must be valid and distinct from the supervisor token")
		}
	}
	if strings.TrimSpace(cfg.ProjectName) == "" || strings.Contains(cfg.ProjectName, "#") {
		return nil, errors.New("forge: invalid project name")
	}
	for _, r := range cfg.ProjectName {
		if !unicode.IsPrint(r) {
			return nil, errors.New("forge: invalid project name")
		}
	}
	if err := PrepareDataDir(cfg.DataDir); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	lock, err := openPrivateFile(filepath.Join(dir, "forge.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("forge: open lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrDataDirLocked
		}
		return nil, fmt.Errorf("forge: lock data directory: %w", err)
	}
	// Never unlink the lock file: another process may already have opened it.
	defer func() {
		if err != nil {
			err = errors.Join(err, unlock(lock))
		}
	}()
	persisted, err := loadProjectConfig(dir, cfg.ProjectName)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "kata.db")
	// Precreate the DB privately; SQLite derives its WAL/SHM modes from it.
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		flags := os.O_RDWR
		if suffix == "" {
			flags |= os.O_CREATE
		}
		f, err := openPrivateFile(dbPath+suffix, flags)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("forge: prepare storage file: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	svc, err := kata.New(context.Background(), kata.Config{
		DSN:     (&url.URL{Scheme: "sqlite", Path: dbPath}).String(),
		Profile: kata.EmbeddingProfileRestricted,
		Access:  hostAccessController{},
		// Forge holds exclusive local authority for the lifetime of Run. This
		// fence needs no access to Kata SQL and stops writes on cancellation.
		WorkerTransactionFence: func(ctx context.Context, _ kata.Transaction) error { return ctx.Err() },
	})
	if err != nil {
		return nil, fmt.Errorf("forge: open Kata: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, svc.Close())
		}
	}()
	result, err := svc.EnsureProject(context.Background(), kata.ProjectSpec{UID: persisted.ProjectUID, Name: persisted.ProjectName})
	if err != nil {
		return nil, fmt.Errorf("forge: ensure project: %w", err)
	}
	signer, err := loadExecutionSigner(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{service: svc, project: result.Project, config: cfg, signer: signer, lock: lock, cancel: cancel, runDone: make(chan struct{})}
	s.tools = newToolHandler(svc.Handler(), result.Project, signer)
	s.codex = newCodexRuntime(s.tools)
	workspaceRoot := cfg.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(dir, "workspaces")
	}
	s.codex.workspaces, err = newWorkspaceStore(filepath.Join(workspaceRoot, result.Project.UID), result.Project.UID)
	if err != nil {
		cancel()
		return nil, err
	}
	s.handler = s.authenticatedHandler()
	go func() {
		codexDone := make(chan struct{})
		go func() { s.codex.run(ctx); close(codexDone) }()
		s.runErr = svc.Run(ctx)
		cancel()
		<-codexDone
		close(s.runDone)
	}()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) Project() kata.Project { return s.project }

// Wait blocks until the background service exits and reports worker failures.
// Hosts should monitor this alongside their listener, not silently keep serving
// after background work has failed. Close also returns these failures.
func (s *Server) Wait() error {
	<-s.runDone
	return s.runErr
}

// Close cancels and joins workers before closing Kata and releasing the lock.
// Concurrent and repeated calls all return the same result.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		runErr := s.Wait()
		s.codex.workspaces.close()
		s.closeErr = errors.Join(runErr, s.service.Close(), unlock(s.lock))
	})
	return s.closeErr
}

func (s *Server) authenticatedHandler() http.Handler {
	want := sha256.Sum256([]byte(s.config.AdminToken))
	worker := sha256.Sum256([]byte(s.config.WorkerToken))
	kataHandler := s.service.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, "{\"status\":\"ok\"}\n")
			return
		}
		headers := r.Header.Values("Authorization")
		scheme, credential, found := strings.Cut(r.Header.Get("Authorization"), " ")
		got := sha256.Sum256([]byte(credential))
		matched := subtle.ConstantTimeCompare(got[:], want[:]) == 1
		workerMatched := subtle.ConstantTimeCompare(got[:], worker[:]) == 1 && s.config.WorkerToken != ""
		if len(headers) != 1 || !found || !strings.EqualFold(scheme, "Bearer") || (!matched && !workerMatched) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="forge"`)
			writeForgeError(w, http.StatusUnauthorized, "auth_required", "authentication required")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet && r.URL.Path == "/forge/v1/project" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": s.project.ID, "uid": s.project.UID, "name": s.project.Name}, "close_protocol": "close-v2"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/forge/v1/codex/") && s.codex != nil {
			s.codex.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/forge/v1/tools/") && s.tools != nil {
			s.tools.ServeHTTP(w, r)
			return
		}
		if !matched {
			writeForgeError(w, http.StatusForbidden, "worker_facade_required", "workers must use the typed Forge façade")
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			writeForgeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		ctx := context.WithValue(r.Context(), supervisorGrantKey{}, true)
		ctx = kata.WithPrincipal(ctx, kata.Principal{Subject: "human:supervisor", Actor: "Human"})
		kataHandler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// The grant cannot be constructed by HTTP clients or by merely supplying a
// Kata Principal. Only Forge's successful authentication middleware creates it.
type supervisorGrantKey struct{}
type hostAccessController struct{}
type supervisorLease struct{}

func (supervisorLease) Revalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if granted, _ := ctx.Value(supervisorGrantKey{}).(bool); !granted {
		return kata.ErrAccessDenied
	}
	return nil
}

func (hostAccessController) Authorize(ctx context.Context, req kata.AccessRequest) (kata.AccessDecision, error) {
	lease := supervisorLease{}
	if req.Principal.Subject != "human:supervisor" || req.Principal.Actor != "Human" {
		return authorizeTool(ctx, req)
	}
	if err := lease.Revalidate(ctx); err != nil {
		return kata.AccessDecision{}, err
	}
	// Native administration is removed by the restricted embedding profile;
	// the initial authenticated human may supervise all remaining task routes.
	return kata.AccessDecision{
		Lease:            lease,
		TransactionFence: func(ctx context.Context, _ kata.Transaction) error { return lease.Revalidate(ctx) },
	}, nil
}

// ValidateAdminToken checks format without including the secret in errors.
func ValidateAdminToken(token string) error {
	if len(token) < 32 {
		return errors.New("forge: admin token must be at least 32 characters")
	}
	for _, b := range []byte(token) {
		if b < 0x21 || b > 0x7e {
			return errors.New("forge: admin token must contain only printable ASCII without whitespace")
		}
	}
	return nil
}

// PrepareDataDir creates or tightens the host-owned directory. It rejects a
// symlink at the final component. The parent directory must be trusted.
func PrepareDataDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("forge: data directory is required")
	}
	// Lstat with a trailing slash follows symlinks on Unix; normalize first.
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("forge: create data directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("forge: data directory must be a real directory")
	}
	return os.Chmod(dir, 0700)
}

func openPrivateFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("forge: state file must be regular")
	}
	if err == nil {
		err = f.Chmod(0600)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func unlock(f *os.File) error {
	return errors.Join(unix.Flock(int(f.Fd()), unix.LOCK_UN), f.Close())
}

func loadProjectConfig(dir, name string) (persistedConfig, error) {
	path := filepath.Join(dir, "config.json")
	f, err := openPrivateFile(path, os.O_RDONLY)
	if err == nil {
		var cfg persistedConfig
		decoder := json.NewDecoder(io.LimitReader(f, 64<<10))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&cfg)
		if decodeErr == nil {
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				decodeErr = errors.New("forge: trailing project configuration data")
			}
		}
		err = errors.Join(decodeErr, f.Close())
		if err != nil {
			return persistedConfig{}, fmt.Errorf("forge: read project configuration: %w", err)
		}
		if _, err := ulid.ParseStrict(cfg.ProjectUID); err != nil {
			return persistedConfig{}, errors.New("forge: invalid persisted project UID")
		}
		if cfg.ProjectName != name {
			return persistedConfig{}, errors.New("forge: project name differs from persisted configuration")
		}
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return persistedConfig{}, err
	}
	if _, err := os.Lstat(filepath.Join(dir, "kata.db")); !errors.Is(err, os.ErrNotExist) {
		return persistedConfig{}, errors.New("forge: refusing to create a new project identity beside existing storage")
	}
	uid, err := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if err != nil {
		return persistedConfig{}, fmt.Errorf("forge: generate project identity: %w", err)
	}
	cfg := persistedConfig{ProjectUID: uid.String(), ProjectName: name}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return persistedConfig{}, err
	}
	// Persist the chosen identity before EnsureProject: a crash during Kata
	// bootstrap must retry the same ULID, never generate a conflicting one.
	if err := writeConfig(dir, append(body, '\n')); err != nil {
		return persistedConfig{}, err
	}
	return cfg, nil
}

func writeConfig(dir string, body []byte) (err error) {
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := f.Write(body)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "config.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
