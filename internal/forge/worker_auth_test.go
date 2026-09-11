package forge

import (
	"fmt"
	"net/http"
	"testing"
)

const testWorkerToken = "worker-0123456789abcdef0123456789abcdef"

func TestWorkerCannotBypassFacadeWithNativeRoutes(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), ProjectName: "workers", AdminToken: testToken, WorkerToken: testWorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path, issue := createIssue(t, s)
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", path, `{"title":"overwrite"}`},
		{"PATCH", path + "/metadata", `{"patch":{"priority":1}}`},
		{"POST", path + "/actions/claim", `{"actor":"Human"}`},
		{"POST", path + "/actions/reopen", `{}`},
		{"POST", path + "/actions/close", `{"reason":"wontfix"}`},
		{"POST", path + "/lease/actions/force_release", `{"actor":"Human","reason":"steal"}`},
		{"DELETE", path, `{}`},
		{"POST", "/api/v1/projects", `{"name":"bypass"}`},
		{"GET", fmt.Sprintf("/api/v1/issues/%s", issue.Issue.UID), ""},
	} {
		out := request(t, s, tc.method, tc.path, "Bearer "+testWorkerToken, tc.body)
		if out.Code != http.StatusForbidden {
			t.Errorf("%s %s bypass: %d %s", tc.method, tc.path, out.Code, out.Body.String())
		}
	}
	info := request(t, s, "GET", "/forge/v1/project", "Bearer "+testWorkerToken, "")
	if info.Code != http.StatusOK {
		t.Fatalf("worker project discovery: %d", info.Code)
	}
	// Worker credentials must never double as supervisor credentials.
	if other, err := New(Config{DataDir: t.TempDir(), ProjectName: "same", AdminToken: testToken, WorkerToken: testToken}); err == nil {
		other.Close()
		t.Fatal("same role credentials accepted")
	}
}
