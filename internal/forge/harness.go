package forge

import (
	"net/http"
	"strings"
)

// A harness profile names one supervised CLI adapter's private control
// transport. It carries no execution semantics: every profile dispatches into
// the same Worker API, with the same execution proof, receipt cache, renewal
// policy and strict-close facade. Adding a profile adds a control namespace and
// diagnostic labels only; it never adds Issue, lease or evidence semantics.
//
// Codex and Claude share one runtime instance. Ephemeral coordination (instance
// registry, actor registry, tenures, receipts, workspaces) is therefore
// identical no matter which harness launched the process, and a lease held
// through one namespace is the same lease the other observes.
type harnessProfile struct {
	// Name labels diagnostics, the derived actor and the release reason.
	Name string
	// CapabilityHeader carries the instance capability on every control request.
	CapabilityHeader string
	// Prefix is the private endpoint namespace served by this runtime.
	Prefix string
}

var (
	codexHarness = harnessProfile{
		Name:             "Codex",
		CapabilityHeader: "X-Forge-Codex-Token",
		Prefix:           "/forge/v1/codex/",
	}
	claudeHarness = harnessProfile{
		Name:             "Claude",
		CapabilityHeader: "X-Forge-Claude-Token",
		Prefix:           "/forge/v1/claude/",
	}
	// harnessProfiles is the complete set of served control namespaces.
	harnessProfiles = []harnessProfile{codexHarness, claudeHarness}
)

// label names the adapter in diagnostics. The shared `forge` CLI checkout route
// acts for no particular harness, and reports itself as Forge.
func (h harnessProfile) label() string {
	if h.Name == "" {
		return "Forge"
	}
	return h.Name
}

// releaseReason names the normal-end release so a durable lease event records
// which adapter shut down, without changing the release protocol itself.
func (h harnessProfile) releaseReason() string {
	return strings.ToLower(h.label()) + "_normal_end"
}

// errorMessage keeps the existing Codex wording exactly, and derives the
// equivalent wording for any further adapter.
func (h harnessProfile) errorMessage(code string) string {
	switch code {
	case "method_not_allowed":
		return h.label() + " endpoint requires POST"
	case "invalid_instance":
		return h.label() + " instance credential is invalid"
	case "validation":
		return "Invalid " + h.label() + " request"
	case "unknown_operation":
		return "Unknown " + h.label() + " operation"
	default:
		return h.label() + " operation failed"
	}
}

func (h harnessProfile) fail(w http.ResponseWriter, status int, code string) {
	toolError(status, code, h.errorMessage(code)).serve(w)
}

// harnessForPath resolves the control namespace, returning the remaining
// operation. An unrecognized path keeps the Codex profile so the caller reports
// an unknown operation exactly as it did before this namespace was generalized.
func harnessForPath(path string) (harnessProfile, string) {
	for _, h := range harnessProfiles {
		if rest, ok := strings.CutPrefix(path, h.Prefix); ok {
			return h, rest
		}
	}
	return codexHarness, path
}

// actorAttribution is the in-process marker that makes a typed tool dispatch
// adopt a supervised adapter's attribution instead of the Pi session actor. It
// is a context capability: HTTP clients cannot construct it.
type actorAttribution struct {
	harness  harnessProfile
	identity actorIdentity
}
