package forge

import (
	"os"
	"testing"
)

func TestProcessIdentityRejectsReuse(t *testing.T) {
	birth, err := processBirth(os.Getpid())
	if err != nil || birth == "" {
		t.Fatalf("birth: %q %v", birth, err)
	}
	if !processAlive(os.Getpid(), birth) {
		t.Fatal("current process not alive")
	}
	if processAlive(os.Getpid(), birth+"stale") {
		t.Fatal("PID reuse accepted")
	}
	if processAlive(-1, birth) {
		t.Fatal("invalid PID accepted")
	}
}
func TestActorIdentitySeparatesInstanceActorAndSession(t *testing.T) {
	a := actorIdentity{Instance: "instance-a", Session: "session-a", Thread: "thread-a"}
	if !a.valid() {
		t.Fatal("valid identity refused")
	}
	base := a.runtime()
	for _, b := range []actorIdentity{
		{Instance: "instance-b", Session: "session-a", Thread: "thread-a"},
		{Instance: "instance-a", Session: "session-b", Thread: "thread-a"},
		{Instance: "instance-a", Session: "session-a", Thread: "thread-b"},
	} {
		if b.runtime() == base {
			t.Fatal("authority collision")
		}
	}
	if !toolUUID(base, "47") {
		t.Fatal("internal compatibility UUID invalid")
	}
	if (actorIdentity{Instance: "a", Session: "", Thread: "t"}).valid() || (actorIdentity{Instance: "a", Session: "s", Thread: "x\ny"}).valid() {
		t.Fatal("invalid identity accepted")
	}
}
