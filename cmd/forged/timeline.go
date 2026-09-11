package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func runTimeline(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("forged timeline", flag.ContinueOnError)
	flags.SetOutput(out)
	base := flags.String("url", "http://127.0.0.1:7347", "loopback Forge URL")
	file := flags.String("token-file", ".forge/admin-token", "0600 supervisor or worker token file")
	after := flags.Int64("after-id", 0, "resume project event cursor")
	limit := flags.Int("limit", 100, "project events per page (1–1000)")
	maxPages := flags.Int("max-pages", 100, "maximum pages; truncation returns an error and resume cursor")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 || *after < 0 || *limit < 1 || *limit > 1000 || *maxPages < 1 {
		return errors.New("usage: forged timeline [flags] ISSUE_REF")
	}
	u, err := url.Parse(*base)
	if err != nil {
		return errors.New("invalid Forge URL")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("Forge URL must be an HTTP loopback IP literal without credentials or a path")
	}
	token, err := readTokenFile(*file)
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	nonce[6] = (nonce[6] & 15) | 64
	nonce[8] = (nonce[8] & 63) | 128
	session := fmt.Sprintf("%x-%x-%x-%x-%x", nonce[:4], nonce[4:6], nonce[6:8], nonce[8:10], nonce[10:])
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cursor := *after
	for pageIndex := 0; pageIndex < *maxPages; pageIndex++ {
		body, _ := json.Marshal(map[string]any{"ref": flags.Arg(0), "after_id": cursor, "limit": *limit})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(*base, "/")+"/forge/v1/tools/issue_timeline", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Forge-Session", session)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("timeline request: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		closeErr := response.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if len(raw) > 8<<20 {
			return errors.New("timeline response exceeds 8 MiB")
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("timeline request failed: HTTP %d", response.StatusCode)
		}
		var page struct {
			Issue  struct{ UID, Title, Status string } `json:"issue"`
			Events []struct {
				ID          int64 `json:"event_id"`
				Type, Actor string
				CreatedAt   string          `json:"created_at"`
				Payload     json.RawMessage `json:"payload"`
			} `json:"events"`
			NextAfterID  int64 `json:"next_after_id"`
			Scanned      int   `json:"scanned_event_count"`
			Reset        bool  `json:"reset_required"`
			ResetAfterID int64 `json:"reset_after_id"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("invalid timeline: %w", err)
		}
		if pageIndex == 0 {
			fmt.Fprintf(out, "Issue %s — %q [%s]\n", page.Issue.UID, page.Issue.Title, page.Issue.Status)
		}
		if page.Reset {
			if page.ResetAfterID <= cursor {
				return errors.New("invalid timeline reset cursor")
			}
			fmt.Fprintf(out, "History was purged; resuming retained events after cursor %d.\n", page.ResetAfterID)
			cursor = page.ResetAfterID
			continue
		}
		for _, event := range page.Events {
			fmt.Fprintf(out, "\n%d %q %s — %q\n", event.ID, event.CreatedAt, event.Type, event.Actor)
			var payload bytes.Buffer
			if err := json.Indent(&payload, event.Payload, "  ", "  "); err != nil {
				return fmt.Errorf("invalid event payload: %w", err)
			}
			fmt.Fprintf(out, "  %s\n", payload.String())
		}
		if page.Scanned == 0 {
			fmt.Fprintf(out, "\nEnd of retained timeline (cursor %d).\n", cursor)
			return nil
		}
		if page.NextAfterID <= cursor {
			return errors.New("timeline cursor did not advance")
		}
		cursor = page.NextAfterID
	}
	return fmt.Errorf("timeline truncated at page limit; resume with --after-id %d", cursor)
}
