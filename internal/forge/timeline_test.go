package forge

import (
	"encoding/json"
	"testing"
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
