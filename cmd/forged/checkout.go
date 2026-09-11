package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go.local/collab-forge/internal/checkout"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func runCheckout(ctx context.Context, args []string, out io.Writer) (resultErr error) {
	f := flag.NewFlagSet("forged checkout", flag.ContinueOnError)
	f.SetOutput(out)
	origin := f.String("url", "http://127.0.0.1:7347", "loopback Forge service")
	tokenFile := f.String("token-file", ".forge/worker-token", "owned 0600 worker token")
	source := f.String("source", ".", "source Git worktree (whole repository)")
	store := f.String("store", "", "private snapshot store outside source (required for capture)")
	ref := f.String("ref", "", "fixed Git commit/ref input")
	dirty := f.Bool("dirty", false, "snapshot quiescent HEAD, index and working files")
	snapshot := f.String("snapshot", "", "restore existing immutable snapshot instead of capture")
	dest := f.String("dest", "", "new execution directory (required)")
	ttl := f.Int("ttl", 300, "lease TTL seconds (60..3600)")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	rest := f.Args()
	modes := 0
	if *dirty {
		modes++
	}
	if *ref != "" {
		modes++
	}
	if *snapshot != "" {
		modes++
	}
	if modes != 1 || *dest == "" || (*snapshot == "" && *store == "") || len(rest) < 3 || rest[1] != "--" {
		return errors.New("usage: forged checkout --dest DIR (--dirty --store DIR | --ref COMMIT --store DIR | --snapshot DIR) [flags] ISSUE -- AGENT [args...]")
	}
	absolute, e := filepath.Abs(*dest)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(absolute); !os.IsNotExist(e) {
		return errors.New("execution destination must not exist")
	}
	token, e := readTokenFile(*tokenFile)
	if e != nil {
		return e
	}
	rt, e := checkout.NewRuntime(*origin, token, *ttl)
	if e != nil {
		return e
	}
	defer rt.Shutdown()
	if e = rt.Claim(ctx, rest[0]); e != nil {
		return e
	}
	// Failure observations are additive history. No error content or credentials
	// are echoed into the Issue; an unconfirmed lease is left to expire by TTL.
	var s checkout.Snapshot
	workspaceID := checkout.UUID()
	stage := "snapshot"
	defer func() {
		if resultErr != nil {
			failure, _ := json.Marshal(map[string]any{"kind": "forge.checkout.failed.v1", "stage": stage, "workspace_id": workspaceID, "snapshot_id": s.ID, "snapshot_location": s.Path, "execution_directory": absolute})
			record, _ := json.Marshal(map[string]any{"ref": rt.Issue(), "body": string(failure)})
			logCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = rt.Tool(logCtx, "issue_comment", record)
		}
	}()
	if *snapshot != "" {
		s, e = checkout.Load(*snapshot)
	} else {
		s, e = checkout.Capture(ctx, checkout.Options{Source: *source, Store: *store, Ref: *ref, Dirty: *dirty})
	}
	if e != nil {
		return e
	}
	stage = "registration"
	if e = rt.Register(ctx, s, workspaceID, "preparing", filepath.Join(absolute, "worktree")); e != nil {
		return e
	}
	stage = "restore"
	work, e := checkout.Restore(ctx, s.Path, absolute, "forge/"+workspaceID)
	if e != nil {
		return e
	}
	stage = "registration"
	if e = rt.Register(ctx, s, workspaceID, "ready", work); e != nil {
		return e
	}
	stage = "launch"
	if e = rt.Guard(ctx); e != nil {
		return e
	}
	// Short Unix paths are required on macOS. Only this same-user runtime directory
	// exposes the limited tool bridge; no execution secret is placed on disk.
	socketDir, e := os.MkdirTemp("/tmp", "forge-run-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(socketDir)
	socket := filepath.Join(socketDir, "execution.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	if e = os.Chmod(socket, 0600); e != nil {
		listener.Close()
		return e
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/") || strings.Contains(strings.TrimPrefix(r.URL.Path, "/"), "/") {
			http.Error(w, "invalid tool request", 400)
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if e != nil {
			http.Error(w, "request exceeds limit", 400)
			return
		}
		reply, e := rt.Tool(r.Context(), strings.TrimPrefix(r.URL.Path, "/"), body)
		w.Header().Set("Content-Type", "application/json")
		if e != nil {
			w.WriteHeader(409)
			json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
			return
		}
		w.Write(reply)
	})}
	go server.Serve(listener)
	defer server.Close()
	executable, _ := os.Executable()
	command := exec.Command(rest[2], rest[3:]...)
	command.Dir = work
	command.Stdin = os.Stdin
	command.Stdout = out
	command.Stderr = out
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "FORGE_") && !strings.HasPrefix(v, "GIT_") {
			command.Env = append(command.Env, v)
		}
	}
	command.Env = append(command.Env, "FORGE_EXECUTION_SOCKET="+socket, "FORGE_ISSUE="+rt.Issue(), "FORGE_CLI="+executable)
	json.NewEncoder(out).Encode(map[string]any{"state": "ready", "issue": rt.Issue(), "snapshot": s.ID, "snapshot_path": s.Path, "worktree": work, "workspace_id": workspaceID})
	if e = ctx.Err(); e != nil {
		return e
	}
	select {
	case <-rt.Lost():
		return errors.New("execution lost before launch")
	default:
	}
	if e = command.Start(); e != nil {
		return errors.New("Agent failed to start; checkout retained")
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	stage = "agent"
	select {
	case e = <-wait:
		if e != nil {
			return errors.New("Agent exited unsuccessfully; checkout retained")
		}
		return nil
	case <-ctx.Done():
		e = ctx.Err()
	case <-rt.Lost():
		e = errors.New("execution lease could not be confirmed; Agent stopped, checkout retained")
	}
	// Best effort process-group cancellation; this is not OS sandboxing and cannot
	// undo side effects or stop a descendant which deliberately detached itself.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	select {
	case <-wait:
	case <-time.After(time.Second):
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-wait
	}
	return e
}
func runExecution(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("forged execution", flag.ContinueOnError)
	f.SetOutput(out)
	socket := f.String("socket", os.Getenv("FORGE_EXECUTION_SOCKET"), "current runtime Unix socket")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	rest := f.Args()
	if *socket == "" || len(rest) < 1 || len(rest) > 2 || strings.ContainsAny(rest[0], "/?#") {
		return errors.New("usage: forged execution [--socket PATH] TOOL [JSON|-]")
	}
	body := []byte(`{}`)
	if len(rest) == 2 {
		if rest[1] == "-" {
			var e error
			body, e = io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
			if e != nil {
				return e
			}
		} else {
			body = []byte(rest[1])
		}
	}
	if len(body) > 1<<20 || !json.Valid(body) {
		return errors.New("expected JSON object at most 1 MiB")
	}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", *socket)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	r, e := http.NewRequestWithContext(ctx, "POST", "http://runtime/"+rest[0], strings.NewReader(string(body)))
	if e != nil {
		return e
	}
	resp, e := client.Do(r)
	if e != nil {
		return errors.New("execution runtime unavailable; never restore old execution credentials")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if e != nil || len(b) > 8<<20 {
		return errors.New("invalid runtime response")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("execution tool rejected: %s", strings.TrimSpace(string(b)))
	}
	_, e = fmt.Fprintln(out, string(b))
	return e
}
