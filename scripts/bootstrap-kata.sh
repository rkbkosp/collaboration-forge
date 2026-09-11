#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# Trusted, versioned source pin; never sourced from a server response.
source "$root/patches/kata/upstream.env"
destination="${1:-$root/kata}"
if [[ -e "$destination" ]]; then
  test "$(git -C "$destination" rev-parse 'HEAD^{tree}')" = "$KATA_PATCHED_TREE" || {
    echo 'Existing Kata checkout is not the pinned patched source; left untouched.' >&2; exit 1;
  }
  test -z "$(git -C "$destination" status --porcelain)" || {
    echo 'Existing Kata checkout has local changes; left untouched.' >&2; exit 1;
  }
  echo 'Pinned Kata source already present.'
  exit 0
fi
mkdir -p "$(dirname "$destination")"
tmp="$(mktemp -d "$(dirname "$destination")/.kata-bootstrap.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
git clone --no-checkout "${KATA_UPSTREAM_URL:-$KATA_REPOSITORY_URL}" "$tmp/checkout"
git -C "$tmp/checkout" checkout --detach "$KATA_BASE_COMMIT"
for patch in "$root"/patches/kata/*.patch; do
  git -C "$tmp/checkout" -c user.name='Forge bootstrap' -c user.email='forge@example.invalid' am "$patch"
done
test "$(git -C "$tmp/checkout" rev-parse 'HEAD^{tree}')" = "$KATA_PATCHED_TREE" || {
  echo 'Patched Kata tree differs from the source pin.' >&2; exit 1;
}
# Do not overwrite a checkout created by another bootstrap while we worked.
test ! -e "$destination"
mv "$tmp/checkout" "$destination"
echo 'Pinned patched Kata source is ready.'
