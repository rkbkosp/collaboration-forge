# Package 1: Codex identity and launcher

2026-09-11. Test-first: Go tests initially failed on missing identity/process
helpers; launcher tests failed with ERR_MODULE_NOT_FOUND. Implemented isolated
instance credentials, UUID-compatible tuple attribution and same-UID process
birth checks. Corrected gopsutil UID typing and launcher environment typing.

Validation: `go test ./internal/forge -run TestCodex` passes; Node 24.19.0
`node --import tsx --test codex/launcher.test.ts` passes 2/2;
`tsc -p codex/tsconfig.json` passes.

This package provides the launcher module, not CLI routing or daemon registration
routes (packages 2/3). The supervisor has no renewal loop; unexpected child exit
retires the instance without normal release. Same-UID access is trusted local
coordination, not an OS security boundary. Codex environment IDs are attribution.
