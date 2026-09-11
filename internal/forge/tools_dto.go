package forge

// These are Forge wire DTOs, not aliases for Kata's larger mutation inputs.
// Keep per-operation inputs separate: adding a capability requires deliberately
// adding both a field here and its controlled translation in tools.go.
type issueListInput struct {
	Status string `json:"status,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}
type issueGetInput struct {
	Ref string `json:"ref"`
}
type issueGraphInput struct {
	Ref   string `json:"ref"`
	Depth *int   `json:"depth,omitempty"`
}
type issueCreateInput struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}
type issueCommentInput struct {
	Ref  string `json:"ref"`
	Body string `json:"body"`
}
type issueLinkInput struct {
	Ref   string `json:"ref"`
	Type  string `json:"type"`
	ToRef string `json:"to_ref"`
}
type issueClaimInput struct {
	Ref        string `json:"ref"`
	AttemptID  string `json:"attempt_id"`
	TTLSeconds *int   `json:"ttl_seconds,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
}
type issueRenewInput struct {
	Ref        string `json:"ref"`
	TTLSeconds *int   `json:"ttl_seconds,omitempty"`
}
type issueReleaseInput struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason,omitempty"`
}
type issueCloseInput struct {
	Ref      string         `json:"ref"`
	Reason   string         `json:"reason,omitempty"`
	Message  string         `json:"message,omitempty"`
	Evidence []toolEvidence `json:"evidence,omitempty"`
	IfMatch  string         `json:"if_match,omitempty"`
}

// toolEvidence mirrors only the public evidence wire fields. Kata remains the
// authority for the typed union, per-reason evidence validation and storage.
// No custom unmarshaler: DisallowUnknownFields must also protect nested items.
type toolEvidence struct {
	Type      string   `json:"type"`
	SHA       string   `json:"sha,omitempty"`
	URL       string   `json:"url,omitempty"`
	Command   string   `json:"command,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Account   string   `json:"account,omitempty"`
	Rationale string   `json:"rationale,omitempty"`
	IssueRef  string   `json:"issue_ref,omitempty"`
}

type toolCommentBody struct {
	Body string `json:"body"`
}
type toolLinkBody struct {
	Type  string `json:"type"`
	ToRef string `json:"to_ref"`
}
type toolTimedLeaseBody struct {
	ClaimKind  string `json:"claim_kind"`
	TTLSeconds int    `json:"ttl_seconds"`
	Purpose    string `json:"purpose,omitempty"`
}
type toolReleaseBody struct {
	Reason string `json:"reason,omitempty"`
}
type toolCloseBody struct {
	Reason        string         `json:"reason,omitempty"`
	Message       string         `json:"message,omitempty"`
	Evidence      []toolEvidence `json:"evidence,omitempty"`
	RetryProtocol string         `json:"retry_protocol"`
}
