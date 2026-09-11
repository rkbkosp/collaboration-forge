# Configuration error diagnostics

A locally installed wrapper supplied connection defaults only under the Forge
source checkout. Running `forge codex --version` from the user's home directory
reproduced `codex_command_failed`; running it under the checkout succeeded.
The actual failing workspace was `/Volumes/SuperDisk/xiangliang/fornax`.

The local wrapper now defaults to the user's formal 127.0.0.1:7348 deployment
from any working directory, preserving explicit environment overrides. Guarded
user-level Forge hooks run only in launcher instances; the old project bundle
was backed up and removed to avoid duplicate calls. Existing user hooks remain.
These installation settings are local, not tracked product configuration.

Product fix: configuration validation now returns `codex_configuration`, with a
safe CLI hint for the endpoint, worker credential file and TTL. It never emits
supplied values or raw filesystem errors. Two tests failed before the fix for
missing worker configuration and invalid credential-bearing URL input, then
passed. Full Node suite: 74 passed; TypeScript check passed. No server change or
restart was required. Independent staged submodule work is excluded from this
commit.
