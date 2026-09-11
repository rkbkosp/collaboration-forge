#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# Exercise real clone + patch application from the fixed submodule, without
# depending on network access.
KATA_UPSTREAM_URL="$root/kata" bash "$root/scripts/bootstrap-kata.sh" "$tmp/kata"
bash "$root/scripts/bootstrap-kata.sh" "$tmp/kata"
printf '\nlocal change\n' >> "$tmp/kata/README.md"
cp "$tmp/kata/README.md" "$tmp/expected-readme"
if bash "$root/scripts/bootstrap-kata.sh" "$tmp/kata"; then
  echo 'Dirty checkout should have been refused.' >&2; exit 1
fi
cmp "$tmp/kata/README.md" "$tmp/expected-readme"
echo 'Bootstrap reconstruction, idempotency, and dirty-checkout preservation passed.'
