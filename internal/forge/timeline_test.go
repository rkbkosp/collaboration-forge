package forge

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestTimelineKeepsEvidenceAndRelatedEventsAndScanCursor(t *testing.T) {
	page, err := projectIssueTimeline("target", json.RawMessage(`{"uid":"target"}`), []byte(`{"events":[{"event_id":1,"issue_uid":"other","type":"issue.created"},{"event_id":2,"issue_uid":"other","related_issue_uid":"target","type":"link.created","actor":"Agent/b","payload":{"type":"blocks"}},{"event_id":3,"issue_uid":"target","type":"issue.closed","actor":"Agent/a","payload":{"evidence":[{"type":"test","command":"go test ./..."}]}},{"event_id":4,"issue_uid":"other","type":"comment.created"}],"next_after_id":4,"reset_required":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if page.NextAfterID != 4 || page.ScannedEventCount != 4 || len(page.Events) != 2 {
		t.Fatalf("incorrect page: %+v", page)
	}
	var close struct {
		Actor   string
		Payload struct{ Evidence []struct{ Command string } }
	}
	if err := json.Unmarshal(page.Events[1], &close); err != nil {
		t.Fatal(err)
	}
	if close.Actor != "Agent/a" || len(close.Payload.Evidence) != 1 || close.Payload.Evidence[0].Command != "go test ./..." {
		t.Fatal("trace evidence was lost")
	}
}

func TestTimelineResetIsExplicit(t *testing.T) {
	page, err := projectIssueTimeline("target", nil, []byte(`{"events":[],"reset_required":true,"reset_after_id":90,"next_after_id":90}`))
	if err != nil || !page.ResetRequired || page.ResetAfterID != 90 || page.NextAfterID != 90 {
		t.Fatalf("reset not preserved: %+v %v", page, err)
	}
	if _, err := projectIssueTimeline("target", nil, []byte(`bad json`)); err == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestTimelineReadsCurrentLeaseWithoutReconstructingHistory(t *testing.T) {
	f := newToolsFixture(t)
	uid, _ := toolsCreate(t, f.handler, "Timeline current execution")
	body := fmt.Sprintf(`{"ref":%q}`, uid)
	read := func() map[string]json.RawMessage {
		t.Helper()
		r := toolsRequest(f.handler, "issue_timeline", toolsRuntimeB, "", body)
		toolsSuccess(t, r)
		page := toolsJSON(t, r)
		var observed time.Time
		if json.Unmarshal(page["observed_at"], &observed) != nil || observed.IsZero() {
			t.Fatal("missing observation time")
		}
		return page
	}
	if string(read()["lease"]) != "null" {
		t.Fatal("unclaimed timeline must explicitly report no lease")
	}
	token, _, _ := toolsClaim(t, f, uid, toolsRuntimeA, toolsAttemptA)
	first := read()
	var lease struct {
		ClaimUID  string    `json:"claim_uid"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(first["lease"], &lease) != nil || lease.ClaimUID == "" {
		t.Fatal("current lease missing")
	}
	var now time.Time
	if json.Unmarshal(first["lease_hub_now"], &now) != nil || !lease.ExpiresAt.After(now) {
		t.Fatal("missing authority clock")
	}
	renew := toolsRequest(f.handler, "issue_renew", toolsRuntimeA, token, fmt.Sprintf(`{"ref":%q,"ttl_seconds":3600}`, uid))
	toolsSuccess(t, renew)
	renewed := read()
	var next struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(renewed["lease"], &next) != nil || !next.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatal("timeline retained obsolete expiry")
	}
	if string(first["events"]) != string(renewed["events"]) {
		t.Fatal("renew must not require heartbeat history events")
	}
	toolsSuccess(t, toolsRequest(f.handler, "issue_release", toolsRuntimeA, token, body))
	if string(read()["lease"]) != "null" {
		t.Fatal("released execution still displayed")
	}
	replacementToken, _, _ := toolsClaim(t, f, uid, toolsRuntimeB, toolsAttemptB)
	var replacement struct {
		ClaimUID string `json:"claim_uid"`
	}
	if json.Unmarshal(read()["lease"], &replacement) != nil || replacement.ClaimUID == lease.ClaimUID {
		t.Fatal("replacement tenure not projected")
	}
	closeBody := fmt.Sprintf(`{"ref":%q,"reason":"done","message":"Verified current lease projection through renewal, release and replacement with the real embedded service.","evidence":[{"type":"test","command":"go test ./internal/forge -run TestTimeline"}]}`, uid)
	toolsSuccess(t, toolsRequest(f.handler, "issue_close", toolsRuntimeB, replacementToken, closeBody, "Idempotency-Key", "timeline-close"))
	closed := read()
	var issue struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(closed["issue"], &issue) != nil || issue.Status != "closed" || string(closed["lease"]) != "null" {
		t.Fatal("close did not project closed/released state")
	}

}
