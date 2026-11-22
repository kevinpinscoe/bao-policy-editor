#!/usr/bin/env bash
#
# live-smoke-home-isolation.test.sh — regression test for KSA-206.
#
# `bao server -dev` writes its root token to $HOME/.vault-token unless it is
# given -dev-no-store-token. scripts/live-smoke.sh once ran the dev server with
# the developer's real HOME, so every smoke run replaced the real
# ~/.vault-token with a throwaway root token. This test fails if that can
# happen again.
#
# Usage:
#
#   bash scripts/live-smoke-home-isolation.test.sh             # the working tree
#   bash scripts/live-smoke-home-isolation.test.sh --rev REV   # a committed
#                                     revision, e.g. --rev fbfc5a5 as a negative
#                                     control (the unfixed script must FAIL)
#
# What it checks:
#
#   static   every `server -dev` line in live-smoke.sh passes
#            -dev-no-store-token, and no `bao` process is started except
#            through the isolated-HOME wrapper.
#   runtime  live-smoke.sh is run twice with HOME pointed at a stand-in home
#            directory, once holding a stand-in ~/.vault-token (fake value) and
#            once with none, and must leave it byte-identical with the same
#            inode, mode and mtime, or still absent. The stand-in keeps the test
#            itself from touching the real HOME.
#   outer    the real ~/.vault-token's metadata (never its contents) is the same
#            after the whole test as before it.
#
# It starts disposable dev servers through live-smoke.sh, on a free loopback
# port, and contacts nothing else. Like live-smoke.sh it is never run by CI.

set -uo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
rev=""
if [[ "${1:-}" == "--rev" ]]; then
  rev="${2:?--rev needs a revision}"
