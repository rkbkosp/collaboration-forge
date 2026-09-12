package forge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// These identifiers are attribution, not credentials. The instance capability
// and local process birth fence are checked separately by the daemon.
//
// Thread and Actor are the same logical slot under two harness wire names: the
// Codex contract spells it `thread_id` and the Claude contract spells it
// `agent_id`. Exactly one is populated, and both feed one registry key and one
// derived runtime session. Keeping the wire name in the serialization keeps a
// Codex and a Claude actor from ever colliding.
type actorIdentity struct {
	Instance string `json:"instance_id"`
	Session  string `json:"session_id"`
	Thread   string `json:"thread_id,omitempty"`
	Actor    string `json:"agent_id,omitempty"`
}

// slot returns the harness-neutral actor value used for validation.
func (i actorIdentity) slot() string {
	if i.Actor != "" {
		return i.Actor
	}
	return i.Thread
}

func attributionID(s string) bool {
	return len(s) > 0 && len(s) <= 128 && !strings.ContainsAny(s, " \t\r\n\x00")
}
func (i actorIdentity) valid() bool {
	return attributionID(i.Instance) && attributionID(i.Session) && attributionID(i.slot())
}
func (i actorIdentity) runtime() string {
	// The serialization is a wire contract, not an implementation detail: this
	// value is embedded in the signed execution proof, so a Codex identity must
	// keep hashing to exactly the same bytes it always did. Omitted empty actor
	// fields preserve that for Codex while giving Claude its own distinct input.
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	h[6] = (h[6] & 15) | 64
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func processBirth(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process")
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return "", fmt.Errorf("process unavailable")
	}
	uids, err := p.Uids()
	if err != nil || len(uids) == 0 || uids[0] != uint32(os.Getuid()) {
		return "", fmt.Errorf("process owner mismatch")
	}
	born, err := p.CreateTime()
	if err != nil || born <= 0 {
		return "", fmt.Errorf("process birth unavailable")
	}
	state, err := p.Status()
	if err != nil {
		return "", fmt.Errorf("process state unavailable")
	}
	for _, s := range state {
		if s == process.Zombie || s == "dead" {
			return "", fmt.Errorf("process ended")
		}
	}
	return strconv.FormatInt(born, 10), nil
}
func processAlive(pid int, birth string) bool {
	now, err := processBirth(pid)
	return err == nil && birth != "" && now == birth
}
