package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"go.kenn.io/kata"
	"net/http"
)

type issueTimelineInput struct {
	Ref     string `json:"ref"`
	AfterID int64  `json:"after_id,omitempty"`
	Limit   *int   `json:"limit,omitempty"`
}

func (h *toolHandler) timeline(ctx context.Context, principal kata.Principal, in issueTimelineInput) *toolResponse {
	limit, ok := toolBoundedInt(in.Limit, 100, 1, 1000)
	if !ok || !toolRef(in.Ref) || in.AfterID < 0 {
		return toolError(http.StatusBadRequest, "validation", "invalid timeline input")
	}
	shown := h.dispatch(ctx, principal, "showIssue", http.MethodGet, fmt.Sprintf("/api/v1/projects/%d/issues/%s", h.project.ID, in.Ref), nil, nil)
	if !shown.success() {
		return shown
	}
	var show struct {
		Issue json.RawMessage `json:"issue"`
	}
	var issue struct {
		UID string `json:"uid"`
	}
	if json.Unmarshal(shown.body, &show) != nil || json.Unmarshal(show.Issue, &issue) != nil || !toolCanonicalUID(issue.UID) {
		return toolError(502, "invalid_response", "invalid issue projection")
	}
	events := h.dispatch(ctx, principal, "pollProjectEvents", http.MethodGet, fmt.Sprintf("/api/v1/projects/%d/events?after_id=%d&limit=%d", h.project.ID, in.AfterID, limit), nil, nil)
	if !events.success() {
		return events
	}
	page, err := projectIssueTimeline(issue.UID, show.Issue, events.body)
	if err != nil {
		return toolError(502, "invalid_response", "invalid event projection")
	}
	body, err := json.Marshal(page)
	if err != nil || len(body) > toolResponseLimit {
		return toolError(502, "response_too_large", "timeline response exceeds limit")
	}
	return &toolResponse{status: http.StatusOK, header: make(http.Header), body: body}
}

// Timeline is a projection of Kata's event API, never a second audit ledger.
// The scan cursor advances over ALL project events, including filtered rows.
// Consumers must use ScannedEventCount, not len(Events), to detect exhaustion.
type timelinePage struct {
	Issue             json.RawMessage   `json:"issue"`
	Events            []json.RawMessage `json:"events"`
	NextAfterID       int64             `json:"next_after_id"`
	ScannedEventCount int               `json:"scanned_event_count"`
	ResetRequired     bool              `json:"reset_required"`
	ResetAfterID      int64             `json:"reset_after_id,omitempty"`
}

func projectIssueTimeline(issueUID string, issue json.RawMessage, eventsBody []byte) (timelinePage, error) {
	var source struct {
		Events        []json.RawMessage `json:"events"`
		NextAfterID   int64             `json:"next_after_id"`
		ResetRequired bool              `json:"reset_required"`
		ResetAfterID  int64             `json:"reset_after_id"`
	}
	if err := json.Unmarshal(eventsBody, &source); err != nil {
		return timelinePage{}, err
	}
	page := timelinePage{Issue: issue, Events: []json.RawMessage{}, NextAfterID: source.NextAfterID, ScannedEventCount: len(source.Events), ResetRequired: source.ResetRequired, ResetAfterID: source.ResetAfterID}
	for _, event := range source.Events {
		var envelope struct {
			IssueUID        string `json:"issue_uid"`
			RelatedIssueUID string `json:"related_issue_uid"`
		}
		if err := json.Unmarshal(event, &envelope); err != nil {
			return timelinePage{}, err
		}
		if envelope.IssueUID == issueUID || envelope.RelatedIssueUID == issueUID {
			page.Events = append(page.Events, event)
		}
	}
	return page, nil
}
