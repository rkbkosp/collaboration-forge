package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"go.kenn.io/kata"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T, dir string) *Server {
	t.Helper()
	s, err := New(Config{DataDir: dir, ProjectName: "forge-test", AdminToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func request(t *testing.T, s *Server, method, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

type issueResponse struct {
	Issue struct {
		UID     string `json:"uid"`
		ShortID string `json:"short_id"`
		Title   string `json:"title"`
		Author  string `json:"author"`
	} `json:"issue"`
}

func createIssue(t *testing.T, s *Server) (string, issueResponse) {
	t.Helper()
	base := fmt.Sprintf("/api/v1/projects/%d/issues", s.Project().ID)
	w := request(t, s, "POST", base, "Bearer "+testToken, `{"title":"Bootstrap issue","actor":"forged-client-actor"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var issue issueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Issue.UID == "" || issue.Issue.ShortID == "" {
		t.Fatalf("missing issue identity: %s", w.Body.String())
	}
	return base + "/" + issue.Issue.ShortID, issue
}

func TestHealthEnsureProjectCreateShowAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, dir)
	p := s.Project()
	if _, err := ulid.ParseStrict(p.UID); err != nil || p.ID <= 0 || p.Name != "forge-test" || p.State != kata.ProjectActive {
		t.Fatalf("project = %+v, UID error = %v", p, err)
	}
	health := request(t, s, "GET", "/health", "", "")
	if health.Code != http.StatusOK || strings.TrimSpace(health.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("health: %d %s", health.Code, health.Body.String())
	}
	if strings.Contains(health.Body.String(), dir) {
		t.Fatal("health leaks DataDir")
	}
	path, created := createIssue(t, s)
	assertShow := func(server *Server) {
		t.Helper()
		w := request(t, server, "GET", path, "Bearer "+testToken, "")
		var shown issueResponse
		if err := json.Unmarshal(w.Body.Bytes(), &shown); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || shown.Issue.UID != created.Issue.UID || shown.Issue.Title != "Bootstrap issue" {
			t.Fatalf("show: %d %s", w.Code, w.Body.String())
		}
		if shown.Issue.Author != "Human" {
			t.Fatalf("client actor must be replaced: %s", w.Body.String())
		}
	}
	assertShow(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = newTestServer(t, dir)
	if s.Project().UID != p.UID || s.Project().ID != p.ID {
		t.Fatalf("project changed across restart: %+v -> %+v", p, s.Project())
	}
	assertShow(s)
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if entry.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			return fmt.Errorf("%s: mode %o, want %o", path, info.Mode().Perm(), want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), p.UID) || !strings.Contains(string(persisted), p.Name) || strings.Contains(string(persisted), testToken) {
		t.Fatalf("config must persist only stable project identity: %s", persisted)
	}
}

func TestAuthenticationFailsClosed(t *testing.T) {
	s := newTestServer(t, t.TempDir())
	for _, path := range []string{"/api/v1/projects", "/api/v1/health", "/kata", "/unknown", "/health/"} {
		for _, auth := range []string{"", "Bearer wrong", "Bearer " + strings.Repeat("x", 32), "Basic " + testToken, "Bearer " + testToken + " extra"} {
			w := request(t, s, "GET", path, auth, "")
			if w.Code != http.StatusUnauthorized {
				t.Errorf("path %s: wrong auth got %d", path, w.Code)
			}
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/projects", nil)
	r.Header.Set("X-Actor", "Human")
	r = r.WithContext(kata.WithPrincipal(r.Context(), kata.Principal{Subject: "human:supervisor", Actor: "Human"}))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("forged principal bypassed token: %d", w.Code)
	}
	r.Header.Add("Authorization", "Bearer "+testToken)
	r.Header.Add("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate authorization accepted: %d", w.Code)
	}
	w = request(t, s, "GET", "/api/v1/projects", "Bearer "+testToken, "")
	if w.Code != http.StatusOK {
		t.Fatalf("authorized list: %d %s", w.Code, w.Body.String())
	}
	unauthorized := request(t, s, "GET", "/api/v1/projects", "", "")
	if unauthorized.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unauthorized content type: %s", unauthorized.Header().Get("Content-Type"))
	}
	var envelope forgeErrorEnvelope
	if err := json.Unmarshal(unauthorized.Body.Bytes(), &envelope); err != nil || envelope.Status != http.StatusUnauthorized || envelope.Error.Code != "auth_required" {
		t.Fatalf("unauthorized envelope: %d %s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestHostAccessRequiresPrivateGrant(t *testing.T) {
	_, err := (hostAccessController{}).Authorize(context.Background(), kata.AccessRequest{Principal: kata.Principal{Subject: "human:supervisor", Actor: "Human"}})
	if !errors.Is(err, kata.ErrAccessDenied) {
		t.Fatalf("without private middleware grant: %v", err)
	}
}

func TestRestrictedAdministration(t *testing.T) {
	s := newTestServer(t, t.TempDir())
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/projects", `{"name":"other","actor":"Human"}`},
		{"POST", fmt.Sprintf("/api/v1/projects/%d/federation/enable", s.Project().ID), `{"actor":"Human"}`},
		{"POST", "/api/v1/federation/enrollments", `{"actor":"Human"}`},
		{"GET", "/api/v1/tokens", ""},
	} {
		w := request(t, s, tc.method, tc.path, "Bearer "+testToken, tc.body)
		if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("restricted %s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestConfigValidation(t *testing.T) {
	for _, token := range []string{"", "short", strings.Repeat("x", 31), strings.Repeat(" ", 32), strings.Repeat("x", 32) + "\n"} {
		if s, err := New(Config{DataDir: t.TempDir(), ProjectName: "forge-test", AdminToken: token}); err == nil {
			_ = s.Close()
			t.Error("accepted invalid token")
		}
	}
	for _, cfg := range []Config{
		{ProjectName: "forge-test", AdminToken: testToken},
		{DataDir: t.TempDir(), AdminToken: testToken},
		{DataDir: t.TempDir(), ProjectName: "bad#name", AdminToken: testToken},
	} {
		if s, err := New(cfg); err == nil {
			_ = s.Close()
			t.Error("accepted invalid config")
		}
	}
}

func TestProjectNameConflictDoesNotReplaceIdentity(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, dir)
	p := s.Project()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := New(Config{DataDir: dir, ProjectName: "renamed", AdminToken: testToken}); err == nil {
		_ = other.Close()
		t.Fatal("changing project name must require explicit future administration")
	}
	if reopened := newTestServer(t, dir); reopened.Project().UID != p.UID {
		t.Fatal("identity replaced")
	}
}

func TestDataDirLock(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, dir)
	if second, err := New(Config{DataDir: dir, ProjectName: "forge-test", AdminToken: testToken}); !errors.Is(err, ErrDataDirLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second Store must be refused before SQLite open: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), "FORGE_LOCK_TEST_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second process: %v\n%s", err, out)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = newTestServer(t, dir)
}

func TestLockHelperProcess(t *testing.T) {
	dir := os.Getenv("FORGE_LOCK_TEST_DIR")
	if dir == "" {
		t.Skip("subprocess only")
	}
	s, err := New(Config{DataDir: dir, ProjectName: "forge-test", AdminToken: testToken})
	if s != nil {
		_ = s.Close()
	}
	if !errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("expected process lock refusal: %v", err)
	}
}

func TestDataDirSymlinkIsRejectedEvenWithTrailingSeparator(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked-data")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{link, link + string(os.PathSeparator)} {
		if err := PrepareDataDir(dir); err == nil {
			t.Errorf("accepted symlink directory %s", dir)
		}
	}
}

func TestMissingOrCorruptConfigCannotReplaceExistingIdentity(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			dir := t.TempDir()
			s := newTestServer(t, dir)
			path, created := createIssue(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "config.json")
			original, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				err = os.WriteFile(configPath, []byte(`{"project_uid":"invalid"}`), 0600)
			} else {
				err = os.Remove(configPath)
			}
			if err != nil {
				t.Fatal(err)
			}
			if other, err := New(Config{DataDir: dir, ProjectName: "forge-test", AdminToken: testToken}); err == nil {
				_ = other.Close()
				t.Fatal("replaced missing/corrupt identity")
			}
			if err := os.WriteFile(configPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			// Failed construction released the process lock; restoring the
			// original configuration can still reach the original Kata issue.
			reopened := newTestServer(t, dir)
			w := request(t, reopened, "GET", path, "Bearer "+testToken, "")
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), created.Issue.UID) {
				t.Fatalf("restored issue: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCloseIsConcurrentAndRepeatable(t *testing.T) {
	s := newTestServer(t, t.TempDir())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "GET", "/api/v1/projects", "Bearer "+testToken, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed service: %d", w.Code)
	}
}
