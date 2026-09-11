# Package 5: bounded Stop guard

Test-first: stop.ts missing; corrected fixture authentication then reproduced
`Stop believed stale lease was live`. Added exact read projection refresh.
A second regression reproduced HTTP 403 when close had committed, its response
was lost, and Stop observed lease expiry: pending close now retains its own proof
independently of the expiring active tenure, preserving Kata receipt-first replay.

Validation: Node Codex tests 9/9 plus dedicated typecheck pass; all Go Codex
race tests pass, including real Kata forced release and expired close replay.

Default strict asks for one continuation when work is active/pending/unknown.
Already-continued turns, explicit pauses, advisory mode, and clearly expressed
requests for user confirmation pause renewal and report unresolved state instead
of looping. Message classification is only advisory UX, never server authority.
SubagentStop does not immediately release, because another hook can continue the
subagent. Pause/interrupt stops automatic renewal; prompt resumes only before
lease deadline, otherwise a fresh claim is required. No force release.
