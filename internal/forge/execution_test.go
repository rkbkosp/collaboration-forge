package forge

import "testing"

func TestExecutionIdentityAndProof(t *testing.T) {
	s := executionSigner{key: []byte("12345678901234567890123456789012")}
	a := s.identity("runtime-a", "attempt-a", "issue-a")
	if a != s.identity("runtime-a", "attempt-a", "issue-a") {
		t.Fatal("network retry changed identity")
	}
	for _, b := range []string{s.identity("runtime-b", "attempt-a", "issue-a"), s.identity("runtime-a", "attempt-b", "issue-a"), s.identity("runtime-a", "attempt-a", "issue-b")} {
		if a == b {
			t.Fatal("logical tenures collided")
		}
	}
	proof := executionProof{Subject: a, Session: s.session("runtime-a"), IssueUID: "issue-a", ClaimUID: "claim-a"}
	token := s.sign(proof)
	got, err := s.verify(token, "runtime-a")
	if err != nil || got != proof {
		t.Fatalf("proof did not round trip: %v", err)
	}
	if _, err := s.verify(token, "runtime-b"); err == nil {
		t.Fatal("another runtime accepted")
	}
	if _, err := s.verify(token+"x", "runtime-a"); err == nil {
		t.Fatal("tampering accepted")
	}
	other := executionSigner{key: []byte("23456789012345678901234567890123")}
	if _, err := other.verify(token, "runtime-a"); err == nil {
		t.Fatal("foreign signer accepted")
	}
	for _, malformed := range []string{"", "a", ".", "a.b", token + ".a"} {
		if _, err := s.verify(malformed, "runtime-a"); err == nil {
			t.Fatal("malformed proof accepted")
		}
	}
}
