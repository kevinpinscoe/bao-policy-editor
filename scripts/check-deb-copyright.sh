#!/usr/bin/env bash
#
# check-deb-copyright.sh — confirm packaging/deb/copyright still describes the
# Go modules actually linked into a built bpe binary.
#
# The .deb ships packaging/deb/copyright as /usr/share/doc/bao-policy-editor/copyright.
# That file has one Files paragraph per Go module statically linked into bpe,
# each ending its Comment with "Go module <path> <version>." A dependency
# added, removed or upgraded in go.mod makes the file wrong without anything
# failing, so this compares the two and exits non-zero on any difference.
#
# Usage:
#
#   bash scripts/check-deb-copyright.sh [path/to/bpe]
#
# The default is the linux/amd64 binary a snapshot build leaves in dist/
# (RUNBOOK.md Step 17). Linux binaries are what the .deb carries; arm64 links
# the same modules.
#
# It checks module paths and versions only. When it fails, update the file by
# hand from the new module's own licence file and source headers (RUNBOOK.md
# Step 16, "Debian copyright file") — never by guessing a licence from a name.

set -euo pipefail

cd "$(dirname "$0")/.."
bin="${1:-dist/bpe_linux_amd64_v1/bpe}"
copyright=packaging/deb/copyright

[ -f "$bin" ] || { echo "no binary at $bin — run a snapshot build first (RUNBOOK.md Step 17)" >&2; exit 2; }
[ -f "$copyright" ] || { echo "missing $copyright" >&2; exit 2; }

linked="$(go version -m "$bin" | awk '$1 == "dep" { print $2, $3 }' | sort)"
listed="$(sed -n 's/^.*Go module \([^ ]*\) \([^ ]*\)\.$/\1 \2/p' "$copyright" | sort)"

if [ -z "$linked" ]; then
  echo "no module information in $bin" >&2
  exit 2
fi

if diff_out="$(diff <(printf '%s\n' "$linked") <(printf '%s\n' "$listed"))"; then
  echo "OK: $copyright lists exactly the $(printf '%s\n' "$linked" | wc -l) modules linked into $bin"
else
  echo "MISMATCH between modules linked into $bin (<) and $copyright (>):"
  printf '%s\n' "$diff_out" | grep '^[<>]'
  exit 1
fi
