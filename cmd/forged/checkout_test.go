package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rkbkosp/collaboration-forge/internal/forge"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodeCheckoutAgentProcess(t *testing.T) {
	if os.Getenv("CHECKOUT_TEST_AGENT") != "1" {
		return
	}
	if _, e := os.Stat("code.txt"); e != nil {
		os.Exit(71)
	}
	if os.Getenv("CHECKOUT_TEST_AGENT_SLEEP") == "1" {
		os.WriteFile("agent-started", []byte("yes"), 0600)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	var output bytes.Buffer
	if e := runExecution(context.Background(), []string{"guard"}, &output); e != nil {
		os.Exit(72)
	}
	os.WriteFile("code.txt", []byte("agent edit\n"), 0644)
	body := `{"reason":"done","message":"Executed checkout integration and verified source isolation and recorded evidence.","evidence":[{"type":"test","command":"Go checkout integration test"}]}`
	if e := runExecution(context.Background(), []string{"issue_close", body}, &output); e != nil {
		os.Exit(73)
	}
	os.Exit(0)
}
func TestCheckoutRunsAgentInRestoredDirtyWorktreeAndCloses(t *testing.T) {
	if os.Getenv("CHECKOUT_TEST_AGENT") == "1" {
		return
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	g := func(a ...string) {
		c := exec.Command("git", a...)
		c.Dir = source
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v %s", e, b)
		}
	}
	g("init", "-q")
	g("config", "user.name", "Test")
	g("config", "user.email", "test@example.invalid")
	os.WriteFile(filepath.Join(source, "code.txt"), []byte("base\n"), 0644)
	g("add", ".")
	g("commit", "-qm", "base")
	os.WriteFile(filepath.Join(source, "code.txt"), []byte("user dirty\n"), 0644)
	admin := strings.Repeat("a", 32)
	worker := strings.Repeat("w", 32)
	svc, e := forge.New(forge.Config{DataDir: filepath.Join(root, "data"), ProjectName: "code", AdminToken: admin, WorkerToken: worker})
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues", server.URL, svc.Project().ID)
	request, _ := http.NewRequest("POST", url, strings.NewReader(`{"title":"Checkout code","body":"Test dirty source checkout"}`))
	request.Header.Set("Authorization", "Bearer "+admin)
	request.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Issue.UID == "" {
		t.Fatal("issue not created")
	}
	token := filepath.Join(root, "worker-token")
	os.WriteFile(token, []byte(worker), 0600)
	t.Setenv("CHECKOUT_TEST_AGENT", "1")
	var out bytes.Buffer
	args := []string{"--url", server.URL, "--token-file", token, "--source", source, "--dirty", "--store", filepath.Join(root, "snapshots"), "--dest", filepath.Join(root, "execution"), created.Issue.UID, "--", os.Args[0], "-test.run=^TestCodeCheckoutAgentProcess$"}
	if e = runCheckout(context.Background(), args, &out); e != nil {
		t.Fatalf("checkout: %v %s", e, out.String())
	}
	b, _ := os.ReadFile(filepath.Join(source, "code.txt"))
	if string(b) != "user dirty\n" {
		t.Fatal("source overwritten")
	}
	b, _ = os.ReadFile(filepath.Join(root, "execution", "worktree", "code.txt"))
	if string(b) != "agent edit\n" {
		t.Fatal("agent did not edit checkout")
	}
	request, _ = http.NewRequest("GET", url+"/"+created.Issue.UID, nil)
	request.Header.Set("Authorization", "Bearer "+admin)
	resp, e = http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var shown struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
		Lease any `json:"lease"`
	}
	json.NewDecoder(resp.Body).Decode(&shown)
	if shown.Issue.Status != "closed" || shown.Lease != nil {
		t.Fatal("not closed/released")
	}
	if strings.Contains(out.String(), worker) {
		t.Fatal("credential leak")
	}
}

