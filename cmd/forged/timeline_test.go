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

	"go.local/collab-forge/internal/forge"
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
	for _, want := range []string{"Timeline acceptance", "Human", "issue.created", "issue.commented", "Independent knowledge contribution", "End of retained timeline"} {
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
