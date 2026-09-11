package forge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLeasePurposeCorrelatesOpaqueExecutionToSubmittingSession(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Attributable execution event")
	result := toolsRequest(f.handler, "issue_claim", toolsRuntimeA, "", fmt.Sprintf(`{"ref":%q,"attempt_id":%q,"purpose":"verify deployment"}`, uid, toolsAttemptA))
	toolsSuccess(t, result)
	var acquired struct{ Lease struct{ Purpose string } }
	if err := json.Unmarshal(result.Body.Bytes(), &acquired); err != nil {
		t.Fatal(err)
	}
	want := "Agent/" + f.signer.session(toolsRuntimeA)[:12] + " [Pi session " + toolsRuntimeA + "]"
	if !strings.HasPrefix(acquired.Lease.Purpose, want) {
		t.Fatal("opaque lease cannot be correlated to the session actor in its durable event")
	}
}
