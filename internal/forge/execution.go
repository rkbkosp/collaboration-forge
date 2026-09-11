package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Execution proofs are credentials, not model-visible tool results. The server
// derives a principal from a Pi session and fresh logical-acquire nonce.
// Repeating an acquire is stable; new runtimes must generate new attempt nonces
// rather than recovering them from session history.
// The persisted signing key permits in-flight receipt recovery across a daemon
// restart. Lease liveness and exact tenure are still checked only by Kata.
type executionSigner struct{ key []byte }
type executionProof struct {
	Subject  string `json:"subject"`
	Session  string `json:"session"`
	IssueUID string `json:"issue_uid"`
	ClaimUID string `json:"claim_uid"`
}

func (s executionSigner) digest(domain string, parts ...string) string {
	encoded, _ := json.Marshal(append([]string{domain}, parts...))
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s executionSigner) session(runtime string) string { return s.digest("session-v1", runtime) }
func (s executionSigner) identity(runtime, attempt, issue string) string {
	return "execution:" + s.digest("execution-v1", runtime, attempt, issue)
}
func (s executionSigner) sign(proof executionProof) string {
	encoded, _ := json.Marshal(proof)
	payload := base64.RawURLEncoding.EncodeToString(encoded)
	return payload + "." + s.digest("execution-proof-v1", payload)
}
func (s executionSigner) verify(token, runtime string) (executionProof, error) {
	var proof executionProof
	invalid := errors.New("invalid execution credential")
	if len(token) > 4096 || runtime == "" {
		return proof, invalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(s.digest("execution-proof-v1", parts[0]))) {
		return proof, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &proof) != nil || proof.Session != s.session(runtime) || proof.Subject == "" || proof.IssueUID == "" || proof.ClaimUID == "" {
		return executionProof{}, invalid
	}
	return proof, nil
}
