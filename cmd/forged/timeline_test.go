package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkbkosp/collaboration-forge/internal/forge"
)

func TestTimelineCommandUsesRealForgeEventsAndPaginates(t *testing.T) {
	token := strings.Repeat("t", 32)
	s, err := forge.New(forge.Config{DataDir: t.TempDir(), ProjectName: "timeline", AdminToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	native := func(path, body string) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("create: %d %s", resp.StatusCode, result)
		}
		return result
	}
	base := fmt.Sprintf("/api/v1/projects/%d/issues", s.Project().ID)
	raw := native(base, `{"title":"Timeline acceptance","body":"Human plan"}`)
	var created struct{ Issue struct{ UID string } }
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	native(base+"/"+created.Issue.UID+"/comments", `{"body":"Independent knowledge contribution"}`)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"timeline", "--url", server.URL, "--token-file", path, "--limit", "1", created.Issue.UID}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Execution authority: Unclaimed", "Last confirmed:", "History", "Timeline acceptance", "Human", "issue.created", "issue.commented", "Independent knowledge contribution", "End of retained timeline"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in timeline: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("credential leaked")
	}
	out.Reset()
	args = []string{"timeline", "--url", server.URL, "--token-file", path, "--limit", "1", "--max-pages", "1", created.Issue.UID}
	if err := run(context.Background(), args, &out); err == nil || !strings.Contains(err.Error(), "--after-id") {
		t.Fatalf("silent timeline truncation: %v", err)
	}
}

func TestTimelineCurrentAuthorityStates(t *testing.T) {
	for _, tc := range []struct{ name, status, fields, want string }{
		{"available", "open", `,"lease":null,"observed_at":"2026-09-11T06:23:10Z"`, "Unclaimed; acquisition is arbitrated by the server"},
		{"closed", "closed", `,"lease":null,"observed_at":"2026-09-11T06:23:10Z"`, "Closed"},
		{"held", "open", `,"lease":{"holder":"opaque-holder","purpose":"Agent/alice [Pi session abc]: work","claim_uid":"claim-1","claim_kind":"timed","acquired_at":"2026-09-11T06:20:00Z","expires_at":"2026-09-11T06:25:00Z"},"lease_hub_now":"2026-09-11T06:23:10Z","observed_at":"2026-09-11T06:23:11Z"`, `Execution authority: Held by "Agent/alice"`},
		{"legacy unknown", "open", "", "Execution authority: Unknown"},
		{"expired requires refresh", "open", `,"lease":{"holder":"old","claim_kind":"timed","expires_at":"2026-09-11T06:20:00Z"},"lease_hub_now":"2026-09-11T06:23:10Z","observed_at":"2026-09-11T06:23:11Z"`, "Execution authority: Unknown"},
		{"missing authority clock", "open", `,"lease":{"holder":"old","claim_kind":"timed","expires_at":"2026-09-11T06:25:00Z"},"observed_at":"2026-09-11T06:23:11Z"`, "Execution authority: Unknown"},
		{"pending", "open", `,"lease":null,"pending_leases":[{}],"observed_at":"2026-09-11T06:23:10Z"`, "Execution authority: Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"issue":{"uid":"issue","title":"Work","status":%q},"events":[],"scanned_event_count":0%s}`, tc.status, tc.fields)
			}))
			defer server.Close()
			tokenFile := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runTimeline(context.Background(), []string{"--url", server.URL, "--token-file", tokenFile, "--details", "abcd"}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q: %s", tc.want, out.String())
			}
			if tc.name == "held" {
				for _, want := range []string{"claim-1", "opaque-holder", "2026-09-11T06:25:00Z", "2026-09-11T06:23:10Z", "History"} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("missing %q: %s", want, out.String())
					}
				}
			}
		})
	}
}

func TestTimelineReadFailureReportsUnknownAndLastConfirmation(t *testing.T) {
	for _, failFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(failFirst), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if failFirst || calls > 1 {
					w.WriteHeader(503)
					return
				}
				fmt.Fprint(w, `{"issue":{"uid":"issue","title":"Work","status":"open"},"lease":null,"observed_at":"2026-09-11T06:23:10Z","events":[],"scanned_event_count":1,"next_after_id":1}`)
			}))
			defer server.Close()
			tokenFile := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenFile, []byte(strings.Repeat("t", 32)), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := runTimeline(context.Background(), []string{"--url", server.URL, "--token-file", tokenFile, "abcd"}, &out)
			if err == nil || !strings.Contains(out.String(), "Execution authority: Unknown") {
				t.Fatalf("missing failed refresh: %v %s", err, out.String())
			}
			if !failFirst && !strings.Contains(out.String(), "Last confirmed: 2026-09-11T06:23:10Z") {
				t.Fatalf("lost confirmation: %s", out.String())
			}
		})
	}
}