func TestCheckoutCancellationRetainsSnapshotAndNewRuntimeRestores(t *testing.T) {
	if os.Getenv("CHECKOUT_TEST_AGENT") == "1" {
		return
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	g := func(a ...string) {
		c := exec.Command("git", a...)
		c.Dir = source
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v %s", e, b)
		}
	}
	g("init", "-q")
	g("config", "user.name", "Test")
	g("config", "user.email", "test@example.invalid")
	os.WriteFile(filepath.Join(source, "code.txt"), []byte("base\n"), 0644)
	g("add", ".")
	g("commit", "-qm", "base")
	os.WriteFile(filepath.Join(source, "code.txt"), []byte("user dirty\n"), 0644)
	admin := strings.Repeat("a", 32)
	worker := strings.Repeat("w", 32)
	svc, e := forge.New(forge.Config{DataDir: filepath.Join(root, "data"), ProjectName: "code", AdminToken: admin, WorkerToken: worker})
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	url := fmt.Sprintf("%s/api/v1/projects/%d/issues", server.URL, svc.Project().ID)
	request, _ := http.NewRequest("POST", url, strings.NewReader(`{"title":"Checkout code","body":"Test dirty source checkout"}`))
	request.Header.Set("Authorization", "Bearer "+admin)
	request.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Issue.UID == "" {
		t.Fatal("issue not created")
	}
	token := filepath.Join(root, "worker-token")
	os.WriteFile(token, []byte(worker), 0600)
	t.Setenv("CHECKOUT_TEST_AGENT", "1")
	var out bytes.Buffer
	args := []string{"--url", server.URL, "--token-file", token, "--source", source, "--dirty", "--store", filepath.Join(root, "snapshots"), "--dest", filepath.Join(root, "execution"), created.Issue.UID, "--", os.Args[0], "-test.run=^TestCodeCheckoutAgentProcess$"}

	t.Setenv("CHECKOUT_TEST_AGENT_SLEEP", "1")
	cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(root, "execution", "worktree", "agent-started")); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	if e = runCheckout(cancelCtx, args, &out); e == nil {
		t.Fatal("cancelled checkout succeeded")
	}
	if _, e = os.Stat(filepath.Join(root, "execution", "worktree", "agent-started")); e != nil {
		t.Fatal("cancelled worktree not retained")
	}
	entries, e := os.ReadDir(filepath.Join(root, "snapshots"))
	if e != nil || len(entries) != 1 {
		t.Fatal("snapshot missing")
	}
	t.Setenv("CHECKOUT_TEST_AGENT_SLEEP", "")
	args = []string{"--url", server.URL, "--token-file", token, "--snapshot", filepath.Join(root, "snapshots", entries[0].Name()), "--dest", filepath.Join(root, "recovery"), created.Issue.UID, "--", os.Args[0], "-test.run=^TestCodeCheckoutAgentProcess$"}
	if e = runCheckout(context.Background(), args, &out); e != nil {
		t.Fatalf("checkout: %v %s", e, out.String())
	}
	b, _ := os.ReadFile(filepath.Join(source, "code.txt"))
	if string(b) != "user dirty\n" {
		t.Fatal("source overwritten")
	}
	b, _ = os.ReadFile(filepath.Join(root, "recovery", "worktree", "code.txt"))
	if string(b) != "agent edit\n" {
		t.Fatal("agent did not edit checkout")
	}
	request, _ = http.NewRequest("GET", url+"/"+created.Issue.UID, nil)
	request.Header.Set("Authorization", "Bearer "+admin)
	resp, e = http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var shown struct {
		Issue struct {
			Status string `json:"status"`
		} `json:"issue"`
		Lease any `json:"lease"`
	}
	json.NewDecoder(resp.Body).Decode(&shown)
	if shown.Issue.Status != "closed" || shown.Lease != nil {
		t.Fatal("not closed/released")
	}
	if strings.Contains(out.String(), worker) {
		t.Fatal("credential leak")
	}
}
