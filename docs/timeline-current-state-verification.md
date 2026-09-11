# Timeline current-state verification — 2026-09-11

Baseline: Forge `d6ea07f`; embedded Kata `b67a696`.

## Test-first evidence

Before implementation:

- `go test ./internal/forge -run TestTimelineReadsCurrent -count=1`
  failed in `TestTimelineReadsCurrentLeaseWithoutReconstructingHistory`:
  `missing observation time`.
- `go test ./cmd/forged -run 'TestTimelineCurrent|TestTimelineReadFailure' -count=1`
  failed because `-details` was undefined and HTTP 503 did not display unknown
  execution authority or the last confirmation time.

The fix forwards Kata lease/time/pending projections with a Forge read-completion
clock, and renders timestamped current state separately from history. No Kata
schema, claim mutation, heartbeat event, or execution credential change.

## Passing checks after implementation

- `go test ./... -count=1`: both root packages pass.
- `go test -race ./... -count=1`: both root packages pass.
- `go build ./...`: pass.
- `go vet ./...`: pass.
- `git diff --check`: pass.

The real embedded Kata regression covers unclaimed, acquired, renewal with
unchanged history, release, replacement tenure, and strict close/released state.
CLI tests cover real HTTP pagination and unclaimed state, plus controlled held,
closed, legacy/missing-clock/expired/pending unknown states and initial/later-page
HTTP failures. The held fixture deliberately uses fixed server timestamps; no
client wall-clock inference controls its displayed observation.

Full Pi/LLM E2E and the Kata full suite were not rerun for this read-only projection
change. Concurrent controller/client/package work is outside this commit.
