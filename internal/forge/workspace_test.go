package forge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestWorkspaceRegistrationRequiresExecutionAndIsIdempotent(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Code workspace")
	token, _, _ := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	body := fmt.Sprintf(`{"ref":%q,"snapshot_id":%q,"base_commit":%q,"repository_id":%q,"workspace_id":%q,"state":"ready","source_kind":"dirty","device":"local","location":"/private/worktree","snapshot_location":"/private/snapshot"}`, uid, strings.Repeat("a", 64), strings.Repeat("b", 40), strings.Repeat("c", 64), toolsAttemptB)
	if w := toolsRequest(f.handler, "issue_workspace", toolsRuntimeA, "", body); w.Code != http.StatusForbidden {
		t.Fatalf("missing proof %d", w.Code)
	}
	for range 2 {
		w := toolsRequest(f.handler, "issue_workspace", toolsRuntimeA, token, body)
		toolsSuccess(t, w)
	}
	shown := toolsRequest(f.handler, "issue_get", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q}`, uid))
	if strings.Count(shown.Body.String(), "forge.workspace.v1") != 1 {
		t.Fatalf("registration missing or duplicated: %s", shown.Body.String())
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_release", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q}`, uid)))
	if w := toolsRequest(f.handler, "issue_workspace", toolsRuntimeA, token, body); w.Code < 400 {
		t.Fatal("stale execution registered workspace")
	}
}
