package forge

import (
	"os"
	"testing"
)

func TestCodexProcessIdentityRejectsReuse(t *testing.T) {
	birth, err := codexProcessBirth(os.Getpid())
	if err != nil || birth == "" {
		t.Fatalf("birth: %q %v", birth, err)
	}
	if !codexProcessAlive(os.Getpid(), birth) {
		t.Fatal("current process not alive")
	}
	if codexProcessAlive(os.Getpid(), birth+"stale") {
		t.Fatal("PID reuse accepted")
	}
	if codexProcessAlive(-1, birth) {
		t.Fatal("invalid PID accepted")
	}
}
func TestCodexIdentitySeparatesInstanceThreadAndSession(t *testing.T) {
	a := codexIdentity{Instance: "instance-a", Session: "session-a", Thread: "thread-a"}
	if !a.valid() {
		t.Fatal("valid identity refused")
	}
	base := a.runtime()
	for _, b := range []codexIdentity{{"instance-b", "session-a", "thread-a"}, {"instance-a", "session-b", "thread-a"}, {"instance-a", "session-a", "thread-b"}} {
		if b.runtime() == base {
			t.Fatal("authority collision")
		}
	}
	if !toolUUID(base, "47") {
		t.Fatal("internal compatibility UUID invalid")
	}
	if (codexIdentity{"a", "", "t"}).valid() || (codexIdentity{"a", "s", "x\ny"}).valid() {
		t.Fatal("invalid identity accepted")
	}
}
