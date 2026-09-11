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
type codexIdentity struct {
	Instance string `json:"instance_id"`
	Session  string `json:"session_id"`
	Thread   string `json:"thread_id"`
}

func codexID(s string) bool {
	return len(s) > 0 && len(s) <= 128 && !strings.ContainsAny(s, " \t\r\n\x00")
}
func (i codexIdentity) valid() bool {
	return codexID(i.Instance) && codexID(i.Session) && codexID(i.Thread)
}
func (i codexIdentity) runtime() string {
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	h[6] = (h[6] & 15) | 64
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func codexProcessBirth(pid int) (string, error) {
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
func codexProcessAlive(pid int, birth string) bool {
	now, err := codexProcessBirth(pid)
	return err == nil && birth != "" && now == birth
}
