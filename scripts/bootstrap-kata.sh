#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# Trusted, versioned source pin; never sourced from a server response.
source "$root/patches/kata/upstream.env"
destination="${1:-$root/kata}"

# The published submodule contains the exact patched source used by Forge.
# Verify it without ever applying patches in-place.  Supplying
# KATA_UPSTREAM_URL opts into the reconstruction path used by the bootstrap
# regression test and by maintainers checking the patch export.
if [[ "$#" -eq 0 && -z "${KATA_UPSTREAM_URL+x}" ]]; then
  source_repo="$root/kata"
  if [[ ! -f "$source_repo/go.mod" ]]; then
    echo 'Kata submodule is not initialized; run: git submodule update --init --checkout kata' >&2
    exit 1
  fi
  actual_commit="$(git -C "$source_repo" rev-parse HEAD)"
  if [[ "$actual_commit" != "$KATA_SUBMODULE_COMMIT" ]]; then
    echo "Kata submodule is at $actual_commit, expected $KATA_SUBMODULE_COMMIT." >&2
    exit 1
  fi
  actual_tree="$(git -C "$source_repo" rev-parse 'HEAD^{tree}')"
  if [[ "$actual_tree" != "$KATA_PATCHED_TREE" ]]; then
    echo "Kata submodule tree is $actual_tree, expected $KATA_PATCHED_TREE." >&2
    exit 1
  fi
  test -z "$(git -C "$source_repo" status --porcelain)" || {
    echo 'Kata submodule has local changes; left untouched.' >&2; exit 1;
  }
  echo 'Pinned patched Kata submodule is ready.'
  exit 0
fi

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
source_repo="${KATA_UPSTREAM_URL:-$KATA_REPOSITORY_URL}"
mkdir -p "$(dirname "$destination")"
tmp="$(mktemp -d "$(dirname "$destination")/.kata-bootstrap.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
git clone --no-checkout "$source_repo" "$tmp/checkout"
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
