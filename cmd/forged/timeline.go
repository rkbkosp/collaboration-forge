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
	details := flags.Bool("details", false, "show exact lease holder and ClaimUID")
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
	lastConfirmed := "none"
	readFailure := func(err error) error {
		fmt.Fprintf(out, "\nExecution authority: Unknown (refresh failed). Last confirmed: %s\n", lastConfirmed)
		return err
	}
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
			return readFailure(fmt.Errorf("timeline request: %w", err))
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		closeErr := response.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return readFailure(err)
		}
		if len(raw) > 8<<20 {
			return readFailure(errors.New("timeline response exceeds 8 MiB"))
		}
		if response.StatusCode != http.StatusOK {
			return readFailure(fmt.Errorf("timeline request failed: HTTP %d", response.StatusCode))
		}
		var page struct {
			Lease         json.RawMessage                     `json:"lease"`
			LeaseHubNow   time.Time                           `json:"lease_hub_now"`
			ObservedAt    time.Time                           `json:"observed_at"`
			PendingLeases []json.RawMessage                   `json:"pending_leases"`
			Issue         struct{ UID, Title, Status string } `json:"issue"`
			Events        []struct {
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
			return readFailure(fmt.Errorf("invalid timeline: %w", err))
		}
		if pageIndex == 0 {
			fmt.Fprintf(out, "Issue %s — %q [%s]\n", page.Issue.UID, page.Issue.Title, page.Issue.Status)
			lastConfirmed = printTimelineAuthority(out, page.Issue.Status, page.Lease, page.LeaseHubNow, page.ObservedAt, len(page.PendingLeases) > 0, *details)
			fmt.Fprintln(out, "\nHistory (read separately; may include changes after the status observation):")
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

// The heading is a timestamped observation, never an execution preflight.
// A local clock or an old acquired event cannot establish current authority.
func printTimelineAuthority(out io.Writer, status string, raw json.RawMessage, hubNow, observed time.Time, pending, details bool) string {
	unknown := func() string {
		fmt.Fprintln(out, "Execution authority: Unknown (refresh required)")
		if !observed.IsZero() {
			fmt.Fprintf(out, "Read completed: %s (server time)\n", observed.Format(time.RFC3339Nano))
		}
		return "none"
	}
	if observed.IsZero() || len(raw) == 0 || pending {
		return unknown()
	}
	var lease *struct {
		Holder     string     `json:"holder"`
		Purpose    string     `json:"purpose"`
		ClaimUID   string     `json:"claim_uid"`
		ClaimKind  string     `json:"claim_kind"`
		AcquiredAt time.Time  `json:"acquired_at"`
		ExpiresAt  *time.Time `json:"expires_at"`
		ReleasedAt *time.Time `json:"released_at"`
	}
	if json.Unmarshal(raw, &lease) != nil {
		return unknown()
	}
	confirmed := observed
	if lease == nil {
		switch status {
		case "open":
			fmt.Fprintln(out, "Execution authority: Unclaimed; acquisition is arbitrated by the server")
		case "closed":
			fmt.Fprintln(out, "Execution authority: Closed")
		default:
			return unknown()
		}
	} else {
		if status != "open" || hubNow.IsZero() || lease.Holder == "" || lease.ClaimUID == "" || lease.ReleasedAt != nil {
			return unknown()
		}
		if lease.ClaimKind == "timed" {
			if lease.ExpiresAt == nil || !lease.ExpiresAt.After(hubNow) {
				return unknown()
			}
		} else if lease.ClaimKind != "hard" {
			return unknown()
		}
		confirmed = hubNow
		label := lease.Holder
		// Forge prefixes purpose with the session display actor at acquire time.
		// It is a label only; exact holder/ClaimUID remain the authority identities.
		if actor, _, ok := strings.Cut(lease.Purpose, " [Pi session "); ok && strings.HasPrefix(actor, "Agent/") {
			label = actor
		}
		fmt.Fprintf(out, "Execution authority: Held by %q (authorization, not proof of activity)\n", label)
		fmt.Fprintf(out, "Acquired: %s\n", lease.AcquiredAt.Format(time.RFC3339Nano))
		if lease.ExpiresAt != nil {
			fmt.Fprintf(out, "Lease expires: %s\n", lease.ExpiresAt.Format(time.RFC3339Nano))
		} else {
			fmt.Fprintln(out, "Lease: hard (no timed expiry)")
		}
		if details {
			fmt.Fprintf(out, "Holder: %q\nClaimUID: %q\n", lease.Holder, lease.ClaimUID)
		}
	}
	stamp := confirmed.Format(time.RFC3339Nano)
	fmt.Fprintf(out, "Last confirmed: %s (server time; rerun to refresh)\n", stamp)
	return stamp
}
