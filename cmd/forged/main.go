// forged is the loopback-only host for Forge's embedded Kata service.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.local/collab-forge/internal/forge"
	"golang.org/x/sys/unix"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "forged:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: forged serve [--data-dir DIR] [--project NAME] [--listen IP:PORT] [--admin-token-file FILE]")
	}
	flags := flag.NewFlagSet("forged serve", flag.ContinueOnError)
	flags.SetOutput(output)
	dir := flags.String("data-dir", ".forge", "private data directory")
	project := flags.String("project", "forge", "stable project name (must match on restart)")
	listen := flags.String("listen", "127.0.0.1:7347", "loopback IP literal and port")
	tokenFile := flags.String("admin-token-file", "", "0600 token file (default: DATA_DIR/admin-token, generated if absent)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("forged serve: unexpected positional arguments")
	}
	if err := validateListen(*listen); err != nil {
		return err
	}
	token, err := loadAdminToken(*dir, *tokenFile)
	if err != nil {
		return err
	}
	svc, err := forge.New(forge.Config{DataDir: *dir, ProjectName: *project, AdminToken: token})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.Join(err, svc.Close())
	}
	host := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	httpDone := make(chan error, 1)
	go func() { httpDone <- host.Serve(listener) }()
	kataDone := make(chan error, 1)
	go func() { kataDone <- svc.Wait() }()
	fmt.Fprintf(output, "forged listening on %s (project %s, UID %s)\n", listener.Addr(), svc.Project().Name, svc.Project().UID)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-httpDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	case workerErr := <-kataDone:
		serveErr = errors.Join(errors.New("Kata background service exited unexpectedly"), workerErr)
	}
	return errors.Join(serveErr, shutdown(host, svc))
}

func shutdown(host *http.Server, svc *forge.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Bound graceful draining separately so cancellation/Close has time left.
	drainCtx, stopDrain := context.WithTimeout(ctx, 5*time.Second)
	drainErr := host.Shutdown(drainCtx)
	stopDrain()
	if drainErr != nil {
		drainErr = errors.Join(drainErr, host.Close())
	}
	closed := make(chan error, 1)
	go func() { closed <- svc.Close() }()
	select {
	case err := <-closed:
		return errors.Join(drainErr, err)
	case <-ctx.Done():
		return errors.Join(drainErr, errors.New("forged: shutdown deadline exceeded"))
	}
}

func validateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("forged: --listen must be a loopback IP literal and numeric port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return errors.New("forged: --listen must use a loopback IP literal")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return errors.New("forged: --listen port must be between 0 and 65535")
	}
	return nil
}

func loadAdminToken(dir, explicitPath string) (string, error) {
	if err := forge.PrepareDataDir(dir); err != nil {
		return "", err
	}
	path := explicitPath
	if path == "" {
		path = filepath.Join(dir, "admin-token")
	}
	token, err := readTokenFile(path)
	if err == nil {
		return token, nil
	}
	if explicitPath != "" || !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("forged: read admin token file: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("forged: generate admin token: %w", err)
	}
	// Publish a fully written secret atomically without replacing a concurrent
	// starter's token. Never expose or log the generated credential.
	f, err := os.CreateTemp(dir, ".admin-token-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := f.WriteString(hex.EncodeToString(secret) + "\n")
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return "", err
	}
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	d, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	if err := errors.Join(d.Sync(), d.Close()); err != nil {
		return "", err
	}
	return readTokenFile(path)
}

func readTokenFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("admin token file must be a regular file with mode 0600")
	}
	if info.Size() > 4096 {
		return "", errors.New("admin token file is too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", errors.New("admin token file is too large")
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if err := forge.ValidateAdminToken(token); err != nil {
		return "", err
	}
	return token, nil
}