elif [[ $# -gt 0 ]]; then
  echo "usage: $0 [--rev REV]" >&2
  exit 64
fi

pass=0
fail=0
check() {  # check DESCRIPTION COMMAND
  if eval "$2"; then
    pass=$((pass + 1)); echo "ok   - $1"
  else
    fail=$((fail + 1)); echo "FAIL - $1"
  fi
}

state() {  # metadata only; the real token file is never opened
  if [[ -L "$1" ]]; then echo symlink
  elif [[ ! -e "$1" ]]; then echo absent
  else stat -c '%i %s %a %Y %Z' "$1" 2>/dev/null || stat -f '%i %z %Lp %m %c' "$1"
  fi
}

real_token="$HOME/.vault-token"
real_before=$(state "$real_token")

tmp=$(mktemp -d -t bpe-ksa206-XXXXXX)
trap 'rm -rf "$tmp"' EXIT

# The tree under test: the working tree, or an exported revision.
if [[ -n "$rev" ]]; then
  tree="$tmp/tree"
  mkdir "$tree"
  git -C "$repo_root" archive "$rev" | tar -x -C "$tree" || { echo "cannot export $rev" >&2; exit 1; }
  echo "# testing live-smoke.sh at $rev (exported to a temporary tree)"
else
  tree="$repo_root"
  echo "# testing the working tree's live-smoke.sh"
fi
smoke="$tree/scripts/live-smoke.sh"
[[ -f "$smoke" ]] || { echo "no scripts/live-smoke.sh in the tree under test" >&2; exit 1; }

# --- static ---------------------------------------------------------------

code=$(grep -v '^[[:space:]]*#' "$smoke")
check "static: every 'server -dev' line passes -dev-no-store-token" \
  '! printf "%s\n" "$code" | grep -E "server[[:space:]]+-dev" | grep -vq -- "-dev-no-store-token" && printf "%s\n" "$code" | grep -qE "server[[:space:]]+-dev"'
check "static: bao runs only through the isolated-HOME wrapper (bao_cli)" \
  '! printf "%s\n" "$code" | grep -E "\"\\\$bao_bin\"[[:space:]]+[a-z]" | grep -vq "bao_cli()"'
check "static: the wrapper sets HOME to the private bao_home" \
  'printf "%s\n" "$code" | grep -qE "bao_cli\(\) \{ HOME=\"\\\$bao_home\""'

# --- runtime --------------------------------------------------------------

# The stand-in HOME breaks mise shims and HOME-relative defaults, so resolve
# the real go toolchain, its caches and bao up front and pass them in.
go_bin="$(go env GOROOT)/bin"
gocache=$(go env GOCACHE); gomodcache=$(go env GOMODCACHE); gopath=$(go env GOPATH)
bao_bin=$(command -v bao || true)
[[ -z "$bao_bin" && -x "$HOME/.local/bin/bao" ]] && bao_bin="$HOME/.local/bin/bao"
[[ -z "$bao_bin" && -x /opt/homebrew/bin/bao ]] && bao_bin=/opt/homebrew/bin/bao
[[ -n "$bao_bin" ]] || { echo "no bao binary found" >&2; exit 1; }

free_port() {
  /usr/bin/python3 -I -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

run_smoke() {  # run_smoke STAND_IN_HOME -> output in $tmp/out, exit in $tmp/rc
  env -i PATH="$go_bin:$(dirname "$bao_bin"):/usr/bin:/bin:/usr/sbin:/sbin" \
    HOME="$1" TMPDIR="${TMPDIR:-/tmp}" GOCACHE="$gocache" GOMODCACHE="$gomodcache" \
    GOPATH="$gopath" GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
    BPE_SMOKE_PORT="$(free_port)" \
    bash "$smoke" > "$tmp/out" 2>&1
  echo $? > "$tmp/rc"
}

# Case 1: a stand-in ~/.vault-token exists (fake value, never a real token).
h1="$tmp/home-with-token"; mkdir "$h1"
( umask 077; printf 'fake-not-a-token-%s' "$RANDOM$RANDOM" > "$h1/.vault-token" )
want1=$(state "$h1/.vault-token"); sum1=$(shasum -a 256 < "$h1/.vault-token")
sleep 1   # so a rewrite within the same second still moves mtime/ctime
run_smoke "$h1"
cp "$tmp/out" "$tmp/out-case1"
check "case 1: live-smoke.sh passed (exit $(cat "$tmp/rc"))" \
  '[ "$(cat "$tmp/rc")" = 0 ] && grep -q "LIVE SMOKE TEST PASSED" "$tmp/out-case1"'
check "case 1: the stand-in ~/.vault-token is byte-identical (same inode, size, mode, mtime, ctime)" \
  '[ "$(state "$h1/.vault-token")" = "$want1" ] && [ "$(shasum -a 256 < "$h1/.vault-token")" = "$sum1" ]'

# Case 2: no ~/.vault-token at all. It must still be absent afterwards.
h2="$tmp/home-without-token"; mkdir "$h2"
run_smoke "$h2"
cp "$tmp/out" "$tmp/out-case2"
check "case 2: live-smoke.sh passed (exit $(cat "$tmp/rc"))" \
  '[ "$(cat "$tmp/rc")" = 0 ] && grep -q "LIVE SMOKE TEST PASSED" "$tmp/out-case2"'
check "case 2: no ~/.vault-token was created" '[ "$(state "$h2/.vault-token")" = absent ]'
# `go test` keeps HOME and may write its own config there (telemetry), so only
# bao/vault CLI state is asserted absent, not an empty directory.
check "case 2: no bao/vault CLI state (.vault-token, .bao*, .vault*) in the stand-in HOME" \
  '[ -z "$(find "$h2" -maxdepth 1 \( -name ".vault*" -o -name ".bao*" \) -print)" ]'

# --- outer ---------------------------------------------------------------

check "outer: the real ~/.vault-token is unchanged by this whole test" \
  '[ "$(state "$real_token")" = "$real_before" ]'

if [[ "$fail" -gt 0 ]]; then
  echo "# live-smoke.sh output, last 15 lines of each case (no token is printed by it):"
  for c in 1 2; do echo "## case $c"; tail -n 15 "$tmp/out-case$c" 2>/dev/null; done
fi
echo "# ${pass} passed, ${fail} failed"
[[ "$fail" -eq 0 ]]
